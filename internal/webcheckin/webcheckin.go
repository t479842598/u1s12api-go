// Package webcheckin 实现 u1s1 网页登录与「每日登录打卡」领取（/api/packages/login-checkin/claim）。
//
// 流程（与官方网页 app.js 一致，全部纯 API 实现）：
//  1. capcat 求解 → cap-token（一次性）
//  2. POST https://u1s1.io/auth/password/login {email, password, "cap-token"} → 会话 cookie
//  3. 重新 capcat 求解 → 新 cap-token（登录已消费第一个）
//  4. POST https://u1s1.io/api/packages/login-checkin/claim {"cap-token": <新token>}（带会话）→ 领取加量包
package webcheckin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"time"

	"github.com/t479842598/u1s12api-go/internal/capcat"
)

const (
	baseURL = "https://u1s1.io"
	// userAgent 与 capcat 求解器一致。
	userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"
)

// Service u1s1 网页签到服务。
type Service struct {
	transport *http.Transport
	cap       *capcat.Solver
}

// New 构造。proxyURL 为空则直连。
func New(proxyURL string) (*Service, error) {
	tr, err := capcat.Transport(proxyURL)
	if err != nil {
		return nil, err
	}
	capSolver, err := capcat.New(proxyURL)
	if err != nil {
		return nil, err
	}
	return &Service{transport: tr, cap: capSolver}, nil
}

// Result 网页打卡结果。
type Result struct {
	OK            bool          `json:"ok"`
	Kind          string        `json:"kind"`
	Tokens        int64         `json:"tokens"`
	Streak        int64         `json:"streak"`
	LongestStreak int64         `json:"longest_streak"`
	ExpiresAt     string        `json:"expires_at"`
	Claims        []ClaimResult `json:"claims"`
}

// ClaimResult 打卡后顺带领取的其他加量包结果（邀请/新用户/临时加量包）。
type ClaimResult struct {
	Kind   string `json:"kind"`
	Label  string `json:"label"`
	Tokens int64  `json:"tokens"`
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
}

// NewSession 完成一次网页登录：capcat 求解 → POST /auth/password/login，
// 返回携带会话 cookie 的 client（供后续带会话请求复用，如打卡 claim、官网 API 抓取）。
func (s *Service) NewSession(ctx context.Context, email, password string) (*http.Client, error) {
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Transport: s.transport, Jar: jar, Timeout: 90 * time.Second}

	capToken, err := s.cap.Solve(ctx)
	if err != nil {
		return nil, fmt.Errorf("网页登录: capcat 求解失败: %w", err)
	}
	if err := s.login(ctx, client, email, password, capToken); err != nil {
		return nil, fmt.Errorf("网页登录: 登录失败: %w", err)
	}
	return client, nil
}

// CheckIn 完成一次网页打卡：登录拿会话 → claim 每日签到 → 顺带领取其他可用加量包。
// 返回 err 仅表示硬失败（登录/capcat 求解）；主打卡业务失败（如今天已打过卡）
// 会记录到 res.Claims 并继续尝试附加包，不当作硬错误。
func (s *Service) CheckIn(ctx context.Context, email, password string) (*Result, error) {
	client, err := s.NewSession(ctx, email, password)
	if err != nil {
		return nil, fmt.Errorf("网页签到: %w", err)
	}

	// claim 前需新 cap-token（登录已消费第一个）
	capToken2, err := s.cap.Solve(ctx)
	if err != nil {
		return nil, fmt.Errorf("网页签到: claim 前 capcat 求解失败: %w", err)
	}
	res, err := s.claim(ctx, client, capToken2)
	if err != nil {
		// 主打卡失败（如今天已打过卡）：拉 /api/me 补连续天数，继续尝试附加包。
		res = &Result{Claims: []ClaimResult{}}
		if me, merr := s.fetchMe(ctx, client); merr == nil && me.LoginCheckin != nil {
			res.Streak = me.LoginCheckin.Streak
			res.LongestStreak = me.LoginCheckin.LongestStreak
		}
		res.Claims = append(res.Claims, ClaimResult{Kind: "login_checkin", Label: "每日签到", Error: err.Error()})
	}
	// 顺带领取其他可用的加量包（邀请 500 万/新用户/500 万临时加量包）。
	s.claimExtras(ctx, client, res)
	return res, nil
}

// claimKind 可领取的加量包定义（/api/me 字段名、展示名、claim 端点、默认额度）。
type claimKind struct {
	key    string // /api/me 中 <key>_claim 字段
	label  string
	path   string
	tokens int64 // 已知默认额度（响应未带 tokens 时用于展示）
}

// extraClaims 打卡之外顺带领取的加量包（与官方 app.js 的领取按钮一致）。
var extraClaims = []claimKind{
	{"invite", "邀请赠送", "/api/packages/invite/claim", 5000000},
	{"new_user", "新用户赠送", "/api/packages/new-user/claim", 5000000},
	{"payment_delay_gift", "临时加量包", "/api/packages/payment-delay-gift/claim", 5000000},
}

