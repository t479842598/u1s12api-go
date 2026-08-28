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
	if len(h) != 2 {
		t.Errorf("辅助端点应只有 authorization + x-u1s1-version，得到 %d 个头", len(h))
	}
}
