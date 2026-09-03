// Package upstream 官方设备凭证（u1s1d- + DPoP ES256 签名）客户端。
//
// 背景：U1S1 官网「仅限 u1s1 客户端使用」的加量包（login_checkin / new_user）
// 只有用官方客户端身份调用才消耗。官方客户端（CLI / 桌面端）用「设备登录」拿到
// u1s1d- 设备凭证，之后每个请求用 RFC 9449 DPoP（ES256）签名 + 完整客户端指纹头，
// 网关才识别为官方客户端。本文件实现同样的认证，供 u1s12api-go 消耗这批加量包。
//
// 指纹与请求头对齐的是**桌面客户端**（app 0.1.9，内嵌 u1s1-cli 1.3.0 + Node 22.23.1），
// 逐项取值与差异说明见 internal/fingerprint 包注释。
//
// 设备登录流程（与官方 login.js 对齐）：
//   POST {origin}/auth/device/start  提交公钥 → {verify_url, poll_secret, interval, expires_in}
//   浏览器打开 verify_url 登录并批准设备
//   POST {origin}/auth/device/poll  轮询 → {status ok, api_key, device_token, device_id}
package upstream

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/t479842598/u1s12api-go/internal/fingerprint"
)

// DeviceJWK 一个 EC P-256 密钥（JWK 表示）。
//
// 序列化顺序由 MarshalJSON 固定为官方客户端（Node webcrypto exportKey("jwk")）的键序：
//
//	公钥 {"key_ops":["verify"],"ext":true,"kty":"EC","x":...,"y":...,"crv":"P-256"}
//	私钥 {"key_ops":["sign"],"ext":true,"kty":"EC","x":...,"y":...,"crv":"P-256","d":...}
//
// 这不是可有可无的装饰：DPoP 头的 JSON 是 ES256 签名输入的一部分，键序不同
// 就会 base64url 出不同的 header 段（签名也跟着变），网关只要对 header 段做过哈希/采样
// 就能区分出我们。struct 字段顺带保留是为了可读性，真正的输出顺序看 MarshalJSON。
type DeviceJWK struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
	D   string `json:"d,omitempty"` // 仅私钥
}

// MarshalJSON 按官方键序输出 JWK（含 Node 导出的 key_ops / ext）。
// 所有序列化路径（/auth/device/start 的 public_jwk、data 目录里的设备密钥、
// DPoP 头里的 jwk）都经这里，保证与官方逐字节一致。
func (j DeviceJWK) MarshalJSON() ([]byte, error) {
	keyOps := `"verify"`
	if j.D != "" {
		keyOps = `"sign"`
	}
	parts := []string{
		`"key_ops":[` + keyOps + `]`,
		`"ext":true`,
		`"kty":` + jsonQuote(j.Kty),
		`"x":` + jsonQuote(j.X),
		`"y":` + jsonQuote(j.Y),
		`"crv":` + jsonQuote(j.Crv),
	}
	if j.D != "" {
		parts = append(parts, `"d":`+jsonQuote(j.D))
	}
	return []byte("{" + strings.Join(parts, ",") + "}"), nil
}

// jsonQuote 把字符串编成带引号的 JSON 字面量（官方用 JSON.stringify，字段顺序显式可控）。
func jsonQuote(s string) string {
	b, err := json.Marshal(s)
	if err != nil { // string 实际上不会序列化失败，回退只为不 panic
		return `""`
	}
	return string(b)
}

// DeviceClient 用设备凭证访问上游网关。
type DeviceClient struct {
	baseURL string
	proxy   string
	// clientVersion 当前 CLI 版本（x-u1s1-version 头）。
	clientVersion func() string
	// profile 当前指纹档案（x-u1s1-platform / UA / X-Stainless-* 与其自洽）。
	profile func() fingerprint.Profile
}

// NewDeviceClient 构造。baseURL 形如 https://api.u1s1.io/v1。
func NewDeviceClient(baseURL, proxy string, clientVersion func() string, profile func() fingerprint.Profile) *DeviceClient {
	return &DeviceClient{
		baseURL:         strings.TrimRight(baseURL, "/"),
		proxy:           proxy,
		clientVersion:   clientVersion,
		profile:         profile,
	}
}

// currentProfile 返回设备客户端应使用的指纹档案（缺省回退 macos-arm64）。
func (c *DeviceClient) currentProfile() fingerprint.Profile {
	if c.profile != nil {
		return c.profile()
	}
	return fingerprint.Profiles[0]
}

