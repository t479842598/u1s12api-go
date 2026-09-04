package fingerprint

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// ---------- chat / aux 头集合（对齐 CLI 1.7.1 抓包） ----------

// TestChatHeadersShape chat 指纹头必须含官方全部小写头名，且值非空。
// 键名大小写是本轮修复重点：Go 的 Header.Set 会规范化成 X-Stainless-Lang，
// 而 undici 全小写 —— 这里断言的是**产出层**（wire 层另有测试）。
func TestChatHeadersShape(t *testing.T) {
	p := DetectProfile("v22.21.1")
	h := ChatFingerprintHeaders(p, "1.7.1", "ATT")

	required := []string{
		"connection", "accept", "accept-language", "sec-fetch-mode", "accept-encoding",
		"user-agent", "x-u1s1-version", "x-u1s1-client", "x-u1s1-platform",
		"x-u1s1-attestation",
		"x-stainless-lang", "x-stainless-package-version", "x-stainless-os",
		"x-stainless-arch", "x-stainless-runtime", "x-stainless-runtime-version",
		"x-stainless-retry-count",
	}
	for _, k := range required {
		if v, ok := h[k]; !ok || strings.TrimSpace(v) == "" {
			t.Errorf("chat 头 %s 缺失或为空（全部键：%v）", k, keysOf(h))
		}
	}
	// 官方全部小写：出现任何大写键都说明我们又在用规范化键了。
	for k := range h {
		if k != strings.ToLower(k) {
			t.Errorf("chat 头键应为小写线格式，得到 %q", k)
		}
	}
	if h["accept"] != "application/json" {
		t.Errorf("chat accept = %q, 期望 application/json", h["accept"])
	}
	if h["accept-language"] != "*" || h["sec-fetch-mode"] != "cors" {
		t.Errorf("accept-language/sec-fetch-mode = %q/%q, 期望 */cors", h["accept-language"], h["sec-fetch-mode"])
	}
	if h["accept-encoding"] != "gzip, deflate" {
		t.Errorf("accept-encoding = %q, 期望 gzip, deflate", h["accept-encoding"])
	}
	if h["x-stainless-package-version"] != SDKPackageVersion {
		t.Errorf("x-stainless-package-version = %q", h["x-stainless-package-version"])
	}
}

// TestChatHeadersOmitAttestation 无令牌时不发该头（官方同样不发），也不能出现空值头。
func TestChatHeadersOmitAttestation(t *testing.T) {
	h := ChatFingerprintHeaders(DetectProfile("v22.21.1"), "1.7.1", "")
	if _, ok := h["x-u1s1-attestation"]; ok {
		t.Error("attestation 为空时不应发 x-u1s1-attestation")
	}
}

// TestAuxHeadersShape 辅助端点：裸 fetch 集合，**不带** X-Stainless-*。
func TestAuxHeadersShape(t *testing.T) {
	h := AuxHeaders("u1s1-k", "1.7.1", NodeUserAgent)
	want := map[string]string{
		"authorization":   "Bearer u1s1-k",
		"x-u1s1-version":  "1.7.1",
		"user-agent":      "node",
		"accept":          "*/*",
		"accept-language": "*",
		"sec-fetch-mode":  "cors",
		"accept-encoding": "gzip, deflate",
		"connection":      "keep-alive", // undici 在 h1 上显式发，Go 默认不发 —— 用小写键补上
	}
	if len(h) != len(want) {
		t.Errorf("辅助头数 = %d, 期望 %d (%v)", len(h), len(want), keysOf(h))
	}
	for k, v := range want {
		if h[k] != v {
			t.Errorf("辅助头 %s = %q, 期望 %q", k, h[k], v)
		}
	}
	for k := range h {
		if strings.HasPrefix(k, "x-stainless") {
			t.Errorf("辅助端点不应带 %s", k)
		}
	}
}

// TestAuxHeadersNoAPIKeyOmitsAuthorization 设备凭证通道由 DPoP 提供 authorization，
// AuxHeaders 不应再塞一个 Bearer 覆盖它。
func TestAuxHeadersNoAPIKeyOmitsAuthorization(t *testing.T) {
	h := AuxHeaders("", "1.7.1", UndiciUserAgent)
	if _, ok := h["authorization"]; ok {
		t.Error("apiKey 为空时不应发 authorization")
	}
	if h["user-agent"] != "undici" {
		t.Errorf("user-agent = %q, 期望 undici", h["user-agent"])
	}
}

// TestClientSurfaceIsTerminal 对齐官方 CLI（terminal），不再是桌面端 desktop。
// 依据：桌面端 0.1.15 仍内嵌 CLI 1.3.0，desktop+新版本是现实中不存在的组合（ADR 0001）。
func TestClientSurfaceIsTerminal(t *testing.T) {
	if ClientSurface != "terminal" {
		t.Errorf("ClientSurface = %q, 期望 terminal", ClientSurface)
	}
}

