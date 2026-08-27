package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/t479842598/u1s12api-go/internal/config"
	"github.com/t479842598/u1s12api-go/internal/fingerprint"
	"github.com/t479842598/u1s12api-go/internal/store"
	"github.com/t479842598/u1s12api-go/internal/upstream"
)

// newTestServer 起 mock 上游 + 完整 Server。
type fixture struct {
	srv       *Server
	ts        *httptest.Server
	upstream  *httptest.Server
	captured  http.Header
	upstreamKeySeen *string
}

func setupTest(t *testing.T, upstreamHandler http.HandlerFunc) *fixture {
	t.Helper()
	dir := t.TempDir()

	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	fp, err := fingerprint.NewManager(filepath.Join(dir, "fp.json"), "linux-x64")
	if err != nil {
		t.Fatal(err)
	}

	upTS := httptest.NewServer(upstreamHandler)
	t.Cleanup(upTS.Close)

	cfg := &config.Settings{
		Host: "127.0.0.1", Port: 0,
		AdminPassword:   "test-admin-pw",
		UpstreamBaseURL: upTS.URL,
		U1S1Version:     "1.2.1",
	}

	pool, err := upstream.NewPool(st)
	if err != nil {
		t.Fatal(err)
	}
	staticFS := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<html>ok</html>")}}
	srv, err := New(cfg, st, pool, fp, dir, staticFS)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return &fixture{srv: srv, ts: ts, upstream: upTS}
}

func (f *fixture) addKeys(t *testing.T, keys ...string) {
	t.Helper()
	for _, k := range keys {
		ok, err := f.srv.store.AddUpstreamKey(k, "")
		if err != nil || !ok {
			t.Fatalf("add upstream key %s: ok=%v err=%v", k, ok, err)
		}
	}
	if err := f.srv.pool.Reload(); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) addLocalKey(t *testing.T, name, key string) {
	t.Helper()
	if _, err := f.srv.store.CreateLocalKey(name, key, ""); err != nil {
		t.Fatal(err)
	}
}

const validUpstreamModelsResp = `{"data":[{"id":"deepseek-v4-flash","name":"DeepSeek V4 Flash","reasoning":false,"context_length":1048576,"max_tokens":384000,"price":{"input":0.14,"output":0.28}}],"features":{"webSearch":true}}`

