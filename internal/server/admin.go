package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/t479842598/u1s12api-go/internal/config"
	"github.com/t479842598/u1s12api-go/internal/fingerprint"
	"github.com/t479842598/u1s12api-go/internal/logging"
	"github.com/t479842598/u1s12api-go/internal/store"
	"github.com/t479842598/u1s12api-go/internal/upstream"
)

// ---- 认证 ----

func (s *Server) handleAdminLogin(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if s.throttle.blocked(ip) {
		writeAPIError(w, http.StatusTooManyRequests, "尝试次数过多，请 15 分钟后再试")
		return
	}
	var body struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIError(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	if body.Key != s.getSettings().AdminPassword {
		s.throttle.recordFail(ip)
		writeAPIError(w, http.StatusUnauthorized, "口令错误")
		return
	}
	s.throttle.reset(ip)
	expires := time.Now().Add(7 * 24 * time.Hour)
	http.SetCookie(w, &http.Cookie{
		Name:     adminCookieName,
		Value:    s.signCookie(expires),
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  expires,
	})
	writeAPIData(w, http.StatusOK, map[string]any{"authenticated": true})
}

func (s *Server) handleAdminLogout(w http.ResponseWriter, _ *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     adminCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})
	writeAPIData(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleAdminSession(w http.ResponseWriter, r *http.Request) {
	writeAPIData(w, http.StatusOK, map[string]any{"authenticated": s.isAdminAuthenticated(r)})
}

// ---- Overview ----

func (s *Server) handleOverview(w http.ResponseWriter, _ *http.Request) {
	loc := time.FixedZone("CST", 8*3600)
	todayStart := time.Now().In(loc)
	todayStart = time.Date(todayStart.Year(), todayStart.Month(), todayStart.Day(), 0, 0, 0, 0, loc)

	today, err := s.store.SummarySince(todayStart.Unix())
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	allTime, err := s.store.SummarySince(0)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	keyStats := s.pool.CountByStatus()
	daily, err := s.store.DailyUsage(14)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	models, err := s.store.TopModels(30, 5)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	recent, err := s.store.RecentRequests(10)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}

	var announcement any
	s.modelsMu.Lock()
	mc := s.modelsCache
	if mc != nil {
		announcement = mc.resp.Announcement
	}
	s.modelsMu.Unlock()

	cfg := s.getSettings()
	fp := s.fp.Current()

	// 授权账号额度汇总（来自库内快照，不做实时请求）。
	accAccounts, err := s.store.ListAccounts()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	accQuota := []map[string]any{}
	for _, a := range accAccounts {
		if !a.Authorized || !a.Enabled {
			continue
		}
		view := buildAccountQuotaView(a)
		accQuota = append(accQuota, map[string]any{
			"id":           a.ID,
			"email_masked": a.EmailMasked,
			"total":        view.Total,
			"capacity":     view.Capacity,
			"updated_at":   view.UpdatedAt,
			"items":        view.Items,
		})
	}

	writeAPIData(w, http.StatusOK, map[string]any{
		"today":         today,
		"totals":        allTime,
		"keys":          keyStats,
		"daily":         daily,
		"models":        models,
		"recent":        recent,
		"account_quota": accQuota,
		"fingerprint": map[string]any{
			"profile":    fp.ID,
			"label":      fp.Label,
			"user_agent": fingerprint.UserAgent(fp),
			"runtime":    fp.RuntimeVersion,
		},
		"upstream_base_url": cfg.UpstreamBaseURL,
		"u1s1_version":      cfg.U1S1Version,
		"announcement":      announcement,
	})
}

// ---- 上游模型 ----

func (s *Server) handleAdminModels(w http.ResponseWriter, r *http.Request) {
	models, cached, err := s.fetchModels(r.Context())
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeAPIData(w, http.StatusOK, map[string]any{
		"models":       models.Data,
		"features":     models.Features,
		"announcement": models.Announcement,
		"cached":       cached,
	})
}

// ---- 上游 U1S1 Key 管理 ----

