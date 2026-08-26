// 北京时间 0 点自动刷新配额：上游 U1S1 免费额度每天北京时间 0 点重置，
// 服务端在重置后自动对全部上游 Key 执行一轮配额检查（与后台「一键刷新」
// 等价）：额度耗尽冷却中的 key 恢复 active，面板额度数字同步更新，
// 无需每天人工到后台点刷新。
package server

import (
	"time"
)

// quotaRefreshBuffer 额度重置缓冲：0 点刚过时上游可能尚未完成重置，
// 延迟一段时间再刷，避免把旧额度当成新一天的值写进面板。
const quotaRefreshBuffer = 2 * time.Minute

// quotaCheckInterval 全量检查逐把限速间隔（与手动一键刷新一致，避免触发风控）。
const quotaCheckInterval = 300 * time.Millisecond

// startupCatchUpDelay 启动补刷延迟：给上游客户端构建等初始化留出时间。
const startupCatchUpDelay = 15 * time.Second

// beijingLoc 北京时间（免费额度重置点所在时区）。
func beijingLoc() *time.Location { return time.FixedZone("CST", 8*3600) }

// todayBeijingStart 今天（北京时间）0 点。
func todayBeijingStart() time.Time {
	t := time.Now().In(beijingLoc())
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, beijingLoc())
}

// nextQuotaRefreshTime 下一次自动刷新时间点：
//   - 当前已越过今天北京时间 0 点但还在缓冲窗口内 → 今天 0 点+缓冲；
//   - 其余情况 → 明天北京时间 0 点+缓冲。
func nextQuotaRefreshTime(now time.Time) time.Time {
	loc := beijingLoc()
	t := now.In(loc)
	midnight := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
	if now.Sub(midnight) <= quotaRefreshBuffer {
		return midnight.Add(quotaRefreshBuffer)
	}
	return midnight.Add(24*time.Hour + quotaRefreshBuffer)
}

// quotaAutoRefreshLoop 每天北京时间 0 点+缓冲执行一轮全量配额检查。
// 与 refreshModelsLoop 同风格：随进程常驻，不单独做退出通知。
func (s *Server) quotaAutoRefreshLoop() {
	s.catchUpQuotaRefresh()
	for {
		now := time.Now()
		next := nextQuotaRefreshTime(now)
		s.nextQuotaCheckAt.Store(next.Unix())
		wait := next.Sub(now)
		if wait <= 0 {
			wait = time.Second // 时钟回拨等异常情况的兜底，避免忙转
		}
		logger.Infof("配额定时刷新：下次 %s（北京时间 0 点+%s）",
			next.In(beijingLoc()).Format("2006-01-02 15:04:05"), quotaRefreshBuffer)
		time.Sleep(wait)
		s.runQuotaAutoRefresh()
	}
}

// catchUpQuotaRefresh 启动补刷：服务若跨北京时间 0 点停过机（或当天从未
// 检查过配额），启动后补一轮，避免面板一直停留在昨天的旧数据。
func (s *Server) catchUpQuotaRefresh() {
	if s.store.LatestUpstreamQuotaCheckedAt() >= todayBeijingStart().Unix() {
		return
	}
	logger.Infof("今天（北京时间）尚未检查过配额，%s 后自动补刷一轮", startupCatchUpDelay)
	time.Sleep(startupCatchUpDelay)
	s.runQuotaAutoRefresh()
}

// runQuotaAutoRefresh 自动触发入口：抢互斥锁（与手动一键刷新互斥），执行并记日志。
func (s *Server) runQuotaAutoRefresh() {
	if !s.quotaChecking.CompareAndSwap(false, true) {
		logger.Infof("配额定时刷新：已有一次全量检查在执行中，本轮跳过")
		return
	}
	defer s.quotaChecking.Store(false)

	results, okCount, total, err := s.checkAllQuotas()
	if err != nil {
		logger.Errorf("配额定时刷新失败: %v", err)
		return
	}
	logger.Infof("配额定时刷新完成: 成功 %d 失败 %d 共 %d", okCount, len(results)-okCount, total)
	for _, r := range results {
		if !r.OK {
			logger.Warnf("配额定时刷新：key #%d 检查失败: %s", r.ID, r.Error)
		}
	}
}

// checkAllQuotas 全量检查所有上游 Key 配额（调用方需已持有 quotaChecking 互斥权）。
// 返回逐 key 结果、成功数、总数。手动接口与定时刷新共用此实现。
func (s *Server) checkAllQuotas() ([]quotaCheckResult, int, int, error) {
	keys, err := s.store.ListUpstreamKeys()
	if err != nil {
		return nil, 0, 0, err
	}
	results := make([]quotaCheckResult, 0, len(keys))
	okCount := 0
	for _, k := range keys {
		if _, cerr := s.checkQuotaFor(k.ID); cerr != nil {
			results = append(results, quotaCheckResult{ID: k.ID, OK: false, Error: truncate(cerr.Error(), 200)})
			continue
		}
		results = append(results, quotaCheckResult{ID: k.ID, OK: true})
		okCount++
		time.Sleep(quotaCheckInterval) // 温和限速，避免触发风控
	}
	return results, okCount, len(keys), nil
}

// quotaCheckResult 单把 key 的检查结果（手动接口 JSON 输出保持原字段）。
type quotaCheckResult struct {
	ID    int64  `json:"id"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// nextQuotaCheckAtSnapshot 供管理端展示下次自动刷新时间（unix 秒；0=未排程）。
func (s *Server) nextQuotaCheckAtSnapshot() int64 { return s.nextQuotaCheckAt.Load() }
