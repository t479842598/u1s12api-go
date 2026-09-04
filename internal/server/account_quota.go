package server

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/t479842598/u1s12api-go/internal/fingerprint"
	"github.com/t479842598/u1s12api-go/internal/store"
)

// ---- 账号额度视图（管理页/概览展示用） ----

// accountQuotaItem 单个额度分组的剩余与容量。
type accountQuotaItem struct {
	Key       string `json:"key"`
	Label     string `json:"label"`
	Remaining int64  `json:"remaining"`
	Total     int64  `json:"total"` // 容量（进度条分母，daily_tokens/total_tokens，未知取剩余）
}

// accountQuotaView 账号加量包分组视图：总剩余 + 总容量（进度条）+ 各分组明细。
type accountQuotaView struct {
	Total     int64              `json:"total"`    // 剩余合计
	Capacity  int64              `json:"capacity"` // 容量合计（进度条分母）
	UpdatedAt int64              `json:"updated_at"`
	Items     []accountQuotaItem `json:"items"`
}

// quotaGroupMeta 分组定义：key/label/匹配 kind 集合。
var quotaGroupMeta = []struct {
	key   string
	label string
	kinds map[string]bool
}{
	{"fixed", "固定额度", map[string]bool{"payment": true, "admin_grant": true, "new_user": true, "gift": true, "payment_delay_gift": true}},
	{"daily", "每日赠送", map[string]bool{"free_first": true, "free_renew": true}},
	{"invite", "邀请额度", map[string]bool{"invite": true}},
	{"checkin", "签到打卡", map[string]bool{"login_checkin": true, "login_checkin_bonus": true}},
}

func quotaGroupKey(kind string) string {
	for _, g := range quotaGroupMeta {
		if g.kinds[kind] {
			return g.key
		}
	}
	return "other"
}

func quotaGroupLabel(key string) string {
	for _, g := range quotaGroupMeta {
		if g.key == key {
			return g.label
		}
	}
	return "其他"
}

// packageCapacity 单个加量包的容量（进度条分母）：优先每日额度，其次总包额度，兜底剩余。
func packageCapacity(p store.AccountPackage) int64 {
	if p.DailyTokens != nil && *p.DailyTokens > 0 {
		return *p.DailyTokens
	}
	if p.TotalTokens != nil && *p.TotalTokens > 0 {
		return *p.TotalTokens
	}
	return p.Remaining
}

// buildAccountQuotaView 从账号加量包快照构建分组视图（含容量，供进度条展示）。
// 分组含剩余 > 0 的包；组容量为其全部包的容量合计，剩余为剩余合计。
func buildAccountQuotaView(acc *store.Account) accountQuotaView {
	view := accountQuotaView{UpdatedAt: acc.UpdatedAt, Items: []accountQuotaItem{}}
	if acc.PackagesJSON == "" {
		return view
	}
	var pkgs []store.AccountPackage
	if err := json.Unmarshal([]byte(acc.PackagesJSON), &pkgs); err != nil {
		return view
	}
	groupRem := map[string]int64{}
	groupCap := map[string]int64{}
	totalRem, totalCap := int64(0), int64(0)
	for _, p := range pkgs {
		if p.Remaining <= 0 {
			continue
		}
		key := quotaGroupKey(p.Kind)
		cap := packageCapacity(p)
		groupRem[key] += p.Remaining
		groupCap[key] += cap
		totalRem += p.Remaining
		totalCap += cap
	}
	view.Total = totalRem
	view.Capacity = totalCap
	for _, g := range quotaGroupMeta {
		if r, ok := groupRem[g.key]; ok && r > 0 {
			view.Items = append(view.Items, accountQuotaItem{Key: g.key, Label: g.label, Remaining: r, Total: groupCap[g.key]})
		}
	}
	if r, ok := groupRem["other"]; ok && r > 0 {
		view.Items = append(view.Items, accountQuotaItem{Key: "other", Label: "其他", Remaining: r, Total: groupCap["other"]})
	}
	return view
}

// refreshAccountQuota 用设备凭证调 /v1/me 刷新账号加量包快照并入库。
// 返回该账号 login_checkin 类包的剩余合计。
func (s *Server) refreshAccountQuota(ctx context.Context, id int64) (int64, error) {
	acc, err := s.store.GetAccount(id)
	if err != nil {
		return 0, err
	}
	if acc.DeviceToken == "" {
		return 0, fmt.Errorf("账号 %s 没有设备凭证", acc.EmailMasked)
	}
	cred, err := s.accountCredential(acc)
	if err != nil {
		return 0, err
	}
	dc := s.deviceClient()
	me, err := dc.DeviceMe(ctx, cred, fingerprint.UndiciUserAgent)
	if err != nil {
		return 0, err
	}
	raw, err := json.Marshal(me.Packages)
	if err != nil {
		return 0, err
	}
	loginRemaining := int64(0)
	for _, p := range me.Packages {
		if p.Kind == "login_checkin" || p.Kind == "login_checkin_bonus" {
			loginRemaining += p.Remaining
		}
	}
	if err := s.store.SaveAccountQuota(id, string(raw), loginRemaining); err != nil {
		return 0, err
	}
	return loginRemaining, nil
}

// refreshAllDeviceQuotas 刷新全部已授权且启用的账号额度快照（0 点自动刷新/手动共用）。
// 调用方需自行限速。返回成功/失败数。
func (s *Server) refreshAllDeviceQuotas() (okCount, failCount int) {
	accounts, err := s.store.ListAuthorizedEnabledAccounts()
	if err != nil {
		logger.Errorf("刷新账号额度: 列表失败: %v", err)
		return 0, 0
	}
	for _, a := range accounts {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		_, err := s.refreshAccountQuota(ctx, a.ID)
		cancel()
		if err != nil {
			logger.Warnf("刷新账号额度: %s 失败: %v", a.EmailMasked, err)
			failCount++
			continue
		}
		okCount++
		time.Sleep(quotaCheckInterval) // 与 key 配额检查一致的温和限速
	}
	return okCount, failCount
}
