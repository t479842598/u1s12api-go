// Package server HTTP 路由、鉴权中间件与管理后台 API。
package server

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/t479842598/u1s12api-go/internal/config"
	"github.com/t479842598/u1s12api-go/internal/fingerprint"
	"github.com/t479842598/u1s12api-go/internal/logging"
	"github.com/t479842598/u1s12api-go/internal/store"
	"github.com/t479842598/u1s12api-go/internal/upstream"
)

var logger = logging.Named("u1s12api")

// Server 持有运行时依赖；settings 支持热替换。
type Server struct {
	cfg         atomicValue // *config.Settings
	pool        *upstream.Pool
	store       *store.Store
	fp          *fingerprint.Manager
	staticFS    fs.FS
	projectRoot string

	clientMu    sync.Mutex
	upstreamCli *upstream.Client

	adminCookieSecret string

	throttle *loginThrottle

	// 北京时间 0 点配额定时刷新：quotaChecking 让自动/手动全量检查互斥，
	// nextQuotaCheckAt 记录下次排程时间（unix 秒，0=未排程）供管理端查询。
	quotaChecking    atomic.Bool
	nextQuotaCheckAt atomic.Int64

	modelsMu    sync.Mutex
	modelsCache *modelsCacheEntry

	// 官网账号设备授权（内存态）与每日签到。
	pending *pendingDeviceMap
}

type atomicValue struct {
	v any
	m sync.RWMutex
}

func (a *atomicValue) Load() any   { a.m.RLock(); defer a.m.RUnlock(); return a.v }
func (a *atomicValue) Store(v any) { a.m.Lock(); defer a.m.Unlock(); a.v = v }

type modelsCacheEntry struct {
	resp      *upstream.ModelsResponse
	fetchedAt time.Time
}

const adminCookieName = "u1s12api_admin"

// New 组装 Server。
func New(cfg *config.Settings, st *store.Store, pool *upstream.Pool, fp *fingerprint.Manager, projectRoot string, staticFS fs.FS) (*Server, error) {
	s := &Server{
		store:       st,
		pool:        pool,
		fp:          fp,
		staticFS:    staticFS,
		projectRoot: projectRoot,
		throttle:    newLoginThrottle(5, 15*time.Minute),
		pending:     &pendingDeviceMap{},
	}
	s.cfg.Store(cfg)

	secret, err := loadOrCreateSecret(filepath.Join(projectRoot, "data", "admin_cookie_secret"))
	if err != nil {
		return nil, err
	}
	s.adminCookieSecret = secret

	cli, err := s.buildClient(cfg)
	if err != nil {
		logger.Warnf("上游客户端构建失败（可在后台设置中修正）: %v", err)
	}
	s.upstreamCli = cli

	// 后台周期拉取模型列表：预热价格缓存（用于成本估算），对齐官方 CLI
	// 启动即刷新模型的习惯。无可用 key 时静默失败，下轮重试。
	go s.refreshModelsLoop()

	// 北京时间 0 点额度重置后自动全量刷新上游 Key 配额
	// （QUOTA_AUTO_REFRESH=0 关闭；测试直构 Settings 零值 false 不启动）。
	if cfg.QuotaAutoRefresh {
		go s.quotaAutoRefreshLoop()
	}

	// 每日自动签到（有已授权账号时才跑；无账号时空转无害）。
	go s.checkinAutoLoop()
	return s, nil
}

func (s *Server) refreshModelsLoop() {
	ctx := context.Background()
	for {
		_, _, _ = s.fetchModels(ctx)
		time.Sleep(5 * time.Minute)
	}
}

func loadOrCreateSecret(path string) (string, error) {
	if data, err := os.ReadFile(path); err == nil && len(data) >= 32 {
		return strings.TrimSpace(string(data)), nil
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	secret := hex.EncodeToString(buf)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(secret), 0o600); err != nil {
		return "", err
	}
	return secret, nil
}

func (s *Server) getSettings() *config.Settings {
	return s.cfg.Load().(*config.Settings)
}

// CurrentSettings 供外部读取。
func (s *Server) CurrentSettings() *config.Settings { return s.getSettings() }

func (s *Server) setSettings(cfg *config.Settings) { s.cfg.Store(cfg) }

func (s *Server) buildClient(cfg *config.Settings) (*upstream.Client, error) {
	return upstream.NewClient(cfg.UpstreamBaseURL, cfg.EgressProxyURL, s.fp,
		func() string { return s.getSettings().U1S1Version })
}

// client 当前上游客户端（配置变更后重建）。
func (s *Server) client() *upstream.Client {
	s.clientMu.Lock()
	defer s.clientMu.Unlock()
	return s.upstreamCli
}

func (s *Server) rebuildClientLocked() error {
	s.clientMu.Lock()
	defer s.clientMu.Unlock()
	cli, err := s.buildClient(s.getSettings())
	if err != nil {
		return err
	}
	s.upstreamCli = cli
	return nil
}

