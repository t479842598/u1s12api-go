package upstream

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/t479842598/u1s12api-go/internal/fingerprint"
)

// ================= 原始字节观测台 =================
//
// 为什么不用 httptest / http.ReadRequest 观察头：net/http 在服务端解析请求时会把
// 头名规范化（dpop → Dpop、x-u1s1-client → X-U1s1-Client），于是永远看不到线上
// 真实大小写 —— 而本轮修复（W2）的靶心正是大小写。这里自己 accept TCP 连接、
// 手工按 CRLF 切原始字节，键名保持客户端写出来的样子。

type rawRequest struct {
	method string
	path   string
	proto  string
	pairs  [][2]string // 线上出现顺序的 (name, value)，name 保留大小写
	body   string
}

func (r *rawRequest) names() []string {
	out := make([]string, 0, len(r.pairs))
	for _, kv := range r.pairs {
		out = append(out, kv[0])
	}
	return out
}

// get 大小写敏感地取头 —— 正是我们要断言的语义。
func (r *rawRequest) get(name string) string {
	for _, kv := range r.pairs {
		if kv[0] == name {
			return kv[1]
		}
	}
	return ""
}

// count 头名出现次数（断言没有重复 UA / accept-encoding）。
func (r *rawRequest) count(name string) int {
	n := 0
	for _, kv := range r.pairs {
		if kv[0] == name {
			n++
		}
	}
	return n
}

// rawResponse 测试侧要回给客户端的响应。
type rawResponse struct {
	status  int
	headers [][2]string
	body    []byte
}

// rawCapture 起一个裸 TCP 假上游，返回 baseURL 与"最近一次请求"读取器。
func rawCapture(t *testing.T, resp func(req *rawRequest) *rawResponse) (baseURL string, last func() *rawRequest) {
	t.Helper()
	var mu sync.Mutex
	var got *rawRequest
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			conn, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				br := bufio.NewReader(c)
				req := readRawRequest(br)
				if req == nil {
					return
				}
				mu.Lock()
				got = req
				mu.Unlock()
				r := resp(req)
				if r == nil {
					r = &rawResponse{status: 200, body: []byte("{}")}
				}
				code := r.status
				if code == 0 {
					code = 200
				}
				var sb strings.Builder
				fmt.Fprintf(&sb, "HTTP/1.1 %d %s\r\n", code, http.StatusText(code))
				for _, kv := range r.headers {
					fmt.Fprintf(&sb, "%s: %s\r\n", kv[0], kv[1])
				}
				fmt.Fprintf(&sb, "Content-Length: %d\r\n\r\n", len(r.body))
				_, _ = c.Write(append([]byte(sb.String()), r.body...))
			}(conn)
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return "http://" + ln.Addr().String(), func() *rawRequest {
		mu.Lock()
		defer mu.Unlock()
		return got
	}
}

// readRawRequest 从字节流里解析请求行 + 头 + 按 content-length 取 body。
func readRawRequest(br *bufio.Reader) *rawRequest {
	line, err := br.ReadString('\n')
	if err != nil {
		return nil
	}
	fields := strings.Fields(line)
	if len(fields) < 3 {
		return nil
	}
	req := &rawRequest{method: fields[0], path: fields[1], proto: fields[2]}
	contentLength := 0
	for {
		hdr, herr := br.ReadString('\n')
		if herr != nil {
			return req
		}
		hdr = strings.TrimRight(hdr, "\r\n")
		if hdr == "" {
			break
		}
		i := strings.Index(hdr, ": ")
		if i <= 0 {
			continue
		}
		name, value := hdr[:i], hdr[i+2:]
		req.pairs = append(req.pairs, [2]string{name, value})
		if strings.EqualFold(name, "content-length") {
			contentLength, _ = strconv.Atoi(strings.TrimSpace(value))
		}
	}
	if contentLength > 0 {
		buf := make([]byte, contentLength)
		if _, rerr := io.ReadFull(br, buf); rerr == nil {
			req.body = string(buf)
		}
	}
	return req
}