func (s *Server) handleListUpstreamKeys(w http.ResponseWriter, _ *http.Request) {
	keys, err := s.store.ListUpstreamKeys()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if keys == nil {
		keys = []*store.UpstreamKey{}
	}
	stats := s.pool.CountByStatus()
	now := time.Now().Unix()
	for _, k := range keys {
		k.Key = "" // 不回明文
		// 冷却已过期但 DB 还没刷新的，前端展示按 active 处理。
		if k.Status == "cooldown" && k.CooldownUntil > 0 && k.CooldownUntil <= now {
			k.Status = "active"
		}
	}
	writeAPIData(w, http.StatusOK, map[string]any{"keys": keys, "stats": stats})
}

type importItem struct {
	Key  string `json:"key"`
	Note string `json:"note"`
}

func (s *Server) handleImportUpstreamKeys(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Items []importItem `json:"items"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIError(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	added, skipped, invalid := 0, 0, 0
	for _, it := range body.Items {
		key := strings.TrimSpace(it.Key)
		if !strings.HasPrefix(key, "u1s1-") {
			invalid++
			continue
		}
		ok, err := s.store.AddUpstreamKey(key, it.Note)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if ok {
			added++
		} else {
			skipped++
		}
	}
	if added > 0 {
		if err := s.pool.Reload(); err != nil {
			writeAPIError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	logger.Infof("导入 U1S1 Key: 新增 %d 跳过 %d 无效 %d", added, skipped, invalid)
	writeAPIData(w, http.StatusOK, map[string]any{"added": added, "skipped": skipped, "invalid": invalid})
}

// handleImportUpstreamKeysText 一键批量导入：每行一把 key，支持「key 备注」。
// 自动跳过空行、注释行（# 开头）与非 u1s1- 前缀行。
func (s *Server) handleImportUpstreamKeysText(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIError(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	items := []importItem{}
	invalidLines := []string{}
	for _, ln := range strings.Split(body.Text, "\n") {
		line := strings.TrimSpace(ln)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		key := fields[0]
		// 备注 = key 之后的全部字段（多词备注不被截断）。
		note := ""
		if len(fields) > 1 {
			note = strings.Join(fields[1:], " ")
		}
		if !strings.HasPrefix(key, "u1s1-") {
			invalidLines = append(invalidLines, truncate(line, 60))
			continue
		}
		items = append(items, importItem{Key: key, Note: note})
	}
	added, skipped, invalid := 0, 0, len(invalidLines)
	for _, it := range items {
		ok, err := s.store.AddUpstreamKey(it.Key, it.Note)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if ok {
			added++
		} else {
			skipped++
		}
	}
	if added > 0 {
		if err := s.pool.Reload(); err != nil {
			writeAPIError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	logger.Infof("文本导入 U1S1 Key: 新增 %d 跳过 %d 无效 %d", added, skipped, invalid)
	writeAPIData(w, http.StatusOK, map[string]any{
		"added": added, "skipped": skipped, "invalid": invalid, "invalid_lines": invalidLines,
	})
}

func parseID(r *http.Request) (int64, error) {
	return strconv.ParseInt(r.PathValue("id"), 10, 64)
}

func (s *Server) handleDeleteUpstreamKey(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "无效 id")
		return
	}
	if err := s.store.DeleteUpstreamKey(id); err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.pool.Reload(); err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	logger.Infof("删除 U1S1 Key #%d", id)
	writeAPIData(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleSetUpstreamKeyStatus(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "无效 id")
		return
	}
	var body struct {
		Status string `json:"status"` // active | disabled
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil ||
		(body.Status != "active" && body.Status != "disabled") {
		writeAPIError(w, http.StatusBadRequest, "status 只支持 active / disabled")
		return
	}
	if err := s.store.SetUpstreamKeyStatus(id, body.Status, time.Time{}, ""); err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.pool.Reload(); err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeAPIData(w, http.StatusOK, map[string]any{"ok": true, "status": body.Status})
}

// checkQuotaFor 拉取单把 key 的配额并落库，返回 (me, error)。
func (s *Server) checkQuotaFor(id int64) (*store.UpstreamKey, error) {
	rows, err := s.store.ListUpstreamKeys()
	if err != nil {
		return nil, err
	}
	var target *store.UpstreamKey
	for _, k := range rows {
		if k.ID == id {
			target = k
			break
		}
	}
	if target == nil {
		return nil, fmt.Errorf("key #%d 不存在", id)
	}
	cli := s.client()
	if cli == nil {
		return nil, fmt.Errorf("上游客户端不可用")
	}
	ctx, cancel := contextTimeout(20 * time.Second)
	defer cancel()
	me, err := cli.Me(ctx, target.Key)
	if err != nil {
		var apiErr *upstream.APIError
		if asAPIError(err, &apiErr) && apiErr.StatusCode == 401 {
			_ = s.store.SetUpstreamKeyStatus(id, "disabled", time.Time{}, "key 无效或已被禁用（401）")
			_ = s.pool.Reload()
		}
		return nil, err
	}
	if err := s.store.UpdateUpstreamQuota(id, me.Email, me.TokensPerUSD, me.DailyFreeRemainingUSD, me.RemainingUSD, me.FreeClaim); err != nil {
		return nil, err
	}
	_ = s.pool.Reload()
	fresh, err := s.store.ListUpstreamKeys()
	if err != nil {
		return nil, err
	}
	for _, k := range fresh {
		if k.ID == id {
			return k, nil
		}
	}
	return target, nil
}

func contextTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

func (s *Server) handleCheckUpstreamQuota(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "无效 id")
		return
	}
	key, err := s.checkQuotaFor(id)
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeAPIData(w, http.StatusOK, key)
}

func (s *Server) handleCheckAllQuotas(w http.ResponseWriter, _ *http.Request) {
	// 与北京时间 0 点的自动刷新互斥：同一时间只允许一轮全量检查。
	if !s.quotaChecking.CompareAndSwap(false, true) {
		writeAPIError(w, http.StatusConflict, "已有一次全量配额检查在进行中（可能是定时刷新），请稍后再试")
		return
	}
	defer s.quotaChecking.Store(false)

	results, okCount, total, err := s.checkAllQuotas()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	logger.Infof("批量检查配额完成: %d/%d 成功", okCount, total)
	writeAPIData(w, http.StatusOK, map[string]any{"results": results, "ok": okCount, "total": total})
}

// ---- 本地分发 Key ----

func randomLocalKey() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "sk-u1s12-" + hex.EncodeToString(buf), nil
}

func (s *Server) handleListLocalKeys(w http.ResponseWriter, _ *http.Request) {
	keys, err := s.store.ListLocalKeys()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if keys == nil {
		keys = []*store.LocalKey{}
	}
	// 列表不回明文；完整 key 通过 POST /api/local-keys/{name}/copy 取回。
	for _, k := range keys {
		k.Key = ""
	}
	writeAPIData(w, http.StatusOK, map[string]any{"keys": keys})
}

func (s *Server) handleCreateLocalKey(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
		Note string `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && err != io.EOF {
		writeAPIError(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	key, err := randomLocalKey()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	created, err := s.store.CreateLocalKey(body.Name, key, body.Note)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	logger.Infof("新建本地 API Key: %s", created.Name)
	// 创建响应返回完整 key（仅此一次），列表里只回掩码。
	full := *created
	full.Key = key
	writeAPIData(w, http.StatusOK, full)
}

func (s *Server) handleUpdateLocalKey(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var body struct {
		Note    *string `json:"note"`
		Enabled *bool   `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIError(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	fields := map[string]any{}
	if body.Note != nil {
		fields["note"] = *body.Note
	}
	if body.Enabled != nil {
		enabledInt := 0
		if *body.Enabled {
			enabledInt = 1
		}
		fields["enabled"] = enabledInt
	}
	if err := s.store.UpdateLocalKey(name, fields); err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeAPIData(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleDeleteLocalKey(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.store.DeleteLocalKey(name); err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	logger.Infof("删除本地 API Key: %s", name)
	writeAPIData(w, http.StatusOK, map[string]any{"ok": true})
}

// handleCopyLocalKey 按名称取回完整密钥供复制（仅管理员登录后可调用）。
func (s *Server) handleCopyLocalKey(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	k, err := s.store.GetLocalKeyByName(name)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, err.Error())
		return
	}
	writeAPIData(w, http.StatusOK, map[string]any{"name": k.Name, "key": k.Key})
}

// ---- 请求记录 ----

func (s *Server) handleListRequests(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	filter := store.RequestFilter{
		Model:   q.Get("model"),
		Status:  q.Get("status"),
		KeyName: q.Get("api_key_name"),
		Limit:   limit,
		Offset:  offset,
	}
	items, err := s.store.ListRequests(filter)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	total, err := s.store.CountRequests(filter)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeAPIData(w, http.StatusOK, map[string]any{"items": items, "total": total})
}

func (s *Server) handleRequestStats(w http.ResponseWriter, r *http.Request) {
	daysStr := r.URL.Query().Get("range")
	days := 0 // 全部
	switch daysStr {
	case "1d":
		days = 1
	case "3d":
		days = 3
	case "7d":
		days = 7
	case "30d":
		days = 30
	}
	stats, err := s.store.RequestStats(days)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeAPIData(w, http.StatusOK, stats)
}

func (s *Server) handleClearRequests(w http.ResponseWriter, _ *http.Request) {
	if err := s.store.ClearRequests(); err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	logger.Infof("清空请求记录")
	writeAPIData(w, http.StatusOK, map[string]any{"ok": true})
}

// ---- 设置 ----

func (s *Server) handleGetSettings(w http.ResponseWriter, _ *http.Request) {
	cfg := s.getSettings()
	writeAPIData(w, http.StatusOK, map[string]any{
		"host":                  cfg.Host,
		"port":                  cfg.Port,
		"has_password":          cfg.AdminPassword != "",
		"upstream_base_url":     cfg.UpstreamBaseURL,
		"egress_proxy":          cfg.EgressProxyURL,
		"fingerprint_profile":   cfg.FingerprintProfile,
		"u1s1_version":          cfg.U1S1Version,
		"bark_key":              cfg.BarkKey,
		"site_feed_check_hours": cfg.SiteFeedCheckHours,
		"log_level":             cfg.LogLevel,
		"profiles":              profileSummaries(s.fp),
		"current_profile":       s.fp.Current().ID,
		// 当前身份的完整构成，便于在后台一眼核对"我们到底像谁"（五者必须同源）。
		"identity":              identitySummary(s.fp),
		"quota_auto_refresh":    cfg.QuotaAutoRefresh,
		"next_quota_refresh_at": s.nextQuotaCheckAtSnapshot(),
	})
}

// profileSummaries 后台可选档案。**auto 排第一**且是当前默认：
// 身份由部署机真实环境派生（ADR 0002），其余是手工伪装逃生口。
func profileSummaries(m *fingerprint.Manager) []map[string]string {
	auto := m.Current()
	out := make([]map[string]string, 0, len(fingerprint.Profiles)+1)
	out = append(out, map[string]string{
		"id":          fingerprint.ProfileIDAuto,
		"label":       "auto（本机真实环境）",
		"user_agent":  fingerprint.UserAgent(auto),
		"runtime":     auto.RuntimeVersion,
		"device_name": fingerprint.DeviceName(auto),
	})
	for _, p := range fingerprint.Profiles {
		out = append(out, map[string]string{
			"id":          p.ID,
			"label":       p.Label,
			"user_agent":  fingerprint.UserAgent(p),
			"runtime":     p.RuntimeVersion,
			"device_name": fingerprint.DeviceName(p),
		})
	}
	return out
}

// identitySummary 当前生效身份的完整构成（platform / 内核 / 主机名 / device_name / UA）。
func identitySummary(m *fingerprint.Manager) map[string]any {
	p := m.Current()
	return map[string]any{
		"auto":           m.IsAuto(),
		"id":             p.ID,
		"hostname":       p.Hostname,
		"platform":       fingerprint.ClientPlatform(p),
		"kernel":         p.UARelease,
		"stainless_os":   p.StainlessOS,
		"stainless_arch": p.StainlessArch,
		"node_version":   p.RuntimeVersion,
		"user_agent":     fingerprint.UserAgent(p),
		"device_name":    fingerprint.DeviceName(p),
		"x_u1s1_client":  fingerprint.ClientSurface,
	}
}

func (s *Server) handleSaveSettings(w http.ResponseWriter, r *http.Request) {
	var body struct {
		UpstreamBaseURL    *string `json:"upstream_base_url,omitempty"`
		EgressProxy        *string `json:"egress_proxy,omitempty"`
		FingerprintProfile *string `json:"fingerprint_profile,omitempty"`
		U1S1Version        *string `json:"u1s1_version,omitempty"`
		BarkKey            *string `json:"bark_key,omitempty"`
		SiteFeedCheckHours *int    `json:"site_feed_check_hours,omitempty"`
		AdminPassword      *string `json:"admin_password,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIError(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}

	patch := config.PatchableFields(s.getSettings())
	if body.UpstreamBaseURL != nil {
		v := strings.TrimSpace(*body.UpstreamBaseURL)
		if v != "" && !strings.HasPrefix(v, "http://") && !strings.HasPrefix(v, "https://") {
			writeAPIError(w, http.StatusBadRequest, "UPSTREAM_BASE_URL 必须以 http(s):// 开头")
			return
		}
		if v == "" {
			v = config.DefaultUpstreamBaseURL
		}
		patch[config.KeyUpstreamBaseURL] = strings.TrimRight(v, "/")
	}
	if body.EgressProxy != nil {
		patch[config.KeyEgressProxy] = strings.TrimSpace(*body.EgressProxy)
	}
	if body.FingerprintProfile != nil {
		v := strings.TrimSpace(*body.FingerprintProfile)
		if v != "auto" {
			if _, ok := fingerprint.ProfileByID(v); !ok {
				writeAPIError(w, http.StatusBadRequest, "未知指纹档案: "+v)
				return
			}
		}
		if v == "auto" {
			patch[config.KeyFingerprintProfile] = "auto"
			// auto：保持当前档案不变（不重掷）
		} else if err := s.fp.SetProfile(v); err != nil {
			writeAPIError(w, http.StatusInternalServerError, err.Error())
			return
		}
		patch[config.KeyFingerprintProfile] = v
	}
	if body.U1S1Version != nil {
		v := strings.TrimSpace(*body.U1S1Version)
		if v == "" {
			v = config.DefaultU1S1Version
		}
		patch[config.KeyU1S1Version] = v
	}
	if body.BarkKey != nil {
		patch[config.KeyBarkKey] = strings.TrimSpace(*body.BarkKey)
	}
	if body.SiteFeedCheckHours != nil {
		v := *body.SiteFeedCheckHours
		if v <= 0 {
			v = config.DefaultSiteFeedCheckHours
		}
		patch[config.KeySiteFeedCheckHours] = strconv.Itoa(v)
	}
	if body.AdminPassword != nil && strings.TrimSpace(*body.AdminPassword) != "" {
		patch[config.KeyAdminPassword] = strings.TrimSpace(*body.AdminPassword)
	}

	newCfg, err := config.Save(s.projectRoot, patch)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "保存 .env 失败: "+err.Error())
		return
	}
	s.setSettings(newCfg)
	if err := s.rebuildClient(); err != nil {
		logger.Warnf("出口代理重建失败: %v", err)
	}
	logger.Infof("设置已更新并写回 .env")
	writeAPIData(w, http.StatusOK, map[string]any{"ok": true, "fingerprint_profile_applied": s.fp.Current().ID})
}

// rebuildClient 是 server.go 中未导出方法的导出包装（供本文件使用）。
func (s *Server) rebuildClient() error { return s.rebuildClientLocked() }

// ---- 模型测试 ----

func (s *Server) handleChatTest(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Model  string `json:"model"`
		Prompt string `json:"prompt"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Prompt) == "" {
		writeAPIError(w, http.StatusBadRequest, "需要 model 与 prompt 字段")
		return
	}
	if strings.TrimSpace(body.Model) == "" {
		body.Model = "deepseek-v4-flash"
	}
	payload, _ := json.Marshal(map[string]any{
		"model": body.Model,
		"messages": []map[string]string{
			{"role": "user", "content": body.Prompt},
		},
		"stream": false,
	})
	// (v0.9.4) 推理一律用授权官网账号（设备凭证），旧版 u1s1- API Key 已被上游禁止用于推理。
	att, cred, cerr := s.bestDeviceCredential(r.Context())
	if cerr != nil {
		writeAPIError(w, http.StatusServiceUnavailable, cerr.Error())
		return
	}
	started := time.Now()
	resp, cerr := s.deviceClient().DeviceChat(r.Context(), cred, payload, att)
	if cerr != nil {
		var apiErr *upstream.APIError
		if asAPIError(cerr, &apiErr) {
			writeJSON(w, http.StatusBadGateway, map[string]any{
				"detail": fmt.Sprintf("上游 %d: %s", apiErr.StatusCode, truncate(apiErr.Body, 500)),
			})
			return
		}
		writeAPIError(w, http.StatusBadGateway, cerr.Error())
		return
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(ioLimit(resp.Body))
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, "读取上游响应失败: "+err.Error())
		return
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage usageShape `json:"usage"`
		Model string     `json:"model"`
	}
	if jerr := json.Unmarshal(data, &parsed); jerr != nil {
		writeAPIError(w, http.StatusBadGateway, "上游响应解析失败: "+truncate(string(data), 300))
		return
	}
	content := ""
	if len(parsed.Choices) > 0 {
		content = parsed.Choices[0].Message.Content
	}
	inTok, outTok := tokensFrom(parsed.Usage)
	writeAPIData(w, http.StatusOK, map[string]any{
		"content":       content,
		"model":         parsed.Model,
		"input_tokens":  inTok,
		"output_tokens": outTok,
		"duration_ms":   time.Since(started).Milliseconds(),
	})
}

func ioLimit(r io.Reader) io.Reader { return io.LimitReader(r, 16<<20) }

// ---- 运行日志 ----

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	sinceID, _ := strconv.ParseInt(q.Get("since_id"), 10, 64)
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	entries := logging.Recent(sinceID, limit, q.Get("level"))
	writeAPIData(w, http.StatusOK, map[string]any{"items": entries})
}

// ---- 出口代理连通性测试 ----

func (s *Server) handleProxyTest(w http.ResponseWriter, r *http.Request) {
	var body struct {
		EgressProxy string `json:"egress_proxy"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	proxyURL := strings.TrimSpace(body.EgressProxy)
	testCli, err := upstream.NewClient("https://api.u1s1.io/v1", proxyURL, s.fp, func() string { return "test" })
	if err != nil {
		writeAPIData(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	ctx, cancel := contextTimeout(15 * time.Second)
	defer cancel()
	// 用一个无效 key 探测可达性：能拿到 401 说明网络通。
	req, _ := http.NewRequestWithContext(ctx, "GET", "https://api.u1s1.io/v1/models", nil)
	for k, v := range map[string]string{"authorization": "Bearer probe", "x-u1s1-version": "probe", "user-agent": fingerprint.UndiciUserAgent} {
		req.Header.Set(k, v)
	}
	resp, perr := testCli.RawDo(req)
	if perr != nil {
		writeAPIData(w, http.StatusOK, map[string]any{"ok": false, "error": perr.Error()})
		return
	}
	defer resp.Body.Close()
	writeAPIData(w, http.StatusOK, map[string]any{"ok": true, "status": resp.StatusCode})
}
