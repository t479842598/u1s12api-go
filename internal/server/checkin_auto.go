// 每日自动签到：U1S1 官网「每日登录打卡」发放 200 万 Token 全模型加量包。
// 有密码的账号走网页打卡：capcat 人机验证 → 网页登录 → claim 领取加量包（纯 API，无需浏览器）；
// 无密码的账号回退设备凭证（DPoP）调 GET /v1/me 的旧机制，尽力而为。
// 服务端在北京时间 0 点后对每个已授权且启用的账号自动执行一次，并记录签到状态。
package server

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/t479842598/u1s12api-go/internal/store"
	"github.com/t479842598/u1s12api-go/internal/upstream"
)

// checkinBuffer 签到缓冲：北京时间 0 点刚过让上游完成重置。
const checkinBuffer = 2 * time.Minute

// checkinStartupDelay 启动补签延迟。
const checkinStartupDelay = 20 * time.Second

// checkinResult 单账号签到结果（JSON 输出）。
type checkinResult struct {
	AccountID int64  `json:"account_id"`
	Email     string `json:"email"`
	OK        bool   `json:"ok"`
	Remaining int64  `json:"remaining"`
	Error     string `json:"error,omitempty"`
}

// checkinOnce 单账号签到：
//   - 有密码的账号走「网页打卡」：capcat 求解 → 网页登录 → claim 领取 200 万/日加量包；
//   - 无密码的账号回退设备凭证调 /v1/me（旧机制，尽力而为）。
func (s *Server) checkinOne(id int64) error {
	acc, err := s.store.GetAccount(id)
	if err != nil {
		return err
	}
	if !acc.Authorized || !acc.Enabled {
		return nil
	}
	if acc.HasPassword {
		return s.webCheckinOne(acc)
	}
	return s.dpopCheckinOne(acc)
}

// webCheckinOne 网页打卡：capcat → 登录 → claim，并记录状态。
func (s *Server) webCheckinOne(acc *store.Account) error {
	// scanAccount 不回明文密码，这里单独直查。
	email, password, err := s.store.GetAccountCredential(acc.ID)
	if err != nil {
		return err
	}
	svc, err := s.webCheckinService()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	res, err := svc.CheckIn(ctx, email, password)
	if err != nil {
		_ = s.store.MarkAccountWebCheckin(acc.ID, "失败："+truncate(err.Error(), 200))
		return err
	}
	_ = s.store.MarkAccountCheckin(acc.ID, res.Tokens)
	status := fmt.Sprintf("已打卡 %d 万 Token（连续 %d 天）", res.Tokens/10000, res.Streak)
	if res.Tokens > 0 && res.Tokens%10000 != 0 {
		status = fmt.Sprintf("已打卡 %d Token（连续 %d 天）", res.Tokens, res.Streak)
	}
	_ = s.store.MarkAccountWebCheckin(acc.ID, status)
	return nil
}

// dpopCheckinOne 旧机制：用设备凭证调 /v1/me 触发加量包发放。
func (s *Server) dpopCheckinOne(acc *store.Account) error {
	cred, err := upstream.AccountToCredential(acc.DeviceToken, acc.DevicePrivateJWK, acc.DevicePublicJWK)
	if err != nil {
		return err
	}
	dc := s.deviceClient()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	me, err := dc.DeviceMe(ctx, cred)
	if err != nil {
		return err
	}
	remaining := int64(-1)
	if me.LoginCheckinRemaining != nil {
		remaining = *me.LoginCheckinRemaining
	}
	return s.store.MarkAccountCheckin(acc.ID, remaining)
}

// runCheckinAll 全量签到（手动接口 + 自动任务共用）。返回逐账号结果、成功数。
func (s *Server) runCheckinAll() ([]checkinResult, int, error) {
	accounts, err := s.store.ListAuthorizedEnabledAccounts()
	if err != nil {
		return nil, 0, err
	}
	results := make([]checkinResult, 0, len(accounts))
	okCount := 0
	for _, a := range accounts {
		res := checkinResult{AccountID: a.ID, Email: a.Email, Remaining: -1}
		if err := s.checkinOne(a.ID); err != nil {
			res.Error = truncate(err.Error(), 200)
			results = append(results, res)
			continue
		}
		// 重新读拿到剩余。
		if updated, e := s.store.GetAccount(a.ID); e == nil {
			res.Remaining = updated.LoginCheckinRemaining
		}
		res.OK = true
		okCount++
		results = append(results, res)
	}
	return results, okCount, nil
}

// checkinAutoLoop 每天北京时间 0 点+缓冲后自动对全部已授权账号做一次签到。
func (s *Server) checkinAutoLoop() {
	// 启动补签：若今天（北京时间）还没签过。
	s.catchUpCheckin()
	for {
		now := time.Now()
		next := nextQuotaRefreshTime(now) // 复用 quotaAutoRefresh 的 0 点+缓冲计算
		wait := next.Sub(now)
		if wait <= 0 {
			wait = time.Second
		}
		logger.Infof("签到定时刷新：下次 %s", next.In(beijingLoc()).Format("2006-01-02 15:04:05"))
		time.Sleep(wait)
		s.runCheckinAuto()
	}
}

// catchUpCheckin 启动补签：今天（北京时间）尚未签到则补一轮。
func (s *Server) catchUpCheckin() {
	// 以「最近一次签到时间」判断今天是否已签到。
	if s.store.LatestCheckinAt() >= todayBeijingStart().Unix() {
		return
	}
	logger.Infof("今天（北京时间）尚未签到，%s 后自动补签一轮", checkinStartupDelay)
	time.Sleep(checkinStartupDelay)
	s.runCheckinAuto()
}

// checkinRunning 防止自动/手动签到并发重入。
var checkinRunning sync.Mutex

// runCheckinAuto 自动签到入口。
func (s *Server) runCheckinAuto() {
	if !checkinRunning.TryLock() {
		logger.Infof("签到定时刷新：已有一次全量签到在执行中，本轮跳过")
		return
	}
	defer checkinRunning.Unlock()
	results, okCount, err := s.runCheckinAll()
	if err != nil {
		logger.Errorf("自动签到失败: %v", err)
		return
	}
	logger.Infof("自动签到完成: 成功 %d 共 %d", okCount, len(results))
	for _, res := range results {
		if !res.OK {
			logger.Warnf("签到失败: account=%s %s", res.Email, res.Error)
		}
	}
}