func jsonResp(body string) *rawResponse {
	return &rawResponse{
		status:  200,
		headers: [][2]string{{"Content-Type", "application/json"}},
		body:    []byte(body),
	}
}

// ================= W1：连接层必须是 HTTP/1.1 =================

// TestTransportsAreH1Only 白盒断言两条通道的 Transport 都关掉了 h2 与透明解压。
// 这三个字段就是 W1/W4 的根因所在：ALPN 不提供 h2 → 不可能协商到 h2；
// DisableCompression → accept-encoding 由我们自己发、响应由我们自己解。
func TestTransportsAreH1Only(t *testing.T) {
	dc := NewDeviceClient("https://api.u1s1.io/v1", "", func() string { return "1.7.1" }, nil)
	dt, ok := dc.httpClient().Transport.(*http.Transport)
	if !ok {
		t.Fatal("设备通道 Transport 类型意外")
	}
	assertH1Transport(t, "设备通道", dt)

	rt, err := buildTransport("")
	if err != nil {
		t.Fatal(err)
	}
	kt, ok := rt.(*http.Transport)
	if !ok {
		t.Fatal("Key 通道 Transport 类型意外")
	}
	assertH1Transport(t, "Key 通道", kt)
}

func assertH1Transport(t *testing.T, name string, tr *http.Transport) {
	t.Helper()
	if tr.ForceAttemptHTTP2 {
		t.Errorf("%s：ForceAttemptHTTP2 仍为 true，会在 ALPN 里提供 h2（官方 allowH2:false）", name)
	}
	if !tr.DisableCompression {
		t.Errorf("%s：DisableCompression 未开启，Transport 会自己补 Accept-Encoding 并透明解压", name)
	}
	if tr.TLSClientConfig == nil || len(tr.TLSClientConfig.NextProtos) != 1 ||
		tr.TLSClientConfig.NextProtos[0] != "http/1.1" {
		got := "nil"
		if tr.TLSClientConfig != nil {
			got = fmt.Sprint(tr.TLSClientConfig.NextProtos)
		}
		t.Errorf("%s：ALPN NextProtos = %s，期望 [http/1.1]", name, got)
	}
}

// TestClientRefusesH2OnlyServer 端到端证明：面对**只接受 h2** 的 TLS 服务，
// 我们的客户端必须握手失败（ALPN 无共同协议）。
//
// 旧行为（ForceAttemptHTTP2:true）会协商成功、之后才因自签证书报 x509；
// 新行为在 ALPN 阶段就谈不拢。两种失败信息不同，所以不需要让客户端信任这张证书。
func TestClientRefusesH2OnlyServer(t *testing.T) {
	addr, err := startH2OnlyTLSListener(t)
	if err != nil {
		t.Fatal(err)
	}
	dc := NewDeviceClient("https://"+addr+"/v1", "", func() string { return "1.7.1" }, nil)
	_, derr := dc.DeviceModels(context.Background(), deviceCredential(t), fingerprint.NodeUserAgent)
	if derr == nil {
		t.Fatal("只接受 h2 的服务不该被我们连上（说明我们提供了 h2）")
	}
	if strings.Contains(derr.Error(), "x509") {
		t.Errorf("错误是证书问题（%v），说明 ALPN 已谈成 h2 —— 我们仍在提供 h2", derr)
	}
	if !strings.Contains(strings.ToLower(derr.Error()), "protocol") &&
		!strings.Contains(strings.ToLower(derr.Error()), "handshake") {
		t.Logf("握手失败信息（供参考）：%v", derr)
	}
}

// startH2OnlyTLSListener 起一个 ALPN 只声明 h2 的 TLS 监听，返回地址。
func startH2OnlyTLSListener(t *testing.T) (string, error) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "h2-only-probe"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return "", err
	}
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	tlsLn := tls.NewListener(ln, &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: []string{"h2"}})
	go func() {
		for {
			c, aerr := tlsLn.Accept()
			if aerr != nil {
				return
			}
			_ = c.Close() // 只关心 ALPN 结果
		}
	}()
	t.Cleanup(func() { _ = tlsLn.Close() })
	return tlsLn.Addr().String(), nil
}

// ================= W2：线格式（小写头名 + 官方头集合） =================

