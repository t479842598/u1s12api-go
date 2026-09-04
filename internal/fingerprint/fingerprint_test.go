package fingerprint

import (
	"regexp"
	"strings"
	"testing"
)

// TestChatHeadersShape 校验 chat 请求指纹头与官方 CLI 一致（逆向自 u1s1-cli 1.2.3）。
func TestChatHeadersShape(t *testing.T) {
	m, err := NewManager(t.TempDir()+"/fp.json", "macos-arm64")
	if err != nil {
		t.Fatal(err)
	}
	h := m.ChatHeaders("u1s1-testkey", "1.2.3")

	required := []string{
		"authorization",
		"user-agent",
		"x-u1s1-version",
		"X-Stainless-Lang",
		"X-Stainless-Package-Version",
		"X-Stainless-OS",
		"X-Stainless-Arch",
		"X-Stainless-Runtime",
		"X-Stainless-Runtime-Version",
		"X-Stainless-Retry-Count",
	}
	for _, k := range required {
		if v, ok := h[k]; !ok || strings.TrimSpace(v) == "" {
			t.Errorf("缺少指纹头 %q", k)
		}
	}
	if h["authorization"] != "Bearer u1s1-testkey" {
		t.Errorf("authorization = %q, 期望 Bearer 形式", h["authorization"])
	}
	if h["x-u1s1-version"] != "1.2.3" {
		t.Errorf("x-u1s1-version = %q", h["x-u1s1-version"])
	}
	// UA 格式: pi ({platform} {release}; {arch})
	re := regexp.MustCompile(`^pi \([a-z32]+ [0-9.]+(-[a-z0-9]+)?; (arm64|x64)\)$`)
	if !re.MatchString(h["user-agent"]) {
		t.Errorf("user-agent = %q 不符合官方格式", h["user-agent"])
	}
}

// TestProfileConsistency UA 与 X-Stainless-* 必须自洽（同档案）。
func TestProfileConsistency(t *testing.T) {
	for _, p := range Profiles {
		if !strings.Contains(UserAgent(p), p.UAArch) {
			t.Errorf("%s: UA 缺少 arch %s", p.ID, p.UAArch)
		}
		switch p.UAPlatform {
		case "darwin":
			if p.StainlessOS != "MacOS" {
				t.Errorf("%s: darwin 应映射 MacOS，得到 %s", p.ID, p.StainlessOS)
			}
		case "linux":
			if p.StainlessOS != "Linux" {
				t.Errorf("%s: linux 应映射 Linux，得到 %s", p.ID, p.StainlessOS)
			}
		case "win32":
			if p.StainlessOS != "Windows" {
				t.Errorf("%s: win32 应映射 Windows，得到 %s", p.ID, p.StainlessOS)
			}
		default:
			t.Errorf("%s: 未知 platform %s", p.ID, p.UAPlatform)
		}
		if p.RuntimeVersion < "v22.19" {
			t.Errorf("%s: Node 版本 %s 低于便携包最低要求 v22.19", p.ID, p.RuntimeVersion)
		}
	}
}

// TestManagerPersistence 档案跨重启保持稳定。
func TestManagerPersistence(t *testing.T) {
	path := t.TempDir() + "/fp.json"
	m1, err := NewManager(path, "")
	if err != nil {
		t.Fatal(err)
	}
	first := m1.Current().ID
	m2, err := NewManager(path, "")
	if err != nil {
		t.Fatal(err)
	}
	if m2.Current().ID != first {
		t.Errorf("档案未持久化: 第一次=%s 第二次=%s", first, m2.Current().ID)
	}
	// 强制切换
	m3, _ := NewManager(path, "windows-x64")
	if m3.Current().ID != "windows-x64" {
		t.Errorf("强制指定档案失败: %s", m3.Current().ID)
	}
}

func TestAuxHeaders(t *testing.T) {
	h := AuxHeaders("u1s1-k", "1.2.3")
	// 官方 fetchModels/fetchMe 只显式设 x-u1s1-version，authorization 由 authorizedFetch 补，
	// UA 是裸 fetch 的运行时默认值（桌面端 undici.install 后→ undici）。
	want := map[string]string{
		"authorization":  "Bearer u1s1-k",
		"x-u1s1-version": "1.2.3",
		"user-agent":     "undici",
	}
	if len(h) != len(want) {
		t.Errorf("辅助端点头数 = %d, 期望 %d (%v)", len(h), len(want), h)
	}
	for k, v := range want {
		if h[k] != v {
			t.Errorf("辅助头 %s = %q, 期望 %q", k, h[k], v)
		}
	}
	// 辅助端点不发 X-Stainless-*（只属于 SDK 发的 chat 请求）。
	for k := range h {
		if strings.HasPrefix(k, "X-Stainless") {
			t.Errorf("辅助端点不应带 %s", k)
		}
	}
}

// TestClientSurfaceIsDesktop 对齐桌面客户端：ensureSigningProxy(cfg, "desktop", ...)。
// CLI TUI 才发 terminal（v0.9.6 及之前我们发的是 terminal）。
func TestClientSurfaceIsDesktop(t *testing.T) {
	if ClientSurface != "desktop" {
		t.Errorf("ClientSurface = %q, 期望 desktop", ClientSurface)
	}
}

// TestUndiciUserAgent 辅助端点 UA 必须是 undici，不能退化成 Go-http-client/1.1。
func TestUndiciUserAgent(t *testing.T) {
	if UndiciUserAgent != "undici" {
		t.Errorf("UndiciUserAgent = %q, 期望 undici", UndiciUserAgent)
	}
}

// TestChatHeadersNoGoDefaultUA chat 指纹头必须自带 UA（pi (...)），
// 任何路径都不能让 Go 的默认 UA 泄到上游。
func TestChatHeadersNoGoDefaultUA(t *testing.T) {
	m, err := NewManager(t.TempDir()+"/fp.json", "linux-x64")
	if err != nil {
		t.Fatal(err)
	}
	h := m.ChatHeaders("u1s1-k", "1.5.0")
	if !strings.HasPrefix(h["user-agent"], "pi (") {
		t.Errorf("chat UA = %q, 期望 pi (...)", h["user-agent"])
	}
	if h["x-u1s1-version"] != "1.5.0" {
		t.Errorf("x-u1s1-version = %q", h["x-u1s1-version"])
	}
}
