// Package sitefeed 抓取 u1s1.io 官网「公告」与「更新记录」并解析为统一条目。
//
// 数据源（逆向官网前端确认，2026-08-29）：
//   - 公告：GET /public/announcements?limit=100 → {"announcements":[{id,title,body,url,published_at}]}
//     （公开接口，无需登录；guides/announcements.js 用的就是它）
//   - 更新记录：GET /guides/changelog → 静态渲染 HTML，<main> 内按 <h2> 版本分块解析
//   - CLI 版本：GET https://registry.npmjs.org/u1s1-cli/latest → {"version":"x.y.z"}
package sitefeed

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"golang.org/x/net/html"

	"github.com/t479842598/u1s12api-go/internal/capcat"
)

// 数据源 URL（var 而非 const：测试可覆盖为 httptest 地址）。
var (
	AnnouncementsURL = "https://u1s1.io/public/announcements?limit=100"
	ChangelogURL     = "https://u1s1.io/guides/changelog"
	NpmLatestURL     = "https://registry.npmjs.org/u1s1-cli/latest"
)

const (
	fetchTimeout = 15 * time.Second
)

// Announcement 官网公告条目（published_at 原样为 "2006-01-02 15:04:05" UTC 文本）。
type Announcement struct {
	ID          int64  `json:"id"`
	Title       string `json:"title"`
	Body        string `json:"body"`
	URL         string `json:"url"`
	PublishedAt string `json:"published_at"`
}

// ChangelogEntry 更新记录的一个版本块（h2 标题 + 块内条目拼接摘要）。
type ChangelogEntry struct {
	Title   string `json:"title"`
	Summary string `json:"summary"`
}

// PostKey 去重键：changelog 的 h2 标题会重复（如两个 Gateway），用块内容 hash 区分。
func (e ChangelogEntry) PostKey() string {
	sum := sha256.Sum256([]byte(e.Title + "\x00" + e.Summary))
	return hex.EncodeToString(sum[:8])
}

// Feed 一次抓取得到的两组条目（均为上游倒序）。
type Feed struct {
	Announcements []Announcement
	Changelog     []ChangelogEntry
}

// Service 官网动态抓取服务。
type Service struct {
	client *http.Client
	// sessionFactory 401 兜底：公告接口要求会话时（当前为公开接口，防御性保留）
	// 由调用方提供「登录并返回带会话 client」的能力；nil 则 401 直接报错。
	sessionFactory func(ctx context.Context) (*http.Client, error)
}

// New 构造；proxyURL 为空则直连。
func New(proxyURL string) (*Service, error) {
	client := &http.Client{Timeout: fetchTimeout}
	if proxyURL != "" {
		tr, err := capcat.Transport(proxyURL)
		if err != nil {
			return nil, err
		}
		client.Transport = tr
	}
	return &Service{client: client}, nil
}

// SetSessionFactory 注册 401 兜底登录能力（见 Service.sessionFactory）。
func (s *Service) SetSessionFactory(f func(ctx context.Context) (*http.Client, error)) {
	s.sessionFactory = f
}

// Fetch 抓取公告与更新记录。任一源失败不影响另一源（错误合并返回，Feed 可为部分结果）。
func (s *Service) Fetch(ctx context.Context) (*Feed, error) {
	feed := &Feed{}
	var errs []string

	ann, err := s.fetchAnnouncements(ctx, s.client)
	if err != nil {
		// 401 兜底：登录一次重试
		if strings.Contains(err.Error(), "http 401") && s.sessionFactory != nil {
			if sess, sessErr := s.sessionFactory(ctx); sessErr == nil {
				if ann2, retryErr := s.fetchAnnouncements(ctx, sess); retryErr == nil {
					ann, err = ann2, nil
				} else {
					err = fmt.Errorf("%v (登录重试后: %v)", err, retryErr)
				}
			} else {
				err = fmt.Errorf("%v (登录兜底失败: %v)", err, sessErr)
			}
		}
	}
	if err != nil {
		errs = append(errs, "公告: "+err.Error())
	} else {
		feed.Announcements = ann
	}

	cl, err := s.fetchChangelog(ctx)
	if err != nil {
		errs = append(errs, "更新记录: "+err.Error())
	} else {
		feed.Changelog = cl
	}

	if len(errs) > 0 {
		return feed, fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return feed, nil
}

// LatestCLIVersion 查询 npm 上 u1s1-cli 的最新版本号。
func (s *Service) LatestCLIVersion(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, NpmLatestURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("npm latest status %d", resp.StatusCode)
	}
	var data struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&data); err != nil {
		return "", err
	}
	if data.Version == "" {
		return "", fmt.Errorf("npm latest 响应缺少 version")
	}
	return data.Version, nil
}