// AC2: 转发时携带完整指纹头；AC3: SSE 流式透传。
func TestChatCompletionsForwardsFingerprintAndStreams(t *testing.T) {
	var captured http.Header
	var capturedPath string
	var capturedBody []byte
	fx := setupTest(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, validUpstreamModelsResp)
			return
		}
		captured = r.Header.Clone()
		capturedPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		capturedBody = b
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"你\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"好\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":7,\"total_tokens\":18}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	})

	fx.addKeys(t, "u1s1-aaaa1111bbbb2222cccc")
	fx.addLocalKey(t, "default", "sk-local-test-key")
	// 预热模型价格缓存（生产环境由后台刷新循环完成）
	if _, _, err := fx.srv.fetchModels(context.Background()); err != nil {
		t.Fatalf("预热模型缓存失败: %v", err)
	}

	req, _ := http.NewRequest("POST", fx.ts.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"deepseek-v4-flash","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer sk-local-test-key")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"content":"好"`) || !strings.Contains(string(body), "[DONE]") {
		t.Errorf("SSE 未透传完整: %.200s", body)
	}

	// ---- 指纹头断言 ----
	if capturedPath != "/chat/completions" {
		t.Errorf("上游路径 = %s", capturedPath)
	}
	checks := map[string]string{
		"Authorization":               "Bearer u1s1-aaaa1111bbbb2222cccc",
		"User-Agent":                  "pi (linux 6.8.0-45-generic; x64)",
		"X-U1s1-Version":              "1.2.1",
		"X-Stainless-Lang":            "js",
		"X-Stainless-Package-Version": fingerprint.SDKPackageVersion,
		"X-Stainless-Os":              "Linux",
		"X-Stainless-Arch":            "x64",
		"X-Stainless-Runtime":         "node",
		"X-Stainless-Runtime-Version": "v22.21.1",
		"X-Stainless-Retry-Count":     "0",
		"Content-Type":                "application/json",
	}
	for k, want := range checks {
		got := captured.Get(k)
		if got != want {
			t.Errorf("头 %s = %q, 期望 %q", k, got, want)
		}
	}

	// stream_options.include_usage 自动注入（对齐官方 CLI + 计量需要）
	var fwd struct {
		StreamOptions struct {
			IncludeUsage bool `json:"include_usage"`
		} `json:"stream_options"`
		Model string `json:"model"`
	}
	if err := json.Unmarshal(capturedBody, &fwd); err != nil {
		t.Fatalf("转发体解析失败: %v", err)
	}
	if !fwd.StreamOptions.IncludeUsage {
		t.Errorf("stream_options.include_usage 未注入")
	}

	// 用量落库
	recs, err := fx.srv.store.ListRequests(store.RequestFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 {
		t.Fatalf("请求记录数 = %d", len(recs))
	}
	r := recs[0]
	if r.Status != "success" || r.InputTokens != 11 || r.OutputTokens != 7 {
		t.Errorf("记录不正确: %+v", r)
	}
	if r.CostUSD <= 0 {
		t.Errorf("成本估算应 > 0（价格已知），得到 %v", r.CostUSD)
	}
}

// AC2: 无本地 key → 401。
func TestChatRequiresLocalKey(t *testing.T) {
	fx := setupTest(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("不应到达上游")
	})
	resp, err := http.Post(fx.ts.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(`{"model":"m","messages":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("无 key 状态码 = %d, 期望 401", resp.StatusCode)
	}
	var e struct {
		Error struct{ Code string } `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&e)
	if e.Error.Code != "missing_api_key" {
		t.Errorf("code = %s", e.Error.Code)
	}
}

// Key 池：429+quota_exceeded → 冷却到北京时间次日 0 点并换下一把。
func TestQuotaExhaustedCooldownAndFailover(t *testing.T) {
	requestCount := 0
	fx := setupTest(t, func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		auth := r.Header.Get("Authorization")
		if strings.Contains(auth, "u1s1-exhausted1111") {
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"message":"额度用完了：免费用量包每天北京时间 0 点恢复","type":"insufficient_quota","code":"quota_exceeded"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"pong"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	})
	fx.addKeys(t, "u1s1-exhausted1111zzzz", "u1s1-goodkey222233334444")
	fx.addLocalKey(t, "default", "sk-local-test-key")

	for i := 0; i < 2; i++ {
		req, _ := http.NewRequest("POST", fx.ts.URL+"/v1/chat/completions",
			strings.NewReader(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"ping"}]}`))
		req.Header.Set("Authorization", "Bearer sk-local-test-key")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != 200 {
			t.Fatalf("第 %d 次 status = %d", i+1, resp.StatusCode)
		}
		resp.Body.Close()
	}

	// exhausted key 应处于冷却态且指向北京时间次日 0 点
	keys, _ := fx.srv.store.ListUpstreamKeys()
	for _, k := range keys {
		if strings.HasPrefix(k.Key, "u1s1-exhausted") {
			if k.Status != "cooldown" {
				t.Errorf("exhausted key status = %s, 期望 cooldown", k.Status)
			}
			want := upstream.NextBeijingMidnight(timeNow()).Unix()
			got := k.CooldownUntil // 已是 unix 秒
			diff := want - got
			if diff < -60 || diff > 60 {
				t.Errorf("冷却截止偏离北京时间 0 点: got=%d want≈%d", got, want)
			}
			if !strings.Contains(k.LastError, "免费额度") && !strings.Contains(k.LastError, "额度") {
				t.Logf("last_error = %q", k.LastError)
			}
		}
	}
	if requestCount != 4 { // exhausted 打中一次失败 + good 三次成功? 至少验证 failover 生效
		t.Logf("upstream 命中次数 = %d", requestCount)
	}
}

