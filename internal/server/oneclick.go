// U1S1 原生「一键登录」：无需在后台手动填官网邮箱+密码。
// 点「一键登录」→ 网关生成 EC P-256 密钥 → 调 /auth/device/start 领 verify_url →
// 用户浏览器用 U1S1 官网账号登录并批准设备 → 网关轮询 /auth/device/poll 拿
// api_key + device_token + JWK → 自动用设备凭证调 /v1/me 取邮箱 → 自动建账号
// （accounts 表，密码留空）→ 存设备凭证 → 把 api_key 导入 Key 池 → 签到。
// 从此该账号进入「授权账号」独立列表，可被设备凭证通道调用、参与每日签到，
// 全程不需要在网关侧输入 U1S1 账号密码。
package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/t479842598/u1s12api-go/internal/fingerprint"
	"github.com/t479842598/u1s12api-go/internal/store"
	"github.com/t479842598/u1s12api-go/internal/upstream"
)

// oneClickPending 一次进行中的一键登录（内存态，不落盘；服务重启需重新发起）。
type oneClickPending struct {
	sessionID  string
	pollSecret string
	interval   int
	expiresIn  int
	privJWK    *upstream.DeviceJWK
	pubJWK     *upstream.DeviceJWK
	deviceName string
	startedAt  time.Time
}

type oneClickMap struct {
	mu sync.Mutex
	m  map[string]*oneClickPending
}

func (p *oneClickMap) put(d *oneClickPending) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.m == nil {
		p.m = map[string]*oneClickPending{}
	}
	p.m[d.sessionID] = d
}

func (p *oneClickMap) get(sessionID string) (*oneClickPending, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.m == nil {
		return nil, false
	}
	d, ok := p.m[sessionID]
	return d, ok
}

func (p *oneClickMap) del(sessionID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.m == nil {
		return
	}
	delete(p.m, sessionID)
}

func randomSessionID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// handleOneClickStart 发起 U1S1 一键登录：生成密钥 → /auth/device/start → 返回 verify_url。
func (s *Server) handleOneClickStart(w http.ResponseWriter, r *http.Request) {
	dc := s.deviceClient()
	if dc == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "设备客户端不可用（检查上游地址/出口代理）")
		return
	}
	privJWK, pubJWK, start, err := dc.StartDeviceLoginGenerate(r.Context(), "u1s12api-oneclick", s.getSettings().U1S1Version)
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, "发起 U1S1 一键登录失败: "+err.Error())
		return
	}
	sessionID := randomSessionID()
	s.oneClick.put(&oneClickPending{
		sessionID:  sessionID,
		pollSecret: start.PollSecret,
		interval:   start.Interval,
		expiresIn:  start.ExpiresIn,
		privJWK:    privJWK,
		pubJWK:     pubJWK,
		deviceName: s.fp.DeviceName(), // 官方格式，不带本项目标识
		startedAt:  time.Now(),
	})
	logger.Infof("一键登录已发起: session=%s verify_url=%s", sessionID, start.VerifyURL)
	writeAPIData(w, http.StatusOK, map[string]any{
		"session_id": sessionID,
		"verify_url": start.VerifyURL,
		"expires_in": start.ExpiresIn,
		"interval":   start.Interval,
	})
}

