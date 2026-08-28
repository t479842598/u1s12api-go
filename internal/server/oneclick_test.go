package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// TestOneClickLoginAutoProvision 一键登录：无需预填邮箱密码，授权后自动建账号、
// 存设备凭证、api_key 入 Key 池，账号进入「授权账号」列表。
func TestOneClickLoginAutoProvision(t *testing.T) {
	fx := setupTest(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/device/start":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"verify_url":"https://u1s1.io/login?device=oneshot","poll_secret":"ps-oneclick","interval":1,"expires_in":900}`))
		case "/auth/device/poll":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ok","api_key":"u1s1-oneclickaaaabbbbcccc","device_token":"u1s1d-abcdef123456","device_id":508}`))
		case "/me":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"email":"oneclick@test.dev","login_checkin_remaining":2000000,"packages":[]}`))
		default:
			http.NotFound(w, r)
		}
	})

	client := &http.Client{}
	// 登录 admin 拿 cookie
	lresp, err := client.Post(fx.ts.URL+"/admin/api/login", "application/json",
		strings.NewReader(`{"key":"test-admin-pw"}`))
	if err != nil {
		t.Fatal(err)
	}
	ck := cookieOf(lresp)
	lresp.Body.Close()

	// 1) 发起一键登录
	sreq, _ := http.NewRequest("POST", fx.ts.URL+"/admin/api/accounts/one-click/start", nil)
	sreq.Header.Set("Cookie", ck)
	sresp, err := client.Do(sreq)
	if err != nil {
		t.Fatal(err)
	}
	var start struct {
		Data struct {
			SessionID string `json:"session_id"`
			VerifyURL string `json:"verify_url"`
		} `json:"data"`
	}
	if err := json.NewDecoder(sresp.Body).Decode(&start); err != nil {
		t.Fatal(err)
	}
	sresp.Body.Close()
	if start.Data.SessionID == "" || start.Data.VerifyURL == "" {
		t.Fatalf("一键登录 start 返回异常: %+v", start.Data)
	}

	// 2) 确认授权（浏览器已批准），单次轮询应返回 authorized
	creq, _ := http.NewRequest("POST", fx.ts.URL+"/admin/api/accounts/one-click/confirm?session_id="+start.Data.SessionID, nil)
	creq.Header.Set("Cookie", ck)
	cresp, err := client.Do(creq)
	if err != nil {
		t.Fatal(err)
	}
	var conf struct {
		Data struct {
			Status    string `json:"status"`
			Authorized bool  `json:"authorized"`
			AccountID int64  `json:"account_id"`
			Email     string `json:"email"`
			APIKey    string `json:"api_key"`
		} `json:"data"`
	}
	if err := json.NewDecoder(cresp.Body).Decode(&conf); err != nil {
		t.Fatal(err)
	}
	cresp.Body.Close()
	if conf.Data.Status != "authorized" || !conf.Data.Authorized {
		t.Fatalf("一键登录确认未授权: %+v", conf.Data)
	}

	// 3) 账号已在「授权账号」列表且已授权
	accounts, err := fx.srv.store.ListAccounts()
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 1 {
		t.Fatalf("账号数 = %d, 期望 1", len(accounts))
	}
	a := accounts[0]
	if a.Email != "oneclick@test.dev" {
		t.Errorf("账号 email = %q", a.Email)
	}
	if !a.Authorized || a.DeviceToken == "" || a.APIKey == "" {
		t.Errorf("账号未完整授权: %+v", a)
	}

	// 4) api_key 已自动导入 Key 池
	keys, _ := fx.srv.store.ListUpstreamKeys()
	found := false
	for _, k := range keys {
		if strings.HasPrefix(k.Key, "u1s1-oneclick") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("一键登录 api_key 未导入 Key 池: %v", keys)
	}
}
