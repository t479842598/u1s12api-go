package server

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/t479842598/u1s12api-go/internal/store"
)

// 生产实测错误体（2026-09-01 欧洲 VPS）：上游模型厂商内容审查。
// 它审查的是请求体文本，与凭证无关 —— 换账号/换 Key 必然同样 400。
const moderationErrBody = `{"error":{"message":"<400> ***.***.DataInspectionFailed: Input text data may contain inappropriate content.","type":"data_inspection_failed","code":"data_inspection_failed","upstream_status":400}}`

// scopedChatFixture 假上游：所有 chat 调用都回内容审查 400，并记录每次调用用的凭证。
func scopedChatFixture(t *testing.T) (*fixture, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var auths []string
	fx := setupTest(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(validUpstreamModelsResp))
		case "/chat/completions":
			mu.Lock()
			auths = append(auths, r.Header.Get("Authorization"))
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(moderationErrBody))
		default:
			http.NotFound(w, r)
		}
	})
	return fx, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), auths...)
	}
}

// TestRequestScopedErrorNoFanOut 回归：上游返回请求级 400（内容审查）时，
// 设备通道必须只打一个账号就透传，不得轮换其余账号、也不得回退 Key 池。
//
// 修复前：无条件 continue → 3 个设备账号 + 1 把 Key 共 4 次上游调用，
// 白烧额度并把「同内容跨多账号重复请求」的特征送给官方风控。
func TestRequestScopedErrorNoFanOut(t *testing.T) {
	fx, chatAuths := scopedChatFixture(t)
	most := mkDeviceAccount(t, fx, "most@test.dev", "u1s1d-most", 10000)
	_ = mkDeviceAccount(t, fx, "middle@test.dev", "u1s1d-middle", 1000)
	_ = mkDeviceAccount(t, fx, "least@test.dev", "u1s1d-least", 500)

	fx.addKeys(t, "u1s1-modelkey1111111111")
	if _, _, err := fx.srv.fetchModels(context.Background()); err != nil {
		t.Fatalf("预热模型缓存失败: %v", err)
	}
	fx.addLocalKey(t, "default", "sk-local-test")

	resp := postDeviceChat(t, fx.ts.URL, "sk-local-test")
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	// 1) 客户端拿到上游原始错误（保持透传设计），状态码 400。
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("透传状态码 = %d，期望 400（body=%.200s）", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "data_inspection_failed") {
		t.Errorf("应把上游内容审查错误原样透传，实际=%.200s", body)
	}

	// 2) 只发生一次上游 chat 调用，且是额度最多的账号。
	auths := chatAuths()
	if len(auths) != 1 {
		t.Fatalf("上游 chat 调用次数 = %d，期望 1（实际凭证: %v）", len(auths), auths)
	}
	if !strings.Contains(auths[0], "u1s1d-most") {
		t.Errorf("首个被调用的应是额度最多的账号，实际=%s", auths[0])
	}

	// 3) 其余账号与 Key 池均未被波及（不白烧额度、不制造跨账号重复特征）。
	// 注：TouchAccount 仅在成功时计数，失败调用不增 total_requests，
	// 首个被调用的账号已由上面 chatAuths() 精确断言；这里只验证未被调用过的账号。
	for _, email := range []string{"middle@test.dev", "least@test.dev"} {
		acc, err := fx.srv.store.GetAccountByEmail(email)
		if err != nil {
			t.Fatalf("GetAccountByEmail %s: %v", email, err)
		}
		if acc.TotalRequests != 0 {
			t.Errorf("%s 不应被调用，但 total_requests=%d", email, acc.TotalRequests)
		}
	}
	for _, a := range auths {
		if strings.HasPrefix(a, "Bearer u1s1-") {
			t.Errorf("请求级错误不应回退 Key 池，但 Key 通道被调用: %s", a)
		}
	}

	// 4) 请求记录只落一条错误，且不该被误判为额度耗尽而冷却账号。
	recs, _ := fx.srv.store.ListRequests(store.RequestFilter{Limit: 50})
	var errRecs int
	for _, rec := range recs {
		if rec.Status == "error" && strings.Contains(rec.Error, "data_inspection_failed") {
			errRecs++
		}
	}
	if errRecs != 1 {
		t.Errorf("内容审查错误记录 = %d 条，期望 1 条", errRecs)
	}
	if fx.srv.deviceIsExhausted(most.ID) {
		t.Error("内容审查 400 不应把账号标记为当日额度耗尽冷却")
	}
}

// TestQuotaErrorStillRotates 对照：额度耗尽（429 quota_exceeded）仍是凭证级故障，
// 必须照旧冷却该账号并轮换到下一个 —— 修复不能把这个方向也短路掉。
func TestQuotaErrorStillRotates(t *testing.T) {
	fx := deviceChatFixture(t, "u1s1d-most")
	mkDeviceAccount(t, fx, "most@test.dev", "u1s1d-most", 10000)
	middle := mkDeviceAccount(t, fx, "middle@test.dev", "u1s1d-middle", 1000)
	prepareDeviceChatFX(t, fx)

	resp := postDeviceChat(t, fx.ts.URL, "sk-local-test")
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("额度耗尽应轮换到下一个账号成功，实际 status=%d", resp.StatusCode)
	}
	ma, _ := fx.srv.store.GetAccount(middle.ID)
	if ma.TotalRequests == 0 {
		t.Error("middle 账号应作为轮换目标被调用")
	}
}
