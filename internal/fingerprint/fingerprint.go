// Package fingerprint 产出与官方 u1s1-cli 一致的请求头**值**。
//
// 对齐目标是**官方 CLI（terminal surface）**，不是桌面客户端 —— 依据与取舍见
// ADR 0001（.lrnev/scenes/00-default/decisions/adr/0001-*.md）。要点：
//
//   - 桌面端截至 0.1.15 仍内嵌 u1s1-cli **1.3.0**，而 npm CLI 已到 **1.7.1**；
//     报 desktop + 新版本是现实中不存在的组合。
//   - 两个入口用的是**同一份 device-auth.js**（1.5.0→1.7.1 逐字节未变），
//     头集合与 DPoP 结构完全相同，真实差异只有两处：
//     x-u1s1-client（terminal|desktop）与裸 fetch 的 UA（node|undici）。
//
// 官方 chat 请求（openai SDK v6.40.0 → 本地签名代理 → authorizedFetch）逐头实测：
//
//	host, connection: keep-alive, accept: application/json,
//	x-stainless-retry-count: 0, x-stainless-lang: js, x-stainless-package-version: 6.40.0,
//	x-stainless-os, x-stainless-arch, x-stainless-runtime: node, x-stainless-runtime-version,
//	user-agent: pi (<os.platform()> <os.release()>; <arch>),   ← pi-ai getPiUserAgent()
//	x-u1s1-version, content-type: application/json, accept-language: *, sec-fetch-mode: cors,
//	accept-encoding: gzip, deflate, x-u1s1-client, x-u1s1-platform, [x-u1s1-attestation],
//	authorization: DPoP u1s1d-…, dpop: <header.payload.sig>, content-length
//
// 辅助端点（/v1/models、/v1/me）是裸 fetch，只带 authorization + x-u1s1-version +
// 运行时默认 UA，**没有** X-Stainless-*：
//
//	CLI 启动阶段 → user-agent: node（CLI 不装 dispatcher）
//	CLI 进 TUI 后 / 桌面端全程 → user-agent: undici（pi-coding-agent 的
//	    interactive-mode.js:1529 调 configureHttpDispatcher() 把 globalThis.fetch
//	    换成独立 undici，此后同进程内的 fetch 都是 undici）
//
// 本包的取值必须与「部署机事实」自洽：hostname / platform / 内核 release / arch /
// device_name 全部由真实环境派生（见 identity.go 与 ADR 0002），只有 Node 版本是
// 声称值，受官方 engines.node >= 22.19.0 约束。
//
// 头的**线格式**（小写头名、响应解压）不在本包，见 internal/upstream/wire.go；
// 已知无法对齐的残差（头顺序、connection 头、User-Agent 大小写、TLS 指纹）见该文件。
//
// 每次官方 CLI 发版后跑 docs/repro/desktop-fingerprint-capture.mjs 逐头核对。
package fingerprint

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// ProfileIDAuto 表示身份由部署机真实环境派生（默认）。
const ProfileIDAuto = "auto"

// Profile 一套自洽的客户端身份。ID 为 ProfileIDAuto 时各字段来自真实主机。
type Profile struct {
	ID             string `json:"id"`
	Label          string `json:"label"`
	Hostname       string `json:"hostname"`        // os.Hostname()，进 device_name
	UAPlatform     string `json:"ua_platform"`     // node os.platform(): darwin/linux/win32
	UARelease      string `json:"ua_release"`      // node os.release()（内核版本，非产品版本）
	UAArch         string `json:"ua_arch"`         // process.arch: arm64/x64
	StainlessOS    string `json:"stainless_os"`    // normalizePlatform: MacOS/Linux/Windows
	StainlessArch  string `json:"stainless_arch"`  // normalizeArch: arm64/x64
	RuntimeVersion string `json:"runtime_version"` // x-stainless-runtime-version，须 >=22.19.0
}

