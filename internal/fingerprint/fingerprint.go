// Package fingerprint 构造与官方客户端一致的请求头指纹。
//
// 官方有两个发请求的入口，指纹只在两处不同：
//
//	u1s1-cli（终端，npm u1s1-cli 1.4.1）      → x-u1s1-client: terminal
//	u1s1 桌面客户端（app 0.1.9，内嵌 u1s1-cli 1.3.0 + Node 22.23.1）→ x-u1s1-client: desktop
//
// 桌面端不是另一个实现：它把 u1s1-cli 当库用（node_modules/u1s1-cli），
// 经 `u1s1-cli/embed` 调 prepareWebEnv → ensureSigningProxy(cfg, "desktop", attestation)，
// CLI 自己则传 fallbackClient="terminal"。device-auth.js 里
// `clientSurface()` 的取值顺序是 U1S1_CLIENT 环境变量 → desktop → fallback。
// 除 x-u1s1-client 之外两者请求头逐字节相同，本项目对齐**桌面客户端**。
//
// chat/completions（OpenAI SDK v6.40.0 经本地签名代理转发）：
//
//	Authorization: DPoP u1s1d-xxx     （设备凭证通道；普通 u1s1- key 通道为 Bearer）
//	dpop: <header.payload.sig>        （每请求新签，见 upstream/device.go）
//	User-Agent: pi ({os.platform()} {os.release()}; {os.arch()})
//	    例: pi (darwin 25.6.0; arm64)  ← pi-ai 的 getPiUserAgent()，覆盖 SDK 默认的
//	                                      "OpenAI/JS 6.40.0"
//	x-u1s1-version: 1.5.0             ← 网关按此识别客户端版本
//	x-u1s1-client: desktop|terminal
//	x-u1s1-platform: {os.platform()}-{os.arch()}   例: darwin-arm64
//	x-u1s1-attestation: <token>       （1.3.0 新增：网关经 /v1/models 签发的客户端证明，
//	                                    绑定 user+device、7 天有效，无法自造，见 upstream/attestation.go）
//	X-Stainless-Lang: js              ← 以下 7 个由 openai SDK 的 getPlatformHeaders()
//	X-Stainless-Package-Version: 6.40.0    与 buildHeaders() 自动附加，签名代理原样转发
//	X-Stainless-OS: MacOS|Linux|Windows    （normalizePlatform 映射）
//	X-Stainless-Arch: arm64|x64            （normalizeArch 映射）
//	X-Stainless-Runtime: node
//	X-Stainless-Runtime-Version: v22.x.x   （实际运行的 Node 版本）
//	X-Stainless-Retry-Count: 0
//	Accept: application/json / Accept-Language: * / Sec-Fetch-Mode: cors
//	    （undici fetch 的固定产物，网关不参与判定，本项目不发）
//
// 辅助端点（/models /me、/auth/device/*）用裸 fetch 调用，只带
// authorization + x-u1s1-version（auth 端点连 x-u1s1-version 都没有，只有 content-type），
// 但**一定带 User-Agent**：
//
//	桌面客户端 → user-agent: undici
//	    桌面端的 Next.js server 在 instrumentation 阶段先跑 pi-coding-agent 的
//	    configureHttpDispatcher()，它调用 undici.install() 把 globalThis.fetch 换成
//	    独立 undici 8.5.0 的实现，之后所有裸 fetch（含 fetchModels/fetchMe）都走 undici。
//	CLI      → user-agent: node（1.4.1 不装 dispatcher，用 Node 内置 fetch）
//
// AuxHeaders / DeviceMe / DeviceModels 与桌面端一致发 undici。
//
// 本包按「档案」成套输出上述头，保证 UA 与 X-Stainless-* 自洽；
// 档案持久化到 data/fingerprint.json —— 同一部署稳定复用同一身份，
// 避免每次重启都变成"新设备"。可用 FINGERPRINT_PROFILE 强制指定档案。
//
// 以上取值来自 2026-09-03 对 u1s1-cli 1.4.1（npm tarball）与桌面客户端 0.1.9
// （u1s1_0.1.9_aarch64.dmg 内 node_modules/u1s1-cli 1.3.0 + Node 22.23.1）的静态核对，
// 并用「本地 mock 网关 + 官方 ensureSigningProxy + 官方 pi-ai openai-completions 客户端」
// 实跑抓包逐头验证。复现脚本：docs/repro/desktop-fingerprint-capture.mjs
// （每次官方 CLI / 桌面端发版后跑一遍，逐头比对下面的取值是否仍然成立）。
package fingerprint

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sync"
)

