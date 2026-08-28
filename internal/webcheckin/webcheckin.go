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
	OK            bool   `json:"ok"`
	Kind          string `json:"kind"`
	Tokens        int64  `json:"tokens"`
	Streak        int64  `json:"streak"`
	LongestStreak int64  `json:"longest_streak"`
	ExpiresAt     string `json:"expires_at"`
	Note          string `json:"note"`
}

// CheckIn 完成一次网页打卡：capcat 求解 → 登录 → 再求解 → claim。
func (s *Service) CheckIn(ctx context.Context, email, password string) (*Result, error) {
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Transport: s.transport, Jar: jar, Timeout: 90 * time.Second}

	// 1+2. capcat 求解 → 登录
	capToken, err := s.cap.Solve(ctx)
	if err != nil {
		return nil, fmt.Errorf("网页签到: capcat 求解失败: %w", err)
	}
	if err := s.login(ctx, client, email, password, capToken); err != nil {
		return nil, fmt.Errorf("网页签到: 登录失败: %w", err)
	}

	// 3+4. 新 cap-token → claim
	capToken2, err := s.cap.Solve(ctx)
	if err != nil {
		return nil, fmt.Errorf("网页签到: claim 前 capcat 求解失败: %w", err)
	}
	return s.claim(ctx, client, capToken2)
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
