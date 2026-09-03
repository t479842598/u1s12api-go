package upstream

import (
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"regexp"
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
	dc := NewDeviceClient(srv.URL, "", func() string { return "1.3.1" }, profile)
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
	if h.Get("x-u1s1-version") != "1.3.1" {
		t.Errorf("x-u1s1-version = %q, 期望 1.3.1", h.Get("x-u1s1-version"))
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
		"X-U1s1-Version":              "1.3.1",
		"X-U1s1-Client":               "desktop", // 对齐桌面客户端（CLI 才是 terminal）
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
	dc := NewDeviceClient(srv.URL, "", func() string { return "1.3.1" }, nil)
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

// ---- 桌面客户端对齐（v0.9.7 逆向核对）----

// TestDeviceChatSurfaceIsDesktop x-u1s1-client 必须是 desktop。
// 桌面客户端（app 0.1.9）经 u1s1-cli/embed 调 ensureSigningProxy(cfg, "desktop", ...)；
// 只有 CLI TUI 才发 terminal。本项目对齐桌面客户端，写死断言防回退。
func TestDeviceChatSurfaceIsDesktop(t *testing.T) {
	if fingerprint.ClientSurface != "desktop" {
		t.Fatalf("fingerprint.ClientSurface = %q, 期望 desktop", fingerprint.ClientSurface)
	}
	dc, captured := deviceFixture(t)
	resp, err := dc.DeviceChat(context.Background(), deviceCredential(t),
		[]byte(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}]}`), "")
	if err != nil {
		t.Fatalf("DeviceChat 失败: %v", err)
	}
	resp.Body.Close()
	if got := captured().Get("x-u1s1-client"); got != "desktop" {
		t.Errorf("x-u1s1-client = %q, 期望 desktop", got)
	}
}

// TestDeviceChatKeepsStainlessHeaders 桌面端 chat 请求确实带 X-Stainless-*（7 个）。
// 抓包证据：桌面端 agent server 用 pi-ai 的 openai-completions（openai SDK 6.40.0）发请求，
// SDK 的 getPlatformHeaders() 无条件附加这些头，签名代理 requestHeaders() 只剔除
// host/connection/content-length/authorization/dpop，其余原样转发。
func TestDeviceChatKeepsStainlessHeaders(t *testing.T) {
	dc, captured := deviceFixture(t)
	resp, err := dc.DeviceChat(context.Background(), deviceCredential(t),
		[]byte(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}]}`), "")
	if err != nil {
		t.Fatalf("DeviceChat 失败: %v", err)
	}
	resp.Body.Close()
	h := captured()
	for _, k := range []string{
		"X-Stainless-Lang", "X-Stainless-Package-Version", "X-Stainless-OS",
		"X-Stainless-Arch", "X-Stainless-Runtime", "X-Stainless-Runtime-Version",
		"X-Stainless-Retry-Count",
	} {
		if h.Get(k) == "" {
			t.Errorf("桌面端 chat 应带 %s，实际缺失", k)
		}
	}
	// chat 用 SDK 的 pi UA，不是 undici（undici 只出现在裸 fetch 的辅助端点）。
	if ua := h.Get("User-Agent"); !strings.HasPrefix(ua, "pi (") {
		t.Errorf("chat User-Agent = %q, 期望 pi (...)", ua)
	}
}

// TestAuxEndpointsSendUndiciUserAgent /v1/me 与 /v1/models 是裸 fetch，
// 桌面端在 Next.js instrumentation 里执行过 undici.install()，因此 UA 是 undici。
// （回归点：Go 默认会发 Go-http-client/1.1。）
func TestAuxEndpointsSendUndiciUserAgent(t *testing.T) {
	dc, captured := deviceFixture(t)

	me, err := dc.DeviceMe(context.Background(), deviceCredential(t))
	if err != nil {
		t.Fatalf("DeviceMe 失败: %v", err)
	}
	_ = me
	if got := captured().Get("User-Agent"); got != "undici" {
		t.Errorf("/me User-Agent = %q, 期望 undici", got)
	}

	if _, err := dc.DeviceModels(context.Background(), deviceCredential(t)); err != nil {
		t.Fatalf("DeviceModels 失败: %v", err)
	}
	if got := captured().Get("User-Agent"); got != "undici" {
		t.Errorf("/models User-Agent = %q, 期望 undici", got)
	}
	// 辅助端点不带 X-Stainless-*（那些只属于 SDK 的 chat 请求）。
	if got := captured().Get("X-Stainless-Lang"); got != "" {
		t.Errorf("/models 不应带 X-Stainless-Lang, 实际 %q", got)
	}
}