// Handler 构建路由。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// 公开端点
	mux.HandleFunc("GET /healthz", s.handleHealthz)

	// OpenAI 兼容 API（本地 key 鉴权）
	mux.HandleFunc("GET /v1/models", s.requireAPIKey(s.handleModels))
	mux.HandleFunc("POST /v1/chat/completions", s.requireAPIKey(s.handleChatCompletions))

	// 管理后台 API
	mux.HandleFunc("POST /admin/api/login", s.handleAdminLogin)
	mux.HandleFunc("POST /admin/api/logout", s.handleAdminLogout)
	mux.HandleFunc("GET /admin/api/session", s.handleAdminSession)
	mux.HandleFunc("GET /admin/api/overview", s.requireAdmin(s.handleOverview))
	mux.HandleFunc("GET /admin/api/models", s.requireAdmin(s.handleAdminModels))
	mux.HandleFunc("GET /admin/api/u1s1-keys", s.requireAdmin(s.handleListUpstreamKeys))
	mux.HandleFunc("POST /admin/api/u1s1-keys", s.requireAdmin(s.handleImportUpstreamKeys))
	mux.HandleFunc("POST /admin/api/u1s1-keys/import-text", s.requireAdmin(s.handleImportUpstreamKeysText))
	mux.HandleFunc("DELETE /admin/api/u1s1-keys/{id}", s.requireAdmin(s.handleDeleteUpstreamKey))
	mux.HandleFunc("PUT /admin/api/u1s1-keys/{id}/status", s.requireAdmin(s.handleSetUpstreamKeyStatus))
	mux.HandleFunc("POST /admin/api/u1s1-keys/{id}/quota", s.requireAdmin(s.handleCheckUpstreamQuota))
	mux.HandleFunc("POST /admin/api/u1s1-keys/check-all", s.requireAdmin(s.handleCheckAllQuotas))
	mux.HandleFunc("GET /admin/api/local-keys", s.requireAdmin(s.handleListLocalKeys))
	mux.HandleFunc("POST /admin/api/local-keys", s.requireAdmin(s.handleCreateLocalKey))
	mux.HandleFunc("PUT /admin/api/local-keys/{name}", s.requireAdmin(s.handleUpdateLocalKey))
	mux.HandleFunc("DELETE /admin/api/local-keys/{name}", s.requireAdmin(s.handleDeleteLocalKey))
	mux.HandleFunc("POST /admin/api/local-keys/{name}/copy", s.requireAdmin(s.handleCopyLocalKey))
	mux.HandleFunc("GET /admin/api/accounts", s.requireAdmin(s.handleListAccounts))
	mux.HandleFunc("POST /admin/api/accounts", s.requireAdmin(s.handleAddAccount))
	mux.HandleFunc("PUT /admin/api/accounts/{id}", s.requireAdmin(s.handleUpdateAccount))
	mux.HandleFunc("DELETE /admin/api/accounts/{id}", s.requireAdmin(s.handleDeleteAccount))
	mux.HandleFunc("POST /admin/api/accounts/{id}/device/start", s.requireAdmin(s.handleDeviceStart))
	mux.HandleFunc("POST /admin/api/accounts/{id}/device/confirm", s.requireAdmin(s.handleDeviceConfirm))
	mux.HandleFunc("POST /admin/api/accounts/check-all-checkin", s.requireAdmin(s.handleCheckAllCheckin))
	mux.HandleFunc("POST /admin/api/accounts/{id}/checkin", s.requireAdmin(s.handleCheckinOne))
	mux.HandleFunc("GET /admin/api/requests", s.requireAdmin(s.handleListRequests))
	mux.HandleFunc("GET /admin/api/requests/stats", s.requireAdmin(s.handleRequestStats))
	mux.HandleFunc("DELETE /admin/api/requests", s.requireAdmin(s.handleClearRequests))
	mux.HandleFunc("GET /admin/api/settings", s.requireAdmin(s.handleGetSettings))
	mux.HandleFunc("PUT /admin/api/settings", s.requireAdmin(s.handleSaveSettings))
	mux.HandleFunc("POST /admin/api/chat-test", s.requireAdmin(s.handleChatTest))
	mux.HandleFunc("GET /admin/api/logs", s.requireAdmin(s.handleLogs))
	mux.HandleFunc("POST /admin/api/proxy-test", s.requireAdmin(s.handleProxyTest))

	// 管理后台 SPA
	mux.HandleFunc("GET /admin", s.serveStaticIndex)
	mux.HandleFunc("GET /admin/", s.serveStatic)
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin/", http.StatusFound)
	})

	return mux
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"time":   time.Now().Format(time.RFC3339),
	})
}

// ---- 本地 key 鉴权 ----

type apiKeyContextKey struct{}

