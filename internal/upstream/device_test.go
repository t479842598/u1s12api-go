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
	dc := NewDeviceClient(srv.URL, "", func() string { return "1.2.3" }, profile)
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
	if h.Get("x-u1s1-version") != "1.2.3" {
		t.Errorf("x-u1s1-version = %q, 期望 1.2.3", h.Get("x-u1s1-version"))
	}
	if !strings.HasPrefix(h.Get("Authorization"), "DPoP ") {
		t.Errorf("Authorization = %q, 期望 DPoP", h.Get("Authorization"))
	}
	if h.Get("dpop") == "" {
		t.Errorf("缺少 dpop 头")
	}
}

// TestDeviceChatClientFingerprint 设备凭证调 /v1/chat/completions 应带官方 1.2.3 完整指纹头：
// DPoP + x-u1s1-version + x-u1s1-client + x-u1s1-platform + UA + X-Stainless-*（同一档案自洽）。
func TestDeviceChatClientFingerprint(t *testing.T) {
	dc, captured := deviceFixture(t)
	resp, err := dc.DeviceChat(context.Background(), deviceCredential(t),
		[]byte(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatalf("DeviceChat 失败: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("DeviceChat status = %d", resp.StatusCode)
	}
	h := captured()
	checks := map[string]string{
		"X-U1s1-Version":              "1.2.3",
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
}