func TestChatRequestWireForm(t *testing.T) {
	baseURL, last := rawCapture(t, func(*rawRequest) *rawResponse { return jsonResp(`{"choices":[]}`) })
	dc := NewDeviceClient(baseURL+"/v1", "", func() string { return "1.7.1" }, nil)
	resp, err := dc.DeviceChat(context.Background(), deviceCredential(t), []byte(`{"model":"m","messages":[]}`), "att-1")
	if err != nil {
		t.Fatalf("DeviceChat 失败: %v", err)
	}
	resp.Body.Close()

	req := last()
	if req == nil {
		t.Fatal("没抓到请求")
	}
	if req.proto != "HTTP/1.1" {
		t.Errorf("请求行协议 = %s，期望 HTTP/1.1", req.proto)
	}
	// 1) 官方全部小写线格式，逐个断言
	for _, want := range []string{
		"accept", "accept-language", "sec-fetch-mode", "accept-encoding",
		"x-u1s1-version", "x-u1s1-client", "x-u1s1-platform", "x-u1s1-attestation",
		"x-stainless-lang", "x-stainless-package-version", "x-stainless-os",
		"x-stainless-arch", "x-stainless-runtime", "x-stainless-runtime-version",
		"x-stainless-retry-count", "authorization", "dpop", "content-type",
	} {
		if req.get(want) == "" {
			t.Errorf("线上缺少小写头 %q，实际头名：%v", want, req.names())
		}
	}
	// 2) 除 Go 硬写的三个特例外，不允许出现任何大写头名。
	//    Host / Content-Length / User-Agent 由 net/http 的 Request.write 以规范形式写，
	//    无法小写（要消除需自研 h1 写器，见设计 D-10 已知残差）。
	for _, k := range req.names() {
		if k == "Host" || k == "Content-Length" || k == "User-Agent" {
			continue
		}
		if k != strings.ToLower(k) {
			t.Errorf("头名 %q 未以小写线格式发出", k)
		}
	}
	// 3) UA 只能一行且是 pi (...)，不能漏出 Go 默认值
	if n := req.count("User-Agent") + req.count("user-agent"); n != 1 {
		t.Errorf("UA 行数 = %d，期望 1（Go 特例与小写键打架了）", n)
	}
	if ua := req.get("User-Agent"); !strings.HasPrefix(ua, "pi (") {
		t.Errorf("User-Agent = %q，期望 pi (...)", ua)
	}
	// 4) accept-encoding 只能一行（Transport 不得再插手补 gzip）
	if n := req.count("accept-encoding") + req.count("Accept-Encoding"); n != 1 {
		t.Errorf("accept-encoding 行数 = %d，期望 1", n)
	}
	// 5) 值与官方抓包一致
	checks := map[string]string{
		"accept":              "application/json",
		"accept-language":     "*",
		"sec-fetch-mode":      "cors",
		"accept-encoding":     "gzip, deflate",
		"x-u1s1-client":       "terminal",
		"x-u1s1-version":      "1.7.1",
		"x-stainless-lang":    "js",
		"x-stainless-runtime": "node",
	}
	for k, want := range checks {
		if got := req.get(k); got != want {
			t.Errorf("线上头 %s = %q，期望 %q", k, got, want)
		}
	}
	// 6) 身份自洽：platform / UA / stainless-os / stainless-arch 必须同源
	plat := req.get("x-u1s1-platform")
	if !strings.Contains(req.get("User-Agent"), strings.Replace(plat, "-", " ", 1)) &&
		!strings.Contains(req.get("User-Agent"), strings.Split(plat, "-")[0]) {
		t.Errorf("UA %q 与 x-u1s1-platform %q 不自洽", req.get("User-Agent"), plat)
	}
}

