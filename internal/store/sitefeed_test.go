package store

import (
	"path/filepath"
	"testing"
)

func TestUpsertSitePostDedup(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	isNew, err := s.UpsertSitePost("announcement", "5", "标题", "正文", "/pricing", "2026-08-27 15:41:26")
	if err != nil || !isNew {
		t.Fatalf("首次插入应 isNew=true, err=%v", err)
	}
	// 同 key 再插：不更新、不新增
	isNew, err = s.UpsertSitePost("announcement", "5", "改过的标题", "改过的正文", "", "")
	if err != nil || isNew {
		t.Fatalf("同 key 二次插入应 isNew=false, err=%v", err)
	}
	posts, err := s.ListSitePosts("announcement", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(posts) != 1 || posts[0].Title != "标题" {
		t.Fatalf("同 key 不应覆盖内容: %+v", posts)
	}
	// changelog 同 key 不同 kind 互不干扰
	if isNew, _ := s.UpsertSitePost("changelog", "5", "v1.2.5", "", "", ""); !isNew {
		t.Fatalf("不同 kind 同 key 应视为新条目")
	}
	// 倒序
	_, _ = s.UpsertSitePost("announcement", "6", "更新", "", "", "")
	posts, _ = s.ListSitePosts("announcement", 10)
	if posts[0].PostKey != "6" {
		t.Fatalf("应按 id 倒序: %+v", posts)
	}
	// 计数
	n, _ := s.CountSitePostsSince("announcement", 0)
	if n != 2 {
		t.Fatalf("计数不符: %d", n)
	}
}

func TestSiteFeedStateRoundTrip(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if v, _ := s.GetSiteFeedState("missing"); v != "" {
		t.Fatalf("缺失 key 应返回空串，得到 %q", v)
	}
	if err := s.SetSiteFeedState("k", "v1"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSiteFeedState("k", "v2"); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.GetSiteFeedState("k"); v != "v2" {
		t.Fatalf("upsert 后应读到 v2，得到 %q", v)
	}
}
