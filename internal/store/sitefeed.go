// 官网公告/更新记录落库：site_posts（去重条目）+ sitefeed_state（检查状态）。
package store

import "time"

// SitePost 官网公告/更新记录条目。
type SitePost struct {
	ID          int64  `json:"id"`
	Kind        string `json:"kind"` // announcement | changelog
	PostKey     string `json:"post_key"`
	Title       string `json:"title"`
	Summary     string `json:"summary"`
	URL         string `json:"url,omitempty"`
	PublishedAt string `json:"published_at,omitempty"`
	FirstSeenAt int64  `json:"first_seen_at"`
}

// UpsertSitePost 插入条目；同 (kind, post_key) 已存在时不改动内容，返回 isNew=false。
func (s *Store) UpsertSitePost(kind, postKey, title, summary, url, publishedAt string) (bool, error) {
	res, err := s.db.Exec(
		`INSERT OR IGNORE INTO site_posts(kind, post_key, title, summary, url, published_at, first_seen_at)
		 VALUES(?,?,?,?,?,?,?)`,
		kind, postKey, title, summary, url, publishedAt, time.Now().Unix())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// ListSitePosts 按 kind 倒序（抓取顺序即上游倒序，id 大者新）取最近 limit 条。
func (s *Store) ListSitePosts(kind string, limit int) ([]SitePost, error) {
	rows, err := s.db.Query(
		`SELECT id, kind, post_key, title, summary, url, published_at, first_seen_at
		 FROM site_posts WHERE kind = ? ORDER BY id DESC LIMIT ?`, kind, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SitePost
	for rows.Next() {
		var p SitePost
		if err := rows.Scan(&p.ID, &p.Kind, &p.PostKey, &p.Title, &p.Summary, &p.URL, &p.PublishedAt, &p.FirstSeenAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// CountSitePostsSince 统计 kind 类别在 since（unix 秒）之后发现的条目数。
func (s *Store) CountSitePostsSince(kind string, since int64) (int64, error) {
	var n int64
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM site_posts WHERE kind = ? AND first_seen_at >= ?`, kind, since).Scan(&n)
	return n, err
}

// GetSiteFeedState 读取状态值；不存在返回空串。
func (s *Store) GetSiteFeedState(key string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM sitefeed_state WHERE key = ?`, key).Scan(&v)
	if err == nil {
		return v, nil
	}
	if err.Error() == "sql: no rows in result set" {
		return "", nil
	}
	return "", err
}

// SetSiteFeedState 写状态值（upsert）。
func (s *Store) SetSiteFeedState(key, value string) error {
	_, err := s.db.Exec(
		`INSERT INTO sitefeed_state(key, value) VALUES(?,?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}