func TestAuxRequestWireForm(t *testing.T) {
	baseURL, last := rawCapture(t, func(*rawRequest) *rawResponse { return jsonResp(`{"data":[]}`) })
	dc := NewDeviceClient(baseURL+"/v1", "", func() string { return "1.7.1" }, nil)
	if _, err := dc.DeviceModels(context.Background(), deviceCredential(t), fingerprint.NodeUserAgent); err != nil {
		t.Fatalf("DeviceModels 失败: %v", err)
	}
	req := last()
	for _, want := range []string{"accept", "accept-language", "sec-fetch-mode", "accept-encoding", "authorization", "dpop", "x-u1s1-version"} {
		if req.get(want) == "" {
			t.Errorf("辅助请求缺少小写头 %q，实际 %v", want, req.names())
		}
	}
	if got := req.get("accept"); got != "*/*" {
		t.Errorf("辅助请求 accept = %q，期望 */*", got)
	}
	if got := req.get("User-Agent"); got != "node" {
		t.Errorf("辅助请求 UA = %q，期望 node（CLI 不装 dispatcher）", got)
	}
	// 辅助端点绝不能带 X-Stainless-*（那是 SDK 的 chat 请求专属）
	for _, k := range req.names() {
		if strings.Contains(strings.ToLower(k), "stainless") {
			t.Errorf("辅助请求不应带 %s", k)
		}
	}
}

// TestAuthEndpointWireForm /auth/device/start：官方裸 fetch，只有 content-type + 运行时 UA，
// **不带** x-u1s1-version（带了反而是我们独有的特征）。
func TestAuthEndpointWireForm(t *testing.T) {
	baseURL, last := rawCapture(t, func(*rawRequest) *rawResponse {
		return jsonResp(`{"verify_url":"https://u1s1.io/verify","poll_secret":"ps","interval":2,"expires_in":900}`)
	})
	dc := NewDeviceClient(baseURL+"/v1", "", func() string { return "1.7.1" }, nil)
	pub := &DeviceJWK{Kty: "EC", Crv: "P-256", X: "AAA", Y: "BBB"}
	if _, err := dc.StartDeviceLogin(context.Background(), pub, "host (linux)", "1.7.1"); err != nil {
		t.Fatalf("StartDeviceLogin 失败: %v", err)
	}
	req := last()
	if got := req.get("x-u1s1-version"); got != "" {
		t.Errorf("auth 端点不应带 x-u1s1-version，实际 %q", got)
	}
	if got := req.get("User-Agent"); got != "node" {
		t.Errorf("auth UA = %q，期望 node", got)
	}
	if got := req.get("content-type"); got != "application/json" {
		t.Errorf("auth content-type = %q", got)
	}
}

// ================= W4：自行解压 =================

func gzipped(t *testing.T, raw string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(raw)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func deflated(t *testing.T, raw string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf)
	if _, err := zw.Write([]byte(raw)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestGzipJSONResponseDecoded 上游回 gzip 的 JSON 时必须交出明文。
// 我们显式声明了 accept-encoding 且 DisableCompression=true，Go 不再透明解压。
func TestGzipJSONResponseDecoded(t *testing.T) {
	payload := `{"object":"list","data":[{"id":"m1"},{"id":"m2"}],"client_attestation":{"token":"` + testAttestationToken + `","expires_in":604800}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(gzipped(t, payload))
	}))
	t.Cleanup(srv.Close)
	dc := NewDeviceClient(srv.URL+"/v1", "", func() string { return "1.7.1" }, nil)
	res, err := dc.DeviceModels(context.Background(), deviceCredential(t), fingerprint.NodeUserAgent)
	if err != nil {
		t.Fatalf("DeviceModels 失败: %v", err)
	}
	if res.ModelCount != 2 {
		t.Errorf("ModelCount = %d，期望 2（说明解压没生效）", res.ModelCount)
	}
}

func TestDeflateJSONResponseDecoded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "deflate")
		_, _ = w.Write(deflated(t, `{"object":"list","data":[{"id":"m1"}]}`))
	}))
	t.Cleanup(srv.Close)
	dc := NewDeviceClient(srv.URL+"/v1", "", func() string { return "1.7.1" }, nil)
	res, err := dc.DeviceModels(context.Background(), deviceCredential(t), fingerprint.NodeUserAgent)
	if err != nil {
		t.Fatalf("deflate 响应应能解开: %v", err)
	}
	if res.ModelCount != 1 {
		t.Errorf("ModelCount = %d，期望 1", res.ModelCount)
	}
}

// TestIdentityResponseUntouched 上游不压缩时原样透传（回归保护）。
func TestIdentityResponseUntouched(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"plain"}]}`))
	}))
	t.Cleanup(srv.Close)
	dc := NewDeviceClient(srv.URL+"/v1", "", func() string { return "1.7.1" }, nil)
	res, err := dc.DeviceModels(context.Background(), deviceCredential(t), fingerprint.NodeUserAgent)
	if err != nil || res.ModelCount != 1 {
		t.Fatalf("明文响应处理失败: %v %+v", err, res)
	}
}

