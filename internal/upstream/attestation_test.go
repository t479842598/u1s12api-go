package upstream

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// testAttestationToken 与真实网关签发格式一致的测试令牌（150 字符）：
// base64url({"v":1,"u":531,"d":655,"exp":2000000000,"n":...}) + "." + 43 字符签名。
const testAttestationToken = "eyJ2IjoxLCJ1Ijo1MzEsImQiOjY1NSwiZXhwIjoyMDAwMDAwMDAwLCJuIjoiNWQ3NTAzYjExMzY3Yjg0NzNkMjkwMjVmZGY0OWM5MTEifQ.oA5ujEl_cCb4KTCHUlPNpJ4RpJinBePyiEBIpQJDjtw"

// makeAttestationToken 造一个指定 exp 的测试令牌。
func makeAttestationToken(t *testing.T, exp int64) string {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"v": 1, "u": 531, "d": 655, "exp": exp, "n": "5d7503b11367b8473d29025fdf49c911"})
	if err != nil {
		t.Fatal(err)
	}
	b := base64.RawURLEncoding.EncodeToString(payload)
	return b + ".oA5ujEl_cCb4KTCHUlPNpJ4RpJinBePyiEBIpQJDjtw"
}

// attestFixture 假的 /v1/models 签发端：可配置令牌与状态码，并统计被调用次数。
type attestFixture struct {
	srv      *httptest.Server
	hits     atomic.Int64
	token    atomic.Value // string
	status   atomic.Int64
	failNext atomic.Bool
}

func newAttestFixture(t *testing.T, token string) *attestFixture {
	f := &attestFixture{}
	f.token.Store(token)
	f.status.Store(http.StatusOK)
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		if f.failNext.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"message":"boom"}}`))
			return
		}
		tok, _ := f.token.Load().(string)
		w.Header().Set("Content-Type", "application/json")
		if tok == "" {
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"m1"}]}`))
			return
		}
		_, _ = w.Write([]byte(fmt.Sprintf(
			`{"object":"list","data":[{"id":"m1"}],"client_attestation":{"token":%q,"expires_in":604800}}`, tok)))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func newTestManager(f *attestFixture, now func() time.Time) *AttestationManager {
	dc := NewDeviceClient(f.srv.URL, "", func() string { return "1.3.0" }, nil)
	m := NewAttestationManager(func() *DeviceClient { return dc })
	if now != nil {
		m.nowFunc = now
	}
	return m
}

// TestAttestationManagerCachesToken 有效期内多次取令牌只签发一次（不每请求多打 /v1/models）。
func TestAttestationManagerCachesToken(t *testing.T) {
	f := newAttestFixture(t, testAttestationToken)
	m := newTestManager(f, nil)
	cred := deviceCredential(t)

	for i := 0; i < 5; i++ {
		if got := m.Token(context.Background(), cred); got != testAttestationToken {
			t.Fatalf("第 %d 次 Token = %q, 期望 %q", i+1, got, testAttestationToken)
		}
	}
	if got := f.hits.Load(); got != 1 {
		t.Errorf("/v1/models 调用次数 = %d, 期望 1（应命中缓存）", got)
	}
}

// TestAttestationManagerSingleFlight 并发取令牌时同一凭证只真正签发一次。
func TestAttestationManagerSingleFlight(t *testing.T) {
	f := newAttestFixture(t, testAttestationToken)
	m := newTestManager(f, nil)
	cred := deviceCredential(t)

	var wg sync.WaitGroup
	results := make([]string, 16)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = m.Token(context.Background(), cred)
		}(i)
	}
	wg.Wait()
	for i, got := range results {
		if got != testAttestationToken {
			t.Errorf("goroutine %d 得到 %q", i, got)
		}
	}
	if got := f.hits.Load(); got != 1 {
		t.Errorf("/v1/models 调用次数 = %d, 期望 1（per-key 锁应合并并发签发）", got)
	}
}

// TestAttestationManagerRefreshesWithinSkew 距过期不足刷新窗口时应重新签发。
func TestAttestationManagerRefreshesWithinSkew(t *testing.T) {
	near := makeAttestationToken(t, time.Now().Add(1*time.Hour).Unix()) // 1h < 6h skew
	f := newAttestFixture(t, near)
	m := newTestManager(f, nil)
	cred := deviceCredential(t)

	_ = m.Token(context.Background(), cred)
	_ = m.Token(context.Background(), cred)
	if got := f.hits.Load(); got != 2 {
		t.Errorf("/v1/models 调用次数 = %d, 期望 2（临期应重签）", got)
	}
}

// TestAttestationManagerDegradesOnFailure 签发失败返回空串（请求照常发、不带该头），不 panic。
func TestAttestationManagerDegradesOnFailure(t *testing.T) {
	f := newAttestFixture(t, testAttestationToken)
	f.failNext.Store(true)
	m := newTestManager(f, nil)
	if got := m.Token(context.Background(), deviceCredential(t)); got != "" {
		t.Errorf("签发失败应返回空串, 实际 %q", got)
	}
}