// apiOrigin /v1 → 域名根（auth 路由挂在根路径）。
func apiOrigin(baseURL string) string {
	return strings.TrimRight(strings.TrimSuffix(baseURL, "/v1"), "/")
}

func b64url(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
func b64urlBig(v *big.Int) string { return b64url(v.Bytes()) }

// byteReader 返回一个 reader（body 为 nil 时返回空 reader）。
func byteReader(b []byte) io.Reader {
	if b == nil {
		return strings.NewReader("")
	}
	return bytes.NewReader(b)
}

// jwkToEC 把 JWK 解析为 ecdsa.PublicKey。
func jwkToEC(jwk *DeviceJWK) (*ecdsa.PublicKey, error) {
	if jwk.Kty != "EC" || jwk.Crv != "P-256" {
		return nil, fmt.Errorf("unsupported jwk: kty=%s crv=%s", jwk.Kty, jwk.Crv)
	}
	xb, err := base64.RawURLEncoding.DecodeString(jwk.X)
	if err != nil {
		return nil, err
	}
	yb, err := base64.RawURLEncoding.DecodeString(jwk.Y)
	if err != nil {
		return nil, err
	}
	return &ecdsa.PublicKey{
		Curve: elliptic.P256(),
		X:     new(big.Int).SetBytes(xb),
		Y:     new(big.Int).SetBytes(yb),
	}, nil
}

// jwkToECPrivate 把 JWK 解析为 ecdsa.PrivateKey。
func jwkToECPrivate(jwk *DeviceJWK) (*ecdsa.PrivateKey, error) {
	pub, err := jwkToEC(jwk)
	if err != nil {
		return nil, err
	}
	if jwk.D == "" {
		return nil, fmt.Errorf("jwk 缺少私钥 d")
	}
	db, err := base64.RawURLEncoding.DecodeString(jwk.D)
	if err != nil {
		return nil, err
	}
	d := new(big.Int).SetBytes(db)
	return &ecdsa.PrivateKey{PublicKey: *pub, D: d}, nil
}

// dpopHtu 去除 query/hash，与官方 dpopHtu 一致。
func dpopHtu(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

// dpopHeaders 构造某请求的 DPoP 签名头（authorization + dpop）。
// 每个请求都要新签名（jti/iat 唯一）。
//
// 与官方 device-auth.js 的 dpopHeaders() 逐字段对齐（CLI 1.3.0/1.4.1 与桌面端同一份代码）：
//
//	header  = JSON.stringify({typ, alg, jwk: devicePublicJwk})
//	payload = JSON.stringify({jti, htm, htu, iat, ath})
//	jti     = randomUUID().replace(/-/g, "")
//
// header 里 jwk 的键序是 Node 导出顺（key_ops, ext, kty, x, y, crv），见 DeviceJWK.MarshalJSON。
// payload 官方用字面量对象，键序固定 jti, htm, htu, iat, ath；Go 的 map[string]any 会被
// json.Marshal 按字母排（ath 在前），签名输入就变了，所以这里用显式 JSON 字符串。
// jti 是去掉连字符的 UUID v4（见 uuidHex）。
func dpopHeaders(deviceToken string, pubJwk, privJWK *DeviceJWK, method, rawURL string) (map[string]string, error) {
	priv, err := jwkToECPrivate(privJWK)
	if err != nil {
		return nil, err
	}
	pubJSON, err := json.Marshal(pubJwk)
	if err != nil {
		return nil, err
	}
	header := b64url([]byte(`{"typ":"dpop+jwt","alg":"ES256","jwk":` + string(pubJSON) + `}`))
	athHash := sha256.Sum256([]byte(deviceToken))
	payload := b64url([]byte(`{"jti":` + jsonQuote(uuidHex()) +
		`,"htm":` + jsonQuote(strings.ToUpper(method)) +
		`,"htu":` + jsonQuote(dpopHtu(rawURL)) +
		`,"iat":` + strconv.FormatInt(time.Now().Unix(), 10) +
		`,"ath":` + jsonQuote(b64url(athHash[:])) + `}`))
	signingInput := header + "." + payload
	digest := sha256.Sum256([]byte(signingInput))
	r, s, err := ecdsa.Sign(rand.Reader, priv, digest[:])
	if err != nil {
		return nil, err
	}
	sig := b64url(rawSignature(r, s, priv.Curve.Params().BitSize))
	return map[string]string{
		"authorization": "DPoP " + deviceToken,
		"dpop":          header + "." + payload + "." + sig,
	}, nil
}

// rawSignature 把 ECDSA (r,s) 编码为 JWS ES256 所需的固定宽度 R||S 拼接（IEEE P1363）。
// P-256 每个分量 32 字节，共 64 字节；高位补零到 ceil(bits/8)。
func rawSignature(r, s *big.Int, bits int) []byte {
	size := (bits + 7) / 8
	out := make([]byte, size*2)
	rb := r.Bytes()
	sb := s.Bytes()
	copy(out[size-len(rb):size], rb)
	copy(out[size*2-len(sb):], sb)
	return out
}

// uuidHex 生成 UUID v4 并去掉连字符 —— 对齐官方 jti 的 randomUUID().replace(/-/g, "")。
// 32 位小写 hex，第 13 位固定为版本号 4，第 17 位为变体位（8|9|a|b）。
// 与随机 hex 的区别就在那两个半字节：官方那里有固定值，纯随机 hex 没有。
func uuidHex() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122 variant
	return hex.EncodeToString(b[:])
}

// parseJWK 从 JSON 字符串解析 JWK。
func parseJWK(s string) (*DeviceJWK, error) {
	if strings.TrimSpace(s) == "" {
		return nil, fmt.Errorf("空 JWK")
	}
	var jwk DeviceJWK
	if err := json.Unmarshal([]byte(s), &jwk); err != nil {
		return nil, err
	}
	return &jwk, nil
}

// ---- 设备登录握手 ----

// DeviceStartResp /auth/device/start 响应。
type DeviceStartResp struct {
	VerifyURL   string `json:"verify_url"`
	PollSecret  string `json:"poll_secret"`
	Interval    int    `json:"interval"`
	ExpiresIn   int    `json:"expires_in"`
}

// StartDeviceLogin 发起设备登录：提交公钥，返回 verify_url + poll_secret。
// 需要先有 P-256 密钥对；这里调用方传入公钥 JWK 或内部生成。
func (c *DeviceClient) StartDeviceLogin(ctx context.Context, pubJWK *DeviceJWK, deviceName, clientVersion string) (*DeviceStartResp, error) {
	u := apiOrigin(c.baseURL) + "/auth/device/start"
	body := map[string]any{
		"public_jwk":     pubJWK,
		"device_name":    deviceName,
		"client_version": clientVersion,
	}
	data, err := c.postJSON(ctx, u, body)
	if err != nil {
		return nil, err
	}
	var out DeviceStartResp
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// StartDeviceLoginGenerate 内部生成 P-256 密钥对并调用 start，返回密钥对与响应。
func (c *DeviceClient) StartDeviceLoginGenerate(ctx context.Context, deviceName, clientVersion string) (*DeviceJWK, *DeviceJWK, *DeviceStartResp, error) {
	privJWK, pubJWK, err := GenerateDeviceKeyPair()
	if err != nil {
		return nil, nil, nil, err
	}
	resp, err := c.StartDeviceLogin(ctx, pubJWK, deviceName, clientVersion)
	if err != nil {
		return nil, nil, nil, err
	}
	return privJWK, pubJWK, resp, nil
}

// GenerateDeviceKeyPair 生成 P-256 密钥对（返回私/公 JWK）。
func GenerateDeviceKeyPair() (*DeviceJWK, *DeviceJWK, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	pubJWK := &DeviceJWK{
		Kty: "EC", Crv: "P-256",
		X: b64urlBig(priv.PublicKey.X),
		Y: b64urlBig(priv.PublicKey.Y),
	}
	privJWK := &DeviceJWK{
		Kty: "EC", Crv: "P-256",
		X: b64urlBig(priv.PublicKey.X),
		Y: b64urlBig(priv.PublicKey.Y),
		D: b64urlBig(priv.D),
	}
	return privJWK, pubJWK, nil
}

// DevicePollResp /auth/device/poll 响应。
type DevicePollResp struct {
	Status      string `json:"status"`
	APIKey      string `json:"api_key"`
	DeviceToken string `json:"device_token"`
	DeviceID    json.Number `json:"device_id"`
}

// PollDeviceLoginOnce 单次轮询设备批准（不循环，由前端反复调用）。
// 返回批准成功凭证或 (nil, nil) 表示尚未批准。
func (c *DeviceClient) PollDeviceLoginOnce(ctx context.Context, pollSecret string) (*DevicePollResp, error) {
	u := apiOrigin(c.baseURL) + "/auth/device/poll"
	data, err := c.postJSON(ctx, u, map[string]any{"poll_secret": pollSecret})
	if err != nil {
		return nil, err
	}
	var resp DevicePollResp
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, nil
	}
	if resp.Status == "ok" && resp.APIKey != "" && resp.DeviceToken != "" {
		return &resp, nil
	}
	if resp.Status == "expired" {
		return nil, nil
	}
	return nil, nil
}

// PollDeviceLogin 轮询设备批准结果（interval 间隔，至 expiresIn 秒）。
// 返回批准成功（status=ok + 凭证）或 (nil, nil)。
func (c *DeviceClient) PollDeviceLogin(ctx context.Context, pollSecret string, interval, expiresIn int) (*DevicePollResp, error) {
	u := apiOrigin(c.baseURL) + "/auth/device/poll"
	deadline := time.Now().Add(time.Duration(expiresIn) * time.Second)
	if interval <= 0 {
		interval = 2
	}
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(interval) * time.Second):
		}
		data, err := c.postJSON(ctx, u, map[string]any{"poll_secret": pollSecret})
		if err != nil {
			continue // 网络抖动下一轮重试
		}
		var resp DevicePollResp
		if json.Unmarshal(data, &resp) != nil {
			continue
		}
		if resp.Status == "ok" && resp.APIKey != "" && resp.DeviceToken != "" {
			return &resp, nil
		}
		if resp.Status == "expired" {
			return nil, nil
		}
	}
	return nil, nil
}

