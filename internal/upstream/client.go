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
		retryAfter := resp.Header.Get("Retry-After")
		defer resp.Body.Close()
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
		return nil, &APIError{StatusCode: resp.StatusCode, Body: string(data), RetryAfter: retryAfter}
	}
	return resp, nil
}

// APIError 上游非 2xx 响应。
type APIError struct {
	StatusCode int
	Body       string
	// RetryAfter 上游 Retry-After 头原值（缺省为空）。
	// Gateway 对可重试的 503 model_unavailable 会带该头（u1s1-cli 1.3.1 同期服务端变更），
	// 透传后才能让客户端按官方退避时长重试，而不是立刻反复重试。
	RetryAfter string
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

// ContentModerationRejected 识别上游模型厂商的「输入内容审查未通过」错误。
//
// 实测形态（阿里云 DashScope 风格，经 Gateway 透传，厂商名被网关打码）：
//
//	HTTP 400 {"error":{"message":"<400> ***.***.DataInspectionFailed: Input text
//	          data may contain inappropriate content.",
//	          "type":"data_inspection_failed","code":"data_inspection_failed",
//	          "upstream_status":400}}
//
// 它审查的是**请求体里的文本**，与用哪把 Key / 哪个设备账号无关，因此对同一请求体
// 是确定性的（见 RequestScopedError）。单独识别它是为了让日志与客户端错误信息
// 能指出「换内容」而不是让人误查网络/额度。
func ContentModerationRejected(statusCode int, body string) bool {
	if statusCode != http.StatusBadRequest {
		return false
	}
	lower := strings.ToLower(body)
	return strings.Contains(lower, "data_inspection_failed") ||
		strings.Contains(lower, "datainspectionfailed") ||
		strings.Contains(lower, "content_policy_violation") ||
		strings.Contains(lower, "inappropriate content")
}

// RequestScopedError 判定「由请求内容决定、与凭证无关」的上游错误 —— 换 Key /
// 换设备账号重试必然得到同样的结果，因此**必须立即停止轮换**、把上游错误原样透传。
//
// 为什么这是必须的（v0.9.1 生产实测踩坑）：设备通道原先对任何 APIError 都
// continue 换下一个账号，于是一次内容审查 400 被放大成「N 个设备账号 + Key 池」
// 共 N+1 次上游调用，后果是：
//  1. 白烧多个账号的免费额度（每次重试都是真实计费的上游请求）；
//  2. 客户端延迟成倍拉长（串行等待每个账号往返）；
//  3. 最危险：在官方风控里形成「同一内容跨多账号短时间内重复请求」的特征，
//     而 u1s1 v1.3.0 起「疑似非官方设备凭据代理会在后台累计风险证据，达到
//     处置条件后自动封禁」，配合 08-30 公告「Token 不得接入第三方工具」，
//     这种特征正是最容易被判为代理转发的模式。
//
// 判定口径：HTTP 400 一律视为请求级 —— 400 的语义就是「请求本身不合法」，
// 而请求体在所有凭证之间完全相同，凭证不是变量。典型成员：内容审查
// （data_inspection_failed）、未知模型（model_not_found）、请求体非法
// （invalid_request_error）。
//
// 例外：额度类错误实测走 429（见 QuotaSignal），这里显式排除，避免上游哪天
// 改口径把额度 400 也短路掉、导致该轮换账号时不轮换。
func RequestScopedError(statusCode int, body string) bool {
	if statusCode != http.StatusBadRequest {
		return false
	}
	if QuotaSignal(statusCode, body) {
		return false
	}
	lower := strings.ToLower(body)
	if strings.Contains(lower, "quota_exceeded") ||
		strings.Contains(lower, "insufficient_quota") ||
		strings.Contains(lower, "quota exhausted") ||
		strings.Contains(lower, "余额不足") {
		return false
	}
	return true
}

// CredentialScopedError 判定「与具体凭证相关、换下一把凭证可能解决」的上游错误。
//
// 只有这类错误才应在多凭证间轮换（继续下一个账号 / 下一把 Key）；非此类错误是
// **请求级**（请求体不合法，见 RequestScopedError）或**网关级**（如 503 model_unavailable，
// 与用哪把凭证无关），轮换无益，应立即透传，避免把一次客户端请求放大成 N 次上游调用、
// 并在官方风控里留下「同内容跨多凭证重复请求」特征。
func CredentialScopedError(statusCode int, body string) bool {
	if statusCode == http.StatusUnauthorized || // 401 凭证无效
		statusCode == http.StatusPaymentRequired || // 402 额度/付费
		statusCode == http.StatusForbidden || // 403（设备令牌失效、换设备等）
		statusCode == http.StatusTooManyRequests { // 429 限流/额度
		return true
	}
	return false
}

// KeyClientOnlyRejected 识别网关「旧版 u1s1- API Key 推理通道已被关闭」的 403。
//
// 实测形态（2026-09-01 直连真网关，带完整 Key 通道指纹头仍 403）：
//
//	HTTP 403 {"error":{"message":"API 推理请求仅支持 u1s1 客户端；旧版 API Key 仅在
//	          明确的历史兼容窗口内可用。请升级并重新登录…","type":"forbidden",
//	           "code":"u1s1_client_only"}}
//
// 它标志着 u1s1 已经收紧“仅限官方客户端”，纯 u1s1- API Key 的推理通道被关闭：
// 旧版 Key 只有仍在“历史兼容窗口”内才可能放行，且继续用非官方客户端的 Key
// 账号会按公告 #6 / v1.3.0 风控被**封禁**。命中后必须：原样透传给客户端并
// 停止换 Key 重试（换一把必然同样 403），同时把该 Key 标记禁用，避免池继续拾取、
// 反复触发导致账号封禁风险。
func KeyClientOnlyRejected(statusCode int, body string) bool {
	if statusCode != http.StatusForbidden {
		return false
	}
	lower := strings.ToLower(body)
	return strings.Contains(lower, "u1s1_client_only") ||
		strings.Contains(lower, "仅支持 u1s1 客户端") ||
		strings.Contains(lower, "仅限 u1s1 客户端") ||
		strings.Contains(lower, "只支持 u1s1 客户端")
}

// NextBeijingMidnight 下一次北京时间 0 点。
func NextBeijingMidnight(now time.Time) time.Time {
	loc := time.FixedZone("CST", 8*3600)
	t := now.In(loc)
	midnight := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
	return midnight.Add(24 * time.Hour)
}