// TestGzipStreamPassthrough 流式响应被 gzip 时，透传给下游的必须是明文 SSE，
// 且响应头不再带 content-encoding / content-length —— 官方 1.5.0 的
// forwardedResponseHeaders 专门剔这两个头，就是因为带着会让下游二次解压失败。
func TestGzipStreamPassthrough(t *testing.T) {
	sse := "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(gzipped(t, sse))
	}))
	t.Cleanup(srv.Close)
	dc := NewDeviceClient(srv.URL+"/v1", "", func() string { return "1.7.1" }, nil)
	resp, err := dc.DeviceChat(context.Background(), deviceCredential(t), []byte(`{"model":"m","messages":[]}`), "")
	if err != nil {
		t.Fatalf("DeviceChat 失败: %v", err)
	}
	defer resp.Body.Close()
	if ce := resp.Header.Get("Content-Encoding"); ce != "" {
		t.Errorf("透传响应仍带 content-encoding=%q", ce)
	}
	if cl := resp.Header.Get("Content-Length"); cl != "" {
		t.Errorf("解压后不应保留 content-length=%q", cl)
	}
	got, _ := io.ReadAll(resp.Body)
	if string(got) != sse {
		t.Errorf("流式解压结果不符：\n got=%q\nwant=%q", got, sse)
	}
}

// TestDecodeResponseBodyUnknownEncoding 未知编码不动 body 也不剔头（交给下游判断）。
func TestDecodeResponseBodyUnknownEncoding(t *testing.T) {
	resp := &http.Response{
		Header: http.Header{"Content-Encoding": []string{"zstd"}},
		Body:   io.NopCloser(strings.NewReader("raw")),
	}
	if err := decodeResponseBody(resp); err != nil {
		t.Fatalf("未知编码不该报错: %v", err)
	}
	if resp.Header.Get("Content-Encoding") != "zstd" {
		t.Error("未知编码不应被剔除")
	}
	got, _ := io.ReadAll(resp.Body)
	if string(got) != "raw" {
		t.Errorf("body 被动过: %q", got)
	}
}

// ================= W7：attestation 失败退避 =================