// TestDeviceLoginRequestFingerprint /auth/device/start：裸 fetch（UA=undici）+
// public_jwk 按官方键序提交。
func TestDeviceLoginRequestFingerprint(t *testing.T) {
	var gotUA string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"verify_url":"https://u1s1.io/auth/device/verify?x=1","poll_secret":"ps","interval":2,"expires_in":900}`))
	}))
	t.Cleanup(srv.Close)
	dc := NewDeviceClient(srv.URL+"/v1", "", func() string { return "1.4.1" }, nil)

	pub := &DeviceJWK{Kty: "EC", Crv: "P-256", X: "AAA", Y: "BBB"}
	if _, err := dc.StartDeviceLogin(context.Background(), pub, "test-device", "1.4.1"); err != nil {
		t.Fatalf("StartDeviceLogin 失败: %v", err)
	}
	if gotUA != "undici" {
		t.Errorf("auth UA = %q, 期望 undici", gotUA)
	}
	wantJwk := `{"key_ops":["verify"],"ext":true,"kty":"EC","x":"AAA","y":"BBB","crv":"P-256"}`
	if !strings.Contains(string(gotBody), `"public_jwk":`+wantJwk) {
		t.Errorf("public_jwk 键序不符官方，body=%.300s", gotBody)
	}
}

// TestDpopProofStructureMatchesOfficial DPoP 头的 JSON 是签名输入的一部分，
// 键序/jti 形状必须与官方 device-auth.js 的 JSON.stringify 逐字节一致。
func TestDpopProofStructureMatchesOfficial(t *testing.T) {
	cred := deviceCredential(t)
	headers, err := dpopHeaders(cred.DeviceToken, cred.PublicJWK, cred.PrivateJWK,
		http.MethodPost, "https://api.u1s1.io/v1/chat/completions?x=1")
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(headers["dpop"], ".")
	if len(parts) != 3 {
		t.Fatalf("dpop 应为三段, 实际 %d", len(parts))
	}

	// 1) header 段：typ, alg, jwk{key_ops, ext, kty, x, y, crv}
	rawHeader, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatal(err)
	}
	wantHeader := `{"typ":"dpop+jwt","alg":"ES256","jwk":{"key_ops":["verify"],"ext":true,` +
		`"kty":"EC","x":"` + cred.PublicJWK.X + `","y":"` + cred.PublicJWK.Y + `","crv":"P-256"}}`
	if string(rawHeader) != wantHeader {
		t.Errorf("dpop header = %s, 期望 %s", rawHeader, wantHeader)
	}

	// 2) payload 段：字段顺序 jti, htm, htu, iat, ath（Go map 会被按字母排序，必须显式拼）
	rawPayload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	order := regexp.MustCompile(`^\{"jti":"([0-9a-f]{32})","htm":"POST","htu":"https://api\.u1s1\.io/v1/chat/completions","iat":\d+,"ath":"[A-Za-z0-9_-]{43}"\}$`)
	m := order.FindStringSubmatch(string(rawPayload))
	if m == nil {
		t.Fatalf("dpop payload 结构/键序不符官方: %s", rawPayload)
	}

	// 3) jti 是去掉连字符的 UUID v4：第 13 位是版本 4，第 17 位是变体 8|9|a|b
	jti := m[1]
	if jti[12] != '4' {
		t.Errorf("jti %s 第 13 位 = %c, 期望版本位 4", jti, jti[12])
	}
	if !strings.ContainsRune("89ab", rune(jti[16])) {
		t.Errorf("jti %s 第 17 位 = %c, 期望变体位 8|9|a|b", jti, jti[16])
	}

	// 4) 换过键序后签名仍必须自洽：用公钥验 ES256 签名
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	if len(sig) != 64 {
		t.Fatalf("ES256 签名应 64 字节, 实际 %d", len(sig))
	}
	pub, err := jwkToEC(cred.PublicJWK)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r := new(big.Int).SetBytes(sig[:32])
	s := new(big.Int).SetBytes(sig[32:])
	if !ecdsa.Verify(pub, digest[:], r, s) {
		t.Error("DPoP 签名验签失败（签名输入与公钥不一致）")
	}

	// 5) ath = base64url(sha256(device_token))
	ath := sha256.Sum256([]byte(cred.DeviceToken))
	if !strings.Contains(string(rawPayload), b64url(ath[:])) {
		t.Errorf("payload 里的 ath 不是 device token 的 sha256: %s", rawPayload)
	}
}

// TestDeviceJWKMarshalKeyOps 私钥导出带 key_ops:["sign"] 与 d，公钥是 ["verify"] 无 d。
func TestDeviceJWKMarshalKeyOps(t *testing.T) {
	priv, pub, err := GenerateDeviceKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	pj, err := json.Marshal(priv)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(pj), `{"key_ops":["sign"],"ext":true,"kty":"EC","x":`) {
		t.Errorf("私钥 JWK 键序不符官方: %s", pj)
	}
	if !strings.Contains(string(pj), `"crv":"P-256","d":"`) {
		t.Errorf("私钥 JWK 应在 crv 之后接 d: %s", pj)
	}
	gj, _ := json.Marshal(pub)
	if strings.Contains(string(gj), `"d"`) {
		t.Errorf("公钥 JWK 不应含 d: %s", gj)
	}
	// 往返解析（存储/读取）不受键序影响
	if _, err := parseJWK(string(gj)); err != nil {
		t.Errorf("公钥 JWK 反解析失败: %v", err)
	}
}
