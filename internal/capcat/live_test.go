package capcat

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestLiveSolve 真实调用 capcat 验证求解器。默认跳过；设置 CAPCAT_LIVE=1 时运行。
// 代理经 CAPCAT_PROXY 指定（为空则直连）。
func TestLiveSolve(t *testing.T) {
	if os.Getenv("CAPCAT_LIVE") != "1" {
		t.Skip("设置 CAPCAT_LIVE=1 运行真实求解验证")
	}
	proxy := os.Getenv("CAPCAT_PROXY")
	s, err := New(proxy)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	start := time.Now()
	tok, err := s.Solve(ctx)
	if err != nil {
		t.Fatalf("Solve: %v", err)
	}
	t.Logf("cap-token=%q 耗时=%s", tok, time.Since(start))
	if len(tok) < 10 {
		t.Fatalf("token 过短: %q", tok)
	}
}