// TestAttestationManagerKeepsStaleOnFailure 重签失败但已有旧令牌时继续复用旧值。
func TestAttestationManagerKeepsStaleOnFailure(t *testing.T) {
	now := time.Unix(1_900_000_000, 0) // 早于令牌 exp=2000000000
	f := newAttestFixture(t, testAttestationToken)
	m := newTestManager(f, func() time.Time { return now })
	cred := deviceCredential(t)

	if got := m.Token(context.Background(), cred); got != testAttestationToken {
		t.Fatalf("首次应拿到令牌, 实际 %q", got)
	}
	// 时间推进到令牌已过期（进入重签路径），同时让签发失败。
	now = time.Unix(2_100_000_000, 0)
	f.failNext.Store(true)
	if got := m.Token(context.Background(), cred); got != testAttestationToken {
		t.Errorf("重签失败应回退旧令牌, 实际 %q", got)
	}
}

// TestAttestationManagerNegativeCache 老网关不签发时缓存空值，不每请求重复探测。
func TestAttestationManagerNegativeCache(t *testing.T) {
	f := newAttestFixture(t, "")
	m := newTestManager(f, nil)
	cred := deviceCredential(t)

	for i := 0; i < 3; i++ {
		if got := m.Token(context.Background(), cred); got != "" {
			t.Fatalf("老网关应返回空串, 实际 %q", got)
		}
	}
	if got := f.hits.Load(); got != 1 {
		t.Errorf("/v1/models 调用次数 = %d, 期望 1（空值应短 TTL 缓存）", got)
	}
}

// TestAttestationManagerInvalidate 显式失效后重新签发（上游 401/403 时由调用方触发）。
func TestAttestationManagerInvalidate(t *testing.T) {
	f := newAttestFixture(t, testAttestationToken)
	m := newTestManager(f, nil)
	cred := deviceCredential(t)

	_ = m.Token(context.Background(), cred)
	m.Invalidate(cred)
	_ = m.Token(context.Background(), cred)
	if got := f.hits.Load(); got != 2 {
		t.Errorf("/v1/models 调用次数 = %d, 期望 2（Invalidate 后应重签）", got)
	}
}

// TestAttestationManagerPerDeviceIsolation 不同设备凭证各自独立缓存（令牌绑定 device_id）。
func TestAttestationManagerPerDeviceIsolation(t *testing.T) {
	f := newAttestFixture(t, testAttestationToken)
	m := newTestManager(f, nil)
	a := deviceCredential(t)
	b := deviceCredential(t) // 另一台设备：密钥对相同但 device_token 不同
	b.DeviceToken = "u1s1d-other-device-9999"

	_ = m.Token(context.Background(), a)
	_ = m.Token(context.Background(), b)
	if got := f.hits.Load(); got != 2 {
		t.Errorf("/v1/models 调用次数 = %d, 期望 2（两台设备各自签发）", got)
	}
	m.mu.Lock()
	n := len(m.cache)
	m.mu.Unlock()
	if n != 2 {
		t.Errorf("缓存条目数 = %d, 期望 2", n)
	}
}

// TestAttestationManagerNilSafe 管理器为 nil 或凭证为空时不 panic（未配置账号的部署）。
func TestAttestationManagerNilSafe(t *testing.T) {
	var m *AttestationManager
	if got := m.Token(context.Background(), deviceCredential(t)); got != "" {
		t.Errorf("nil 管理器应返回空串, 实际 %q", got)
	}
	m2 := newTestManager(newAttestFixture(t, testAttestationToken), nil)
	if got := m2.Token(context.Background(), nil); got != "" {
		t.Errorf("nil 凭证应返回空串, 实际 %q", got)
	}
	if got := m2.Token(context.Background(), &DeviceCredential{}); got != "" {
		t.Errorf("空 device_token 应返回空串, 实际 %q", got)
	}
}

// TestAttestationIgnoresOversizedToken 官方对 token 长度 >1024 视为异常丢弃，本实现同口径。
func TestAttestationIgnoresOversizedToken(t *testing.T) {
	big := strings.Repeat("a", maxAttestationTokenLen+1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fmt.Sprintf(`{"data":[{"id":"m1"}],"client_attestation":{"token":%q,"expires_in":604800}}`, big)))
	}))
	t.Cleanup(srv.Close)
	dc := NewDeviceClient(srv.URL, "", func() string { return "1.3.0" }, nil)
	res, err := dc.DeviceModels(context.Background(), deviceCredential(t))
	if err != nil {
		t.Fatalf("DeviceModels 失败: %v", err)
	}
	if res.Attestation != "" {
		t.Errorf("超长令牌应丢弃, 实际长度 %d", len(res.Attestation))
	}
}