// TestAttestationFailureBackoffSingleProbe 上游持续失败时，多个请求只能产生一次探测。
// 这是本轮最重要的性能与形态修复：没有退避时，上游黑洞会让每个 chat 请求都串行多等
// 一次 /v1/models（实测最坏 +30s），并把 models:chat 比例推成 1:1（官方约 1:几千）。
func TestAttestationFailureBackoffSingleProbe(t *testing.T) {
	var probes int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&probes, 1)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"boom"}}`))
	}))
	t.Cleanup(srv.Close)
	dc := NewDeviceClient(srv.URL+"/v1", "", func() string { return "1.7.1" }, nil)
	m := NewAttestationManager(func() *DeviceClient { return dc })
	cred := deviceCredential(t)
	for i := 0; i < 5; i++ {
		if tok := m.Token(context.Background(), cred); tok != "" {
			t.Fatalf("签发失败时应返回空串，第 %d 次得到 %q", i+1, tok)
		}
	}
	if n := atomic.LoadInt32(&probes); n != 1 {
		t.Errorf("5 个请求产生了 %d 次 /v1/models 探测，期望 1（30s 冷却未生效）", n)
	}
}

// TestAttestationProbeResumesAfterCooldown 冷却期过后要恢复探测，否则账号永远拿不到令牌。
func TestAttestationProbeResumesAfterCooldown(t *testing.T) {
	var probes int32
	fail := true
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&probes, 1)
		mu.Lock()
		broken := fail
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if broken {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"message":"boom"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"m1"}],"client_attestation":{"token":"` + testAttestationToken + `","expires_in":604800}}`))
	}))
	t.Cleanup(srv.Close)
	dc := NewDeviceClient(srv.URL+"/v1", "", func() string { return "1.7.1" }, nil)
	m := NewAttestationManager(func() *DeviceClient { return dc })
	cred := deviceCredential(t)

	m.Token(context.Background(), cred) // 失败
	m.Token(context.Background(), cred) // 冷却，不探测
	if n := atomic.LoadInt32(&probes); n != 1 {
		t.Fatalf("冷却期应只探测 1 次，实际 %d", n)
	}
	// 把失败时间推过冷却窗口（等价于时间流逝 30s）
	m.mu.Lock()
	e := m.cache[cred.DeviceToken]
	e.lastFailure = e.lastFailure.Add(-attestFailCooldown - time.Second)
	m.cache[cred.DeviceToken] = e
	m.mu.Unlock()

	mu.Lock()
	fail = false
	mu.Unlock()
	if tok := m.Token(context.Background(), cred); tok == "" {
		t.Error("冷却期过后应恢复探测并拿到令牌")
	}
	if n := atomic.LoadInt32(&probes); n != 2 {
		t.Errorf("冷却后应再探测一次，总次数 %d", n)
	}
}

