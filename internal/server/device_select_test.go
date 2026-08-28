package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/t479842598/u1s12api-go/internal/store"
	"github.com/t479842598/u1s12api-go/internal/upstream"
)

const deviceQuotaErrBody = `{"error":{"message":"免费用量包余额不足，请到仪表盘查看可用包或充值","type":"insufficient_quota","code":"quota_exceeded"}}`

const deviceChatOKBody = `{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`

// mkDeviceAccount 创建一个已授权设备账号并写入打卡剩余额度。
func mkDeviceAccount(t *testing.T, fx *fixture, email, tok string, remaining int64) *store.Account {
	t.Helper()
	privJWK, pubJWK, err := upstream.GenerateDeviceKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	pj, _ := json.Marshal(privJWK)
	uj, _ := json.Marshal(pubJWK)
	ok, err := fx.srv.store.AddAccount(email, "pw", "")
	if err != nil || !ok {
		t.Fatalf("AddAccount %s: ok=%v err=%v", email, ok, err)
	}
	a, err := fx.srv.store.GetAccountByEmail(email)
	if err != nil {
		t.Fatal(err)
	}
	if err := fx.srv.store.SaveAccountDeviceCredential(a.ID, tok, "u1s1-"+tok, "508", string(pj), string(uj), "dev-"+email); err != nil {
		t.Fatal(err)
	}
	if err := fx.srv.store.MarkAccountCheckin(a.ID, remaining); err != nil {
		t.Fatal(err)
	}
	return a
}

// postDeviceChat 用本地 key 发一次对话请求。
func postDeviceChat(t *testing.T, url, localKey string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("POST", url+"/v1/chat/completions",
		strings.NewReader(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+localKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// deviceChatFixture 假上游：token 含 denyTok 的账号返回 429 额度耗尽，其余成功。
func deviceChatFixture(t *testing.T, denyTok string) *fixture {
	return setupTest(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(validUpstreamModelsResp))
		case "/chat/completions":
			if strings.Contains(r.Header.Get("Authorization"), denyTok) {
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(deviceQuotaErrBody))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(deviceChatOKBody))
		default:
			http.NotFound(w, r)
		}
	})
}

// prepareDeviceChatFX 预热模型缓存与本地 key。
func prepareDeviceChatFX(t *testing.T, fx *fixture) {
	t.Helper()
	fx.addKeys(t, "u1s1-modelkey1111111111")
	if _, _, err := fx.srv.fetchModels(context.Background()); err != nil {
		t.Fatalf("预热模型缓存失败: %v", err)
	}
	fx.addLocalKey(t, "default", "sk-local-test")
}

// TestDeviceChannelPreferredAccount 有额度的账号直接调用：额度最多的账号被直接选中
// 成功，不再先打额度最少的账号（否则每次请求都从额度最少的账号开始轮询）。
func TestDeviceChannelPreferredAccount(t *testing.T) {
	// least 账号一旦被调用即返回 429——调度正确时它不应被调用。
	fx := deviceChatFixture(t, "u1s1d-least")
	least := mkDeviceAccount(t, fx, "least@test.dev", "u1s1d-least", 500)
	most := mkDeviceAccount(t, fx, "most@test.dev", "u1s1d-most", 10000)
	middle := mkDeviceAccount(t, fx, "middle@test.dev", "u1s1d-middle", 1000)
	prepareDeviceChatFX(t, fx)

	resp := postDeviceChat(t, fx.ts.URL, "sk-local-test")
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("对话 status = %d", resp.StatusCode)
	}

	la, _ := fx.srv.store.GetAccount(least.ID)
	ma, _ := fx.srv.store.GetAccount(most.ID)
	mi, _ := fx.srv.store.GetAccount(middle.ID)
	if ma.TotalRequests == 0 {
		t.Errorf("有额度的账号（额度最多）应被直接调用，但 total_requests=0")
	}
	if la.TotalRequests != 0 {
		t.Errorf("不应先打额度最少的账号，但 least total_requests=%d", la.TotalRequests)
	}
	if mi.TotalRequests != 0 {
		t.Errorf("中间账号不应被使用，但 total_requests=%d", mi.TotalRequests)
	}

	// least 未被调用：不应出现 quota_exceeded 失败记录。
	recs, _ := fx.srv.store.ListRequests(store.RequestFilter{Limit: 50})
	for _, rec := range recs {
		if rec.Status == "error" && strings.Contains(rec.Error, "quota_exceeded") {
			t.Errorf("调度不应触发额度耗尽错误（least 被调用）: %s", rec.Error)
		}
	}
}

// TestDeviceChannelCooldownRotation 冷却轮询：额度最多的账号触发 quota_exceeded 后
// 被标记冷却，本请求切到下一个有额度的账号；后续请求不再调用已冷却账号。
func TestDeviceChannelCooldownRotation(t *testing.T) {
	fx := deviceChatFixture(t, "u1s1d-most")
	most := mkDeviceAccount(t, fx, "most@test.dev", "u1s1d-most", 10000)
	middle := mkDeviceAccount(t, fx, "middle@test.dev", "u1s1d-middle", 1000)
	least := mkDeviceAccount(t, fx, "least@test.dev", "u1s1d-least", 500)
	prepareDeviceChatFX(t, fx)

	// 第 1 次请求：most 429 → 冷却 → 切到 middle 成功。
	resp := postDeviceChat(t, fx.ts.URL, "sk-local-test")
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("第 1 次对话 status = %d", resp.StatusCode)
	}
	if !fx.srv.deviceIsExhausted(most.ID) {
		t.Errorf("most 触发 quota_exceeded 后应被标记当日额度耗尽冷却")
	}
	mi, _ := fx.srv.store.GetAccount(middle.ID)
	la, _ := fx.srv.store.GetAccount(least.ID)
	if mi.TotalRequests != 1 {
		t.Errorf("冷却后应切到下一个有额度的账号 middle，但 middle total_requests=%d", mi.TotalRequests)
	}
	if la.TotalRequests != 0 {
		t.Errorf("middle 已成功，least 不应被调用，但 total_requests=%d", la.TotalRequests)
	}

	// 第 2 次请求：已冷却的 most 不再被调用，middle 继续服务。
	resp = postDeviceChat(t, fx.ts.URL, "sk-local-test")
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("第 2 次对话 status = %d", resp.StatusCode)
	}
	ma2, _ := fx.srv.store.GetAccount(most.ID)
	mi2, _ := fx.srv.store.GetAccount(middle.ID)
	if ma2.TotalRequests != 0 {
		t.Errorf("已冷却账号不应再被调用，但 most total_requests=%d", ma2.TotalRequests)
	}
	if mi2.TotalRequests != 2 {
		t.Errorf("middle 应继续服务第 2 次请求，但 total_requests=%d", mi2.TotalRequests)
	}
}