func (c *DeviceClient) postJSON(ctx context.Context, u string, body any) ([]byte, error) {
	payload, _ := json.Marshal(body)
	if payload == nil {
		payload = []byte("{}")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, byteReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("content-type", "application/json")
	// 官方设备登录用裸 fetch，不覆盖 UA：桌面端（undici.install 后）发 undici，CLI 发 node。
	// 本项目对齐桌面客户端；必须显式设置，否则 Go 会发 Go-http-client/1.1。
	req.Header.Set("User-Agent", fingerprint.UndiciUserAgent)
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("device auth %d: %s", resp.StatusCode, string(data))
	}
	return data, nil
}

func (c *DeviceClient) httpClient() *http.Client {
	tr := &http.Transport{
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 120 * time.Second,
	}
	if c.proxy != "" {
		if u, err := url.Parse(c.proxy); err == nil && (u.Scheme == "http" || u.Scheme == "https") {
			tr.Proxy = http.ProxyURL(u)
		}
	}
	return &http.Client{Transport: tr}
}

// ---- 设备凭证调用网关（模拟官方客户端） ----

// DeviceMeResult /v1/me 中与签到/加量包相关字段（裁剪出需要的）。
type DeviceMeResult struct {
	Email string `json:"email"`
	// login_checkin 加量包剩余 token（nil=无）。
	LoginCheckinRemaining *int64  `json:"login_checkin_remaining"`
	Packages              []MePackage `json:"packages"`
	FreeClaim             string  `json:"free_claim"`
}

