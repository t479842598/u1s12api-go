// 客户端证明（x-u1s1-attestation）令牌缓存。
//
// 背景（逆向自 u1s1-cli）：网关在 GET /v1/models 响应体返回
//
//	client_attestation: { token: "<base64url(json)>.<base64url(sig)>", expires_in: 604800 }
//
// 官方客户端把它交给签名代理，由代理注入到**每个**转发到上游的 chat 请求头
// `x-u1s1-attestation`（device-auth.js 的 attachAttestationHeader）。
//
// 令牌语义（真实网关实测，2026-08-30）：
//   - payload = {"v":<版本>,"u":<user id>,"d":<device_id>,"exp":<unix 秒>,"n":<16B nonce hex>}
//   - exp = 签发时刻 + 604800（7 天）；每次调用 /v1/models 都重新签发（n/exp/签名都变）
//   - 绑定 user + device，跨账号不可复用；签名由网关私钥产生，客户端无法自造
//   - 普通 u1s1- api_key 通道**不签发**（实测 NOT ISSUED），官方也只在设备凭证通道发该头
//
// 刷新节奏照搬官方（device-auth.js 的三个常量）：距过期 24h 内提前重签、签发失败
// 冷却 30s、手里没令牌时最多为一个请求等 4s。冷却这一条尤其重要：上游黑洞时
// 若逐请求重探 /v1/models，每个 chat 都要串行多等一次探测（实测最坏 +30s），
// 还会把 models:chat 比例推成 1:1 —— 官方约 1:几千，那是很容易被“组合证据”看出的形态。
//
// 与官方的唯一有意差异：官方在 TUI 里把刷新丢后台、不阻塞当前请求；我们放在
// 锁内同步做，但有 30s 冷却与 4s 上限兜底，最坏影响被夹在常数内，且换来的是
// 可确定性测试（不会因后台 goroutine 而计数漂移）。
package upstream

import (
	"context"
	"sync"
	"time"

	"github.com/t479842598/u1s12api-go/internal/fingerprint"
	"github.com/t479842598/u1s12api-go/internal/logging"
)

var attLogger = logging.Named("u1s12api/attestation")

const (
	// attestRefreshSkew 官方 ATTESTATION_REFRESH_MARGIN_MS：距过期不足该时长即提前重签。
	attestRefreshSkew = 24 * time.Hour
	// attestFailCooldown 官方 ATTESTATION_REFRESH_COOLDOWN_MS：失败后这段时间不再探测。
	attestFailCooldown = 30 * time.Second
	// attestProbeCap 手里没令牌时，为一个请求最多等多久（官方 ATTESTATION_BLOCK_TIMEOUT_MS）。
	attestProbeCap = 4 * time.Second
	// attestFallbackTTL 令牌 payload 解析失败时的保守缓存时长。
	attestFallbackTTL = time.Hour
	// attestNotIssuedTTL 网关明确未签发时的短缓存，避免每次请求都去探测。
	attestNotIssuedTTL = time.Hour
)

type attestationEntry struct {
	token   string
	expires time.Time
	// notIssued 网关本次未签发（老网关或字段未上线）：记为已知空值，短 TTL 内不再探测。
	notIssued bool
	// lastFailure 最近一次签发失败时刻；冷却期内不再向上游探测（官方 lastFailureMs）。
	lastFailure time.Time
}

// usable 缓存命中：未签发标记只看是否超过短 TTL；正常令牌需未过期且未进刷新窗口。
func (e attestationEntry) usable(now time.Time, skew time.Duration) bool {
	if e.notIssued {
		return now.Before(e.expires)
	}
	return e.token != "" && now.Add(skew).Before(e.expires)
}

// AttestationManager 按设备凭证缓存网关签发的客户端证明令牌。
//
// clientFn 提供当前配置下的设备客户端（配置可热改，故不固定持有实例）。
// 并发安全：同一凭证的并发请求只会真正重签一次（per-key 锁 + 双检）。
type AttestationManager struct {
	clientFn func() *DeviceClient

	mu      sync.Mutex
	cache   map[string]attestationEntry // key: device_token
	keyMu   map[string]*sync.Mutex
	skew    time.Duration
	nowFunc func() time.Time
}

// NewAttestationManager 构造。clientFn 每次需要签发时被调用。
func NewAttestationManager(clientFn func() *DeviceClient) *AttestationManager {
	return &AttestationManager{
		clientFn: clientFn,
		cache:    map[string]attestationEntry{},
		keyMu:    map[string]*sync.Mutex{},
		skew:     attestRefreshSkew,
		nowFunc:  time.Now,
	}
}

// lockFor 取该凭证的签发锁（惰性创建，随缓存清理）。
func (m *AttestationManager) lockFor(key string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	if l, ok := m.keyMu[key]; ok {
		return l
	}
	l := &sync.Mutex{}
	m.keyMu[key] = l
	return l
}