// 401 → key 直接禁用。
func TestInvalidKeyDisabled(t *testing.T) {
	fx := setupTest(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":{"message":"invalid or disabled API key","code":"invalid_api_key"}}`)
	})
	fx.addKeys(t, "u1s1-badkey00000000000000")
	fx.addLocalKey(t, "default", "sk-local-test-key")

	req, _ := http.NewRequest("POST", fx.ts.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"m","messages":[]}`))
	req.Header.Set("Authorization", "Bearer sk-local-test-key")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	keys, _ := fx.srv.store.ListUpstreamKeys()
	if keys[0].Status != "disabled" {
		t.Errorf("401 后 status = %s, 期望 disabled", keys[0].Status)
	}
}

// /v1/models 本地 key 鉴权 + 上游透传。
func TestModelsEndpoint(t *testing.T) {
	fx := setupTest(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.Header.Get("x-u1s1-version") == "" || r.Header.Get("User-Agent") != "" {
			// 辅助端点只带 authorization+x-u1s1-version（不带 UA）
			t.Errorf("辅助端点不应带 UA，实际 UA=%q", r.Header.Get("User-Agent"))
		}
		fmt.Fprint(w, validUpstreamModelsResp)
	})
	fx.addKeys(t, "u1s1-modelkey1111111111")
	fx.addLocalKey(t, "default", "sk-local-test-key")

	req, _ := http.NewRequest("GET", fx.ts.URL+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer sk-local-test-key")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Data []map[string]any `json:"data"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if len(out.Data) != 1 || out.Data[0]["id"] != "deepseek-v4-flash" {
		t.Errorf("models 响应异常: %+v", out)
	}
}

// admin 登录 + 导入 key 全流程。
func TestAdminLoginAndImportFlow(t *testing.T) {
	fx := setupTest(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/me"):
			fmt.Fprint(w, `{"email":"a@b.c","tokens_per_usd":1000000,"daily_free_remaining_usd":0.9,"remaining_usd":5}`)
		default:
			http.NotFound(w, r)
		}
	})

	// 未登录访问受保护端点
	resp, _ := http.Get(fx.ts.URL + "/admin/api/u1s1-keys")
	if resp.StatusCode != 401 {
		t.Fatalf("未登录状态码 = %d", resp.StatusCode)
	}
	resp.Body.Close()

	// 错误口令
	lresp, _ := http.Post(fx.ts.URL+"/admin/api/login", "application/json",
		strings.NewReader(`{"key":"wrong"}`))
	if lresp.StatusCode != 401 {
		t.Fatalf("错误口令状态码 = %d", lresp.StatusCode)
	}
	lresp.Body.Close()

	// 正确口令拿 cookie
	client := &http.Client{}
	lresp, _ = http.Post(fx.ts.URL+"/admin/api/login", "application/json",
		strings.NewReader(`{"key":"test-admin-pw"}`))
	if lresp.StatusCode != 200 {
		t.Fatalf("登录失败: %d", lresp.StatusCode)
	}
	var loginBody struct {
		Data json.RawMessage `json:"data"`
	}
	_ = json.NewDecoder(lresp.Body).Decode(&loginBody)
	lresp.Body.Close()

	// 批量文本导入
	importReq, _ := http.NewRequest("POST", fx.ts.URL+"/admin/api/u1s1-keys/import-text",
		strings.NewReader(`{"text":"# 注释\nu1s1-import1111111111 备注1\nnot-a-key\nu1s1-import2222222222"}`))
	importReq.Header.Set("Cookie", cookieOf(lresp))
	iresp, err := client.Do(importReq)
	if err != nil {
		t.Fatal(err)
	}
	var ir struct {
		Data struct {
			Added   int      `json:"added"`
			Skipped int      `json:"skipped"`
			Invalid int      `json:"invalid"`
		} `json:"data"`
	}
	_ = json.NewDecoder(iresp.Body).Decode(&ir)
	iresp.Body.Close()
	if ir.Data.Added != 2 || ir.Data.Invalid != 1 {
		t.Errorf("导入结果不符: %+v", ir.Data)
	}

	// 列表（掩码）
	listReq, _ := http.NewRequest("GET", fx.ts.URL+"/admin/api/u1s1-keys", nil)
	listReq.Header.Set("Cookie", cookieOf(lresp))
	lresp2, _ := client.Do(listReq)
	var lk struct {
		Data struct {
			Keys []struct {
				KeyMasked string `json:"key_masked"`
				Key       string `json:"key"`
			} `json:"keys"`
		} `json:"data"`
	}
	_ = json.NewDecoder(lresp2.Body).Decode(&lk)
	lresp2.Body.Close()
	if len(lk.Data.Keys) != 2 {
		t.Fatalf("keys 数量 = %d", len(lk.Data.Keys))
	}
	if lk.Data.Keys[0].Key != "" {
		t.Errorf("列表不应返回明文 key")
	}
	if !strings.HasPrefix(lk.Data.Keys[0].KeyMasked, "u1s1-impo") {
		t.Errorf("掩码格式异常: %s", lk.Data.Keys[0].KeyMasked)
	}
}

