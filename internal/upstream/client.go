// Package upstream 上游 U1S1 网关客户端与本地 Key 池。
package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/proxy"

	"github.com/t479842598/u1s12api-go/internal/fingerprint"
)

// Client 上游 HTTP 客户端：注入指纹头、可选出口代理。
type Client struct {
	baseURL string
	http    *http.Client
	fp      *fingerprint.Manager
	version func() string // 当前 x-u1s1-version
}

// NewClient 构造。baseURL 形如 https://api.u1s1.io/v1；proxyURL 为空则直连。
func NewClient(baseURL string, proxyURL string, fp *fingerprint.Manager, versionFn func() string) (*Client, error) {
	transport, err := buildTransport(proxyURL)
	if err != nil {
		return nil, err
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http: &http.Client{
			Transport: transport,
			Timeout:   0, // 流式响应不限时，由请求 context 控制
		},
		fp:      fp,
		version: versionFn,
	}, nil
}

func buildTransport(proxyURL string) (http.RoundTripper, error) {
	t := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          64,
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: 120 * time.Second,
	}
	if proxyURL == "" {
		return t, nil
	}
	u, err := url.Parse(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("解析出口代理 %q 失败: %w", proxyURL, err)
	}
	switch u.Scheme {
	case "http", "https":
		t.Proxy = http.ProxyURL(u)
	case "socks5", "socks5h":
		var auth *proxy.Auth
		if u.User != nil {
			pw, _ := u.User.Password()
			auth = &proxy.Auth{User: u.User.Username(), Password: pw}
		}
		dialer, derr := proxy.SOCKS5("tcp", hostPort(u), auth, proxy.Direct)
		if derr != nil {
			return nil, fmt.Errorf("socks5 拨号器创建失败: %w", derr)
		}
		ctxDialer, ok := dialer.(proxy.ContextDialer)
		if !ok {
			return nil, fmt.Errorf("socks5 拨号器不支持 context")
		}
		t.Proxy = nil
		t.DialContext = ctxDialer.DialContext
	default:
		return nil, fmt.Errorf("不支持的代理协议: %s", u.Scheme)
	}
	return t, nil
}

func hostPort(u *url.URL) string {
	if _, _, err := net.SplitHostPort(u.Host); err == nil {
		return u.Host
	}
	return net.JoinHostPort(u.Hostname(), defaultPort(u.Scheme))
}

func defaultPort(scheme string) string {
	if scheme == "https" {
		return "443"
	}
	return "1080"
}

// Version 当前 CLI 版本号。
func (c *Client) Version() string { return c.version() }

// ModelDef /models 返回的模型定义（字段名对齐上游）。
type ModelDef struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	Reasoning     bool    `json:"reasoning"`
	ContextLength int64   `json:"context_length"`
	MaxTokens     int64   `json:"max_tokens"`
	Price         Price   `json:"price"`
}

// Price 每百万 token 美元价。
type Price struct {
	Input     float64 `json:"input"`
	Output    float64 `json:"output"`
	CacheRead float64 `json:"cache_read,omitempty"`
}

// ModelsResponse GET /models 响应。
type ModelsResponse struct {
	Data         []ModelDef     `json:"data"`
	Features     map[string]any `json:"features,omitempty"`
	Announcement any            `json:"announcement,omitempty"`
}

// MeInfo GET /me 响应中的配额字段。
type MeInfo struct {
	Email                 string  `json:"email"`
	TokensPerUSD          float64 `json:"tokens_per_usd"`
	DailyFreeRemainingUSD float64 `json:"daily_free_remaining_usd"`
	RemainingUSD          float64 `json:"remaining_usd"`
	FreeClaim             string  `json:"free_claim,omitempty"`
}

func (c *Client) do(ctx context.Context, method, path string, headers map[string]string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if req.Header.Get("User-Agent") == "" {
		// 对齐 Node fetch（undici）：辅助端点不发 User-Agent。
		// Go 会默认补 "Go-http-client/1.1"，显式置空才能抑制。
		req.Header.Set("User-Agent", "")
	}
	if body != nil && req.Header.Get("content-type") == "" {
		req.Header.Set("content-type", "application/json")
	}
	return c.http.Do(req)
}

// Models 拉取模型列表。
func (c *Client) Models(ctx context.Context, apiKey string) (*ModelsResponse, error) {
	resp, err := c.do(ctx, http.MethodGet, "/models", fingerprint.AuxHeaders(apiKey, c.version()), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &APIError{StatusCode: resp.StatusCode, Body: string(data)}
	}
	out := &ModelsResponse{}
	if err := json.Unmarshal(data, out); err != nil {
		return nil, fmt.Errorf("解析 /models 响应失败: %w", err)
	}
	return out, nil
}

// Me 查询配额。
func (c *Client) Me(ctx context.Context, apiKey string) (*MeInfo, error) {
	resp, err := c.do(ctx, http.MethodGet, "/me", fingerprint.AuxHeaders(apiKey, c.version()), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &APIError{StatusCode: resp.StatusCode, Body: string(data)}
	}
	out := &MeInfo{}
	if err := json.Unmarshal(data, out); err != nil {
		return nil, fmt.Errorf("解析 /me 响应失败: %w", err)
	}
	return out, nil
}

// RawDo 发送预构建请求（供代理探测等复用传输层）。
func (c *Client) RawDo(req *http.Request) (*http.Response, error) {
	return c.http.Do(req)
}

// Chat 转发 chat/completions。成功时返回原始响应（流式 SSE 或 JSON），
// 调用方负责 Close；失败（状态码>=400）返回 *APIError。
func (c *Client) Chat(ctx context.Context, apiKey string, body []byte) (*http.Response, error) {
	headers := c.fp.ChatHeaders(apiKey, c.version())
	resp, err := c.do(ctx, http.MethodPost, "/chat/completions", headers, body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
		return nil, &APIError{StatusCode: resp.StatusCode, Body: string(data)}
	}
	return resp, nil
}

// APIError 上游非 2xx 响应。
type APIError struct {
	StatusCode int
	Body       string
}

func (e *APIError) Error() string {
	body := e.Body
	if len(body) > 300 {
		body = body[:300] + "…"
	}
	return fmt.Sprintf("upstream %d: %s", e.StatusCode, body)
}

// QuotaSignal 从错误体中识别「免费额度耗尽」信号：
//
//	HTTP 429 + {"code":"quota_exceeded","type":"insufficient_quota",
//	            "message":"额度用完了：…每天北京时间 0 点恢复…"}
//
// 命中后该 key 冷却到次日北京时间 0 点。
func QuotaSignal(statusCode int, body string) bool {
	if statusCode != http.StatusTooManyRequests {
		return false
	}
	lower := strings.ToLower(body)
	if strings.Contains(lower, `"quota_exceeded"`) ||
		strings.Contains(lower, `"insufficient_quota"`) ||
		strings.Contains(lower, "额度用完") ||
		strings.Contains(lower, "quota exhausted") {
		return true
	}
	return false
}

// NextBeijingMidnight 下一次北京时间 0 点。
func NextBeijingMidnight(now time.Time) time.Time {
	loc := time.FixedZone("CST", 8*3600)
	t := now.In(loc)
	midnight := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
	return midnight.Add(24 * time.Hour)
}
