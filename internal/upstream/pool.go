package upstream

import (
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/t479842598/u1s12api-go/internal/store"
)

// KeyState 池内单把 key 的运行态（DB 行的内存镜像 + 冷却判定）。
type KeyState struct {
	ID           int64
	Key          string
	Status       string // active|cooldown|disabled
	CooldownUntil time.Time
}

// Pool 上游 key 池：轮询选取、按结果冷却/禁用。
type Pool struct {
	mu      sync.RWMutex
	keys    []*KeyState
	rr      atomic.Int64
	store   *store.Store
}

// NewPool 从 DB 加载全部 key。
func NewPool(st *store.Store) (*Pool, error) {
	p := &Pool{store: st}
	if err := p.Reload(); err != nil {
		return nil, err
	}
	return p, nil
}

// Reload 从 DB 重建内存态（管理端增删 key 后调用）。
func (p *Pool) Reload() error {
	rows, err := p.store.ListUpstreamKeys()
	if err != nil {
		return err
	}
	now := time.Now()
	keys := make([]*KeyState, 0, len(rows))
	for _, r := range rows {
		st := &KeyState{ID: r.ID, Key: r.Key, Status: r.Status}
		if r.CooldownUntil > 0 {
			st.CooldownUntil = time.Unix(r.CooldownUntil, 0)
			if st.CooldownUntil.Before(now) && st.Status == "cooldown" {
				st.Status = "active"
				st.CooldownUntil = time.Time{}
				_ = p.store.SetUpstreamKeyStatus(r.ID, "active", time.Time{}, "")
			}
		}
		if st.Status == "" {
			st.Status = "active"
		}
		keys = append(keys, st)
	}
	p.mu.Lock()
	p.keys = keys
	p.mu.Unlock()
	return nil
}

var ErrNoKey = errors.New("没有可用的 U1S1 Key（全部禁用或冷却中），请到后台添加或恢复")

// Pick 轮询选一把可用 key（active 且不在冷却期）。找不到返回 ErrNoKey。
func (p *Pool) Pick() (*KeyState, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	n := len(p.keys)
	if n == 0 {
		return nil, ErrNoKey
	}
	now := time.Now()
	start := int(p.rr.Add(1)) % n
	for i := 0; i < n; i++ {
		k := p.keys[(start+i)%n]
		if k.Status != "active" {
			continue
		}
		if !k.CooldownUntil.IsZero() && k.CooldownUntil.After(now) {
			continue
		}
		return k, nil
	}
	return nil, ErrNoKey
}

// ReportResult 把一次上游调用的结果反馈进池：
//   - 401                → disabled（key 无效）
//   - 402 / 429+额度用完  → cooldown 到次日北京时间 0 点（免费额度耗尽）
//   - 429 其他            → 短冷却 90s（普通限流）
//   - 2xx                → 清除错误与短暂冷却标记
//
// body 用于识别 quota_exceeded 信号。
func (p *Pool) ReportResult(id int64, statusCode int, body string) {
	p.mu.Lock()
	var ks *KeyState
	for _, k := range p.keys {
		if k.ID == id {
			ks = k
			break
		}
	}
	if ks == nil {
		p.mu.Unlock()
		return
	}
	now := time.Now()
	switch {
	case statusCode == httpUnauthorized:
		ks.Status = "disabled"
		ks.CooldownUntil = time.Time{}
		p.mu.Unlock()
		_ = p.store.SetUpstreamKeyStatus(id, "disabled", time.Time{}, "key 无效或已被禁用（401）")
		return

	case statusCode == httpPaymentRequired || QuotaSignal(statusCode, body):
		until := NextBeijingMidnight(now)
		ks.Status = "cooldown"
		ks.CooldownUntil = until
		p.mu.Unlock()
		_ = p.store.SetUpstreamKeyStatus(id, "cooldown", until, "免费额度已用完，北京时间 0 点恢复")
		return

	case statusCode == httpTooManyRequests:
		until := now.Add(90 * time.Second)
		ks.Status = "cooldown"
		ks.CooldownUntil = until
		p.mu.Unlock()
		_ = p.store.SetUpstreamKeyStatus(id, "cooldown", until, "触发限流（429），90 秒后重试")
		return

	case statusCode >= 200 && statusCode < 300:
		ks.CooldownUntil = time.Time{}
		if ks.Status == "active" || ks.Status == "" {
			p.mu.Unlock()
			return
		}
		// 成功请求本身说明 key 可用：从短暂冷却恢复（disabled 不动）。
		ks.Status = "active"
		p.mu.Unlock()
		_ = p.store.SetUpstreamKeyStatus(id, "active", time.Time{}, "")
		return

	default:
		// 5xx 等：不动状态，由调用方换下一把重试。
		p.mu.Unlock()
	}
}

const (
	httpUnauthorized    = 401
	httpPaymentRequired = 402
	httpTooManyRequests = 429
)

// CountByStatus 各状态数量（含运行时冷却修正）。
func (p *Pool) CountByStatus() map[string]int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := map[string]int{"total": len(p.keys)}
	now := time.Now()
	for _, k := range p.keys {
		st := k.Status
		if st == "cooldown" && !k.CooldownUntil.IsZero() && k.CooldownUntil.Before(now) {
			st = "active"
		}
		out[st]++
	}
	return out
}

// ActiveCount 可立即服务的 key 数量。
func (p *Pool) ActiveCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	now := time.Now()
	n := 0
	for _, k := range p.keys {
		if k.Status == "active" && (k.CooldownUntil.IsZero() || k.CooldownUntil.Before(now)) {
			n++
		}
	}
	return n
}

// DisableKey 把某把 Key 标记为 disabled（上游已封禁该通道，如 u1s1_client_only）。
// 用于“换一把 Key 也必然同样 403”的确定性拒绝：立即禁用让池不再拾取，
// 避免轮询到该 Key 反复触发、并在官方风控里留下频次痕迹。
func (p *Pool) DisableKey(id int64, reason string) {
	p.mu.Lock()
	for _, k := range p.keys {
		if k.ID == id {
			k.Status = "disabled"
			k.CooldownUntil = time.Time{}
			p.mu.Unlock()
			_ = p.store.SetUpstreamKeyStatus(id, "disabled", time.Time{}, reason)
			return
		}
	}
	p.mu.Unlock()
}
