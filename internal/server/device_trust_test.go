// 设备信任状态处置（spec 04 / F-10 / 设计 D-09）。
//
// 依据是官方 u1s1-cli 1.7.1 自己给状态码下的定义：
//   - 401 AuthError      → "设备被移除/换过钥匙"，重新授权可恢复；
//   - 403 AccessDeniedError → "封禁/停用/设备不受信任"，**重新登录也没用**，
//     官方命中后直接 process.exit(1) 停止一切请求（index.js:205-208）。
//
// 所以 403 绝不能当成"换一把凭证就好"去轮换 —— 那等于对一台已被判不受信任的
// 设备反复敲门，本身就是风控最容易识别的行为。
package server

import (
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/t479842598/u1s12api-go/internal/store"
)

const deviceNotTrustedBody = `{"error":{"message":"当前设备不受信任，账号已受限","type":"forbidden","code":"device_not_trusted"}}`
const deviceRetiredBody = `{"error":{"message":"登录已失效,请重新运行 u1s1 login","type":"unauthorized","code":"invalid_device_token"}}`

// trustFixture 假上游：命中 denyTok 的账号在 chat 上返回指定状态码，并统计被打次数。
func trustFixture(t *testing.T, denyTok string, status int, body string) (*fixture, func() map[string]int) {
	t.Helper()
	var mu sync.Mutex
	hits := map[string]int{}
	fx := setupTest(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(validUpstreamModelsResp))
		case "/chat/completions":
			auth := r.Header.Get("Authorization")
			mu.Lock()
			hits[auth]++
			mu.Unlock()
			if strings.Contains(auth, denyTok) {
				w.WriteHeader(status)
				_, _ = w.Write([]byte(body))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(deviceChatOKBody))
		default:
			http.NotFound(w, r)
		}
	})
	return fx, func() map[string]int {
		mu.Lock()
		defer mu.Unlock()
		out := map[string]int{}
		for k, v := range hits {
			out[k] = v
		}
		return out
	}
}

func hitsFor(hits map[string]int, tok string) int {
	n := 0
	for k, v := range hits {
		if strings.Contains(k, tok) {
			n += v
		}
	}
	return n
}

// captureBark 临时替换 Bark 推送入口，返回被推送的消息列表。
func captureBark(t *testing.T) *[]string {
	t.Helper()
	var mu sync.Mutex
	var sent []string
	orig := barkPushFn
	barkPushFn = func(key, title, body, url string) (bool, error) {
		mu.Lock()
		sent = append(sent, title+"|"+body)
		mu.Unlock()
		return true, nil
	}
	t.Cleanup(func() { barkPushFn = orig })
	return &sent
}

// Test403DisablesAccountAndStopsRotation 设备通道 403：账号停用、上游只被打一次、
// 不轮换到下一个账号、发出 Bark 告警、客户端拿到透传错误。
func Test403DisablesAccountAndStopsRotation(t *testing.T) {
	fx, getHits := trustFixture(t, "u1s1d-bad", http.StatusForbidden, deviceNotTrustedBody)
	bad := mkDeviceAccount(t, fx, "bad@test.dev", "u1s1d-bad", 9000) // 额度最高 → 必然被首选
	good := mkDeviceAccount(t, fx, "good@test.dev", "u1s1d-good", 100)
	sent := captureBark(t)
	prepareDeviceChatFX(t, fx)

	resp := postDeviceChat(t, fx.ts.URL, "sk-local-test")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("客户端应拿到透传的 403，实际 %d", resp.StatusCode)
	}

	hits := getHits()
	if n := hitsFor(hits, "u1s1d-bad"); n != 1 {
		t.Errorf("不受信任设备被打 %d 次，期望 1（官方语义：重登也没用，不该重试）", n)
	}
	if n := hitsFor(hits, "u1s1d-good"); n != 0 {
		t.Errorf("403 后不应再换下一个账号敲门，实际 good 被打 %d 次", n)
	}
	ga, _ := fx.srv.store.GetAccount(good.ID)
	if !ga.Enabled {
		t.Error("无辜账号不应被牵连停用")
	}
	ba, _ := fx.srv.store.GetAccount(bad.ID)
	if ba.Enabled {
		t.Error("403 账号必须被停用")
	}
	if ba.DeviceStatusReason == "" {
		t.Error("停用原因必须入库，便于后台展示与排障")
	}
	if len(*sent) == 0 {
		t.Error("403 停用必须 Bark 告警（否则账号会悄悄死掉）")
	}
}