// meInfo /api/me 中领取状态与打卡信息相关字段。
type meInfo struct {
	InviteClaim            string `json:"invite_claim"`
	NewUserClaim           string `json:"new_user_claim"`
	PaymentDelayGiftClaim  string `json:"payment_delay_gift_claim"`
	LoginCheckin           *struct {
		Streak        int64 `json:"streak"`
		LongestStreak int64 `json:"longest_streak"`
	} `json:"login_checkin"`
}

// fetchMe 拉取 /api/me（带会话），解析各加量包领取状态。
func (s *Service) fetchMe(ctx context.Context, client *http.Client) (*meInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/me", nil)
	if err != nil {
		return nil, err
	}
	setHeaders(req, "/")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("获取 /api/me 失败: http %d: %s", resp.StatusCode, truncate(string(data), 200))
	}
	var out meInfo
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("解析 /api/me 失败: %w", err)
	}
	return &out, nil
}

// claimExtras 领取所有当前可用的加量包（邀请/新用户/临时加量包）。
// 每个需要新 cap-token；结果并入 res.Claims（失败仅记录，不影响主结果）。
func (s *Service) claimExtras(ctx context.Context, client *http.Client, res *Result) {
	me, err := s.fetchMe(ctx, client)
	if err != nil {
		return // /api/me 拉取失败则跳过附加加量包，主打卡结果不受影响
	}
	for _, ck := range extraClaims {
		if !claimAvailable(me, ck.key) {
			continue
		}
		capTok, err := s.cap.Solve(ctx)
		if err != nil {
			res.Claims = append(res.Claims, ClaimResult{Kind: ck.key, Label: ck.label, Tokens: ck.tokens, Error: "capcat 求解失败: " + err.Error()})
			continue
		}
		tokens, err := s.claimOne(ctx, client, ck.path, capTok)
		res.Claims = append(res.Claims, ClaimResult{Kind: ck.key, Label: ck.label, Tokens: tokens, OK: err == nil, Error: errMsg(err)})
	}
}

func claimAvailable(me *meInfo, key string) bool {
	switch key {
	case "invite":
		return me.InviteClaim == "available"
	case "new_user":
		return me.NewUserClaim == "available"
	case "payment_delay_gift":
		return me.PaymentDelayGiftClaim == "available"
	}
	return false
}

// claimOne 领取单个加量包，返回领取到的 token 数（未知为 0）。
func (s *Service) claimOne(ctx context.Context, client *http.Client, path, capToken string) (int64, error) {
	body, _ := json.Marshal(map[string]string{"cap-token": capToken})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+path, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	setHeaders(req, "/")
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	var out struct {
		OK     bool  `json:"ok"`
		Tokens int64 `json:"tokens"`
		Error  struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return 0, fmt.Errorf("解析领取响应失败: %s", truncate(string(data), 200))
	}
	if resp.StatusCode != http.StatusOK || !out.OK {
		msg := out.Error.Message
		if msg == "" {
			msg = fmt.Sprintf("http %d", resp.StatusCode)
		}
		return 0, fmt.Errorf("%s", msg)
	}
	return out.Tokens, nil
}

func errMsg(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

type loginResp struct {
	OK    bool `json:"ok"`
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (s *Service) login(ctx context.Context, client *http.Client, email, password, capToken string) error {
	body, _ := json.Marshal(map[string]string{
		"email":     email,
		"password":  password,
		"cap-token": capToken,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/auth/password/login", bytes.NewReader(body))
	if err != nil {
		return err
	}
	setHeaders(req, "/login")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	if resp.StatusCode != http.StatusOK {
		var out loginResp
		msg := ""
		if err := json.Unmarshal(data, &out); err == nil {
			msg = out.Error.Message
		}
		if msg == "" {
			msg = truncate(string(data), 200)
		}
		return fmt.Errorf("http %d: %s", resp.StatusCode, msg)
	}
	return nil
}

type claimResp struct {
	OK            bool   `json:"ok"`
	Kind          string `json:"kind"`
	Tokens        int64  `json:"tokens"`
	Streak        int64  `json:"streak"`
	LongestStreak int64  `json:"longest_streak"`
	ExpiresAt     string `json:"expires_at"`
	Error         struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (s *Service) claim(ctx context.Context, client *http.Client, capToken string) (*Result, error) {
	body, _ := json.Marshal(map[string]string{"cap-token": capToken})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/packages/login-checkin/claim", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	setHeaders(req, "/")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	var out claimResp
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("解析 claim 响应失败: %s", truncate(string(data), 200))
	}
	if resp.StatusCode != http.StatusOK || !out.OK {
		msg := out.Error.Message
		if msg == "" {
			msg = fmt.Sprintf("http %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("%s", msg)
	}
	return &Result{
		OK:            true,
		Kind:          out.Kind,
		Tokens:        out.Tokens,
		Streak:        out.Streak,
		LongestStreak: out.LongestStreak,
		ExpiresAt:     out.ExpiresAt,
	}, nil
}

func setHeaders(req *http.Request, referer string) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", baseURL)
	req.Header.Set("Referer", baseURL+referer)
	req.Header.Set("User-Agent", userAgent)
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
