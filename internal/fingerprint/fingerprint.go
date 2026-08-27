// Package fingerprint 构造与官方 u1s1 CLI 一致的请求头指纹。
//
// u1s1 CLI（pi-coding-agent 换皮）向 https://api.u1s1.io/v1 发起 chat/completions
// 时由 OpenAI SDK v6.40.0 附带以下指纹头（逆向自 u1s1-cli 1.2.0 dist）：
//
//	Authorization: Bearer u1s1-xxx
//	User-Agent: pi ({os.platform()} {os.release()}; {os.arch()})
//	    例: pi (darwin 24.6.0; arm64)
//	x-u1s1-version: 1.2.0          ← 网关按此识别客户端版本
//	X-Stainless-Lang: js
//	X-Stainless-Package-Version: 6.40.0
//	X-Stainless-OS: MacOS|Linux|Windows   （normalizePlatform 映射）
//	X-Stainless-Arch: arm64|x64
//	X-Stainless-Runtime: node
//	X-Stainless-Runtime-Version: v22.x.x  （便携包 Node ≥22.19）
//	X-Stainless-Retry-Count: 0
//
// 辅助端点（/models /me 等，CLI 用裸 fetch 调用）只带 authorization +
// x-u1s1-version，不带 UA / X-Stainless-*。AuxHeaders 与之一致。
//
// 本包按「档案」成套输出上述头，保证 UA 与 X-Stainless-* 自洽；
// 档案持久化到 data/fingerprint.json —— 同一部署稳定复用同一身份，
// 避免每次重启都变成"新设备"。可用 FINGERPRINT_PROFILE 强制指定档案。
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

// OpenAI SDK 版本号（u1s1-cli 0.19.5 内嵌 openai 6.40.0）。
const SDKPackageVersion = "6.40.0"

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

// AuxHeaders 构造辅助端点（/models、/me 等）的头 —— 与 CLI 的 api.js 保持一致：
// 仅 authorization + x-u1s1-version。
func AuxHeaders(apiKey, cliVersion string) map[string]string {
	return map[string]string{
		"authorization":  "Bearer " + apiKey,
		"x-u1s1-version": cliVersion,
	}
}
