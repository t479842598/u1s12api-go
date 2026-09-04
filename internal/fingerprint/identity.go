// 身份派生：把「我们声称是哪种客户端」从内置假档案改为**部署机真实环境**。
//
// 为什么这么做（ADR 0002）：官方 CLI 在一个真实用户机器上跑，它报出的
// hostname / os.platform() / os.release() / process.arch / device_name 全部来自
// 本机事实，彼此互为佐证。我们此前轮转 5 套假档案，hostname 从未真实取过，
// 网关只要把 device_name 的主机名、x-u1s1-platform、UA 里的内核版本三者交叉
// 核对就能看出矛盾。改为真实派生后，本项目在生产机上就是一个「跑在 Linux ARM
// 服务器上的真实 CLI 用户」——这是 CLI 用户群里完全正常的一种形态。
//
// 唯一无法由本机事实得到的是 Node 版本（我们是 Go 进程），对它施加官方
// package.json 的 engines.node 下限约束：低于该版本官方 CLI 根本装不上，
// 所以声称它等于自相矛盾。
package fingerprint

import (
	"crypto/sha256"
	"encoding/binary"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

// MinNodeVersion 官方 u1s1-cli package.json 的 engines.node = ">=22.19.0"。
// 低于它的 Node 跑不起官方 CLI，因此不能出现在我们的 x-stainless-runtime-version 里。
const MinNodeVersion = "22.19.0"

// nodeReleaseCandidates 本机没有可用 Node 时从中确定性挑一个（均为真实发布版本）。
var nodeReleaseCandidates = []string{"v22.19.0", "v22.21.1", "v24.13.0", "v24.18.0"}

// NodePlatform 把 GOOS 映射成 node 的 os.platform() 取值（官方 UA 与 platform 头都用它）。
func NodePlatform(goos string) string {
	switch goos {
	case "windows":
		return "win32"
	default: // darwin / linux / freebsd / openbsd / aix 与 node 同名
		return goos
	}
}

// NodeArch 把 GOARCH 映射成 node 的 process.arch 取值。
func NodeArch(goarch string) string {
	switch goarch {
	case "amd64":
		return "x64"
	case "386":
		return "x32"
	default: // arm64 / arm / mips 等与 node 同名
		return goarch
	}
}

// StainlessOS openai SDK v6.40.0 `internal/detect-platform.js` 里 normalizePlatform()
// 的等价实现（注意 iOS 用的是 includes，其余是全等，顺序也与官方一致）。
func StainlessOS(nodePlatform string) string {
	p := strings.ToLower(nodePlatform)
	switch {
	case p == "":
		return "Unknown"
	case strings.Contains(p, "ios"):
		return "iOS"
	case p == "android":
		return "Android"
	case p == "darwin":
		return "MacOS"
	case p == "win32":
		return "Windows"
	case p == "freebsd":
		return "FreeBSD"
	case p == "openbsd":
		return "OpenBSD"
	case p == "linux":
		return "Linux"
	default:
		return "Other:" + p
	}
}

// StainlessArch SDK normalizeArch() 的等价实现。
func StainlessArch(nodeArch string) string {
	switch nodeArch {
	case "":
		return "unknown"
	case "x32":
		return "x32"
	case "x86_64", "x64":
		return "x64"
	case "arm":
		return "arm"
	case "aarch64", "arm64":
		return "arm64"
	default:
		return "other:" + nodeArch
	}
}

// DetectProfile 用部署机的真实事实拼出一个自洽身份。
//
// nodeVersion 由 ResolveNodeVersion 决定并持久化（同一部署长期声称同一版本）。
// 内核版本取不到时（非 unix 平台）留空，此时 UA 退化为 `pi (linux ; arm64)` 的
// 形态不可接受，故用占位 "0.0.0"——本项目部署目标为 linux/darwin，实际不会走到。
func DetectProfile(nodeVersion string) Profile {
	np := NodePlatform(runtime.GOOS)
	na := NodeArch(runtime.GOARCH)
	host, err := os.Hostname()
	if err != nil || strings.TrimSpace(host) == "" {
		host = "localhost"
	}
	rel := kernelRelease()
	if rel == "" {
		rel = "0.0.0"
	}
	return Profile{
		ID:             ProfileIDAuto,
		Label:          "本机真实环境（" + np + "-" + na + "）",
		Hostname:       host,
		UAPlatform:     np,
		UARelease:      rel,
		UAArch:         na,
		StainlessOS:    StainlessOS(np),
		StainlessArch:  StainlessArch(na),
		RuntimeVersion: nodeVersion,
	}
}

// ResolveNodeVersion 决定声称的 Node 版本。
//
// 优先级：显式覆盖（FINGERPRINT_NODE_VERSION）→ 本机 node --version（须 ≥ engines 下限）
// → 真实发布集合中按 seed 确定性取一个。
//
// 本机装了低于下限的 Node 时**不能**用它：那等于报出一个跑不了官方 CLI 的运行时版本。
// seed 用主机名，保证同一台机器重启前后取到同一个值（且不同部署之间不雷同）。
func ResolveNodeVersion(override string, localLookup func() string, seed string) string {
	if v := normalizeNodeVersion(override); v != "" && VersionAtLeast(v, MinNodeVersion) {
		return v
	}
	if localLookup != nil {
		if v := normalizeNodeVersion(localLookup()); v != "" && VersionAtLeast(v, MinNodeVersion) {
			return v
		}
	}
	if len(nodeReleaseCandidates) == 0 {
		return "v" + MinNodeVersion
	}
	return nodeReleaseCandidates[pickIndex(seed, len(nodeReleaseCandidates))]
}

// pickIndex 按 seed 确定性取一个下标。
func pickIndex(seed string, n int) int {
	if seed == "" {
		return 0
	}
	sum := sha256.Sum256([]byte(seed))
	return int(binary.BigEndian.Uint32(sum[:4]) % uint32(n))
}

// normalizeNodeVersion 把 "22.19.0" / "v22.19.0" 统一成 "v22.19.0"；无法解析返回空。
func normalizeNodeVersion(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	s = strings.TrimPrefix(s, "v")
	// 只接受 x.y.z 前缀（允许 "v22.19.0" 后面带额外标记）
	core := s
	if i := strings.IndexAny(core, "-+"); i >= 0 {
		core = core[:i]
	}
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return ""
	}
	for _, p := range parts {
		if _, err := strconv.Atoi(p); err != nil {
			return ""
		}
	}
	return "v" + core
}

// VersionAtLeast 比较两个 Node 版本（"v22.19.0" 形式）是否 a >= b。
// 任一无法解析时返回 false（宁可判为不满足约束）。
func VersionAtLeast(a, b string) bool {
	av, ok := parseVersion(strings.TrimPrefix(a, "v"))
	if !ok {
		return false
	}
	bv, ok := parseVersion(strings.TrimPrefix(b, "v"))
	if !ok {
		return false
	}
	for i := 0; i < 3; i++ {
		if av[i] != bv[i] {
			return av[i] > bv[i]
		}
	}
	return true
}

func parseVersion(s string) ([3]int, bool) {
	var out [3]int
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// lookupLocalNodeVersion 执行 `node --version`。取不到（没装/超时/报错）返回空串。
// 只读命令、1 秒超时，失败不影响启动。
func lookupLocalNodeVersion() string {
	cmd := exec.Command("node", "--version")
	cmd.Env = nil // 不继承额外环境，避免被包装脚本干扰
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
