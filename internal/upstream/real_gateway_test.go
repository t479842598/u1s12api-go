package upstream

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/t479842598/u1s12api-go/internal/config"
	"github.com/t479842598/u1s12api-go/internal/fingerprint"
)

// lookupNodeForTest 本机 node --version（活体测试里给身份用）。
func lookupNodeForTest() string {
	out, err := exec.Command("node", "--version").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// 真实网关集成检查（**默认跳过**，不参与常规测试）。
//
// 用途：每次同步官方 u1s1-cli 版本后，用它确认本项目对真实上游的指纹/凭证仍然有效，
// 不必临时写探针。凭证从环境变量注入，绝不落仓库；只读地调 /v1/models，
// 只有显式设 U1S1_REAL_CHAT=1 才会额外发一次真实 chat（会消耗额度）。
//
//	从生产库取凭证（欧洲 VPS）：
//	  sqlite3 -separator '||' /opt/u1s12api/data/u1s12api.db \
//	    "select device_token,device_private_jwk,device_public_jwk from accounts where device_id='655';"
//	本地执行：
//	  U1S1_DEV_TOKEN=... U1S1_DEV_PRIV='{"kty":...}' U1S1_DEV_PUB='{"kty":...}' \
//	  U1S1_PROXY=http://127.0.0.1:7897 U1S1_REAL_CHAT=1 \
//	  go test ./internal/upstream/ -run TestRealGateway -v
//
// 可选：U1S1_EXPECT_VERSION 覆盖版本（默认取 config.DefaultU1S1Version）；
// U1S1_FAKE_PROFILE=macos-arm64 改用伪装档案（默认用本机真实环境，与线上一致）。
func TestRealGatewayAttestation(t *testing.T) {
	tok := os.Getenv("U1S1_DEV_TOKEN")
	priv := os.Getenv("U1S1_DEV_PRIV")
	pub := os.Getenv("U1S1_DEV_PUB")
	if tok == "" || priv == "" || pub == "" {
		t.Skip("未设置 U1S1_DEV_TOKEN/U1S1_DEV_PRIV/U1S1_DEV_PUB，跳过真实网关检查")
	}
	pj, err := parseJWK(priv)
	if err != nil {
		t.Fatalf("私钥 JWK 解析失败: %v", err)
	}
	uj, err := parseJWK(pub)
	if err != nil {
		t.Fatalf("公钥 JWK 解析失败: %v", err)
	}
	// 身份必须给全：chat 的 UA / x-u1s1-platform / X-Stainless-* 全部由它派生，
	// 留零值会发出 "pi ( ; )" 这种空指纹，让活体测试假失败。
	// 默认用部署机真实环境（与线上一致）；U1S1_FAKE_PROFILE=macos-arm64 可改走伪装档案。
	ident := fingerprint.DetectProfile(fingerprint.ResolveNodeVersion(os.Getenv("U1S1_NODE_VERSION"), lookupNodeForTest, tok[:12]))
	if fp := os.Getenv("U1S1_FAKE_PROFILE"); fp != "" {
		if p2, ok := fingerprint.ProfileByID(fp); ok {
			ident = p2
		}
	}
	cred := &DeviceCredential{DeviceToken: tok, PrivateJWK: pj, PublicJWK: uj, Profile: ident}

	version := os.Getenv("U1S1_EXPECT_VERSION")
	if version == "" {
		version = config.DefaultU1S1Version
	}
	dc := NewDeviceClient("https://api.u1s1.io/v1", os.Getenv("U1S1_PROXY"),
		func() string { return version }, func() fingerprint.Profile { return ident })
	m := NewAttestationManager(func() *DeviceClient { return dc })

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// 1) 签发：真实网关应下发 x-u1s1-attestation 令牌。
	t0 := time.Now()
	got := m.Token(ctx, cred)
	if got == "" {
		t.Fatal("未取到 attestation 令牌（网关未签发或请求失败）")
	}
	t.Logf("✅ 令牌已签发 len=%d 首次耗时=%s", len(got), time.Since(t0).Round(time.Millisecond))

	// 2) 结构：payload 必须带 user/device 绑定与 7 天 TTL。
	p, ok := decodeAttestationPayload(got)
	if !ok {
		t.Fatalf("令牌 payload 解析失败: %.40s…", got)
	}
	if p.U == 0 || p.D == 0 {
		t.Errorf("payload 缺少 user/device 绑定: v=%d u=%d d=%d", p.V, p.U, p.D)
	}
	left := time.Until(time.Unix(p.Exp, 0))
	if left < 6*24*time.Hour || left > 8*24*time.Hour {
		t.Errorf("TTL 异常：剩余 %s（期望约 7 天）", left)
	}
	t.Logf("✅ payload v=%d u=%d d=%d exp=%s，TTL 剩余 %s", p.V, p.U, p.D,
		time.Unix(p.Exp, 0).Format(time.RFC3339), left.Round(time.Minute))

	// 3) 缓存：第二次必须命中缓存（0 网络往返）。
	t1 := time.Now()
	got2 := m.Token(ctx, cred)
	elapsed := time.Since(t1)
	if got2 != got {
		t.Errorf("缓存未命中：两次令牌不一致")
	}
	if elapsed > 100*time.Millisecond {
		t.Errorf("第二次耗时 %s，疑似未命中缓存", elapsed)
	}
	t.Logf("✅ 缓存命中（第二次耗时 %s）", elapsed.Round(time.Microsecond))

	// 4) 可选真实 chat：确认带该头的请求不被网关以指纹/证明理由拒绝。
	if os.Getenv("U1S1_REAL_CHAT") != "1" {
		t.Log("未设 U1S1_REAL_CHAT=1，跳过真实 chat（避免消耗额度）")
		return
	}
	resp, err := dc.DeviceChat(ctx, cred,
		[]byte(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}],"max_tokens":4}`), got)
	if err != nil {
		var ae *APIError
		if errors.As(err, &ae) {
			if ae.StatusCode == 401 || ae.StatusCode == 403 {
				t.Errorf("带真实 attestation 仍被拒绝（status=%d）: %.200s", ae.StatusCode, ae.Body)
			} else {
				t.Logf("✅ 网关未以指纹/证明理由拒绝（status=%d, %.120s）", ae.StatusCode, ae.Body)
			}
			return
		}
		t.Fatalf("DeviceChat 网络错误: %v", err)
	}
	defer resp.Body.Close()
	t.Logf("✅ 真实 chat 成功 status=%d", resp.StatusCode)
}
