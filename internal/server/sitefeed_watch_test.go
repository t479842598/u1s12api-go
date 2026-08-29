package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/t479842598/u1s12api-go/internal/sitefeed"
)

// siteFeedFixture 起公告/更新记录/npm 三个 mock 上游并注入 barkPushFn。
// 响应体运行期可变（httptest.Server 启动后改 Config.Handler 不生效，必须用闭包读变量）。
type siteFeedFixture struct {
	fx      *fixture
	annBody atomicString
	clBody  atomicString
	pushes  []barkCall
	mu      sync.Mutex
}

type barkCall struct{ key, title, body, url string }

// atomicString 简单的互斥保护字符串（响应体热替换用）。
type atomicString struct {
	mu sync.Mutex
	v  string
}

func (a *atomicString) set(v string) { a.mu.Lock(); a.v = v; a.mu.Unlock() }
func (a *atomicString) get() string  { a.mu.Lock(); defer a.mu.Unlock(); return a.v }

func (f *siteFeedFixture) pushCalls() []barkCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]barkCall(nil), f.pushes...)
}

// newSiteFeedFixture annBody/changelogBody 为各上游响应体；npmVer 为 npm latest 版本号。
func newSiteFeedFixture(t *testing.T, annBody, changelogBody, npmVer string) *siteFeedFixture {
	t.Helper()
	f := &siteFeedFixture{fx: setupTest(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })}
	f.annBody.set(annBody)
	f.clBody.set(changelogBody)

	annTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(f.annBody.get()))
	}))
	clTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(f.clBody.get()))
	}))
	npmTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"version":"` + npmVer + `"}`))
	}))
	t.Cleanup(func() { annTS.Close(); clTS.Close(); npmTS.Close() })

	oldAnn, oldCL, oldNpm := sitefeed.AnnouncementsURL, sitefeed.ChangelogURL, sitefeed.NpmLatestURL
	sitefeed.AnnouncementsURL = annTS.URL + "/public/announcements?limit=100"
	sitefeed.ChangelogURL = clTS.URL + "/guides/changelog"
	sitefeed.NpmLatestURL = npmTS.URL + "/latest"
	t.Cleanup(func() {
		sitefeed.AnnouncementsURL, sitefeed.ChangelogURL, sitefeed.NpmLatestURL = oldAnn, oldCL, oldNpm
	})

	// 推送需要已配置 Bark key；注入记录型 barkPushFn
	f.fx.srv.getSettings().BarkKey = "test-bark-key"
	oldPush := barkPushFn
	barkPushFn = func(key, title, body, url string) (bool, error) {
		f.mu.Lock()
		f.pushes = append(f.pushes, barkCall{key, title, body, url})
		f.mu.Unlock()
		return true, nil
	}
	t.Cleanup(func() { barkPushFn = oldPush })
	return f
}

const twoAnnJSON = `{"announcements":[
	{"id":2,"title":"公告B","body":"b","url":"","published_at":"2026-08-28 10:00:00"},
	{"id":1,"title":"公告A","body":"a","url":"/pricing","published_at":"2026-08-27 09:00:00"}
]}`

const threeAnnJSON = `{"announcements":[
	{"id":3,"title":"公告C","body":"c","url":"","published_at":"2026-08-29 10:00:00"},
	{"id":2,"title":"公告B","body":"b","url":"","published_at":"2026-08-28 10:00:00"},
	{"id":1,"title":"公告A","body":"a","url":"/pricing","published_at":"2026-08-27 09:00:00"}
]}`

const sampleCL = `<html><body><main><h1>更新记录</h1>
<h2>v1.2.3</h2><h3>修复</h3><ul><li>修复登录</li></ul></main></body></html>`

const sampleCLNew = `<html><body><main><h1>更新记录</h1>
<h2>v1.2.4</h2><h3>修复（CLI）</h3><ul><li>修复超时</li></ul>
<h2>v1.2.3</h2><h3>修复</h3><ul><li>修复登录</li></ul></main></body></html>`

func TestCheckSiteFeedFirstSnapshotNoPush(t *testing.T) {
	f := newSiteFeedFixture(t, twoAnnJSON, sampleCL, "1.2.3")

	res := f.fx.srv.checkSiteFeedAndNotify(context.Background())
	if res.Error != "" {
		t.Fatalf("检查出错: %s", res.Error)
	}
	if len(res.NewAnnouncements) != 0 || len(res.NewChangelog) != 0 {
		t.Fatalf("首次快照不应产生待推送新条目: %+v", res)
	}
	if len(f.pushCalls()) != 0 {
		t.Fatalf("首次快照不应推送: %+v", f.pushCalls())
	}
	anns, _ := f.fx.srv.store.ListSitePosts("announcement", 10)
	cls, _ := f.fx.srv.store.ListSitePosts("changelog", 10)
	if len(anns) != 2 || len(cls) != 1 {
		t.Fatalf("快照入库数量不符: ann=%d cl=%d", len(anns), len(cls))
	}
}

func TestCheckSiteFeedNotifyOnNewEntries(t *testing.T) {
	f := newSiteFeedFixture(t, twoAnnJSON, sampleCL, "1.2.3")
	f.fx.srv.checkSiteFeedAndNotify(context.Background()) // 建快照

	// 上游各加一条：公告 id=3、更新记录 v1.2.4
	f.annBody.set(threeAnnJSON)
	f.clBody.set(sampleCLNew)

	res := f.fx.srv.checkSiteFeedAndNotify(context.Background())
	if len(res.NewAnnouncements) != 1 || res.NewAnnouncements[0] != "公告C" {
		t.Fatalf("应发现 1 条新公告: %+v", res.NewAnnouncements)
	}
	if len(res.NewChangelog) != 1 || res.NewChangelog[0] != "v1.2.4" {
		t.Fatalf("应发现 1 条新更新记录: %+v", res.NewChangelog)
	}
	calls := f.pushCalls()
	if len(calls) != 2 {
		t.Fatalf("应合并推送 2 条（公告 1 + 更新记录 1）: %+v", calls)
	}
	if !strings.Contains(calls[0].title, "公告更新 1 条") || !strings.Contains(calls[0].body, "公告C") {
		t.Fatalf("公告推送文案不符: %+v", calls[0])
	}
	if !strings.Contains(calls[1].title, "更新记录新增 1 条") || !strings.Contains(calls[1].body, "同步 U1S1_VERSION") {
		t.Fatalf("更新记录推送应含版本同步提示: %+v", calls[1])
	}
}

func TestCheckSiteFeedRepeatNoDup(t *testing.T) {
	f := newSiteFeedFixture(t, threeAnnJSON, sampleCLNew, "1.2.3")
	f.fx.srv.checkSiteFeedAndNotify(context.Background())
	f.fx.srv.checkSiteFeedAndNotify(context.Background())
	if calls := f.pushCalls(); len(calls) != 0 {
		t.Fatalf("重复检查不应再推送: %+v", calls)
	}
}

func TestCheckNPMVersionNotifyOnce(t *testing.T) {
	f := newSiteFeedFixture(t, twoAnnJSON, sampleCL, "1.2.4") // npm 1.2.4 vs 本地 1.2.3

	res := f.fx.srv.checkSiteFeedAndNotify(context.Background())
	if res.NpmVersion != "1.2.4" || !res.CliPushed {
		t.Fatalf("应检测到 npm 新版本并推送: %+v", res)
	}
	calls := f.pushCalls()
	if len(calls) != 1 || !strings.Contains(calls[0].body, "1.2.3 → npm 最新 1.2.4") {
		t.Fatalf("CLI 推送文案不符: %+v", calls)
	}
	// 同版本再查一次不重复推送
	f.fx.srv.checkSiteFeedAndNotify(context.Background())
	if calls := f.pushCalls(); len(calls) != 1 {
		t.Fatalf("同一版本只推送一次: %+v", calls)
	}
}

func TestVersionGreater(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"1.2.4", "1.2.3", true},
		{"1.2.3", "1.2.4", false},
		{"1.2.3", "1.2.3", false},
		{"1.10.0", "1.9.9", true},
		{"v1.2.4", "1.2.3", true},
		{"2.0", "1.9.9", true},
		{"1.2", "1.2.0", false},
	}
	for _, c := range cases {
		if got := versionGreater(c.a, c.b); got != c.want {
			t.Errorf("versionGreater(%q,%q)=%v 期望 %v", c.a, c.b, got, c.want)
		}
	}
}
