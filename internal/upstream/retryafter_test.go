package upstream

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/t479842598/u1s12api-go/internal/fingerprint"
)

// modelUnavailableBody Gateway 对可重试的临时路由不可用返回的错误壳
// （u1s1-cli 1.3.1 同期服务端变更：已知模型渠道全被过滤 → 503 + Retry-After）。
const modelUnavailableBody = `{"error":{"message":"模型暂时不可用，请稍后重试","type":"unavailable","code":"model_unavailable"}}`

func newClientForTest(t *testing.T, url string) *Client {
	t.Helper()
	fp, err := fingerprint.NewManager(filepath.Join(t.TempDir(), "fp.json"), "linux-x64")
	if err != nil {
		t.Fatal(err)
	}
	cli, err := NewClient(url, "", fp, func() string { return "1.3.1" })
	if err != nil {
		t.Fatal(err)
	}
	return cli
}

// TestChatCapturesRetryAfter 上游 503 带 Retry-After 时应捕获到 APIError，供上层透传给客户端。
func TestChatCapturesRetryAfter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(modelUnavailableBody))
	}))
	t.Cleanup(srv.Close)

	_, err := newClientForTest(t, srv.URL).Chat(context.Background(), "u1s1-testkey123456",
		[]byte(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}]}`))
	var apiErr *APIError
	if !asAPIErrorForTest(err, &apiErr) {
		t.Fatalf("期望 *APIError，实际 %T %v", err, err)
	}
	if apiErr.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("StatusCode = %d, 期望 503", apiErr.StatusCode)
	}
	if apiErr.RetryAfter != "7" {
		t.Errorf("RetryAfter = %q, 期望 \"7\"", apiErr.RetryAfter)
	}
}

// TestChatRetryAfterEmpty 上游不带 Retry-After 时字段为空（不影响既有行为）。
func TestChatRetryAfterEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"code":"quota_exceeded"}}`))
	}))
	t.Cleanup(srv.Close)

	_, err := newClientForTest(t, srv.URL).Chat(context.Background(), "u1s1-testkey123456",
		[]byte(`{"model":"m","messages":[]}`))
	var apiErr *APIError
	if !asAPIErrorForTest(err, &apiErr) {
		t.Fatalf("期望 *APIError，实际 %T %v", err, err)
	}
	if apiErr.RetryAfter != "" {
		t.Errorf("无该头时 RetryAfter 应为空, 实际 %q", apiErr.RetryAfter)
	}
}

// TestDeviceChatCapturesRetryAfter 设备凭证通道同样捕获 Retry-After（保持 APIError 语义一致）。
func TestDeviceChatCapturesRetryAfter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "Wed, 21 Oct 2026 07:28:00 GMT")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(modelUnavailableBody))
	}))
	t.Cleanup(srv.Close)

	dc := NewDeviceClient(srv.URL, "", func() string { return "1.3.1" }, nil)
	_, err := dc.DeviceChat(context.Background(), deviceCredential(t),
		[]byte(`{"model":"m","messages":[]}`), "attest-token")
	var apiErr *APIError
	if !asAPIErrorForTest(err, &apiErr) {
		t.Fatalf("期望 *APIError，实际 %T %v", err, err)
	}
	if apiErr.RetryAfter != "Wed, 21 Oct 2026 07:28:00 GMT" {
		t.Errorf("RetryAfter = %q, 期望 HTTP-date 原值", apiErr.RetryAfter)
	}
}

// asAPIErrorForTest 测试内 errors.As 等价物（避免为断言引入额外依赖）。
func asAPIErrorForTest(err error, target **APIError) bool {
	e, ok := err.(*APIError)
	if ok {
		*target = e
	}
	return ok
}
