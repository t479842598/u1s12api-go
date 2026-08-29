// Package config 加载/保存 .env 配置（对齐 freebuff2api-go 的做法：
// 管理后台改设置时原子写回 .env，运行期通过 atomic.Value 热替换）。
package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// Settings 运行配置。字段与 .env 键一一对应。
type Settings struct {
	Host               string // 监听地址
	Port               int    // 监听端口
	AdminPassword      string // 管理后台登录口令
	UpstreamBaseURL    string // 上游网关，默认 https://api.u1s1.io/v1
	EgressProxyURL     string // 出口代理（http/https/socks5），空=直连
	FingerprintProfile string // 头指纹档案：auto | macos-arm64 | macos-x64 | linux-x64 | linux-arm64 | windows-x64
	U1S1Version        string // x-u1s1-version 头的值（跟随官方 CLI 版本）
	BarkKey            string // Bark 推送密钥（api.day.app/<key>），空=官网动态只入库不推送
	SiteFeedCheckHours int    // 官网公告/更新记录检查间隔（小时）
	LogLevel           string // debug|info|warn|error
	LogColor           bool
	Debug              bool
	QuotaAutoRefresh   bool // 北京时间 0 点额度重置后自动全量刷新上游 Key 配额
	FirstRun           bool // 本次启动是否新生成了 ADMIN_PASSWORD（用于启动时打印提醒）
}

const (
	DefaultUpstreamBaseURL    = "https://api.u1s1.io/v1"
	DefaultU1S1Version        = "1.2.3"
	DefaultSiteFeedCheckHours = 24
	defaultProfile            = "auto"
)

// Env 键名。
const (
	KeyHost               = "HOST"
	KeyPort               = "PORT"
	KeyAdminPassword      = "ADMIN_PASSWORD"
	KeyUpstreamBaseURL    = "UPSTREAM_BASE_URL"
	KeyEgressProxy        = "EGRESS_PROXY"
	KeyFingerprintProfile = "FINGERPRINT_PROFILE"
	KeyU1S1Version        = "U1S1_VERSION"
	KeyBarkKey            = "BARK_KEY"
	KeySiteFeedCheckHours = "SITEFEED_CHECK_HOURS"
	KeyLogLevel           = "LOG_LEVEL"
	KeyLogColor           = "LOG_COLOR"
	KeyDebug              = "DEBUG"
	KeyQuotaAutoRefresh   = "QUOTA_AUTO_REFRESH"
)

var mu sync.Mutex

// Load 从 projectRoot/.env 读取配置；环境变量优先。文件不存在时创建默认 .env。
func Load(projectRoot string) (*Settings, error) {
	mu.Lock()
	defer mu.Unlock()
	envPath := filepath.Join(projectRoot, ".env")
	kv := readEnvFile(envPath)

	firstRun := false
	if _, ok := kv[KeyAdminPassword]; !ok {
		// 首次启动生成随机口令并落盘，避免默认弱口令。
		pw, err := randomToken(18)
		if err != nil {
			return nil, fmt.Errorf("generate admin password: %w", err)
		}
		kv[KeyAdminPassword] = pw
		firstRun = true
	}

	s := &Settings{
		Host:               getStr(kv, KeyHost, "127.0.0.1"),
		Port:               getInt(kv, KeyPort, 8080),
		AdminPassword:      getStr(kv, KeyAdminPassword, ""),
		UpstreamBaseURL:    strings.TrimRight(getStr(kv, KeyUpstreamBaseURL, DefaultUpstreamBaseURL), "/"),
		EgressProxyURL:     strings.TrimSpace(getStr(kv, KeyEgressProxy, "")),
		FingerprintProfile: getStr(kv, KeyFingerprintProfile, defaultProfile),
		U1S1Version:        getStr(kv, KeyU1S1Version, DefaultU1S1Version),
		BarkKey:            getStr(kv, KeyBarkKey, ""),
		SiteFeedCheckHours: getInt(kv, KeySiteFeedCheckHours, DefaultSiteFeedCheckHours),
		LogLevel:           getStr(kv, KeyLogLevel, "info"),
		LogColor:           getBool(kv, KeyLogColor, true),
		Debug:              getBool(kv, KeyDebug, false),
		QuotaAutoRefresh:   getBool(kv, KeyQuotaAutoRefresh, true), // 默认开启
		FirstRun:           firstRun,
	}
	applyOSEnv(s)
	return s, nil
}

func applyOSEnv(s *Settings) {
	if v := os.Getenv("U1S12API_HOST"); v != "" {
		s.Host = v
	}
	if v := os.Getenv("U1S12API_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			s.Port = p
		}
	}
	if v := os.Getenv("U1S12API_ADMIN_PASSWORD"); v != "" {
		s.AdminPassword = v
	}
}

