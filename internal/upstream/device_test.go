package upstream

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/t479842598/u1s12api-go/internal/fingerprint"
)

// deviceFixture 起一个捕获请求头的假上游，返回设备客户端与捕获器。
func deviceFixture(t *testing.T) (*DeviceClient, func() http.Header) {
	t.Helper()
	var captured http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r.Header.Clone()
		switch {
		case strings.HasSuffix(r.URL.Path, "/me"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"email":"a@b.c","login_checkin_remaining":2000000,"packages":[]}`))
		case strings.HasSuffix(r.URL.Path, "/models"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"deepseek-v4-flash"}],"features":{"web_search":true},` +
				`"client_attestation":{"token":"` + testAttestationToken + `","expires_in":604800}}`))
		case strings.HasSuffix(r.URL.Path, "/chat/completions"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	// 用 linux-x64 档案：验证 x-u1s1-platform=linux-x64、UA/Stainless 自洽。
	profile := func() fingerprint.Profile {
		p, _ := fingerprint.ProfileByID("linux-x64")
		return p
	}
	dc := NewDeviceClient(srv.URL, "", func() string { return "1.3.0" }, profile)
	return dc, func() http.Header { return captured }
}

func deviceCredential(t *testing.T) *DeviceCredential {
	t.Helper()
	privJWK, pubJWK, err := GenerateDeviceKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	return &DeviceCredential{
		DeviceToken: "u1s1d-abcdef123456",
		PrivateJWK:  privJWK,
		PublicJWK:   pubJWK,
	}
}

// TestDeviceMeSendsVersionHeader 设备凭证调 /v1/me 应带 x-u1s1-version（官方 fetchMe 一致），
// 且鉴权为 DPoP。
func TestDeviceMeSendsVersionHeader(t *testing.T) {
	dc, captured := deviceFixture(t)
	if _, err := dc.DeviceMe(context.Background(), deviceCredential(t)); err != nil {
		t.Fatalf("DeviceMe 失败: %v", err)
	}
	h := captured()
	if h.Get("x-u1s1-version") != "1.3.0" {
		t.Errorf("x-u1s1-version = %q, 期望 1.3.0", h.Get("x-u1s1-version"))
	}
	if !strings.HasPrefix(h.Get("Authorization"), "DPoP ") {
		t.Errorf("Authorization = %q, 期望 DPoP", h.Get("Authorization"))
	}
	if h.Get("dpop") == "" {
		t.Errorf("缺少 dpop 头")
	}
}

// TestDeviceChatClientFingerprint 设备凭证调 /v1/chat/completions 应带官方 1.3.0 完整指纹头：
// DPoP + x-u1s1-version + x-u1s1-client + x-u1s1-platform + UA + X-Stainless-*（同一档案自洽）。
func TestDeviceChatClientFingerprint(t *testing.T) {
	dc, captured := deviceFixture(t)
	resp, err := dc.DeviceChat(context.Background(), deviceCredential(t),
		[]byte(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}]}`), "")
	if err != nil {
		t.Fatalf("DeviceChat 失败: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("DeviceChat status = %d", resp.StatusCode)
	}
	h := captured()
	checks := map[string]string{
		"X-U1s1-Version":              "1.3.0",
		"X-U1s1-Client":               fingerprint.ClientSurface,
		"X-U1s1-Platform":             "linux-x64",
		"User-Agent":                  fingerprint.UserAgent(fingerprint.Profiles[2]), // linux-x64
		"X-Stainless-Lang":            "js",
		"X-Stainless-Package-Version": fingerprint.SDKPackageVersion,
		"X-Stainless-Os":              "Linux",
		"X-Stainless-Arch":            "x64",
		"X-Stainless-Runtime":         "node",
		"X-Stainless-Runtime-Version": "v22.21.1",
		"X-Stainless-Retry-Count":     "0",
	}
	for k, want := range checks {
		if got := h.Get(k); got != want {
			t.Errorf("头 %s = %q, 期望 %q", k, got, want)
		}
	}
	if !strings.HasPrefix(h.Get("Authorization"), "DPoP u1s1d-") {
		t.Errorf("Authorization = %q, 期望 DPoP u1s1d-", h.Get("Authorization"))
	}
	if h.Get("dpop") == "" {
		t.Errorf("缺少 dpop 头")
	}
	// 无令牌时不带 x-u1s1-attestation（对齐官方：代理仅在拿到令牌时注入）。
	if v := h.Get("x-u1s1-attestation"); v != "" {
		t.Errorf("attestation 为空时不应发该头，实际 %q", v)
	}
}

// TestDeviceChatSendsAttestation v1.3.0 新增头：有令牌时设备通道必须带 x-u1s1-attestation。
func TestDeviceChatSendsAttestation(t *testing.T) {
	dc, captured := deviceFixture(t)
	resp, err := dc.DeviceChat(context.Background(), deviceCredential(t),
		[]byte(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}]}`), "attest-token-xyz")
	if err != nil {
		t.Fatalf("DeviceChat 失败: %v", err)
	}
	resp.Body.Close()
	if got := captured().Get("x-u1s1-attestation"); got != "attest-token-xyz" {
		t.Errorf("x-u1s1-attestation = %q, 期望 attest-token-xyz", got)
	}
}

// TestDeviceModelsParsesAttestation GET /v1/models 应解析 client_attestation.token 与过期时刻。
func TestDeviceModelsParsesAttestation(t *testing.T) {
	dc, _ := deviceFixture(t)
	res, err := dc.DeviceModels(context.Background(), deviceCredential(t))
	if err != nil {
		t.Fatalf("DeviceModels 失败: %v", err)
	}
	if res.Attestation != testAttestationToken {
		t.Errorf("Attestation = %q, 期望 %q", res.Attestation, testAttestationToken)
	}
	// payload exp 权威：固定值 2000000000。
	if got := res.ExpiresAt.Unix(); got != 2000000000 {
		t.Errorf("ExpiresAt = %d, 期望 2000000000（取令牌 payload 的 exp）", got)
	}
	if res.ModelCount != 1 {
		t.Errorf("ModelCount = %d, 期望 1", res.ModelCount)
	}
}

// TestDeviceModelsNoAttestation 老网关不返回 client_attestation 时不报错、令牌为空。
func TestDeviceModelsNoAttestation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"m1"}]}`))
	}))
	t.Cleanup(srv.Close)
	dc := NewDeviceClient(srv.URL, "", func() string { return "1.3.0" }, nil)
	res, err := dc.DeviceModels(context.Background(), deviceCredential(t))
	if err != nil {
		t.Fatalf("DeviceModels 失败: %v", err)
	}
	if res.Attestation != "" {
		t.Errorf("期望空令牌, 实际 %q", res.Attestation)
	}
}

// TestAttestationPayloadDecode 真实网关令牌格式（base64url(JSON).base64url(sig)）可解出绑定字段。
func TestAttestationPayloadDecode(t *testing.T) {
	p, ok := decodeAttestationPayload(testAttestationToken)
	if !ok {
		t.Fatal("payload 解析失败")
	}
	if p.D != 655 || p.U != 531 || p.Exp != 2000000000 {
		t.Errorf("payload = %+v, 期望 d=655 u=531 exp=2000000000", p)
	}
	if _, bad := decodeAttestationPayload("not-a-token"); bad {
		t.Error("非法令牌应解析失败")
	}
}