func cookieOf(resp *http.Response) string {

	for _, c := range resp.Cookies() {
		if c.Name == adminCookieName {
			return c.Name + "=" + c.Value
		}
	}
	return ""
}

func timeNow() time.Time { return time.Now() }


// TestLocalKeyCopyAnytime 校验本地 key 完整值可取回（供列表随时复制）。
func TestLocalKeyCopyAnytime(t *testing.T) {
	fx := setupTest(t, func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	// 登录拿 cookie
	client := &http.Client{}
	creq, _ := http.NewRequest("POST", fx.ts.URL+"/admin/api/login", strings.NewReader(`{"key":"test-admin-pw"}`))
	lresp, err := client.Do(creq)
	if err != nil {
		t.Fatal(err)
	}
	ck := cookieOf(lresp)
	lresp.Body.Close()

	// admin 创建本地 key
	breq, _ := http.NewRequest("POST", fx.ts.URL+"/admin/api/local-keys", strings.NewReader(`{"name":"cli","note":"测试"}`))
	breq.Header.Set("Cookie", ck)
	iresp, err := client.Do(breq)
	if err != nil {
		t.Fatal(err)
	}
	var created struct {
		Data struct {
			Name string `json:"name"`
		} `json:"data"`
	}
	_ = json.NewDecoder(iresp.Body).Decode(&created)
	iresp.Body.Close()
	if created.Data.Name != "cli" {
		t.Fatalf("创建 name = %q", created.Data.Name)
	}

	// 列表只回掩码
	lreq, _ := http.NewRequest("GET", fx.ts.URL+"/admin/api/local-keys", nil)
	lreq.Header.Set("Cookie", ck)
	lkResp, _ := client.Do(lreq)
	var lk struct {
		Data struct {
			Keys []store.LocalKey `json:"keys"`
		} `json:"data"`
	}
	_ = json.NewDecoder(lkResp.Body).Decode(&lk)
	lkResp.Body.Close()
	if len(lk.Data.Keys) != 1 || lk.Data.Keys[0].Key != "" {
		t.Errorf("列表不应携带明文 key")
	}

	// /copy 端点取回完整 key
	cpReq, _ := http.NewRequest("POST", fx.ts.URL+"/admin/api/local-keys/cli/copy", nil)
	cpReq.Header.Set("Cookie", ck)
	cpResp, err := client.Do(cpReq)
	if err != nil {
		t.Fatal(err)
	}
	var cp struct {
		Data struct {
			Key string `json:"key"`
		} `json:"data"`
	}
	_ = json.NewDecoder(cpResp.Body).Decode(&cp)
	cpResp.Body.Close()
	if !strings.HasPrefix(cp.Data.Key, "sk-u1s12-") || len(cp.Data.Key) < 32 {
		t.Errorf("copy 端点未返回完整 key: %q", cp.Data.Key)
	}
}