// Test401MarksUnauthorizedAndKeepsRotating 401：标记需重新授权（不动 enabled），
// 但换下一个账号继续是合理的（那台设备确实失效了，别的设备可能正常）。
func Test401MarksUnauthorizedAndKeepsRotating(t *testing.T) {
	fx, getHits := trustFixture(t, "u1s1d-gone", http.StatusUnauthorized, deviceRetiredBody)
	gone := mkDeviceAccount(t, fx, "gone@test.dev", "u1s1d-gone", 9000)
	mkDeviceAccount(t, fx, "ok@test.dev", "u1s1d-ok", 100)
	prepareDeviceChatFX(t, fx)

	resp := postDeviceChat(t, fx.ts.URL, "sk-local-test")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("401 后应轮换到正常账号并成功，实际 %d", resp.StatusCode)
	}
	hits := getHits()
	if n := hitsFor(hits, "u1s1d-gone"); n != 1 {
		t.Errorf("失效设备被打 %d 次，期望 1", n)
	}
	ga, _ := fx.srv.store.GetAccount(gone.ID)
	if ga.Authorized {
		t.Error("401 账号应标记为需重新授权（authorized=0）")
	}
	if !ga.Enabled {
		t.Error("401 是可恢复状态，不应把账号整个停用")
	}
	if ga.DeviceStatusReason == "" {
		t.Error("需重新授权的原因应入库供后台展示")
	}
}