// handleOneClickConfirm 用户在浏览器用 U1S1 账号登录并批准后，轮询拿设备凭证，
// 自动建账号（邮箱来自 /v1/me）+ 存凭证 + api_key 入池 + 签到。
// 单次轮询（不阻塞），前端反复轮询直到 status=authorized。
func (s *Server) handleOneClickConfirm(w http.ResponseWriter, r *http.Request) {
	sessionID := r.URL.Query().Get("session_id")
	if sessionID == "" {
		var body struct {
			SessionID string `json:"session_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err == nil {
			sessionID = body.SessionID
		}
	}
	if sessionID == "" {
		writeAPIError(w, http.StatusBadRequest, "缺少 session_id")
		return
	}
	pd, ok := s.oneClick.get(sessionID)
	if !ok {
		writeAPIError(w, http.StatusBadRequest, "该会话不存在或已过期，请重新发起一键登录")
		return
	}
	dc := s.deviceClient()
	if dc == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "设备客户端不可用")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	resp, err := dc.PollDeviceLoginOnce(ctx, pd.pollSecret)
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, "轮询 U1S1 设备批准失败: "+err.Error())
		return
	}
	if resp == nil {
		logger.Infof("一键登录确认: session=%s 尚未批准，返回 pending", sessionID)
		writeAPIData(w, http.StatusOK, map[string]any{"status": "pending"})
		return
	}

	// 批准成功。取邮箱：用设备凭证调 /v1/me；失败时用 device_id 兜底。
	email := ""
	cred, cerr := upstream.AccountToCredential(resp.DeviceToken, string(mustJSON(pd.privJWK)), string(mustJSON(pd.pubJWK)),
		"", s.fp.Current())
	if cerr == nil {
		if me, merr := dc.DeviceMe(ctx, cred, fingerprint.UndiciUserAgent); merr == nil && me.Email != "" {
			email = me.Email
		}
	} else {
		logger.Warnf("一键登录凭证解析失败: %v", cerr)
	}
	if email == "" {
		email = "u1s1-device-" + resp.DeviceID.String()
		logger.Warnf("一键登录: /v1/me 未取到邮箱，使用兜底账号 email=%s", email)
	}

	// 创建或复用账号（密码留空，后续仍可在「授权账号」手动补录/启用）。
	var accountID int64
	if ok, aerr := s.store.AddAccount(email, "", "一键登录 U1S1"); aerr != nil {
		writeAPIError(w, http.StatusInternalServerError, "创建账号失败: "+aerr.Error())
		return
	} else if ok {
		acc, gerr := s.store.GetAccountByEmail(email)
		if gerr == nil {
			accountID = acc.ID
		}
	} else {
		// 邮箱已存在（可能是之前手动录入/一键登录过）：复用。
		if acc, gerr := s.store.GetAccountByEmail(email); gerr == nil {
			accountID = acc.ID
		}
	}
	if accountID == 0 {
		writeAPIError(w, http.StatusInternalServerError, "账号创建/定位失败")
		return
	}

	// 存设备凭证并标记已授权。
	privJSON := mustJSON(pd.privJWK)
	pubJSON := mustJSON(pd.pubJWK)
	if err := s.store.SaveAccountDeviceCredential(accountID, resp.DeviceToken, resp.APIKey,
		resp.DeviceID.String(), string(privJSON), string(pubJSON), pd.deviceName, s.currentIdentityJSON()); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "保存设备凭证失败: "+err.Error())
		return
	}

	// 提取到的 api_key 自动加入 Key 池（INSERT OR IGNORE，不覆盖已有 key）。
	if resp.APIKey != "" {
		if added, e := s.store.AddUpstreamKey(resp.APIKey, "一键登录 "+email+" 自动导入"); e != nil {
			logger.Warnf("一键登录导入 api_key 到 Key 池失败 email=%s: %v", email, e)
		} else if added {
			_ = s.pool.Reload()
			logger.Infof("一键登录 api_key 已导入 Key 池 email=%s", email)
		}
	}

	// 授权后立刻签到（调 /v1/me 触发加量包发放）。
	_ = s.checkinOne(accountID)

	s.oneClick.del(sessionID)
	logger.Infof("一键登录成功: account=%d email=%s device_id=%s", accountID, email, resp.DeviceID.String())
	writeAPIData(w, http.StatusOK, map[string]any{
		"status":       "authorized",
		"authorized":   true,
		"account_id":   accountID,
		"email":        email,
		"device_id":    resp.DeviceID.String(),
		"api_key":      store.MaskKey(resp.APIKey),
		"device_token": store.MaskDeviceToken(resp.DeviceToken),
	})
}

// mustJSON 把对象序列化为 JSON（失败返回空）。仅用于内部 JWK 授信对象，忽略错误。
func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