type announcementsResp struct {
	Announcements []Announcement `json:"announcements"`
}

func (s *Service) fetchAnnouncements(ctx context.Context, client *http.Client) ([]Announcement, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, AnnouncementsURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, truncate(string(data), 120))
	}
	var out announcementsResp
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("解析公告响应失败: %w", err)
	}
	if out.Announcements == nil {
		return nil, fmt.Errorf("公告响应结构变化（announcements 为空）")
	}
	return out.Announcements, nil
}

// fetchChangelog 抓取静态渲染的更新记录页并按 <h2> 分块解析。
func (s *Service) fetchChangelog(ctx context.Context) ([]ChangelogEntry, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ChangelogURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http %d", resp.StatusCode)
	}
	doc, err := html.Parse(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	main := findElement(doc, "main")
	if main == nil {
		return nil, fmt.Errorf("更新记录页结构变化（未找到 main）")
	}
	entries := splitChangelogBlocks(main)
	if len(entries) == 0 {
		return nil, fmt.Errorf("更新记录页解析到 0 个版本块（结构可能变化）")
	}
	return entries, nil
}

// findElement 深度优先找第一个 tag 名匹配的元素。
func findElement(n *html.Node, tag string) *html.Node {
	if n.Type == html.ElementNode && n.Data == tag {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if found := findElement(c, tag); found != nil {
			return found
		}
	}
	return nil
}

// splitChangelogBlocks 按 main 内顶层顺序遍历：<h2> 开新块（跳过页面标题 h1），
// 其余元素的文本（h3 小节、li、p）追加到当前块摘要。
func splitChangelogBlocks(main *html.Node) []ChangelogEntry {
	var entries []ChangelogEntry
	var cur *ChangelogEntry
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type != html.ElementNode {
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				walk(c)
			}
			return
		}
		switch n.Data {
		case "h1":
			return // 页面大标题
		case "h2":
			entries = append(entries, ChangelogEntry{Title: strings.TrimSpace(textOf(n))})
			cur = &entries[len(entries)-1]
			return
		case "h3":
			if cur != nil {
				appendSummary(cur, "【"+strings.TrimSpace(textOf(n))+"】")
			}
			return
		case "li", "p":
			if cur != nil {
				if line := strings.TrimSpace(textOf(n)); line != "" {
					appendSummary(cur, line)
				}
			}
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	for c := main.FirstChild; c != nil; c = c.NextSibling {
		walk(c)
	}
	// 过滤空块（main 里可能有说明性 p 在第一个 h2 之前）
	out := entries[:0]
	for _, e := range entries {
		if e.Title != "" {
			out = append(out, e)
		}
	}
	return out
}

func appendSummary(e *ChangelogEntry, line string) {
	if e.Summary != "" {
		e.Summary += "\n"
	}
	e.Summary += line
	if len(e.Summary) > 500 {
		e.Summary = e.Summary[:500] + "…"
	}
}

// textOf 递归收集元素内文本（li 内常有嵌套标签）。
func textOf(n *html.Node) string {
	var b strings.Builder
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
			return
		}
		if n.Type == html.ElementNode && (n.Data == "script" || n.Data == "style") {
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.Join(strings.Fields(b.String()), " ")
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
