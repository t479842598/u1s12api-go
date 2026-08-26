package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/t479842598/u1s12api-go/internal/store"
)

func at(y int, mo time.Month, d, h, mi, s int) time.Time {
	return time.Date(y, mo, d, h, mi, s, 0, beijingLoc())
}

// TestNextQuotaRefreshTime 排程点 = 北京时间 0 点 + quotaRefreshBuffer。
func TestNextQuotaRefreshTime(t *testing.T) {
	cases := []struct {
		name string
		now  time.Time
		want time.Time
	}{
		{"白天下午排到明天0点加缓冲", at(2026, 8, 26, 15, 30, 0), at(2026, 8, 27, 0, 2, 0)},
		{"临近午夜仍排明天", at(2026, 8, 26, 23, 59, 59), at(2026, 8, 27, 0, 2, 0)},
		{"缓冲窗口内补今天这一轮", at(2026, 8, 26, 0, 0, 30), at(2026, 8, 26, 0, 2, 0)},
		{"缓冲已过排明天", at(2026, 8, 26, 0, 5, 0), at(2026, 8, 27, 0, 2, 0)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := nextQuotaRefreshTime(c.now)
			if !got.Equal(c.want) {
				t.Fatalf("nextQuotaRefreshTime(%s) = %s, want %s",
					c.now.Format(time.RFC3339), got.Format(time.RFC3339), c.want.Format(time.RFC3339))
			}
		})
	}
}

// TestLatestUpstreamQuotaCheckedAt 启动补刷依赖的「今天是否已检查过」判断。
func TestLatestUpstreamQuotaCheckedAt(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	if got := st.LatestUpstreamQuotaCheckedAt(); got != 0 {
		t.Fatalf("空库应返回 0，got %d", got)
	}
	ok, err := st.AddUpstreamKey("u1s1-aaaa1111bbbb2222cccc", "")
	if err != nil || !ok {
		t.Fatalf("add key: ok=%v err=%v", ok, err)
	}
	if got := st.LatestUpstreamQuotaCheckedAt(); got != 0 {
		t.Fatalf("从未检查过应返回 0，got %d", got)
	}
	before := time.Now().Unix()
	if err := st.UpdateUpstreamQuota(1, "a@b.c", 1000, 1.5, 2.5, ""); err != nil {
		t.Fatalf("update quota: %v", err)
	}
	if got := st.LatestUpstreamQuotaCheckedAt(); got < before {
		t.Fatalf("检查后应 >= %d，got %d", before, got)
	}
}

// adminCookie 带管理员登录 cookie 的请求。
func adminCookie(s *Server) *http.Cookie {
	return &http.Cookie{Name: adminCookieName, Value: s.signCookie(time.Now().Add(time.Hour))}
}

// TestCheckAllQuotasConflictWhenBusy 全量检查进行中时，手动一键刷新返回 409。
func TestCheckAllQuotasConflictWhenBusy(t *testing.T) {
	fx := setupTest(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"email":"a@b.c","tokens_per_usd":1000,"daily_free_remaining_usd":1.5,"remaining_usd":2.5}`))
	})
	fx.addKeys(t, "u1s1-aaaa1111bbbb2222cccc")

	// 模拟定时刷新正在执行。
	fx.srv.quotaChecking.Store(true)
	defer fx.srv.quotaChecking.Store(false)

	req := httptest.NewRequest(http.MethodPost, "/admin/api/u1s1-keys/check-all", nil)
	req.AddCookie(adminCookie(fx.srv))
	rec := httptest.NewRecorder()
	fx.srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("busy 时应返回 409，got %d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Detail == "" {
		t.Fatalf("应返回 detail 错误信息: err=%v body=%s", err, rec.Body.String())
	}
}

// TestCheckAllQuotasNormal 非忙时手动一键刷新正常工作（互斥锁正确释放）。
func TestCheckAllQuotasNormal(t *testing.T) {
	fx := setupTest(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"email":"a@b.c","tokens_per_usd":1000,"daily_free_remaining_usd":1.5,"remaining_usd":2.5}`))
	})
	fx.addKeys(t, "u1s1-aaaa1111bbbb2222cccc")

	req := httptest.NewRequest(http.MethodPost, "/admin/api/u1s1-keys/check-all", nil)
	req.AddCookie(adminCookie(fx.srv))
	rec := httptest.NewRecorder()
	fx.srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("应返回 200，got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data struct {
			OK    int `json:"ok"`
			Total int `json:"total"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Data.OK != 1 || resp.Data.Total != 1 {
		t.Fatalf("期望 1/1 成功，got %+v", resp.Data)
	}
	// 互斥锁已释放：再次调用仍成功。
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/admin/api/u1s1-keys/check-all", nil)
	req2.AddCookie(adminCookie(fx.srv))
	fx.srv.Handler().ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("第二次调用应成功（锁已释放），got %d body=%s", rec2.Code, rec2.Body.String())
	}
}
