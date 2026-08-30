// 客户端证明（x-u1s1-attestation）令牌缓存。
//
// 背景（逆向自 u1s1-cli 1.3.0）：网关在 GET /v1/models 响应体新增
//
//	client_attestation: { token: "<base64url(json)>.<base64url(sig)>", expires_in: 604800 }
//
// 官方客户端把该 token 交给签名代理，由代理注入到**每个**转发到上游的
// chat 请求头 `x-u1s1-attestation`（dist/device-auth.js:138）。
//
// 令牌语义（真实网关实测，2026-08-30）：
//   - payload = {"v":1,"u":<user id>,"d":<device_id>,"exp":<unix 秒>,"n":<16B nonce hex>}
//   - exp = 签发时刻 + 604800（7 天）；每次调用 /v1/models 都重新签发（n/exp/签名都变）
//   - 绑定 user + device，跨账号不可复用；签名由网关私钥产生，客户端无法自造
//   - 普通 u1s1- api_key 通道**不签发**（实测 NOT ISSUED），官方也只在设备凭证通道发该头
//
// 因此本缓存按设备凭证分别持有令牌，临期自动重签；签发失败时降级为
// 「不带该头」——与官方无令牌时的行为一致，绝不因取不到令牌而阻断请求。
package upstream

import (
	"context"
	"sync"
	"time"

	"github.com/t479842598/u1s12api-go/internal/logging"
)

var attLogger = logging.Named("u1s12api/attestation")

// attestRefreshSkew 距过期不足该时长即提前重签（7 天 TTL 下相当于每天最多重签一次）。
const attestRefreshSkew = 6 * time.Hour

// attestFallbackTTL 令牌 payload 解析失败时的保守缓存时长。
const attestFallbackTTL = time.Hour

type attestationEntry struct {
	token   string
	expires time.Time
	// notIssued 网关本次未签发（老网关或字段未上线）：记为已知空值，短 TTL 内不再探测。
	notIssued bool
}

// valid 缓存命中：未签发标记只看是否超过短 TTL；正常令牌需未过期且未进刷新窗口。
func (e attestationEntry) valid(now time.Time, skew time.Duration) bool {
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
// 签发失败（网络抖动、老网关无该字段、401 等）时返回空串——调用方应照常发请求、
// 不带 x-u1s1-attestation 头。若已有未过期令牌则失败时继续复用旧值。
func (m *AttestationManager) Token(ctx context.Context, cred *DeviceCredential) string {
	if m == nil || cred == nil || cred.DeviceToken == "" {
		return ""
	}
	key := cred.DeviceToken

	// 快路径：命中未过期缓存。
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

	res, err := m.clientFn().DeviceModels(ctx, cred)
	if err != nil {
		attLogger.Warnf("签发 x-u1s1-attestation 失败（本次请求不带该头，不阻断）: %v", err)
		// 失败时若有（哪怕已临期的）旧令牌，继续用它比不带更接近官方行为。
		m.mu.Lock()
		old := m.cache[key]
		m.mu.Unlock()
		return old.token
	}
	if res.Attestation == "" {
		// 网关未签发（老网关或未上线该字段）：短 TTL 缓存空值，避免每次请求都探测。
		m.put(key, attestationEntry{notIssued: true, expires: m.nowFunc().Add(attestFallbackTTL)})
		return ""
	}
	m.put(key, attestationEntry{token: res.Attestation, expires: res.ExpiresAt})
	attLogger.Debugf("已签发 x-u1s1-attestation device=%s len=%d expires=%s",
		deviceTokenHint(cred.DeviceToken), len(res.Attestation), res.ExpiresAt.Format(time.RFC3339))
	return res.Attestation
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
	if !ok || !e.valid(m.nowFunc(), m.skew) {
		return "", false
	}
	return e.token, true
}
func (m *AttestationManager) put(key string, e attestationEntry) {
	now := m.nowFunc()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cache[key] = e
	// 顺手清理彻底过期的条目与对应锁，防止长期运行下无界增长。
	for k, v := range m.cache {
		if now.After(v.expires) {
			delete(m.cache, k)
			delete(m.keyMu, k)
		}
	}
}

// deviceTokenHint 设备令牌的可读前缀（日志用，不落全量密钥）。
func deviceTokenHint(tok string) string {
	if len(tok) <= 12 {
		return tok
	}
	return tok[:12] + "…"
}