// Profiles 手工覆盖用的假档案（FINGERPRINT_PROFILE=macos-arm64 等）。
//
// 默认**不再**从这里轮转（ADR 0002）：真实派生身份才能做到 hostname / platform /
// 内核版本 / device_name 互相印证。这里保留是为了在需要临时伪装成 mac/win 时可用，
// 因此每套档案的 hostname 与 runtime 版本也必须与它的 platform 自洽。
var Profiles = []Profile{
	{
		ID: "macos-arm64", Label: "macOS · Apple Silicon（手工伪装）",
		Hostname: "MacBook-Pro-2", UAPlatform: "darwin", UARelease: "24.6.0", UAArch: "arm64",
		StainlessOS: "MacOS", StainlessArch: "arm64", RuntimeVersion: "v22.21.1",
	},
	{
		ID: "macos-x64", Label: "macOS · Intel（手工伪装）",
		Hostname: "iMac-Pro", UAPlatform: "darwin", UARelease: "24.6.0", UAArch: "x64",
		StainlessOS: "MacOS", StainlessArch: "x64", RuntimeVersion: "v22.19.0",
	},
	{
		ID: "linux-x64", Label: "Linux x64（手工伪装）",
		Hostname: "ip-10-0-1-42", UAPlatform: "linux", UARelease: "6.8.0-45-generic", UAArch: "x64",
		StainlessOS: "Linux", StainlessArch: "x64", RuntimeVersion: "v22.21.1",
	},
	{
		ID: "linux-arm64", Label: "Linux ARM64（手工伪装）",
		Hostname: "ip-10-0-1-43", UAPlatform: "linux", UARelease: "6.8.0-45-generic", UAArch: "arm64",
		StainlessOS: "Linux", StainlessArch: "arm64", RuntimeVersion: "v24.5.0",
	},
	{
		ID: "windows-x64", Label: "Windows x64（手工伪装）",
		Hostname: "DESKTOP-A1B2C3D", UAPlatform: "win32", UARelease: "10.0.26100", UAArch: "x64",
		StainlessOS: "Windows", StainlessArch: "x64", RuntimeVersion: "v22.19.0",
	},
}

// SDKPackageVersion openai SDK 版本（pi-ai 0.84.4 依赖 openai 6.40.0，CLI 1.3.0~1.7.1 未变）。
const SDKPackageVersion = "6.40.0"

// ClientSurface x-u1s1-client 头。官方取值顺序：U1S1_CLIENT 环境变量
// （terminal|web|desktop|cloud）→ process.versions.electron 时 desktop → 入口 fallback。
// CLI TUI 传 fallbackClient="terminal"，本项目对齐 CLI → terminal。
const ClientSurface = "terminal"

// NodeUserAgent CLI 启动阶段裸 fetch（fetchModels/fetchMe、/auth/device/*）的 UA。
// CLI 不装 undici dispatcher，用的是 Node 内置 fetch，其默认 UA 就是 "node"。
const NodeUserAgent = "node"

// UndiciUserAgent 同一进程装了 undici dispatcher 之后（CLI 进 TUI 后的 attestation
// refresh、桌面端全程）裸 fetch 的 UA。我们必须显式发一个 UA，
// 否则 Go 会发 "Go-http-client/1.1"，那是明显的非官方指纹。
const UndiciUserAgent = "undici"

// AcceptEncoding 官方 fetch 的固定产物（实测 CLI 与桌面端链路均为该值，不含 br）。
const AcceptEncoding = "gzip, deflate"

// ClientPlatform x-u1s1-platform 头：node os.platform() + "-" + process.arch，如 linux-arm64。
func ClientPlatform(p Profile) string {
	return p.UAPlatform + "-" + p.UAArch
}

// UserAgent 官方 UA：pi (<os.platform()> <os.release()>; <arch>)，由 pi-ai getPiUserAgent() 生成。
func UserAgent(p Profile) string {
	return fmt.Sprintf("pi (%s %s; %s)", p.UAPlatform, p.UARelease, p.UAArch)
}

