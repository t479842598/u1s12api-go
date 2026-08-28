// 官网账号管理：录入账号（邮箱+密码）、列出、启停、删除。
// 账号用于设备授权（拿 u1s1d- 设备凭证）后消耗「仅限 u1s1 客户端使用」的加量包，
// 并支持每日自动签到。
package server

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/t479842598/u1s12api-go/internal/store"
)

// handleListAccounts 账号列表（不回明文密码/完整凭证）。
func (s *Server) handleListAccounts(w http.ResponseWriter, _ *http.Request) {
	accounts, err := s.store.ListAccounts()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if accounts == nil {
		accounts = []*store.Account{}
	}
	for _, a := range accounts {
		a.Password = ""
		a.DeviceToken = ""
		a.APIKey = ""
		a.DevicePrivateJWK = ""
	}
	writeAPIData(w, http.StatusOK, map[string]any{"accounts": accounts})
}

// handleAddAccount 新增账号（email + password）。
func (s *Server) handleAddAccount(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		Note     string `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIError(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	if body.Email == "" {
		writeAPIError(w, http.StatusBadRequest, "缺少邮箱")
		return
	}
	ok, err := s.store.AddAccount(body.Email, body.Password, body.Note)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		writeAPIError(w, http.StatusConflict, "该邮箱已存在")
		return
	}
	logger.Infof("新增官网账号: %s", body.Email)
	writeAPIData(w, http.StatusOK, map[string]any{"added": true})
}

// handleUpdateAccount 启停/备注。
func (s *Server) handleUpdateAccount(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "无效 id")
		return
	}
	var body struct {
		Enabled *bool  `json:"enabled"`
		Note    *string `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIError(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	fields := map[string]any{}
	if body.Enabled != nil {
		fields["enabled"] = boolToInt(*body.Enabled)
	}
	if body.Note != nil {
		fields["note"] = *body.Note
	}
	if err := s.store.UpdateAccount(id, fields); err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeAPIData(w, http.StatusOK, map[string]any{"ok": true})
}

// handleDeleteAccount 删除账号。
func (s *Server) handleDeleteAccount(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "无效 id")
		return
	}
	if err := s.store.DeleteAccount(id); err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeAPIData(w, http.StatusOK, map[string]any{"ok": true})
}

// handleCheckAllCheckin 手动触发：对全部已授权账号做一次签到（调 /v1/me 触发加量包发放）。
func (s *Server) handleCheckAllCheckin(w http.ResponseWriter, _ *http.Request) {
	results, okCount, err := s.runCheckinAll()
	if err != nil && okCount == 0 {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeAPIData(w, http.StatusOK, map[string]any{"ok": okCount, "total": len(results), "results": results})
}

// handleCheckinOne 单账号签到。
func (s *Server) handleCheckinOne(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "无效 id")
		return
	}
	if err := s.checkinOne(id); err != nil {
		writeAPIError(w, http.StatusBadGateway, "签到失败: "+err.Error())
		return
	}
	acc, _ := s.store.GetAccount(id)
	writeAPIData(w, http.StatusOK, map[string]any{
		"ok":                       true,
		"login_checkin_remaining":   acc.LoginCheckinRemaining,
		"last_checkin_at":           acc.LastCheckinAt,
	})
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
