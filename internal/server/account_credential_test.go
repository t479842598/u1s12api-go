package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// TestAccountCredentialEndpoint 复制凭证接口：admin 鉴权 + 明文邮箱/密码回传，
// 供前端「复制账号/复制密码」后到官网打卡页手动登录。
func TestAccountCredentialEndpoint(t *testing.T) {
	fx := setupTest(t, func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	ok, err := fx.srv.store.AddAccount("cred@test.dev", "pw-123456", "")
	if err != nil || !ok {
		t.Fatalf("AddAccount: ok=%v err=%v", ok, err)
	}
	a, err := fx.srv.store.GetAccountByEmail("cred@test.dev")
	if err != nil {
		t.Fatal(err)
	}
	credURL := fx.ts.URL + "/admin/api/accounts/" + strconv.FormatInt(a.ID, 10) + "/credential"

	// 未登录 → 401。
	resp, _ := http.Get(credURL)
	if resp.StatusCode != 401 {
		t.Fatalf("未登录状态码 = %d", resp.StatusCode)
	}
	resp.Body.Close()

	// 登录拿 cookie。
	lresp, _ := http.Post(fx.ts.URL+"/admin/api/login", "application/json",
		strings.NewReader(`{"key":"test-admin-pw"}`))
	if lresp.StatusCode != 200 {
		t.Fatalf("登录失败: %d", lresp.StatusCode)
	}
	cookie := cookieOf(lresp)
	lresp.Body.Close()

	// 已登录 → 返回明文邮箱+密码。
	req, _ := http.NewRequest("GET", credURL, nil)
	req.Header.Set("Cookie", cookie)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("已登录状态码 = %d", resp.StatusCode)
	}
	var body struct {
		Data struct {
			Email    string `json:"email"`
			Password string `json:"password"`
		} `json:"data"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body.Data.Email != "cred@test.dev" || body.Data.Password != "pw-123456" {
		t.Errorf("凭证不符: %+v", body.Data)
	}

	// 不存在的账号 → 404。
	req404, _ := http.NewRequest("GET", fx.ts.URL+"/admin/api/accounts/99999/credential", nil)
	req404.Header.Set("Cookie", cookie)
	resp404, err := http.DefaultClient.Do(req404)
	if err != nil {
		t.Fatal(err)
	}
	resp404.Body.Close()
	if resp404.StatusCode != 404 {
		t.Errorf("不存在账号状态码 = %d", resp404.StatusCode)
	}
}