// MePackage /v1/me packages 里的单个加量包。
type MePackage struct {
	ID          int64  `json:"id"`
	Kind        string `json:"kind"`
	Scope       string `json:"scope"`
	DailyTokens *int64 `json:"daily_tokens"`
	TotalTokens *int64 `json:"total_tokens"`
	UsedToday   int64  `json:"used_today"`
	UsedTokens  int64  `json:"used_tokens"`
	Remaining   int64  `json:"remaining"`
	ExpiresAt   string `json:"expires_at"`
	Note        string `json:"note"`
	CreatedAt   string `json:"created_at"`
}

// DeviceMe 用设备凭证调 GET /v1/me（触发每日加量包发放），返回裁剪结果。
func (c *DeviceClient) DeviceMe(ctx context.Context, account *DeviceCredential) (*DeviceMeResult, error) {
	u := c.baseURL + "/me"
	headers, err := dpopHeaders(account.DeviceToken, account.PublicJWK, account.PrivateJWK, http.MethodGet, u)
	if err != nil {
		return nil, err
	}
	// 官方 fetchMe 带 x-u1s1-version（authorizedFetch 的 init.headers 里显式设置），
	// UA 是裸 fetch 的运行时默认值（桌面端 undici.install 后→ undici）。
	headers["x-u1s1-version"] = c.clientVersion()
	headers["user-agent"] = fingerprint.UndiciUserAgent
	resp, err := c.doDevice(ctx, http.MethodGet, "/me", headers, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, &APIError{StatusCode: resp.StatusCode, Body: string(data)}
	}
	var raw struct {
		Email    string      `json:"email"`
		Packages []MePackage `json:"packages"`
		FreeClaim string     `json:"free_claim"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("解析 /v1/me 响应失败: %w", err)
	}
	out := &DeviceMeResult{Email: raw.Email, Packages: raw.Packages, FreeClaim: raw.FreeClaim}
	for _, p := range raw.Packages {
		if p.Kind == "login_checkin" {
			r := p.Remaining
			out.LoginCheckinRemaining = &r
			break
		}
	}
	return out, nil
}

// DeviceModelsResult GET /v1/models（设备凭证）中与客户端证明相关的字段。
type DeviceModelsResult struct {
	// Attestation 网关签发的 x-u1s1-attestation 令牌（v1.3.0 新增）。
	// 官方客户端从该响应体 client_attestation.token 取值，注入签名代理转发的每个请求。
	Attestation string
	// ExpiresAt 令牌过期时刻（由 expires_in / 令牌 payload 的 exp 推出）。
	ExpiresAt time.Time
	// ModelCount 模型数量（仅用于日志与自检）。
	ModelCount int
}

// maxAttestationTokenLen 官方 fetchModels 的上限：token 长度 >1024 视为异常丢弃。
const maxAttestationTokenLen = 1024

// DeviceModels 用设备凭证调 GET /v1/models，取回网关签发的客户端证明令牌。
//
// 与官方 fetchModels 一致：只带 authorization(DPoP) + x-u1s1-version + 裸 fetch 的运行时
// UA（桌面端 undici.install 后→ undici），不带 X-Stainless-*（那些头只属于 SDK 发的 chat 请求）。
// 令牌是**按设备签发**的（payload 含 u=user id、d=device_id），无法伪造或跨账号复用，
// 且每次调用都会重新签发（nonce/exp 变化），因此必须按账号分别获取并缓存。
func (c *DeviceClient) DeviceModels(ctx context.Context, account *DeviceCredential) (*DeviceModelsResult, error) {
	u := c.baseURL + "/models"
	headers, err := dpopHeaders(account.DeviceToken, account.PublicJWK, account.PrivateJWK, http.MethodGet, u)
	if err != nil {
		return nil, err
	}
	headers["x-u1s1-version"] = c.clientVersion()
	headers["user-agent"] = fingerprint.UndiciUserAgent
	resp, err := c.doDevice(ctx, http.MethodGet, "/models", headers, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, &APIError{StatusCode: resp.StatusCode, Body: string(data)}
	}
	var raw struct {
		Data []json.RawMessage `json:"data"`
		// client_attestation 可能是对象或缺失（老网关）。
		ClientAttestation *struct {
			Token     string `json:"token"`
			ExpiresIn int64  `json:"expires_in"`
		} `json:"client_attestation"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("解析 /v1/models 响应失败: %w", err)
	}
	out := &DeviceModelsResult{ModelCount: len(raw.Data)}
	if raw.ClientAttestation == nil {
		return out, nil
	}
	tok := raw.ClientAttestation.Token
	if tok == "" || len(tok) > maxAttestationTokenLen {
		return out, nil
	}
	out.Attestation = tok
	out.ExpiresAt = attestationExpiry(tok, raw.ClientAttestation.ExpiresIn)
	return out, nil
}

