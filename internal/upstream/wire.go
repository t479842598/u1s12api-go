// 上游请求的**线格式**层：把头以官方（undici fetch）在字节层的样子写出去，并把
// 上游压缩过的响应解回来。
//
// 为什么单独一层：Go 的 http.Header.Set 会把头名规范化（dpop → Dpop、
// x-stainless-lang → X-Stainless-Lang），而 undici 发的头名**全部小写**。
// 头名在 HTTP 语义上大小写无关，但网关看到的字节不一样。实测（Go 1.26）：
// 直接给 map 赋小写键，net/http 会原样写出；但有两个特例必须走规范键 ——
//
//	user-agent：Request.write 用规范键查找，找不到就补 "User-Agent: Go-http-client/1.1"，
//	            于是出现两行 UA 且错误的那行在前；
//	accept-encoding：Transport 用规范键判断是否自己补 "gzip"，找不到就多出一行。
//
// 本项目 DisableCompression=true 且显式发小写 accept-encoding，故后者只需不发规范键。
//
// 已知残差（对齐到"集合/大小写/值"一致为止，以下四项在 Go 的 net/http 下无法消除，
// 要消除需自研 HTTP/1.1 写器，收益未证实，见 spec 04 设计 D-10）：
//  1. 头**顺序**：Go 按字母序写，undici 按插入序写。
//  2. ~~connection: keep-alive~~ —— 已消除：实测用小写键 `h["connection"]` 可以直接
//     透传该头，且不会让 Transport 误判为 close（它只查规范键上的 "close"）。
//  3. user-agent 的大小写：Go 硬写规范形式 "User-Agent:"。
//  4. TLS ClientHello：Go crypto/tls 与 Node/BoringSSL 天然不同。
package upstream

import (
	"compress/gzip"
	"compress/zlib"
	"io"
	"net/http"
	"strings"
)

// SetWireHeader 以小写头名写入（user-agent 例外，理由见包注释）。
// 值为空时不写 —— 官方在无 attestation 时也不发该头。
func SetWireHeader(h http.Header, name, value string) {
	if value == "" {
		return
	}
	if strings.EqualFold(name, "user-agent") {
		h.Set("User-Agent", value)
		return
	}
	h[name] = []string{value}
}

// ApplyWireHeaders 把 kv 全部按线格式写进 req。kv 的键应已是小写（fingerprint 包产出）。
func ApplyWireHeaders(req *http.Request, kv map[string]string) {
	for k, v := range kv {
		SetWireHeader(req.Header, strings.ToLower(k), v)
	}
}

// decodeResponseBody 按 content-encoding 包装上游响应体，并剔除会误导下游的头。
//
// 因为我们显式声明了 accept-encoding 且 Transport.DisableCompression=true，Go 不再
// 透明解压，必须自己处理。官方 1.5.0 也踩过同一件事：device-auth.js 的
// forwardedResponseHeaders 专门剔掉 content-encoding —— 上游已解压却把该头透传给
// 下游，下游会二次解压失败（表现为 "terminated" / incorrect header check）。
//
// Node/undici 的 "deflate" 是 zlib 封装（不是 raw deflate），故用 zlib.NewReader。
// 解压失败不返回错误给调用方去阻断请求：记录性错误由调用方决定，这里只在
// **构造 reader 失败**时返回 error，读流中途坏掉则原样把错误抛给读方。
func decodeResponseBody(resp *http.Response) error {
	if resp == nil || resp.Body == nil {
		return nil
	}
	enc := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding")))
	if enc == "" {
		return nil
	}
	var (
		reader io.ReadCloser
		err    error
	)
	switch enc {
	case "gzip":
		reader, err = gzip.NewReader(resp.Body)
	case "deflate":
		reader, err = zlib.NewReader(resp.Body)
	default:
		// 未知编码：不动 body，也不剔头，让下游按原样处理。
		return nil
	}
	if err != nil {
		return err
	}
	resp.Body = reader
	resp.Header.Del("Content-Encoding")
	// 解压后长度已变，保留 content-length 会让下游截断或报错。
	resp.Header.Del("Content-Length")
	return nil
}
