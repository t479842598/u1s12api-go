// 官网公告/更新记录监听 + Bark 推送 + npm 版本检测。
//
// 定时（SITEFEED_CHECK_HOURS 小时，默认 24；启动时距上次检查超期则补查一次）：
//  1. 抓取官网公开数据源：公告 /public/announcements、更新记录 /guides/changelog（静态 HTML）。
//     新条目 INSERT OR IGNORE 入 site_posts；首次运行只建快照不推送，之后新增条目
//     合并为一条 Bark 推送（公告、更新记录各一条）。
//  2. 查询 npm registry 的 u1s1-cli 最新版本，与配置的 U1S1_VERSION 比较——有新版本时
//     单独推送一次（同一版本只推一次，去重存 sitefeed_state），提示同步指纹。
//
// BARK_KEY 为空时照常抓取入库（管理后台仍可查看），仅跳过推送。
// 任何一步失败只记日志，不影响服务主流程，下轮重试。
package server

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/t479842598/u1s12api-go/internal/sitefeed"
	"github.com/t479842598/u1s12api-go/internal/webcheckin"
)

const (
	siteFeedStateLastCheck   = "last_check_at"       // unix 秒
	siteFeedStateFirstDone   = "first_snapshot_done" // "1" = 已建过快照
	siteFeedStateCLINotified = "notified_cli_version"
	siteFeedStateNpmLatest   = "npm_latest_version" // 最近一次查到的 npm 最新版本
	siteFeedStateCLICheckAt  = "npm_checked_at"     // unix 秒
	announcementsPageURL     = "https://u1s1.io/guides/announcements"
	changelogPageURL         = "https://u1s1.io/guides/changelog"
)

// cliVersionRe 更新记录条目标题里的 CLI 版本号（v1.2.5 等）。
var cliVersionRe = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)

// SiteFeedCheckResult 一次检查的返回值（供管理后台手动刷新展示）。
type SiteFeedCheckResult struct {
	CheckedAt          int64    `json:"checked_at"`
	Error              string   `json:"error,omitempty"`
	NewAnnouncements   []string `json:"new_announcements,omitempty"` // 新公告标题
	NewChangelog       []string `json:"new_changelog,omitempty"`     // 新更新记录标题
	AnnouncementPushed bool     `json:"announcement_pushed"`
	ChangelogPushed    bool     `json:"changelog_pushed"`
	LocalVersion       string   `json:"local_version"`
	NpmVersion         string   `json:"npm_version,omitempty"`
	NpmError           string   `json:"npm_error,omitempty"`
	CliPushed          bool     `json:"cli_pushed"`
}

// siteFeedService 构造抓取服务（跟随 EGRESS_PROXY 出口），并注册 401 登录兜底。
func (s *Server) siteFeedService() (*sitefeed.Service, error) {
	svc, err := sitefeed.New(s.getSettings().EgressProxyURL)
	if err != nil {
		return nil, err
	}
	svc.SetSessionFactory(s.webSessionFactory)
	return svc, nil
}

// webSessionFactory 401 兜底：用账号池第一个有密码的授权账号网页登录拿会话。
func (s *Server) webSessionFactory(ctx context.Context) (*http.Client, error) {
	accs, err := s.store.ListAuthorizedEnabledAccounts()
	if err != nil {
		return nil, err
	}
	for _, acc := range accs {
		if !acc.HasPassword {
			continue
		}
		email, password, err := s.store.GetAccountCredential(acc.ID)
		if err != nil || email == "" || password == "" {
			continue
		}
		svc, err := webcheckin.New(s.getSettings().EgressProxyURL)
		if err != nil {
			continue
		}
		return svc.NewSession(ctx, email, password)
	}
	return nil, fmt.Errorf("没有存有密码的授权账号，无法登录兜底")
}

// RunSiteFeedWatcher 后台循环：启动时超期补查一次，之后按 interval 轮询；ctx 取消即停。
// SITEFEED_CHECK_HOURS<=0 时不启动（对齐 QuotaAutoRefresh 惯例：测试直构 Settings 零值天然不跑；
// 生产由 config.Load 保证默认 24）。
func RunSiteFeedWatcher(ctx context.Context, s *Server) {
	interval := time.Duration(s.getSettings().SiteFeedCheckHours) * time.Hour
	if interval <= 0 {
		logger.Infof("SITEFEED_CHECK_HOURS<=0，官网动态监听关闭")
		return
	}
	logger.Infof("官网动态监听已启动（间隔 %s，bark=%s）",
		interval, map[bool]string{true: "已配置", false: "未配置"}[strings.TrimSpace(s.getSettings().BarkKey) != ""])

	runOnce := func() {
		defer func() {
			if p := recover(); p != nil {
				logger.Errorf("官网动态检查 panic: %v", p)
			}
		}()
		res := s.checkSiteFeedAndNotify(ctx)
		if res.Error != "" {
			logger.Warnf("官网动态检查失败: %s", res.Error)
			return
		}
		logger.Infof("官网动态检查完成: 公告新增 %d 推送 %v，更新记录新增 %d 推送 %v，npm=%s",
			len(res.NewAnnouncements), res.AnnouncementPushed,
			len(res.NewChangelog), res.ChangelogPushed, orDash(res.NpmVersion))
	}

	// 启动补查：从未检查过或距上次检查已超期间隔。
	if last := s.siteFeedLastCheck(); last == 0 || time.Since(time.Unix(last, 0)) >= interval {
		runOnce()
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			logger.Infof("官网动态监听已停止")
			return
		case <-ticker.C:
			runOnce()
		}
	}
}

func (s *Server) siteFeedLastCheck() int64 {
	v, err := s.store.GetSiteFeedState(siteFeedStateLastCheck)
	if err != nil || v == "" {
		return 0
	}
	n, _ := strconv.ParseInt(v, 10, 64)
	return n
}

