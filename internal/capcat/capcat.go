// Package capcat 实现 capcat.ai 人机验证 format-2 协议的纯 Go 求解器。
//
// 协议（已逆向并实测）：
//  1. POST {base}/{siteKey}/challenge       → {token, format:2, challenges:[...]}
//     - rsw 挑战：y = x^(2^t) mod N（大整数模幂）
//     - instrumentation 挑战：base64+deflate-raw 压缩的一段 JS，内含每次随机生成的
//       确定性算术程序（4 个随机变量、随机初始值/掩码/运算序列，算子集固定）。
//       用 goja 以最小 DOM stub 执行该算术段即可得到正确 state，无需真实浏览器。
//  2. POST {base}/{siteKey}/redeem          body {token, solutions} →
//     {success:true, token:"<id>:<secret>"}（即 cap-token，~10 分钟有效、一次性）
//
// cap-token 随后用于 u1s1 网页登录与打卡（见 internal/webcheckin）。
package capcat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"time"

	"golang.org/x/net/proxy"
)

const (
	// BaseURL capcat 验证服务端点。
	BaseURL = "https://api.capcat.ai"
	// SiteKey u1s1 官网公开的站点标识（cap-widget data-cap-api-endpoint 里的路径段）。
	SiteKey = "f8ad0853ed20b00d"
	// userAgent 与浏览器一致的 UA（capcat 按指纹风控）。
	userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"
)

// Solver capcat 求解器。
type Solver struct {
	http *http.Client
}

// New 构造求解器。proxyURL 为空则直连（与主客户端一致走同一出口，challenge 按出口 IP 绑定）。
func New(proxyURL string) (*Solver, error) {
	tr, err := Transport(proxyURL)
	if err != nil {
		return nil, err
	}
	return &Solver{http: &http.Client{Transport: tr, Timeout: 60 * time.Second}}, nil
}

// Transport 构建带可选出口代理的传输层（与 upstream 客户端一致的语义）。
func Transport(proxyURL string) (*http.Transport, error) {
	return buildTransport(proxyURL)
}

// Solve 完成一次 capcat 验证，返回 cap-token（一次性，~10 分钟有效）。
func (s *Solver) Solve(ctx context.Context) (string, error) {
	ch, err := s.fetchChallenge(ctx)
	if err != nil {
		return "", err
	}
	solutions, err := solveChallenges(ch.Challenges)
	if err != nil {
		return "", err
	}
	return s.redeem(ctx, ch.Token, solutions)
}

// ---- 挑战 ----

type challengeResp struct {
	Token      string              `json:"token"`
	Format     int                 `json:"format"`
	Challenges []challengeEntry    `json:"challenges"`
}

type challengeEntry struct {
	Protocol string          `json:"protocol"`
	Payload  json.RawMessage `json:"payload"`
}

type rswPayload struct {
	N string `json:"N"`
	X string `json:"x"`
	T int    `json:"t"`
}

type instrPayload struct {
	Blob string `json:"blob"`
}

func (s *Solver) fetchChallenge(ctx context.Context) (*challengeResp, error) {
	var body bytes.Buffer
	body.WriteString("{}")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint("/challenge"), &body)
	if err != nil {
		return nil, err
	}
	setHeaders(req)
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("capcat challenge: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("capcat challenge: http %d: %s", resp.StatusCode, truncate(string(data), 300))
	}
	var out challengeResp
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("capcat challenge: 解析响应失败: %w", err)
	}
	if out.Token == "" {
		return nil, fmt.Errorf("capcat challenge: 响应缺少 token: %s", truncate(string(data), 300))
	}
	return &out, nil
}

// ---- 求解 ----

func solveChallenges(chs []challengeEntry) ([]json.RawMessage, error) {
	sols := make([]json.RawMessage, 0, len(chs))
	for _, c := range chs {
		switch c.Protocol {
		case "rsw":
			var p rswPayload
			if err := json.Unmarshal(c.Payload, &p); err != nil {
				return nil, fmt.Errorf("rsw payload: %w", err)
			}
			y, err := solveRSW(p)
			if err != nil {
				return nil, err
			}
			raw, _ := json.Marshal(struct {
				Y string `json:"y"`
			}{Y: y})
			sols = append(sols, raw)
		case "instrumentation":
			var p instrPayload
			if err := json.Unmarshal(c.Payload, &p); err != nil {
				return nil, fmt.Errorf("instrumentation payload: %w", err)
			}
			instr, err := solveInstrumentation(p.Blob)
			if err != nil {
				return nil, err
			}
			raw, _ := json.Marshal(struct {
				Instr json.RawMessage `json:"instr"`
			}{Instr: instr})
			sols = append(sols, raw)
		default:
			return nil, fmt.Errorf("capcat: 未知挑战协议 %q", c.Protocol)
		}
	}
	return sols, nil
}

// solveRSW 求解重复模平方 y = x^(2^t) mod N。
func solveRSW(p rswPayload) (string, error) {
	N, ok := new(big.Int).SetString(p.N, 16)
	if !ok || N.Sign() <= 0 {
		return "", fmt.Errorf("rsw: 非法 N")
	}
	x, ok := new(big.Int).SetString(p.X, 16)
	if !ok || x.Sign() < 0 {
		return "", fmt.Errorf("rsw: 非法 x")
	}
	if p.T < 0 {
		return "", fmt.Errorf("rsw: 非法 t=%d", p.T)
	}
	// 指数 2^t；Exp 按指数二进制位做模乘，共 t 次（t≈7.5 万，毫秒级）。
	exp := new(big.Int).Lsh(big.NewInt(1), uint(p.T))
	y := new(big.Int).Exp(x, exp, N)
	return fmt.Sprintf("%x", y), nil
}

// ---- redeem ----

type redeemResp struct {
	Success bool   `json:"success"`
	Token   string `json:"token"`
	Expires int64  `json:"expires"`
	Error   string `json:"error"`
	Reason  string `json:"reason"`
}

func (s *Solver) redeem(ctx context.Context, token string, solutions []json.RawMessage) (string, error) {
	body, _ := json.Marshal(map[string]any{"token": token, "solutions": solutions})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint("/redeem"), bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	setHeaders(req)
	resp, err := s.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("capcat redeem: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("capcat redeem: http %d: %s", resp.StatusCode, truncate(string(data), 300))
	}
	var out redeemResp
	if err := json.Unmarshal(data, &out); err != nil {
		return "", fmt.Errorf("capcat redeem: 解析响应失败: %w", err)
	}
	if !out.Success || out.Token == "" {
		return "", fmt.Errorf("capcat redeem: %s (%s)", out.Error, out.Reason)
	}
	return out.Token, nil
}

// ---- 工具 ----

func endpoint(path string) string {
	return BaseURL + "/" + SiteKey + path
}

func setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://u1s1.io")
	req.Header.Set("Referer", "https://u1s1.io/login")
	req.Header.Set("User-Agent", userAgent)
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// buildTransport 与 upstream 相同的出口代理传输层。
func buildTransport(proxyURL string) (*http.Transport, error) {
	t := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          32,
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
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
	port := "443"
	if u.Scheme == "http" {
		port = "80"
	}
	return net.JoinHostPort(u.Hostname(), port)
}