// attestationExpiry 推算令牌过期时刻：优先用令牌 payload 里的 exp（权威，与网关同时钟），
// 其次用响应体的 expires_in，最后保守回退 1 小时。
func attestationExpiry(token string, expiresInSeconds int64) time.Time {
	if payload, ok := decodeAttestationPayload(token); ok && payload.Exp > 0 {
		return time.Unix(payload.Exp, 0)
	}
	if expiresInSeconds > 0 {
		return time.Now().Add(time.Duration(expiresInSeconds) * time.Second)
	}
	return time.Now().Add(time.Hour)
}

// AttestationPayload 令牌 payload（base64url(JSON)）里已知的字段。
type AttestationPayload struct {
	V   int    `json:"v"`   // 版本号
	U   int64  `json:"u"`   // user id
	D   int64  `json:"d"`   // device_id（与 accounts.device_id 对应）
	Exp int64  `json:"exp"` // 过期 unix 秒
	N   string `json:"n"`   // 随机 nonce
}

// decodeAttestationPayload 解析 `<base64url(json)>.<base64url(sig)>` 的 payload 部分。
func decodeAttestationPayload(token string) (*AttestationPayload, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return nil, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, false
	}
	var p AttestationPayload
	if json.Unmarshal(raw, &p) != nil {
		return nil, false
	}
	return &p, true
}