// TestUserAgentConstants CLI 启动阶段裸 fetch 发 node，装了 dispatcher 后发 undici；
// 两者都必须显式发，绝不能让 Go 的 Go-http-client/1.1 泄到上游。
func TestUserAgentConstants(t *testing.T) {
	if NodeUserAgent != "node" {
		t.Errorf("NodeUserAgent = %q", NodeUserAgent)
	}
	if UndiciUserAgent != "undici" {
		t.Errorf("UndiciUserAgent = %q", UndiciUserAgent)
	}
}

// ---------- 身份派生 ----------

var deviceNameRe = regexp.MustCompile(`^.+ \((darwin|linux|win32|freebsd|openbsd|netbsd|sunos|android|aix)\)$`)

// TestAutoIdentitySelfConsistent hostname/platform/内核/arch/UA/stainless/device_name 必须同源。
// 这是 ADR 0002 的全部意义：网关交叉核对任何两项都对得上。
func TestAutoIdentitySelfConsistent(t *testing.T) {
	p := DetectProfile("v22.21.1")
	if p.Hostname == "" {
		t.Fatal("真实主机名不能为空")
	}
	if p.UAPlatform != NodePlatform(runtime.GOOS) {
		t.Errorf("UAPlatform = %q, 期望 %q", p.UAPlatform, NodePlatform(runtime.GOOS))
	}
	if p.UAArch != NodeArch(runtime.GOARCH) {
		t.Errorf("UAArch = %q, 期望 %q", p.UAArch, NodeArch(runtime.GOARCH))
	}
	if p.StainlessOS != StainlessOS(p.UAPlatform) {
		t.Errorf("StainlessOS = %q, 与 platform %q 不自洽", p.StainlessOS, p.UAPlatform)
	}
	if p.StainlessArch != StainlessArch(p.UAArch) {
		t.Errorf("StainlessArch = %q, 与 arch %q 不自洽", p.StainlessArch, p.UAArch)
	}
	// UA 里必须同时出现 platform、内核版本、arch 三项。
	ua := UserAgent(p)
	for _, want := range []string{p.UAPlatform, p.UAArch, p.UARelease} {
		if !strings.Contains(ua, want) {
			t.Errorf("UA %q 不含 %q", ua, want)
		}
	}
	// x-u1s1-platform 与 UA 同源。
	if got := ClientPlatform(p); got != p.UAPlatform+"-"+p.UAArch {
		t.Errorf("ClientPlatform = %q", got)
	}
	// device_name 用官方格式，且平台段与 x-u1s1-platform 的 platform 部分一致。
	dn := DeviceName(p)
	if !deviceNameRe.MatchString(dn) {
		t.Errorf("device_name %q 不符合官方格式 `<hostname> (<platform>)`", dn)
	}
	if !strings.HasSuffix(dn, "("+p.UAPlatform+")") {
		t.Errorf("device_name %q 的平台段与 x-u1s1-platform 不一致", dn)
	}
	if !strings.HasPrefix(dn, p.Hostname+" ") {
		t.Errorf("device_name %q 的主机名与 os.Hostname() 不一致", dn)
	}
}

// TestDeviceNameNoProjectIdentity device_name 绝不能带本项目标识或邮箱 ——
// 它会永久落在网关设备记录里并显示在用户官网设备页上。
func TestDeviceNameNoProjectIdentity(t *testing.T) {
	for _, p := range append([]Profile{DetectProfile("v22.21.1")}, Profiles...) {
		dn := DeviceName(p)
		lower := strings.ToLower(dn)
		for _, bad := range []string{"u1s12api", "@", "gmail", "oneclick"} {
			if strings.Contains(lower, bad) {
				t.Errorf("device_name %q 含敏感/自证标识 %q", dn, bad)
			}
		}
		if !deviceNameRe.MatchString(dn) {
			t.Errorf("档案 %s 的 device_name %q 不符合官方格式", p.ID, dn)
		}
	}
}