// DeviceName 官方 device_name 格式：`${hostname()} (${platform()})`（login.js）。
// 平台段必须与 x-u1s1-platform 的 platform 部分同源，否则网关交叉核对即露馅。
func DeviceName(p Profile) string {
	host := p.Hostname
	if host == "" {
		host = "localhost"
	}
	return host + " (" + p.UAPlatform + ")"
}

// ProfileByID 按 ID 查假档案（覆盖通道）。
func ProfileByID(id string) (Profile, bool) {
	for _, p := range Profiles {
		if p.ID == id {
			return p, true
		}
	}
	return Profile{}, false
}

// Manager 管理当前生效身份（持久化 + 可热覆盖）。
type Manager struct {
	mu        sync.RWMutex
	statePath string
	current   Profile
	// nodeVersion 已解析并持久化的声称 Node 版本（auto 身份与伪装身份都用它保持一致）。
	nodeVersion string
	// forced 为真表示身份来自 FINGERPRINT_PROFILE 显式覆盖，不参与 auto 派生。
	forced bool
}

// stateFile 是 data/fingerprint.json 的内容。
//
// Schema 只用于记录版本，**不作为是否沿用旧档案的判据**：旧版（无 schema）会随机挑一个
// 假档案并持久化其 id，而那个 id 正是已授权设备一直在用的身份 —— 升级时必须沿用，
// 否则同一台 device_token 会突然换个操作系统（见 NewManager 注释）。
type stateFile struct {
	Schema      int    `json:"schema"`
	ProfileID   string `json:"profile_id"`
	NodeVersion string `json:"node_version,omitempty"`
}

const stateSchema = 2

// NewManager 加载或初始化身份。
//
// forcedID：FINGERPRINT_PROFILE 的值。空或 "auto" 时：已有持久化档案的部署**原样沿用**，
// 只有全新安装（无状态文件）才走真实主机派生；forcedID 命中 Profiles 里的 id 则强制该伪装档案。
//
// 为什么不能把旧档案强制迁成 auto（重要）：已授权设备的 platform / UA / X-Stainless-*
// 是从这个档案发出去的，网关侧对这台 device_token 的认知也建立在此之上。一迁 auto，
// 同一台设备会突然从 macos-x64 变成 linux-arm64 —— 那是真实设备不会有的形态，而且
// 只能靠重新授权才能抹平。把升级代价转嫁给用户是错的设计：升级应当对已有设备无感。
func NewManager(statePath, forcedID, nodeVersionOverride string) (*Manager, error) {
	m := &Manager{statePath: statePath}
	st, stErr := loadState(statePath) // stErr != nil 就是首次启动

	// Node 版本一旦确定就长期沿用，避免重启后声称的运行时版本漂移。
	nodeVersion := normalizeNodeVersion(st.NodeVersion)
	if nodeVersion == "" || !VersionAtLeast(nodeVersion, MinNodeVersion) {
		seed, _ := os.Hostname()
		nodeVersion = ResolveNodeVersion(nodeVersionOverride, lookupLocalNodeVersion, seed)
	}
	m.nodeVersion = nodeVersion

	// 1) 环境变量显式指定 → 无条件尊重（伪装逃生口）。
	if forcedID != "" && forcedID != ProfileIDAuto {
		if p, ok := ProfileByID(forcedID); ok {
			m.set(p, true)
			return m, m.persist()
		}
	}
	// 2) 只要状态文件里存着一个有效档案（不管是旧版随机选的、还是后台手动切的），
	//    就继续用它 —— 保证升级对已授权设备无感。
	if stErr == nil && st.ProfileID != "" && st.ProfileID != ProfileIDAuto {
		if p, ok := ProfileByID(st.ProfileID); ok {
			m.set(p, true)
			// 补写 schema / node_version，但**不改 profile_id**：身份不能因为升级就变。
			return m, m.persist()
		}
		// 档案 id 已不存在（被删）→ 落回 auto，绝不随机再挑一个。
	}
	// 3) 全新安装（或档案已失效）→ 本机真实环境派生。
	m.set(DetectProfile(m.nodeVersion), false)
	return m, m.persist()
}