// Profile 一个自洽的客户端身份档案。
type Profile struct {
	ID             string `json:"id"`             // macos-arm64 ...
	Label          string `json:"label"`          // 展示名
	UAPlatform     string `json:"ua_platform"`    // node os.platform(): darwin/linux/win32
	UARelease      string `json:"ua_release"`     // node os.release()
	UAArch         string `json:"ua_arch"`        // process.arch: arm64/x64
	StainlessOS    string `json:"stainless_os"`   // normalizePlatform: MacOS/Linux/Windows
	StainlessArch  string `json:"stainless_arch"` // normalizeArch: arm64/x64
	RuntimeVersion string `json:"runtime_version"`
}

// Profiles 内置档案集合（取值均为真实存在的系统/Node 组合）。
var Profiles = []Profile{
	{
		ID: "macos-arm64", Label: "macOS · Apple Silicon",
		UAPlatform: "darwin", UARelease: "24.6.0", UAArch: "arm64",
		StainlessOS: "MacOS", StainlessArch: "arm64", RuntimeVersion: "v22.21.1",
	},
	{
		ID: "macos-x64", Label: "macOS · Intel",
		UAPlatform: "darwin", UARelease: "24.6.0", UAArch: "x64",
		StainlessOS: "MacOS", StainlessArch: "x64", RuntimeVersion: "v22.19.0",
	},
	{
		ID: "linux-x64", Label: "Linux x64",
		UAPlatform: "linux", UARelease: "6.8.0-45-generic", UAArch: "x64",
		StainlessOS: "Linux", StainlessArch: "x64", RuntimeVersion: "v22.21.1",
	},
	{
		ID: "linux-arm64", Label: "Linux ARM64",
		UAPlatform: "linux", UARelease: "6.8.0-45-generic", UAArch: "arm64",
		StainlessOS: "Linux", StainlessArch: "arm64", RuntimeVersion: "v24.5.0",
	},
	{
		ID: "windows-x64", Label: "Windows x64",
		UAPlatform: "win32", UARelease: "10.0.26100", UAArch: "x64",
		StainlessOS: "Windows", StainlessArch: "x64", RuntimeVersion: "v22.19.0",
	},
}

// OpenAI SDK 版本号（u1s1-cli 1.4.1/1.5.0 与桌面端 0.1.9/0.1.11 内嵌的 pi-ai 0.84.4 → openai 6.40.0）。
const SDKPackageVersion = "6.40.0"

// ClientSurface x-u1s1-client 头。官方取值：
//
//	U1S1_CLIENT 环境变量显式指定（terminal|web|desktop|cloud）优先；
//	否则桌面客户端（web.js 传 fallbackClient="desktop"）发 desktop，
//	CLI TUI（index.js 传 "terminal"）发 terminal。
//
// 本项目对齐桌面客户端 → desktop。
const ClientSurface = "desktop"

// UndiciUserAgent 辅助端点（/models /me、/auth/device/*）的 User-Agent。
// 桌面客户端的 Next.js server 启动时执行 undici.install()，把 globalThis.fetch
// 换成独立 undici 8.5.0 的实现，这些裸 fetch 请求因此带 user-agent: undici。
// （CLI 不装 dispatcher，同一请求是 user-agent: node。）
// 关键是我们**必须发一个 UA**：Go 默认发 Go-http-client/1.1，那是明显的非官方指纹。
const UndiciUserAgent = "undici"