// TestStainlessMappings openai SDK v6.40.0 detect-platform.js 的映射表逐项核对。
func TestStainlessMappings(t *testing.T) {
	cases := []struct{ in, want string }{
		{"darwin", "MacOS"}, {"linux", "Linux"}, {"win32", "Windows"},
		{"freebsd", "FreeBSD"}, {"openbsd", "OpenBSD"}, {"android", "Android"},
		{"sunos", "Other:sunos"}, {"", "Unknown"}, {"plan9", "Other:plan9"},
	}
	for _, c := range cases {
		if got := StainlessOS(c.in); got != c.want {
			t.Errorf("StainlessOS(%q) = %q, 期望 %q", c.in, got, c.want)
		}
	}
	archCases := []struct{ in, want string }{
		{"x64", "x64"}, {"x86_64", "x64"}, {"x32", "x32"}, {"arm", "arm"},
		{"arm64", "arm64"}, {"aarch64", "arm64"}, {"", "unknown"}, {"riscv", "other:riscv"},
	}
	for _, c := range archCases {
		if got := StainlessArch(c.in); got != c.want {
			t.Errorf("StainlessArch(%q) = %q, 期望 %q", c.in, got, c.want)
		}
	}
	if NodePlatform("windows") != "win32" || NodeArch("amd64") != "x64" {
		t.Error("GOOS/GOARCH → node 取值映射错误")
	}
}

// TestNodeVersionRespectsEnginesFloor 官方 CLI engines 是 >=22.19.0：
// 低于它的 Node 跑不起官方 CLI，声称它等于自相矛盾，必须弃用。
func TestNodeVersionRespectsEnginesFloor(t *testing.T) {
	low := func() string { return "v18.20.0" }
	got := ResolveNodeVersion("", low, "seed-host")
	if !VersionAtLeast(got, MinNodeVersion) {
		t.Errorf("本机 Node 低于 engines 下限时得到 %q，仍不满足 >=%s", got, MinNodeVersion)
	}
	// 本机合规时优先用本机值。
	high := func() string { return "v24.18.0" }
	if got := ResolveNodeVersion("", high, "seed-host"); got != "v24.18.0" {
		t.Errorf("本机 Node 合规时应沿用，得到 %q", got)
	}
	// 显式覆盖优先，但同样要过下限。
	if got := ResolveNodeVersion("v22.19.0", high, "seed-host"); got != "v22.19.0" {
		t.Errorf("显式覆盖未生效，得到 %q", got)
	}
	if got := ResolveNodeVersion("v16.0.0", high, "seed-host"); got != "v24.18.0" {
		t.Errorf("覆盖值低于下限时应回退本机，得到 %q", got)
	}
	// 无本机 Node：按 seed 确定性取，且同 seed 结果稳定。
	a := ResolveNodeVersion("", func() string { return "" }, "host-a")
	b := ResolveNodeVersion("", func() string { return "" }, "host-a")
	if a != b {
		t.Errorf("同一 seed 两次结果不同：%q vs %q", a, b)
	}
	if !VersionAtLeast(a, MinNodeVersion) {
		t.Errorf("候选集合里出现了低于下限的版本 %q", a)
	}
}

func TestNormalizeNodeVersion(t *testing.T) {
	for in, want := range map[string]string{
		"v22.19.0": "v22.19.0", "22.19.0": "v22.19.0", "v22.19.0-nightly": "v22.19.0",
		"v22.19": "", "abc": "", "": "",
	} {
		if got := normalizeNodeVersion(in); got != want {
			t.Errorf("normalizeNodeVersion(%q) = %q, 期望 %q", in, got, want)
		}
	}
}

// ---------- Manager：默认 auto、迁移、Node 版本稳定 ----------