// Token 返回该设备凭证可用的 attestation 令牌；缺失或临期则向网关重新签发。
//
// 任何签发失败都**不阻断请求**：返回旧令牌（若有）或空串，调用方照常发请求、
// 不带 x-u1s1-attestation 头 —— 与官方无令牌时的行为一致。
func (m *AttestationManager) Token(ctx context.Context, cred *DeviceCredential) string {
	if m == nil || cred == nil || cred.DeviceToken == "" {
		return ""
	}
	key := cred.DeviceToken

	// 快路径：命中可用缓存。
	if tok, ok := m.cached(key); ok {
		return tok
	}
	// 慢路径：per-key 锁 + 双检，避免同一账号并发请求各打一次 /v1/models。
	l := m.lockFor(key)
	l.Lock()
	defer l.Unlock()
	if tok, ok := m.cached(key); ok {
		return tok
	}

	now := m.nowFunc()
	m.mu.Lock()
	old := m.cache[key]
	m.mu.Unlock()

	// 失败冷却：官方同样在失败后 30s 内不再刷新，避免把抖动的网关打爆。
	if !old.lastFailure.IsZero() && now.Sub(old.lastFailure) < attestFailCooldown {
		return old.token
	}

	// 手里已有（哪怕临期的）令牌时，探测失败也继续用它，比不带更接近官方行为。
	probeCtx, cancel := context.WithTimeout(ctx, attestProbeCap)
	defer cancel()
	res, err := m.clientFn().DeviceModels(probeCtx, cred, m.auxUA(key))
	if err != nil {
		m.recordFailure(key, old)
		attLogger.Warnf("签发 x-u1s1-attestation 失败（%v 内不再探测，本次请求不带该头）: %v", attestFailCooldown, err)
		return old.token
	}
	if res.Attestation == "" {
		// 网关未签发（老网关或未上线该字段）：短 TTL 缓存空值，避免每次请求都探测。
		m.put(key, attestationEntry{notIssued: true, expires: m.nowFunc().Add(attestNotIssuedTTL)})
		return ""
	}
	m.put(key, attestationEntry{token: res.Attestation, expires: res.ExpiresAt})
	attLogger.Debugf("已签发 x-u1s1-attestation device=%s len=%d expires=%s",
		deviceTokenHint(cred.DeviceToken), len(res.Attestation), res.ExpiresAt.Format(time.RFC3339))
	return res.Attestation
}

// recordFailure 记下这次探测失败（保留原有令牌与过期时刻，只更新失败时间）。
func (m *AttestationManager) recordFailure(key string, old attestationEntry) {
	e := old
	e.lastFailure = m.nowFunc()
	m.mu.Lock()
	m.cache[key] = e
	m.mu.Unlock()
}

// auxUA 该凭证本次取令牌应用的裸 fetch UA。
//
// 官方 CLI 只在启动阶段用内置 fetch（UA: node）；进入 TUI 后 interactive-mode.js 调
// configureHttpDispatcher() 把 globalThis.fetch 换成独立 undici，此后同进程内的
// 刷新请求 UA 变成 undici。我们对应：每个凭证第一次取令牌发 node，之后发 undici。
func (m *AttestationManager) auxUA(key string) string {
	m.mu.Lock()
	_, seen := m.cache[key]
	m.mu.Unlock()
	if seen {
		return fingerprint.UndiciUserAgent
	}
	return fingerprint.NodeUserAgent
}

// Invalidate 丢弃某凭证的缓存令牌（上游以 401/403 拒绝时调用，下次请求重新签发）。
func (m *AttestationManager) Invalidate(cred *DeviceCredential) {
	if m == nil || cred == nil || cred.DeviceToken == "" {
		return
	}
	m.mu.Lock()
	delete(m.cache, cred.DeviceToken)
	m.mu.Unlock()
}

func (m *AttestationManager) cached(key string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.cache[key]
	if !ok || !e.usable(m.nowFunc(), m.skew) {
		return "", false
	}
	return e.token, true
}

func (m *AttestationManager) put(key string, e attestationEntry) {
	now := m.nowFunc()
	m.mu.Lock()
	m.cache[key] = e
	// 顺手清理彻底过期的条目与对应锁，防止长期运行下无界增长。
	for k, v := range m.cache {
		if now.After(v.expires) {
			delete(m.cache, k)
			delete(m.keyMu, k)
		}
	}
	m.mu.Unlock()
}

// deviceTokenHint 设备令牌的可读前缀（日志用，不落全量密钥）。
func deviceTokenHint(tok string) string {
	if len(tok) <= 12 {
		return tok
	}
	return tok[:12] + "…"
}