func (s *Server) requireAPIKey(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		presented := extractAPIKey(r)
		if presented == "" {
			logger.Warnf("API 鉴权失败：缺少 API Key，ip=%s path=%s", clientIP(r), r.URL.Path)
			writeOpenAIError(w, http.StatusUnauthorized, "missing_api_key", "缺少 API Key：请以 Authorization: Bearer sk-... 或 X-API-Key 提供")
			return
		}
		name, err := s.store.AuthenticateLocalKey(presented)
		if err != nil {
			writeOpenAIError(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		if name == "" {
			logger.Warnf("API 鉴权失败：key 无效或已禁用，ip=%s path=%s key=%s…", clientIP(r), r.URL.Path, truncate(presented, 12))
			writeOpenAIError(w, http.StatusUnauthorized, "invalid_api_key", "API Key 无效或已禁用")
			return
		}
		next.ServeHTTP(w, r.WithContext(contextWith(r, apiKeyContextKey{}, name)))
	}
}

func extractAPIKey(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		return strings.TrimSpace(auth[7:])
	}
	if v := r.Header.Get("X-API-Key"); v != "" {
		return strings.TrimSpace(v)
	}
	return ""
}

// ---- 管理员鉴权 ----

func (s *Server) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.isAdminAuthenticated(r) {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"detail": "未登录或会话已过期"})
			return
		}
		next.ServeHTTP(w, r)
	}
}

func (s *Server) signCookie(expiresAt time.Time) string {
	payload := fmt.Sprintf("admin|%d", expiresAt.Unix())
	mac := hmac.New(sha256.New, []byte(s.adminCookieSecret))
	mac.Write([]byte(payload))
	return fmt.Sprintf("%d.%s", expiresAt.Unix(), hex.EncodeToString(mac.Sum(nil)))
}

func (s *Server) verifyCookie(value string) bool {
	var unix int64
	var sigHex string
	if _, err := fmt.Sscanf(value, "%d.%s", &unix, &sigHex); err != nil {
		return false
	}
	if time.Now().Unix() > unix {
		return false
	}
	payload := fmt.Sprintf("admin|%d", unix)
	mac := hmac.New(sha256.New, []byte(s.adminCookieSecret))
	mac.Write([]byte(payload))
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(sigHex))
}

func (s *Server) isAdminAuthenticated(r *http.Request) bool {
	c, err := r.Cookie(adminCookieName)
	if err != nil || c.Value == "" {
		return false
	}
	return s.verifyCookie(c.Value)
}

func clientIP(r *http.Request) string {
	// 反向代理（nginx）场景：优先取代理转发的真实 IP。
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		// 取最左的非空项（发起端）。
		if i := strings.Index(fwd, ","); i >= 0 {
			fwd = fwd[:i]
		}
		fwd = strings.TrimSpace(fwd)
		if fwd != "" {
			return fwd
		}
	}
	if real := strings.TrimSpace(r.Header.Get("X-Real-IP")); real != "" {
		return real
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ---- 响应工具 ----

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// apiEnvelope 对齐 freebuff2api-go 的 {data: ...} 包装。
func writeAPIData(w http.ResponseWriter, status int, data any) {
	writeJSON(w, status, map[string]any{"data": data})
}

func writeAPIError(w http.ResponseWriter, status int, detail string) {
	writeJSON(w, status, map[string]any{"detail": detail})
}

func writeOpenAIError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    "invalid_request_error",
			"code":    code,
		},
	})
}

// ---- 静态资源 ----

func (s *Server) serveStaticIndex(w http.ResponseWriter, r *http.Request) {
	s.serveStatic(w, r)
}

func (s *Server) serveStatic(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/admin")
	if path == "" || path == "/" {
		path = "/index.html"
	}
	f, err := s.staticFS.Open(strings.TrimPrefix(path, "/"))
	if err != nil {
		// SPA fallback：非静态路径一律回 index.html
		s.serveIndex(w)
		return
	}
	stat, serr := f.Stat()
	f.Close()
	if serr != nil || stat.IsDir() {
		s.serveIndex(w)
		return
	}
	// 实际静态文件：剥离 /admin 前缀后交给 FileServerFS，
	// 否则 FileServerFS 会在 FS 里找 "admin/assets/..." 导致 404。
	http.StripPrefix("/admin", http.FileServerFS(s.staticFS)).ServeHTTP(w, r)
}

func (s *Server) serveIndex(w http.ResponseWriter) {
	index, err := fs.ReadFile(s.staticFS, "index.html")
	if err != nil {
		http.Error(w, "index.html not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(index)
}

// ---- 登录限流 ----

type loginThrottle struct {
	mu       sync.Mutex
	failures map[string]*failureState
	maxFails int
	window   time.Duration
}

type failureState struct {
	count        int
	blockedUntil time.Time
}

func newLoginThrottle(maxFails int, window time.Duration) *loginThrottle {
	return &loginThrottle{failures: map[string]*failureState{}, maxFails: maxFails, window: window}
}

func (t *loginThrottle) blocked(ip string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	st, ok := t.failures[ip]
	return ok && st.blockedUntil.After(time.Now())
}

func (t *loginThrottle) recordFail(ip string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	st := t.failures[ip]
	if st == nil {
		st = &failureState{}
		t.failures[ip] = st
	}
	st.count++
	if st.count >= t.maxFails {
		st.blockedUntil = time.Now().Add(t.window)
		st.count = 0
	}
}

func (t *loginThrottle) reset(ip string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.failures, ip)
}
