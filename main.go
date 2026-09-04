// u1s12api-go — 把 U1S1（有一说一）官方网关包装成标准 OpenAI 兼容 API，
// 附带多 Key 池轮询、免费额度监控与管理后台。架构对齐 freebuff2api-go。
package main

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/t479842598/u1s12api-go/internal/config"
	"github.com/t479842598/u1s12api-go/internal/fingerprint"
	"github.com/t479842598/u1s12api-go/internal/logging"
	"github.com/t479842598/u1s12api-go/internal/server"
	"github.com/t479842598/u1s12api-go/internal/store"
	"github.com/t479842598/u1s12api-go/internal/upstream"
)

//go:embed static
var staticFS embed.FS

func main() {
	root := projectRoot()

	settings, err := config.Load(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config load failed:", err)
		os.Exit(1)
	}

	logFilePath := filepath.Join(root, "data", "logs", "app.log")
	logging.Configure(settings.LogLevel, settings.LogColor, logFilePath)
	appLogger := logging.Named("u1s12api")
	appLogger.Infof("u1s12api 启动 upstream=%s profile=%s u1s1_version=%s",
		settings.UpstreamBaseURL, settings.FingerprintProfile, settings.U1S1Version)
	if settings.FirstRun {
		appLogger.Infof("首次启动：已生成管理口令 ADMIN_PASSWORD=%s（已写入 .env，请尽快登录后台修改）",
			settings.AdminPassword)
	}

	if err := os.MkdirAll(filepath.Join(root, "data"), 0o755); err != nil {
		appLogger.Errorf("创建 data 目录失败: %v", err)
		os.Exit(1)
	}

	st, err := store.Open(filepath.Join(root, "data", "u1s12api.db"))
	if err != nil {
		appLogger.Errorf("打开数据库失败: %v", err)
		os.Exit(1)
	}
	defer st.Close()

	fp, err := fingerprint.NewManager(filepath.Join(root, "data", "fingerprint.json"), settings.FingerprintProfile, settings.FingerprintNodeVersion)
	if err != nil {
		appLogger.Errorf("初始化头指纹失败: %v", err)
		os.Exit(1)
	}
	appLogger.Infof("请求头指纹: %s (%s) platform=%s", fp.Current().ID,
		fingerprint.UserAgent(fp.Current()), fingerprint.ClientPlatform(fp.Current()))
	// 把升级前就已授权的账号钉到它们一直在用的身份上，避免同一台设备突然换操作系统
	// （那只能靠重新授权抹平，等于把升级代价转嫁给用户）。
	if n, err := st.PinDeviceIdentityForAccounts(fingerprint.IdentityJSON(fp.Current())); err != nil {
		appLogger.Warnf("固定已授权账号身份快照失败（不影响使用，首次请求时会逐个回填）: %v", err)
	} else if n > 0 {
		appLogger.Infof("已为 %d 个既有授权账号固定身份快照 %s（升级不改变它们的对外身份）", n, fp.Current().ID)
	}

	pool, err := upstream.NewPool(st)
	if err != nil {
		appLogger.Errorf("加载 Key 池失败: %v", err)
		os.Exit(1)
	}
	if n := pool.ActiveCount(); n == 0 {
		appLogger.Warnf("当前没有可用的 U1S1 Key，请登录 /admin 后台导入（格式：每行一把 u1s1- 开头的 Key）")
	} else {
		appLogger.Infof("Key 池就绪: %d 把可用", n)
	}

	staticSub, err := fs.Sub(staticFS, "static")
	if err != nil {
		appLogger.Errorf("静态资源 embed 失败: %v", err)
		os.Exit(1)
	}

	srv, err := server.New(settings, st, pool, fp, root, staticSub)
	if err != nil {
		appLogger.Errorf("初始化 Server 失败: %v", err)
		os.Exit(1)
	}
	handler := srv.Handler()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	httpServer := &http.Server{
		Addr:              fmt.Sprintf("%s:%d", settings.Host, settings.Port),
		Handler:           handler,
		ReadHeaderTimeout: 30 * time.Second,
	}

	go func() {
		appLogger.Infof("listening on http://%s:%d  管理后台: http://%s:%d/admin/",
			settings.Host, settings.Port, displayHost(settings.Host), settings.Port)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			appLogger.Errorf("server error: %v", err)
			stop()
		}
	}()

	<-ctx.Done()
	appLogger.Infof("shutting down...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(shutdownCtx)
}

func displayHost(h string) string {
	if h == "" || h == "0.0.0.0" || h == "::" {
		return "127.0.0.1"
	}
	return h
}

func projectRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	return dir
}
