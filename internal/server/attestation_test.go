package server

import (
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// serverAttestationToken 与真实网关签发格式一致的测试令牌（payload exp=2033，不会临期）。
const serverAttestationToken = "eyJ2IjoxLCJ1Ijo1MzEsImQiOjY1NSwiZXhwIjoyMDAwMDAwMDAwLCJuIjoiNWQ3NTAzYjExMzY3Yjg0NzNkMjkwMjVmZGY0OWM5MTEifQ.oA5ujEl_cCb4KTCHUlPNpJ4RpJinBePyiEBIpQJDjtw"

// attestUpstreamFixture 假上游：设备凭证（DPoP）调 /models 时签发 client_attestation，
// 并记录每次 chat 的请求头与 /models 的签发次数。
type attestUpstreamFixture struct {
	mu          sync.Mutex
	chatHeaders []http.Header
	modelsSigns int
	plainModels int
}

func (f *attestUpstreamFixture) handler(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/models":
		f.mu.Lock()
		if strings.HasPrefix(r.Header.Get("Authorization"), "DPoP ") {
			f.modelsSigns++
		} else {
			f.plainModels++
		}
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		// 设备凭证与普通 key 都返回模型列表；仅设备凭证响应带 client_attestation
		// （与真实网关一致：实测普通 key 不签发）。
		if strings.HasPrefix(r.Header.Get("Authorization"), "DPoP ") {
			_, _ = w.Write([]byte(`{"data":[{"id":"deepseek-v4-flash","name":"D","context_length":1048576,"max_tokens":384000,"price":{"input":0.14,"output":0.28}}],"client_attestation":{"token":"` + serverAttestationToken + `","expires_in":604800}}`))
			return
		}
		_, _ = w.Write([]byte(validUpstreamModelsResp))
	case "/chat/completions":
		f.mu.Lock()
		f.chatHeaders = append(f.chatHeaders, r.Header.Clone())
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(deviceChatOKBody))
	default:
		http.NotFound(w, r)
	}
}

func (f *attestUpstreamFixture) lastChat() http.Header {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.chatHeaders) == 0 {
		return nil
	}
	return f.chatHeaders[len(f.chatHeaders)-1]
}

func (f *attestUpstreamFixture) chatCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.chatHeaders)
}

func (f *attestUpstreamFixture) counts() (signs, plains int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.modelsSigns, f.plainModels
}

// TestDeviceChannelSendsAttestationHeader 端到端：设备凭证通道发 chat 前先从
// /v1/models 取网关签发的令牌，并把它放进 x-u1s1-attestation 头。
func TestDeviceChannelSendsAttestationHeader(t *testing.T) {
	af := &attestUpstreamFixture{}
	fx := setupTest(t, af.handler)
	mkDeviceAccount(t, fx, "attest@test.dev", "u1s1d-attest", 5_000_000)
	prepareDeviceChatFX(t, fx)

	resp := postDeviceChat(t, fx.ts.URL, "sk-local-test")
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("chat status = %d, 期望 200", resp.StatusCode)
	}
	h := af.lastChat()
	if h == nil {
		t.Fatal("上游未收到 chat 请求")
	}
	if got := h.Get("x-u1s1-attestation"); got != serverAttestationToken {
		t.Errorf("x-u1s1-attestation = %.20s…, 期望网关签发的令牌", got)
	}
	// 设备通道仍应保留原有指纹（不因新头回归）。
	if !strings.HasPrefix(h.Get("Authorization"), "DPoP u1s1d-") {
		t.Errorf("Authorization = %.20s, 期望 DPoP 设备凭证", h.Get("Authorization"))
	}
	if h.Get("x-u1s1-client") == "" || h.Get("x-u1s1-platform") == "" {
		t.Errorf("设备通道指纹头缺失: client=%q platform=%q", h.Get("x-u1s1-client"), h.Get("x-u1s1-platform"))
	}
}

// TestDeviceChannelReusesAttestationToken 令牌有效期内多个请求只签发一次（不每请求多打 /models）。
func TestDeviceChannelReusesAttestationToken(t *testing.T) {
	af := &attestUpstreamFixture{}
	fx := setupTest(t, af.handler)
	mkDeviceAccount(t, fx, "attest2@test.dev", "u1s1d-attest2", 5_000_000)
	prepareDeviceChatFX(t, fx)

	for i := 0; i < 3; i++ {
		resp := postDeviceChat(t, fx.ts.URL, "sk-local-test")
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("第 %d 次 chat status = %d", i+1, resp.StatusCode)
		}
	}
	signs, _ := af.counts()
	if signs != 1 {
		t.Errorf("设备凭证 /models 签发次数 = %d, 期望 1（应命中缓存）", signs)
	}
	if n := af.chatCount(); n != 3 {
		t.Errorf("chat 次数 = %d, 期望 3", n)
	}
}

// TestInferenceRequiresAuthorizedAccount 无授权官网账号（设备凭证）时，推理请求被拒绝。
// （v0.9.4）推理一律只用设备凭证通道，旧版 u1s1- API Key 已被上游禁止用于推理，
// 未配置账号时返回明确错误而非回退 Key 池。
func TestInferenceRequiresAuthorizedAccount(t *testing.T) {
	af := &attestUpstreamFixture{}
	fx := setupTest(t, af.handler)
	// 不建授权账号 → 无设备凭证，也不应回退 Key 池。
	fx.addLocalKey(t, "default", "sk-local-test")

	resp := postDeviceChat(t, fx.ts.URL, "sk-local-test")
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("无授权账号应返回 503，实际 status=%d body=%.200s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "no_authorized_account") {
		t.Errorf("错误应含 no_authorized_account，实际=%.200s", body)
	}
	// 不应回退 Key 池：上游不应收到任何 chat 请求。
	if n := af.chatCount(); n != 0 {
		t.Errorf("不应回退 Key 池做推理，但上游收到 %d 次 chat", n)
	}
}

// TestDeviceChannelStillWorksWithoutAttestation 网关不签发令牌（老网关）时请求照常成功。
func TestDeviceChannelStillWorksWithoutAttestation(t *testing.T) {
	var mu sync.Mutex
	var chatSeen http.Header
	fx := setupTest(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(validUpstreamModelsResp)) // 无 client_attestation
		case "/chat/completions":
			mu.Lock()
			chatSeen = r.Header.Clone()
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(deviceChatOKBody))
		default:
			http.NotFound(w, r)
		}
	})
	mkDeviceAccount(t, fx, "legacy@test.dev", "u1s1d-legacy", 5_000_000)
	prepareDeviceChatFX(t, fx)

	resp := postDeviceChat(t, fx.ts.URL, "sk-local-test")
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("老网关下 chat 应照常成功, status = %d", resp.StatusCode)
	}
	mu.Lock()
	defer mu.Unlock()
	if chatSeen == nil {
		t.Fatal("上游未收到 chat 请求")
	}
	if v := chatSeen.Get("x-u1s1-attestation"); v != "" {
		t.Errorf("无令牌时不应发该头, 实际 %q", v)
	}
}