// ClientPlatform x-u1s1-platform 头：node os.platform() + process.arch，如 darwin-arm64。
func ClientPlatform(p Profile) string {
	return p.UAPlatform + "-" + p.UAArch
}

// ProfileByID 按 ID 查档案。
func ProfileByID(id string) (Profile, bool) {
	for _, p := range Profiles {
		if p.ID == id {
			return p, true
		}
	}
	return Profile{}, false
}

// UserAgent 官方 CLI 的 User-Agent 格式：pi ({platform} {release}; {arch})。
func UserAgent(p Profile) string {
	return fmt.Sprintf("pi (%s %s; %s)", p.UAPlatform, p.UARelease, p.UAArch)
}

// Manager 管理当前生效档案（持久化 + 可热切换）。
type Manager struct {
	mu      sync.RWMutex
	statePath string
	current Profile
}

type stateFile struct {
	ProfileID string `json:"profile_id"`
}

// NewManager 加载或初始化档案状态。forcedID 非空时强制使用该档案；
// 否则沿用上次持久化的档案；首次启动随机挑选一个。
func NewManager(statePath, forcedID string) (*Manager, error) {
	m := &Manager{statePath: statePath}
	if forcedID != "" && forcedID != "auto" {
		if p, ok := ProfileByID(forcedID); ok {
			m.current = p
			_ = m.persist()
			return m, nil
		}
	}
	if st, err := loadState(statePath); err == nil && st.ProfileID != "" {
		if p, ok := ProfileByID(st.ProfileID); ok {
			m.current = p
			return m, nil
		}
	}
	idx, err := rand.Int(rand.Reader, big.NewInt(int64(len(Profiles))))
	if err != nil {
		m.current = Profiles[0]
	} else {
		m.current = Profiles[idx.Int64()]
	}
	if err := m.persist(); err != nil {
		return m, fmt.Errorf("persist fingerprint state: %w", err)
	}
	return m, nil
}

// Current 当前档案。
func (m *Manager) Current() Profile {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.current
}

// SetProfile 热切换档案并持久化。
func (m *Manager) SetProfile(id string) error {
	p, ok := ProfileByID(id)
	if !ok {
		return fmt.Errorf("unknown profile %q", id)
	}
	m.mu.Lock()
	m.current = p
	m.mu.Unlock()
	return m.persist()
}

func (m *Manager) persist() error {
	st := stateFile{ProfileID: m.Current().ID}
	data, _ := json.MarshalIndent(st, "", "  ")
	if err := os.MkdirAll(filepath.Dir(m.statePath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(m.statePath, data, 0o600)
}

func loadState(path string) (stateFile, error) {
	var st stateFile
	data, err := os.ReadFile(path)
	if err != nil {
		return st, err
	}
	err = json.Unmarshal(data, &st)
	return st, err
}

// ChatHeaders 构造 chat/completions 请求的完整指纹头。
func (m *Manager) ChatHeaders(apiKey, cliVersion string) map[string]string {
	p := m.Current()
	return map[string]string{
		"authorization":                "Bearer " + apiKey,
		"user-agent":                   UserAgent(p),
		"x-u1s1-version":               cliVersion,
		"X-Stainless-Lang":             "js",
		"X-Stainless-Package-Version":  SDKPackageVersion,
		"X-Stainless-OS":               p.StainlessOS,
		"X-Stainless-Arch":             p.StainlessArch,
		"X-Stainless-Runtime":          "node",
		"X-Stainless-Runtime-Version":  p.RuntimeVersion,
		"X-Stainless-Retry-Count":      "0",
	}
}

// AuxHeaders 构造辅助端点（/models、/me 等）的头 —— 与官方 fetchModels/fetchMe 一致：
// authorization + x-u1s1-version + undici UA（裸 fetch 不覆盖 UA 时的运行时默认值）。
func AuxHeaders(apiKey, cliVersion string) map[string]string {
	return map[string]string{
		"authorization":  "Bearer " + apiKey,
		"x-u1s1-version": cliVersion,
		"user-agent":     UndiciUserAgent,
	}
}