// TestAttestationKeepsOldTokenOnFailure 有旧令牌时失败要复用旧值，比不带更接近官方行为。
func TestAttestationKeepsOldTokenOnFailure(t *testing.T) {
	var fail atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if fail.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"message":"boom"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"m1"}],"client_attestation":{"token":"` + testAttestationToken + `","expires_in":604800}}`))
	}))
	t.Cleanup(srv.Close)
	dc := NewDeviceClient(srv.URL+"/v1", "", func() string { return "1.7.1" }, nil)
	m := NewAttestationManager(func() *DeviceClient { return dc })
	cred := deviceCredential(t)
	first := m.Token(context.Background(), cred)
	if first == "" {
		t.Fatal("首次应拿到令牌")
	}
	// 让它临期（进入 24h 刷新窗口）+ 上游转坏 → 应仍返回旧令牌而不是空
	m.mu.Lock()
	e := m.cache[cred.DeviceToken]
	e.expires = time.Now().Add(2 * time.Hour)
	m.cache[cred.DeviceToken] = e
	m.mu.Unlock()
	fail.Store(true)
	if got := m.Token(context.Background(), cred); got != first {
		t.Errorf("失败时应复用旧令牌，得到 %q（期望 %q…）", truncStr(got, 12), truncStr(first, 12))
	}
}

// TestAttestationConstantsMatchOfficial 三个常量对应官方 device-auth.js 的三个 MS 常量。
func TestAttestationConstantsMatchOfficial(t *testing.T) {
	if attestRefreshSkew != 24*time.Hour {
		t.Errorf("attestRefreshSkew = %v，期望 24h（官方 ATTESTATION_REFRESH_MARGIN_MS）", attestRefreshSkew)
	}
	if attestFailCooldown != 30*time.Second {
		t.Errorf("attestFailCooldown = %v，期望 30s（官方 ATTESTATION_REFRESH_COOLDOWN_MS）", attestFailCooldown)
	}
	if attestProbeCap != 4*time.Second {
		t.Errorf("attestProbeCap = %v，期望 4s（官方 ATTESTATION_BLOCK_TIMEOUT_MS）", attestProbeCap)
	}
	e := attestationEntry{token: "t", expires: time.Now().Add(20 * time.Hour)}
	if e.usable(time.Now(), attestRefreshSkew) {
		t.Error("距过期 20h 应视为临期（24h 窗口内）")
	}
	if !(attestationEntry{token: "t", expires: time.Now().Add(48 * time.Hour)}).usable(time.Now(), attestRefreshSkew) {
		t.Error("距过期 48h 应仍可用")
	}
}

// ================= W7：登录响应边界校验 =================

func TestStartDeviceLoginClampsBounds(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		// 越界值：interval 9999、expires_in 86400
		_, _ = w.Write([]byte(`{"verify_url":"https://u1s1.io/verify","poll_secret":"ps","interval":9999,"expires_in":86400}`))
	}))
	t.Cleanup(srv.Close)
	dc := NewDeviceClient(srv.URL+"/v1", "", func() string { return "1.7.1" }, nil)
	pub := &DeviceJWK{Kty: "EC", Crv: "P-256", X: "AAA", Y: "BBB"}
	resp, err := dc.StartDeviceLogin(context.Background(), pub, fingerprint.DeviceName(fingerprint.DetectProfile("v22.21.1")), "1.7.1")
	if err != nil {
		t.Fatalf("StartDeviceLogin 失败: %v", err)
	}
	if resp.Interval < 1 || resp.Interval > 30 {
		t.Errorf("interval 未被夹到 1..30，得到 %d", resp.Interval)
	}
	if resp.ExpiresIn < 1 || resp.ExpiresIn > 1800 {
		t.Errorf("expires_in 未被夹到 1..1800，得到 %d", resp.ExpiresIn)
	}
	var body map[string]any
	_ = json.Unmarshal([]byte(gotBody), &body)
	dn, _ := body["device_name"].(string)
	if !strings.Contains(dn, " (") || !strings.HasSuffix(dn, ")") {
		t.Errorf("device_name = %q，不符合 `<hostname> (<platform>)`", dn)
	}
	if strings.Contains(strings.ToLower(dn), "u1s12api") || strings.Contains(dn, "@") {
		t.Errorf("device_name 含项目标识或邮箱：%q", dn)
	}
}

func TestStartDeviceLoginRejectsBadVerifyURL(t *testing.T) {
	for _, bad := range []string{
		`{"verify_url":"javascript:alert(1)","poll_secret":"ps"}`,
		`{"verify_url":"https://u1s1.io/v","poll_secret":"ps\u0001"}`,
		`{"verify_url":"https://u1s1.io/v","poll_secret":""}`,
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(bad))
		}))
		dc := NewDeviceClient(srv.URL+"/v1", "", func() string { return "1.7.1" }, nil)
		pub := &DeviceJWK{Kty: "EC", Crv: "P-256", X: "A", Y: "B"}
		if _, err := dc.StartDeviceLogin(context.Background(), pub, "h (linux)", "1.7.1"); err == nil {
			t.Errorf("畸形 start 响应应被拒绝：%s", bad)
		}
		srv.Close()
	}
}

// TestPollValidatesCredentialShape 只接受形状合法的凭证（对齐官方 boundedCredential）。
func TestPollValidatesCredentialShape(t *testing.T) {
	rejected := []struct{ name, body string }{
		{"api_key 前缀错", `{"status":"ok","api_key":"sk-wrong","device_token":"u1s1d-ok","device_id":7}`},
		{"device_token 前缀错", `{"status":"ok","api_key":"u1s1-ok","device_token":"bad-token","device_id":7}`},
		{"含控制字符", "{\"status\":\"ok\",\"api_key\":\"u1s1-ok\",\"device_token\":\"u1s1d-a\\u0001b\",\"device_id\":7}"},
		{"未批准", `{"status":"pending"}`},
		{"expired", `{"status":"expired"}`},
	}
	for _, c := range rejected {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(c.body))
			}))
			defer srv.Close()
			dc := NewDeviceClient(srv.URL+"/v1", "", func() string { return "1.7.1" }, nil)
			got, err := dc.PollDeviceLoginOnce(context.Background(), "ps")
			if err != nil {
				t.Fatalf("不应报错: %v", err)
			}
			if got != nil {
				t.Errorf("畸形响应不应被当批准成功：%+v", got)
			}
		})
	}
}

// TestPollAcceptsWithoutPositiveDeviceID 官方对 device_id 的处理是"非正整数就留空"，
// 登录本身仍然成功（我们只把它当展示与 attestation 绑定核对用）。
func TestPollAcceptsWithoutPositiveDeviceID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","api_key":"u1s1-ok","device_token":"u1s1d-ok","device_id":0}`))
	}))
	t.Cleanup(srv.Close)
	dc := NewDeviceClient(srv.URL+"/v1", "", func() string { return "1.7.1" }, nil)
	got, err := dc.PollDeviceLoginOnce(context.Background(), "ps")
	if err != nil || got == nil {
		t.Fatalf("device_id 非正仍应接受登录（官方行为）: %v %+v", err, got)
	}
	if got.DeviceID.String() != "" {
		t.Errorf("非正 device_id 应被清掉，得到 %s", got.DeviceID.String())
	}
}