// Save 把补丁合并进 .env（保留注释与未知行）并返回新 Settings。
func Save(projectRoot string, patch map[string]string) (*Settings, error) {
	mu.Lock()
	defer mu.Unlock()
	envPath := filepath.Join(projectRoot, ".env")
	lines := readEnvLines(envPath)
	seen := map[string]bool{}
	out := make([]string, 0, len(lines)+len(patch))
	for _, ln := range lines {
		trimmed := strings.TrimSpace(ln)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			out = append(out, ln)
			continue
		}
		k, _, ok := strings.Cut(trimmed, "=")
		if !ok {
			out = append(out, ln)
			continue
		}
		key := strings.TrimSpace(k)
		if v, hit := patch[key]; hit {
			seen[key] = true
			out = append(out, key+"="+v)
		} else {
			out = append(out, ln)
		}
	}
	for k, v := range patch {
		if !seen[k] {
			out = append(out, k+"="+v)
		}
	}
	content := strings.Join(out, "\n") + "\n"
	if err := os.WriteFile(envPath, []byte(content), 0o600); err != nil {
		return nil, err
	}

	kv := readEnvFile(envPath)
	s := &Settings{
		Host:               getStr(kv, KeyHost, "127.0.0.1"),
		Port:               getInt(kv, KeyPort, 8080),
		AdminPassword:      getStr(kv, KeyAdminPassword, ""),
		UpstreamBaseURL:    strings.TrimRight(getStr(kv, KeyUpstreamBaseURL, DefaultUpstreamBaseURL), "/"),
		EgressProxyURL:     strings.TrimSpace(getStr(kv, KeyEgressProxy, "")),
		FingerprintProfile: getStr(kv, KeyFingerprintProfile, defaultProfile),
		U1S1Version:        getStr(kv, KeyU1S1Version, DefaultU1S1Version),
		BarkKey:            getStr(kv, KeyBarkKey, ""),
		SiteFeedCheckHours: getInt(kv, KeySiteFeedCheckHours, DefaultSiteFeedCheckHours),
		LogLevel:           getStr(kv, KeyLogLevel, "info"),
		LogColor:           getBool(kv, KeyLogColor, true),
		Debug:              getBool(kv, KeyDebug, false),
		QuotaAutoRefresh:   getBool(kv, KeyQuotaAutoRefresh, true),
	}
	applyOSEnv(s)
	return s, nil
}

// PatchableFields 把 Settings 序列化成可写回 .env 的键值。
func PatchableFields(s *Settings) map[string]string {
	return map[string]string{
		KeyHost:               s.Host,
		KeyPort:               strconv.Itoa(s.Port),
		KeyAdminPassword:      s.AdminPassword,
		KeyUpstreamBaseURL:    s.UpstreamBaseURL,
		KeyEgressProxy:        s.EgressProxyURL,
		KeyFingerprintProfile: s.FingerprintProfile,
		KeyU1S1Version:        s.U1S1Version,
		KeyBarkKey:            s.BarkKey,
		KeySiteFeedCheckHours: strconv.Itoa(s.SiteFeedCheckHours),
		KeyLogLevel:           s.LogLevel,
		KeyLogColor:           boolStr(s.LogColor),
		KeyDebug:              boolStr(s.Debug),
		KeyQuotaAutoRefresh:   boolStr(s.QuotaAutoRefresh),
	}
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func readEnvFile(path string) map[string]string {
	kv := map[string]string{}
	for _, ln := range readEnvLines(path) {
		trimmed := strings.TrimSpace(ln)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		k, v, ok := strings.Cut(trimmed, "=")
		if !ok {
			continue
		}
		kv[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"'`)
	}
	return kv
}

func readEnvLines(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		// 首次启动给一份带注释的模板。
		defaults := `# u1s12api-go 配置
# HOST=127.0.0.1
# PORT=8080
# ADMIN_PASSWORD=首次启动自动生成
# UPSTREAM_BASE_URL=https://api.u1s1.io/v1
# EGRESS_PROXY=socks5://127.0.0.1:7897
# FINGERPRINT_PROFILE=auto
# U1S1_VERSION=1.2.3
# BARK_KEY=                  ← Bark 推送密钥（api.day.app/<key>），空=官网动态只入库不推送
# SITEFEED_CHECK_HOURS=24    ← 官网公告/更新记录检查间隔（小时）
# QUOTA_AUTO_REFRESH=true   ← 北京时间 0 点额度重置后自动刷新全部上游 Key 配额
`
		_ = os.WriteFile(path, []byte(defaults), 0o600)
		return nil
	}
	return strings.Split(string(data), "\n")
}

func getStr(kv map[string]string, key, def string) string {
	if v, ok := kv[key]; ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return def
}

func getInt(kv map[string]string, key string, def int) int {
	if v, ok := kv[key]; ok {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return n
		}
	}
	return def
}

func getBool(kv map[string]string, key string, def bool) bool {
	v, ok := kv[key]
	if !ok {
		return def
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return def
}

func randomToken(nBytes int) (string, error) {
	buf := make([]byte, nBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
