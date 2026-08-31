package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/t479842598/u1s12api-go/internal/upstream"
)

// TestSafeRetryAfter 只放行 RFC 9110 的两种合法形式，其余一律丢弃（防响应头注入/垃圾值透传）。
func TestSafeRetryAfter(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"7", "7"},
		{"  120 ", "120"},
		{"0", "0"},
		{"Wed, 21 Oct 2026 07:28:00 GMT", "Wed, 21 Oct 2026 07:28:00 GMT"},
		{"", ""},
		{"   ", ""},
		// 注入与畸形值
		{"7\r\nX-Injected: yes", ""},
		{"7\nX-Injected: yes", ""},
		{"<script>", ""},
		{"-5", ""},
		{"7.5", ""},
		{"soon", ""},
		{"99999999999999999999", ""}, // 超长数字（>10 位）
	}
	for _, c := range cases {
		if got := safeRetryAfter(c.in); got != c.want {
			t.Errorf("safeRetryAfter(%q) = %q, 期望 %q", c.in, got, c.want)
		}
	}
}

// TestPassthroughPreservesRetryAfter 透传上游错误时保留合法 Retry-After。
func TestPassthroughPreservesRetryAfter(t *testing.T) {
	rec := httptest.NewRecorder()
	passthroughUpstreamError(rec, &upstream.APIError{
		StatusCode: http.StatusServiceUnavailable,
		Body:       `{"error":{"code":"model_unavailable"}}`,
		RetryAfter: "7",
	})
	if got := rec.Code; got != http.StatusServiceUnavailable {
		t.Errorf("status = %d, 期望 503", got)
	}
	if got := rec.Header().Get("Retry-After"); got != "7" {
		t.Errorf("Retry-After = %q, 期望 \"7\"", got)
	}
}

// TestPassthroughDropsUnsafeRetryAfter 非法 Retry-After 不得出现在响应头里。
func TestPassthroughDropsUnsafeRetryAfter(t *testing.T) {
	rec := httptest.NewRecorder()
	passthroughUpstreamError(rec, &upstream.APIError{
		StatusCode: http.StatusServiceUnavailable,
		Body:       `{}`,
		RetryAfter: "7\r\nX-Injected: yes",
	})
	if got := rec.Header().Get("Retry-After"); got != "" {
		t.Errorf("非法值不应透传, 实际 %q", got)
	}
}

// TestPassthroughNoRetryAfterWhenAbsent 上游没带该头时不凭空造一个。
func TestPassthroughNoRetryAfterWhenAbsent(t *testing.T) {
	rec := httptest.NewRecorder()
	passthroughUpstreamError(rec, &upstream.APIError{StatusCode: 429, Body: `{}`})
	if got := rec.Header().Get("Retry-After"); got != "" {
		t.Errorf("无 Retry-After 时不应出现该头, 实际 %q", got)
	}
}

// TestChatForwardsRetryAfterEndToEnd 端到端：上游 503 model_unavailable + Retry-After
// 经 Key 池通道透传给本地客户端（503 不在换 key 重试白名单内，直接原样返回）。
func TestChatForwardsRetryAfterEndToEnd(t *testing.T) {
	fx := setupTest(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(validUpstreamModelsResp))
		case "/chat/completions":
			w.Header().Set("Retry-After", "9")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"message":"模型暂时不可用","type":"unavailable","code":"model_unavailable"}}`))
		default:
			http.NotFound(w, r)
		}
	})
	fx.addKeys(t, "u1s1-e2ekeye2ekeye2ekey1")
	fx.addLocalKey(t, "default", "sk-local-test")

	resp := postDeviceChat(t, fx.ts.URL, "sk-local-test")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, 期望 503", resp.StatusCode)
	}
	if got := resp.Header.Get("Retry-After"); got != "9" {
		t.Errorf("客户端收到的 Retry-After = %q, 期望 \"9\"", got)
	}
}