func TestPollAcceptsWellFormedCredential(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","api_key":"u1s1-abc","device_token":"u1s1d-xyz","device_id":656}`))
	}))
	t.Cleanup(srv.Close)
	dc := NewDeviceClient(srv.URL+"/v1", "", func() string { return "1.7.1" }, nil)
	got, err := dc.PollDeviceLoginOnce(context.Background(), "ps")
	if err != nil || got == nil {
		t.Fatalf("合法响应应被接受: %v %+v", err, got)
	}
	if got.DeviceID.String() != "656" {
		t.Errorf("device_id = %s", got.DeviceID.String())
	}
}

// ================= W6：身份按账号生效 =================

// TestDeviceChatUsesAccountIdentity 两个账号带不同身份快照时，各自请求的头必须与自身一致。
func TestDeviceChatUsesAccountIdentity(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, strings.Join([]string{
			r.Header.Get("x-u1s1-platform"), r.Header.Get("x-stainless-os"),
			r.Header.Get("x-stainless-arch"), r.Header.Get("User-Agent"),
		}, "|"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	t.Cleanup(srv.Close)
	// 全局身份故意给 mac：win 账号必须不受它影响
	dc := NewDeviceClient(srv.URL+"/v1", "", func() string { return "1.7.1" }, func() fingerprint.Profile {
		p, _ := fingerprint.ProfileByID("macos-arm64")
		return p
	})
	mac, _ := fingerprint.ProfileByID("macos-arm64")
	win, _ := fingerprint.ProfileByID("windows-x64")
	for _, p := range []fingerprint.Profile{mac, win} {
		cred := deviceCredential(t)
		cred.Profile = p
		resp, err := dc.DeviceChat(context.Background(), cred, []byte(`{"model":"m","messages":[]}`), "")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 {
		t.Fatalf("期望 2 次请求，实际 %d", len(seen))
	}
	if !strings.HasPrefix(seen[0], "darwin-arm64|MacOS|arm64|pi (darwin") {
		t.Errorf("mac 账号身份串了：%q", seen[0])
	}
	if !strings.HasPrefix(seen[1], "win32-x64|Windows|x64|pi (win32") {
		t.Errorf("win 账号身份串了：%q", seen[1])
	}
}

// TestAccountToCredentialFallsBackOnBadIdentity 身份快照损坏时回退全局，不阻断请求。
func TestAccountToCredentialFallsBackOnBadIdentity(t *testing.T) {
	priv, pub, err := GenerateDeviceKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	pj, _ := json.Marshal(priv)
	pbj, _ := json.Marshal(pub)
	fallback, _ := fingerprint.ProfileByID("linux-arm64")
	cred, err := AccountToCredential("u1s1d-x", string(pj), string(pbj), `{坏 JSON`, fallback)
	if err != nil {
		t.Fatalf("身份快照坏了不该失败: %v", err)
	}
	if cred.Profile.ID != fallback.ID {
		t.Errorf("未回退到全局身份：%+v", cred.Profile)
	}
	snap, _ := json.Marshal(fingerprint.Profile{ID: "x", UAPlatform: "darwin", UAArch: "arm64", UARelease: "25.6.0", Hostname: "h"})
	cred2, err := AccountToCredential("u1s1d-x", string(pj), string(pbj), string(snap), fallback)
	if err != nil {
		t.Fatal(err)
	}
	if cred2.Profile.UARelease != "25.6.0" {
		t.Errorf("合法快照未生效：%+v", cred2.Profile)
	}
}

// ================= 辅助 =================

func truncStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