// set 更新当前身份并补齐 Node 版本，保证 runtime 版本与持久化值一致。
func (m *Manager) set(p Profile, forced bool) {
	if p.RuntimeVersion == "" {
		p.RuntimeVersion = m.nodeVersion
	}
	m.mu.Lock()
	m.current, m.forced = p, forced
	m.mu.Unlock()
}

// Current 当前身份。
func (m *Manager) Current() Profile {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.current
}

// IsAuto 当前身份是否来自真实主机派生。
func (m *Manager) IsAuto() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return !m.forced
}

// DeviceName 当前身份对应的官方格式设备名。
func (m *Manager) DeviceName() string { return DeviceName(m.Current()) }

// SetProfile 热切换身份并持久化（后台伪装用；"auto" 回到真实主机派生）。
func (m *Manager) SetProfile(id string) error {
	if id == "" || id == ProfileIDAuto {
		m.mu.RLock()
		nv := m.nodeVersion
		m.mu.RUnlock()
		m.set(DetectProfile(nv), false)
		return m.persist()
	}
	p, ok := ProfileByID(id)
	if !ok {
		return fmt.Errorf("unknown profile %q", id)
	}
	m.set(p, true)
	return m.persist()
}

func (m *Manager) persist() error {
	m.mu.RLock()
	st := stateFile{Schema: stateSchema, ProfileID: m.current.ID, NodeVersion: m.nodeVersion}
	if m.forced && m.current.ID == ProfileIDAuto {
		st.ProfileID = ProfileIDAuto
	}
	m.mu.RUnlock()
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
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

// ChatFingerprintHeaders 返回 chat 请求应发的全部指纹头（**不含** authorization / dpop /
// content-type / content-length，那些由调用方按实际请求补）。
//
// 取值与顺序依据 2026-09-04 对 CLI 1.7.1 的抓包（docs/repro/desktop-fingerprint-capture.mjs）。
// attestation 为空则不发该头 —— 官方在无令牌时同样不发（device-auth.js:attachAttestationHeader）。
func ChatFingerprintHeaders(p Profile, cliVersion, attestation string) map[string]string {
	h := map[string]string{
		// undici 在 h1 上显式发该头，Go 默认不发（实测小写键可直接透传）。
		"connection":                  "keep-alive",
		"accept":                      "application/json",
		"accept-language":             "*",
		"sec-fetch-mode":              "cors",
		"accept-encoding":             AcceptEncoding,
		"user-agent":                  UserAgent(p),
		"x-u1s1-version":              cliVersion,
		"x-u1s1-client":               ClientSurface,
		"x-u1s1-platform":             ClientPlatform(p),
		"x-stainless-lang":            "js",
		"x-stainless-package-version": SDKPackageVersion,
		"x-stainless-os":              p.StainlessOS,
		"x-stainless-arch":            p.StainlessArch,
		"x-stainless-runtime":         "node",
		"x-stainless-runtime-version": p.RuntimeVersion,
		"x-stainless-retry-count":     "0",
	}
	if attestation != "" {
		h["x-u1s1-attestation"] = attestation
	}
	return h
}

// AuxHeaders 辅助端点（/models、/me、/auth/device/*）的头 —— 与官方裸 fetch 一致：
// 只带 authorization + x-u1s1-version + 运行时默认 UA，**不带** X-Stainless-*。
//
// ua 取 NodeUserAgent（CLI 启动阶段）或 UndiciUserAgent（进程已装 dispatcher）。
func AuxHeaders(apiKey, cliVersion, ua string) map[string]string {
	if ua == "" {
		ua = NodeUserAgent
	}
	h := map[string]string{
		"connection":      "keep-alive",
		"accept":          "*/*",
		"accept-language": "*",
		"sec-fetch-mode":  "cors",
		"accept-encoding": AcceptEncoding,
		"user-agent":      ua,
	}
	if apiKey != "" {
		h["authorization"] = "Bearer " + apiKey
	}
	if cliVersion != "" {
		h["x-u1s1-version"] = cliVersion
	}
	return h
}