// DeviceChat 用设备凭证调 POST /v1/chat/completions（消耗「仅限客户端」加量包）。
// attestation 为网关签发的 x-u1s1-attestation 令牌，空串则不发该头（v1.3.0 前的行为）。
// 成功返回原始响应（流式 SSE 或 JSON），调用方负责 Close；失败返回 *APIError。
func (c *DeviceClient) DeviceChat(ctx context.Context, account *DeviceCredential, body []byte, attestation string) (*http.Response, error) {
	u := c.baseURL + "/chat/completions"
	dp, err := dpopHeaders(account.DeviceToken, account.PublicJWK, account.PrivateJWK, http.MethodPost, u)
	if err != nil {
		return nil, err
	}
	headers := map[string]string{}
	for k, v := range dp {
		headers[k] = v
	}
	// 追加客户端指纹头（与官方签名代理一致：代理把本地 SDK 请求头原样转发，
	// 再补 x-u1s1-client / x-u1s1-version / x-u1s1-platform / x-u1s1-attestation）。
	// X-Stainless-* 开套必须保留：它们由 openai SDK v6.40.0 的 getPlatformHeaders() 自动附加，
	// 而两个官方入口（CLI TUI 与桌面端 agent server）都用 pi-ai 的 openai-completions 发 chat，
	// 签名代理的 requestHeaders() 只剔除 host/connection/content-length/authorization/dpop。
	// 2026-09-03 用官方 ensureSigningProxy + 官方 pi-ai 客户端实跑抓包确认桌面端发这 7 个头。
	p := c.currentProfile()
	headers["user-agent"] = fingerprint.UserAgent(p)
	headers["x-u1s1-version"] = c.clientVersion()
	headers["x-u1s1-client"] = fingerprint.ClientSurface
	headers["x-u1s1-platform"] = fingerprint.ClientPlatform(p)
	headers["X-Stainless-Lang"] = "js"
	headers["X-Stainless-Package-Version"] = fingerprint.SDKPackageVersion
	headers["X-Stainless-OS"] = p.StainlessOS
	headers["X-Stainless-Arch"] = p.StainlessArch
	headers["X-Stainless-Runtime"] = "node"
	headers["X-Stainless-Runtime-Version"] = p.RuntimeVersion
	headers["X-Stainless-Retry-Count"] = "0"
	// v1.3.0 新增：网关签发的客户端证明。官方签名代理仅在拿到令牌时注入，缺失即不发。
	if attestation != "" {
		headers["x-u1s1-attestation"] = attestation
	}
	resp, err := c.doDevice(ctx, http.MethodPost, "/chat/completions", headers, body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		retryAfter := resp.Header.Get("Retry-After")
		defer resp.Body.Close()
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
		return nil, &APIError{StatusCode: resp.StatusCode, Body: string(data), RetryAfter: retryAfter}
	}
	return resp, nil
}

func (c *DeviceClient) doDevice(ctx context.Context, method, path string, headers map[string]string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, byteReader(body))
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if body != nil && req.Header.Get("content-type") == "" {
		req.Header.Set("content-type", "application/json")
	}
	return c.httpClient().Do(req)
}

// DeviceCredential 设备凭证聚合（供上层使用）。
type DeviceCredential struct {
	DeviceToken string
	PublicJWK   *DeviceJWK
	PrivateJWK  *DeviceJWK
}

// AccountToCredential 从 store.Account 构造 DeviceCredential。
func AccountToCredential(deviceToken, privJWKJSON, pubJWKJSON string) (*DeviceCredential, error) {
	priv, err := parseJWK(privJWKJSON)
	if err != nil {
		return nil, err
	}
	pub, err := parseJWK(pubJWKJSON)
	if err != nil {
		return nil, err
	}
	return &DeviceCredential{DeviceToken: deviceToken, PrivateJWK: priv, PublicJWK: pub}, nil
}