// TestInferenceNeverUsesKeyChannel 回归保护 v0.9.4 的架构不变量：
// 推理只走设备凭证通道，没有可用授权账号时直接 503，绝不拿旧版 u1s1- Key 去打上游
// （那条通道已被网关关进历史兼容窗口，继续打有封号风险）。
// 同时保护本次改动没把 KeyClientOnlyRejected 的分类语义带坏（单元级见 errorclass_test）。
func TestInferenceNeverUsesKeyChannel(t *testing.T) {
	var mu sync.Mutex
	chatCalls := 0
	fx := setupTest(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(validUpstreamModelsResp))
		case "/chat/completions":
			mu.Lock()
			chatCalls++
			mu.Unlock()
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"message":"API 推理请求仅支持 u1s1 客户端","type":"forbidden","code":"u1s1_client_only"}}`))
		default:
			http.NotFound(w, r)
		}
	})
	fx.addKeys(t, "u1s1-key1111111111", "u1s1-key2222222222")
	fx.addLocalKey(t, "default", "sk-local-test")
	if _, _, err := fx.srv.fetchModels(t.Context()); err != nil {
		t.Fatalf("预热模型失败: %v", err)
	}

	resp := postDeviceChat(t, fx.ts.URL, "sk-local-test")
	defer resp.Body.Close()
	mu.Lock()
	got := chatCalls
	mu.Unlock()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("无授权账号应 503 no_authorized_account，实际 %d", resp.StatusCode)
	}
	if got != 0 {
		t.Errorf("推理不该打上游 chat，实际打了 %d 次", got)
	}
}

// TestDisabledAccountNotSelected 已被停用的账号不再参与调度（403 停用的持久效果）。
func TestDisabledAccountNotSelected(t *testing.T) {
	fx, getHits := trustFixture(t, "", http.StatusOK, "")
	bad := mkDeviceAccount(t, fx, "off@test.dev", "u1s1d-off", 999999)
	if err := fx.srv.store.DisableAccountByGateway(bad.ID, "人工停用测试"); err != nil {
		t.Fatal(err)
	}
	mkDeviceAccount(t, fx, "on@test.dev", "u1s1d-on", 10)
	prepareDeviceChatFX(t, fx)

	resp := postDeviceChat(t, fx.ts.URL, "sk-local-test")
	resp.Body.Close()
	if n := hitsFor(getHits(), "u1s1d-off"); n != 0 {
		t.Errorf("已停用账号不应被调用，实际 %d 次", n)
	}
}

// TestStoreDeviceStatusMethods store 层三个状态方法的基本契约。
func TestStoreDeviceStatusMethods(t *testing.T) {
	fx := setupTest(t, func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	a := mkDeviceAccount(t, fx, "st@test.dev", "u1s1d-st", 100)

	if err := fx.srv.store.DisableAccountByGateway(a.ID, "403 不受信任"); err != nil {
		t.Fatal(err)
	}
	got, _ := fx.srv.store.GetAccount(a.ID)
	if got.Enabled || got.DeviceStatusReason != "403 不受信任" {
		t.Errorf("停用未生效：%+v", got)
	}
	// 重授权要恢复可用并清掉原因
	if err := fx.srv.store.SaveAccountDeviceCredential(a.ID, "u1s1d-st", "u1s1-x", "509", "{}", "{}", "h (linux)", `{"id":"auto"}`); err != nil {
		t.Fatal(err)
	}
	got, _ = fx.srv.store.GetAccount(a.ID)
	if !got.Enabled || !got.Authorized || got.DeviceStatusReason != "" {
		t.Errorf("重授权后应完全恢复：%+v", got)
	}
	if got.DeviceIdentity != `{"id":"auto"}` {
		t.Errorf("身份快照未入库：%q", got.DeviceIdentity)
	}
	if err := fx.srv.store.MarkAccountUnauthorized(a.ID, "401 需重授权"); err != nil {
		t.Fatal(err)
	}
	got, _ = fx.srv.store.GetAccount(a.ID)
	if got.Authorized || !got.Enabled {
		t.Errorf("401 标记应只清 authorized：%+v", got)
	}
	var _ *store.Account = got
}

// TestIdentityBackfillActuallyWrites 老库账号没有身份快照时，首次使用要真的回填成功。
//
// 这条测试是防一个已修掉的静默失效：UpdateAccount 是 enabled/note/password 白名单实现，
// 用它写 device_identity 不会报错、但也永远写不进去。
func TestIdentityBackfillActuallyWrites(t *testing.T) {
	fx := setupTest(t, func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	mkDeviceAccount(t, fx, "bf@test.dev", "u1s1d-bf", 100)
	// 从库里重新取：mkDeviceAccount 返回的是写凭证之前取的那一行，JWK 还是空的。
	a, err := fx.srv.store.GetAccountByEmail("bf@test.dev")
	if err != nil {
		t.Fatal(err)
	}
	if a.DeviceIdentity != "" {
		t.Fatal("测试前提：新账号应无身份快照")
	}
	cred, err := fx.srv.accountCredential(a)
	if err != nil {
		t.Fatal(err)
	}
	if cred.Profile.UAPlatform == "" {
		t.Error("凭证应带上可用的身份（快照缺失时回退全局）")
	}
	fresh, _ := fx.srv.store.GetAccount(a.ID)
	if fresh.DeviceIdentity == "" {
		t.Fatal("首次使用后身份快照必须已回填")
	}
	// 回填要稳定：再取一次不应变化
	cred2, _ := fx.srv.accountCredential(fresh)
	if cred2.Profile.UAPlatform != cred.Profile.UAPlatform || cred2.Profile.RuntimeVersion != cred.Profile.RuntimeVersion {
		t.Errorf("回填后身份漂移：%+v vs %+v", cred2.Profile, cred.Profile)
	}
	// 回填是幂等的：SetAccountDeviceIdentity 带 AND device_identity='' 条件，
	// 已有快照时不得被覆盖（换身份只能走重授权）。
	if err := fx.srv.store.SetAccountDeviceIdentity(a.ID, `{"id":"second","ua_platform":"aix","ua_arch":"x64"}`); err != nil {
		t.Fatal(err)
	}
	after, _ := fx.srv.store.GetAccount(a.ID)
	if after.DeviceIdentity != fresh.DeviceIdentity {
		t.Errorf("已绑定的快照被回填覆盖了：%q → %q", fresh.DeviceIdentity, after.DeviceIdentity)
	}
	// 重授权（SaveAccountDeviceCredential）才允许换快照
	pinned := `{"id":"pinned","ua_platform":"freebsd","ua_arch":"x64"}`
	if err := fx.srv.store.SaveAccountDeviceCredential(a.ID, after.DeviceToken, after.APIKey, after.DeviceID,
		after.DevicePrivateJWK, after.DevicePublicJWK, "h (freebsd)", pinned); err != nil {
		t.Fatal(err)
	}
	got, _ := fx.srv.store.GetAccount(a.ID)
	c3, err := fx.srv.accountCredential(got)
	if err != nil {
		t.Fatal(err)
	}
	if c3.Profile.UAPlatform != "freebsd" {
		t.Errorf("重授权后应使用新快照：%+v", c3.Profile)
	}
}

// TestPinDeviceIdentityOnUpgrade 升级启动时把既有授权账号钉成部署档案，
// 这样它们对外发的 platform/UA 与升级前逐字节相同 —— 不需要重新授权。
func TestPinDeviceIdentityOnUpgrade(t *testing.T) {
	fx := setupTest(t, func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	a := mkDeviceAccount(t, fx, "pin1@test.dev", "u1s1d-pin1", 100)
	mkDeviceAccount(t, fx, "pin2@test.dev", "u1s1d-pin2", 200)
	// 未授权账号不该被钉（它还没有设备凭证，将来授权时自然用当时的身份）
	fx.srv.store.AddAccount("fresh@test.dev", "", "")

	n, err := fx.srv.store.PinDeviceIdentityForAccounts(fx.srv.currentIdentityJSON())
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("应钉住 2 个已授权账号，实际 %d", n)
	}
	got, _ := fx.srv.store.GetAccount(a.ID)
	if got.DeviceIdentity == "" {
		t.Fatal("已授权账号未被钉身份")
	}
	cred, err := fx.srv.accountCredential(got)
	if err != nil {
		t.Fatal(err)
	}
	// 钉住的身份必须就是部署当前身份（升级前后一致）
	want := fx.srv.fp.Current()
	if cred.Profile.UAPlatform != want.UAPlatform || cred.Profile.UAArch != want.UAArch ||
		cred.Profile.UARelease != want.UARelease || cred.Profile.RuntimeVersion != want.RuntimeVersion {
		t.Errorf("钉住的身份与部署身份不符：got=%+v want=%+v", cred.Profile, want)
	}
	// 幂等：再跑一次不重复更新、也不改变已钉住的值
	if n2, _ := fx.srv.store.PinDeviceIdentityForAccounts(`{"id":"other","ua_platform":"aix","ua_arch":"x64"}`); n2 != 0 {
		t.Errorf("已钉住的账号不应被再次覆盖，第二次影响 %d 行", n2)
	}
	after, _ := fx.srv.store.GetAccount(a.ID)
	if after.DeviceIdentity != got.DeviceIdentity {
		t.Error("第二次钉身份改变了已有快照")
	}
	// 未授权账号仍为空（将来授权时用当时身份）
	if f, err := fx.srv.store.GetAccountByEmail("fresh@test.dev"); err == nil && f.DeviceIdentity != "" {
		t.Error("未授权账号不该被钉身份")
	}
}
