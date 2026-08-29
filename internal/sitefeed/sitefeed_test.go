package sitefeed

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

func parseHTML(t *testing.T, src string) *html.Node {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

const sampleChangelogHTML = `<!doctype html><html><body>
<main>
<h1 class="docs-title">更新记录</h1>
<p>u1s1 版本更新记录，按时间倒序排列。</p>
<h2>Gateway</h2>
<h3>新增或改进</h3>
<ul><li>公告 API 新增 <code>/public/announcements</code></li><li>额度明细分组展示</li></ul>
<h2>v1.2.5</h2>
<h3>修复（CLI）</h3>
<ul><li>修复生图断流重试</li><li>修复超时处理</li></ul>
<h2>Gateway</h2>
<h3>改进</h3>
<ul><li>网关性能优化</li></ul>
</main>
</body></html>`

const minChangelogHTML = `<html><body><main><h2>v0.0.1</h2><h3>初始</h3><ul><li>发布</li></ul></main></body></html>`

// newFeedUpstreams 起三个 mock 上游（公告/更新记录/npm）并替换包级 URL，返回恢复函数。
func newFeedUpstreams(t *testing.T, annBody, clBody, npmBody string) {
	t.Helper()
	annTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(annBody))
	}))
	clTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(clBody))
	}))
	npmTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(npmBody))
	}))
	t.Cleanup(func() { annTS.Close(); clTS.Close(); npmTS.Close() })

	oldAnn, oldCL, oldNpm := AnnouncementsURL, ChangelogURL, NpmLatestURL
	AnnouncementsURL = annTS.URL + "/public/announcements?limit=100"
	ChangelogURL = clTS.URL + "/guides/changelog"
	NpmLatestURL = npmTS.URL + "/latest"
	t.Cleanup(func() {
		AnnouncementsURL, ChangelogURL, NpmLatestURL = oldAnn, oldCL, oldNpm
	})
}

func newTestService(t *testing.T) *Service {
	t.Helper()
	svc, err := New("")
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func TestSplitChangelogBlocks(t *testing.T) {
	entries := splitChangelogBlocks(parseHTML(t, sampleChangelogHTML))
	if len(entries) != 3 {
		t.Fatalf("期望 3 个版本块，得到 %d: %+v", len(entries), entries)
	}
	if entries[0].Title != "Gateway" || entries[1].Title != "v1.2.5" || entries[2].Title != "Gateway" {
		t.Fatalf("标题不符: %+v", entries)
	}
	if !strings.Contains(entries[0].Summary, "【新增或改进】") || !strings.Contains(entries[0].Summary, "公告 API 新增") {
		t.Fatalf("摘要未含 h3 与 li 内容: %q", entries[0].Summary)
	}
	// 同标题不同内容的块 PostKey 必须不同；同内容重复调用必须稳定
	if entries[0].PostKey() == entries[2].PostKey() {
		t.Fatalf("两个 Gateway 块 PostKey 相同: %s", entries[0].PostKey())
	}
	if entries[0].PostKey() != entries[0].PostKey() {
		t.Fatalf("PostKey 不稳定")
	}
	// 页面标题 h1 与说明 p 不应出现在第一个块摘要里
	if strings.Contains(entries[0].Summary, "更新记录") || strings.Contains(entries[0].Summary, "按时间倒序") {
		t.Fatalf("h1/p 泄漏进块摘要: %q", entries[0].Summary)
	}
}

func TestFetchAnnouncements(t *testing.T) {
	newFeedUpstreams(t, `{"announcements":[
		{"id":5,"title":"连续打卡奖励上线","body":"详情……","url":"/dashboard#sec-usage","published_at":"2026-08-27 15:41:26"},
		{"id":4,"title":"免费用量包适用模型说明","body":"……","url":"/pricing","published_at":"2026-08-27 14:59:02"}
	]}`, minChangelogHTML, `{"version":"1.2.5"}`)

	svc := newTestService(t)
	feed, err := svc.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(feed.Announcements) != 2 {
		t.Fatalf("期望 2 条公告，得到 %d", len(feed.Announcements))
	}
	if feed.Announcements[0].ID != 5 || feed.Announcements[0].Title != "连续打卡奖励上线" {
		t.Fatalf("公告字段解析不符: %+v", feed.Announcements[0])
	}
}

func TestFetchAnnouncementsBadStructure(t *testing.T) {
	newFeedUpstreams(t, `{"unexpected":true}`, minChangelogHTML, `{"version":"1.2.5"}`)
	svc := newTestService(t)
	if _, err := svc.Fetch(context.Background()); err == nil || !strings.Contains(err.Error(), "公告") {
		t.Fatalf("公告结构变化时应报错，得到: %v", err)
	}
}

func TestLatestCLIVersion(t *testing.T) {
	newFeedUpstreams(t, `{"announcements":[]}`, minChangelogHTML, `{"version":"1.2.5","name":"u1s1-cli"}`)
	svc := newTestService(t)
	v, err := svc.LatestCLIVersion(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if v != "1.2.5" {
		t.Fatalf("期望 1.2.5，得到 %s", v)
	}
}

func TestFetchChangelogHTMLErrorOnEmpty(t *testing.T) {
	newFeedUpstreams(t, `{"announcements":[]}`,
		`<html><body><main><p>空</p></main></body></html>`, `{"version":"1.2.5"}`)
	svc := newTestService(t)
	if _, err := svc.Fetch(context.Background()); err == nil || !strings.Contains(err.Error(), "0 个版本块") {
		t.Fatalf("空 main 应报 0 个版本块错误，得到: %v", err)
	}
}