// checkSiteFeedAndNotify 执行一次完整检查：抓取 → 入库去重 → 合并推送 + npm 版本检测。
func (s *Server) checkSiteFeedAndNotify(ctx context.Context) SiteFeedCheckResult {
	res := SiteFeedCheckResult{
		CheckedAt:    time.Now().Unix(),
		LocalVersion: s.getSettings().U1S1Version,
	}

	svc, err := s.siteFeedService()
	if err != nil {
		res.Error = err.Error()
		return res
	}

	feed, fetchErr := svc.Fetch(ctx)
	if fetchErr != nil {
		res.Error = fetchErr.Error()
	}
	firstSnapshot, _ := s.store.GetSiteFeedState(siteFeedStateFirstDone)
	isFirst := firstSnapshot != "1"

	barkKey := strings.TrimSpace(s.getSettings().BarkKey)

	// 公告入库（post_key = 上游公告 id）
	for _, a := range feed.Announcements {
		isNew, err := s.store.UpsertSitePost("announcement", strconv.FormatInt(a.ID, 10),
			a.Title, a.Body, a.URL, a.PublishedAt)
		if err != nil {
			logger.Warnf("公告入库失败 id=%d: %v", a.ID, err)
			continue
		}
		if isNew && !isFirst {
			res.NewAnnouncements = append(res.NewAnnouncements, a.Title)
		}
	}
	// 更新记录入库（post_key = 块内容 hash）
	for _, c := range feed.Changelog {
		isNew, err := s.store.UpsertSitePost("changelog", c.PostKey(), c.Title, c.Summary, changelogPageURL, "")
		if err != nil {
			logger.Warnf("更新记录入库失败 %s: %v", c.Title, err)
			continue
		}
		if isNew && !isFirst {
			res.NewChangelog = append(res.NewChangelog, c.Title)
		}
	}
	_ = s.store.SetSiteFeedState(siteFeedStateFirstDone, "1")

	// 合并推送（bark key 未配置时跳过，条目照常入库供后台查看）
	if barkKey != "" && !isFirst {
		if len(res.NewAnnouncements) > 0 {
			title := fmt.Sprintf("u1s1 官网公告更新 %d 条", len(res.NewAnnouncements))
			body := strings.Join(res.NewAnnouncements, "\n")
			if ok, _ := barkPushFn(barkKey, title, body, announcementsPageURL); ok {
				res.AnnouncementPushed = true
			}
		}
		if len(res.NewChangelog) > 0 {
			title := fmt.Sprintf("u1s1 更新记录新增 %d 条", len(res.NewChangelog))
			lines := make([]string, 0, len(res.NewChangelog))
			hasCLIVersion := false
			for _, t := range res.NewChangelog {
				lines = append(lines, t)
				if cliVersionRe.MatchString(t) {
					hasCLIVersion = true
				}
			}
			body := strings.Join(lines, "\n")
			if hasCLIVersion {
				body += fmt.Sprintf("\n⚠️ CLI 有新版本，请同步 U1S1_VERSION 指纹（当前适配 %s）", res.LocalVersion)
			}
			if ok, _ := barkPushFn(barkKey, title, body, changelogPageURL); ok {
				res.ChangelogPushed = true
			}
		}
	} else if isFirst {
		logger.Infof("官网动态首次运行：建立快照（公告 %d 条、更新记录 %d 块），不推送",
			len(feed.Announcements), len(feed.Changelog))
	}

	// npm 版本检测（独立于 feed 抓取成败）
	if latest, err := svc.LatestCLIVersion(ctx); err != nil {
		res.NpmError = err.Error()
		logger.Warnf("npm 版本查询失败: %v", err)
	} else {
		res.NpmVersion = latest
		_ = s.store.SetSiteFeedState(siteFeedStateNpmLatest, latest)
		_ = s.store.SetSiteFeedState(siteFeedStateCLICheckAt, strconv.FormatInt(time.Now().Unix(), 10))
		if versionGreater(latest, res.LocalVersion) {
			notified, _ := s.store.GetSiteFeedState(siteFeedStateCLINotified)
			if notified != latest {
				title := fmt.Sprintf("u1s1-cli 有新版本 %s", latest)
				body := fmt.Sprintf("本地适配 %s → npm 最新 %s，请同步 U1S1_VERSION 指纹。", res.LocalVersion, latest)
				if barkKey != "" {
					if ok, _ := barkPushFn(barkKey, title, body, changelogPageURL); ok {
						res.CliPushed = true
					}
				}
				// 无论是否配置 bark 都记录已通知版本，避免之后配置后重复打扰
				_ = s.store.SetSiteFeedState(siteFeedStateCLINotified, latest)
			}
		}
	}

	_ = s.store.SetSiteFeedState(siteFeedStateLastCheck, strconv.FormatInt(res.CheckedAt, 10))
	return res
}

// versionGreater 语义化版本比较：a 是否大于 b（点分段数值比较，非数字段退化为字符串比较）。
func versionGreater(a, b string) bool {
	as := strings.Split(strings.TrimPrefix(strings.TrimSpace(a), "v"), ".")
	bs := strings.Split(strings.TrimPrefix(strings.TrimSpace(b), "v"), ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		av, bv := "", ""
		if i < len(as) {
			av = as[i]
		}
		if i < len(bs) {
			bv = bs[i]
		}
		an, aerr := strconv.Atoi(av)
		bn, berr := strconv.Atoi(bv)
		switch {
		case aerr == nil && berr == nil:
			if an != bn {
				return an > bn
			}
		default:
			if av != bv {
				return av > bv
			}
		}
	}
	return false
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}