// TestManagerDefaultsToAuto 无状态文件时身份来自真实主机，而不是从 Profiles 里随机挑。
func TestManagerDefaultsToAuto(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fp.json")
	m, err := NewManager(path, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !m.IsAuto() {
		t.Error("默认应为 auto（真实主机派生）")
	}
	if m.Current().ID != ProfileIDAuto {
		t.Errorf("Current().ID = %q, 期望 auto", m.Current().ID)
	}
	if m.Current().Hostname == "" {
		t.Error("auto 身份必须带真实主机名")
	}
}

// TestManagerLegacyStatePreserved 旧版会随机挑一个假档案并持久化其 id。
// 那个 id 就是已授权设备一直在用的身份 —— 升级后必须**原样沿用**：
// 同一台 device_token 突然换个操作系统是真实设备不会有的形态，而且只能靠
// 重新授权抹平，等于把升级代价转嫁给用户。
func TestManagerLegacyStatePreserved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fp.json")
	// 生产机当前的真实内容（v0.9.9 写的，无 schema 字段）
	if err := os.WriteFile(path, []byte(`{"profile_id":"macos-x64"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := NewManager(path, "auto", "")
	if err != nil {
		t.Fatal(err)
	}
	if m.Current().ID != "macos-x64" {
		t.Errorf("升级后身份变了：%q，期望沿用 macos-x64（已授权设备会跳 platform）", m.Current().ID)
	}
	if m.IsAuto() {
		t.Error("沿用既有档案时 IsAuto 应为 false")
	}
	// 对外可见的头值必须与升级前逐项一致
	want := Profile{ID: "macos-x64", UAPlatform: "darwin", UARelease: "24.6.0", UAArch: "x64",
		StainlessOS: "MacOS", StainlessArch: "x64", RuntimeVersion: "v22.19.0"}
	got := m.Current()
	if got.UAPlatform != want.UAPlatform || got.UAArch != want.UAArch || got.UARelease != want.UARelease ||
		got.StainlessOS != want.StainlessOS || got.StainlessArch != want.StainlessArch ||
		got.RuntimeVersion != want.RuntimeVersion {
		t.Errorf("升级前后头值不一致：\n got=%+v\nwant=%+v", got, want)
	}
	if UserAgent(got) != "pi (darwin 24.6.0; x64)" {
		t.Errorf("UA 变了：%q", UserAgent(got))
	}
	if ClientPlatform(got) != "darwin-x64" {
		t.Errorf("x-u1s1-platform 变了：%q", ClientPlatform(got))
	}
	// 补写 schema 后 profile_id 不得变
	st, err := loadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.ProfileID != "macos-x64" || st.Schema != stateSchema {
		t.Errorf("持久化状态不符：%+v", st)
	}
	// 再重启一次仍然沿用（幂等）
	m2, _ := NewManager(path, "", "")
	if m2.Current().ID != "macos-x64" {
		t.Errorf("二次启动身份漂移：%q", m2.Current().ID)
	}
}

// TestManagerFreshInstallUsesAuto 只有全新安装（无状态文件）才走真实主机派生。
func TestManagerFreshInstallUsesAuto(t *testing.T) {
	m, err := NewManager(filepath.Join(t.TempDir(), "fp.json"), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !m.IsAuto() || m.Current().ID != ProfileIDAuto {
		t.Errorf("全新安装应为 auto：%+v", m.Current())
	}
}

// TestManagerDeletedProfileFallsBackToAuto 档案 id 被删时落回 auto，绝不随机再挑一个。
func TestManagerDeletedProfileFallsBackToAuto(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fp.json")
	if err := os.WriteFile(path, []byte(`{"profile_id":"不存在的档案"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := NewManager(path, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if m.Current().ID != ProfileIDAuto {
		t.Errorf("未知档案应落回 auto，得到 %q", m.Current().ID)
	}
}

// TestManagerExplicitOverrideHonored FINGERPRINT_PROFILE 指定假档案时按伪装走（逃生口）。
func TestManagerExplicitOverrideHonored(t *testing.T) {
	m, err := NewManager(filepath.Join(t.TempDir(), "fp.json"), "windows-x64", "")
	if err != nil {
		t.Fatal(err)
	}
	if m.IsAuto() || m.Current().ID != "windows-x64" {
		t.Errorf("显式覆盖未生效: %+v", m.Current())
	}
	if !strings.HasSuffix(DeviceName(m.Current()), "(win32)") {
		t.Errorf("伪装档案的 device_name 平台段不自洽: %q", DeviceName(m.Current()))
	}
}

// TestManagerNodeVersionStableAcrossRestarts 声称的 Node 版本必须跨重启稳定，
// 否则同一设备每次重启就换一个运行时版本 —— 那也是真实设备不会有的形态。
func TestManagerNodeVersionStableAcrossRestarts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fp.json")
	m1, err := NewManager(path, "", "v22.19.0")
	if err != nil {
		t.Fatal(err)
	}
	first := m1.Current().RuntimeVersion
	m2, err := NewManager(path, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if m2.Current().RuntimeVersion != first {
		t.Errorf("重启后 Node 版本漂移：%q → %q", first, m2.Current().RuntimeVersion)
	}
}

// TestSetProfileBackToAuto 后台热切到假档案再切回 auto。
func TestSetProfileBackToAuto(t *testing.T) {
	m, err := NewManager(filepath.Join(t.TempDir(), "fp.json"), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.SetProfile("macos-arm64"); err != nil {
		t.Fatal(err)
	}
	if m.IsAuto() {
		t.Error("切到假档案后 IsAuto 应为 false")
	}
	if err := m.SetProfile(ProfileIDAuto); err != nil {
		t.Fatal(err)
	}
	if !m.IsAuto() {
		t.Error("切回 auto 失败")
	}
	if err := m.SetProfile("不存在的档案"); err == nil {
		t.Error("未知档案 id 应报错")
	}
}

// TestProfileJSONRoundTrip 身份快照要能原样入库/出库（accounts.device_identity）。
func TestProfileJSONRoundTrip(t *testing.T) {
	p := DetectProfile("v22.21.1")
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var got Profile
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got != p {
		t.Errorf("身份快照序列化往返不一致：%+v vs %+v", got, p)
	}
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
