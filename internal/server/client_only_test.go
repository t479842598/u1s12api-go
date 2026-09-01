package server

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// 上游「设备通道不可用但存在授权账号」时不得回退 Key 池（v0.9.3）。
//
// 背景：u1s1 网关已关闭旧版 u1s1- API Key 推理通道（403 u1s1_client_only），
// 设备账号全部不可用（额度耗尽 / 上游限流 / 网络异常）时，若回退 Key 池只会
// 403 并给账号带来封禁风险。所以必须返回清晰的设备通道错误，而非落到 Key 池。
func TestDeviceChannelUnavailableNoKeyFallback(t *testing.T) {
	var mu sync.Mutex
	var chatAuths []string
	fx := setupTest(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(validUpstreamModelsResp))
		case "/chat/completions":
			mu.Lock()
			chatAuths = append(chatAuths, r.Header.Get("Authorization"))
			mu.Unlock()
			// 设备凭证（DPoP）统一返回“上游限流”429（非 quota_exceeded，故不触发冷却标记，
			// 修复前会 continue 穿完所有账号再回退 Key 池）。
			if strings.HasPrefix(r.Header.Get("Authorization"), "DPoP ") {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"error":{"message":"上游限流(渠道 36cb79 429)，请稍后再试","type":"api_error","upstream_status":429}}`))
				return
			}
			// 若 Key 池被调到（Bug），这里会成功 200 —— 用来识别“不该回退却回退了”。
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(deviceChatOKBody))
		default:
			http.NotFound(w, r)
		}
	})
	mkDeviceAccount(t, fx, "a@test.dev", "u1s1d-a", 10000)
	mkDeviceAccount(t, fx, "b@test.dev", "u1s1d-b", 5000)
	fx.addKeys(t, "u1s1-modelkey1111111111")
	if _, _, err := fx.srv.fetchModels(context.Background()); err != nil {
		t.Fatalf("预热模型缓存失败: %v", err)
	}
	fx.addLocalKey(t, "default", "sk-local-test")

	resp := postDeviceChat(t, fx.ts.URL, "sk-local-test")
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("期望 503 device_channel_unavailable，实际 status=%d body=%.200s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "device_channel_unavailable") {
		t.Errorf("错误应含 device_channel_unavailable，实际=%.200s", body)
	}

	auths := chatAuths
	for _, a := range auths {
		if strings.HasPrefix(a, "Bearer u1s1-") {
			t.Errorf("设备通道不可用时不应当回退 Key 池，但 Key 被调用: %s", a)
		}
	}
	if len(auths) != 2 {
		t.Errorf("设备账号调用次数=%d，期望 2（a、b 各一次，均 429）", len(auths))
	}
}
