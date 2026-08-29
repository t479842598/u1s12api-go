// 官网动态（公告/更新记录）管理后台 API：列表查询 + 手动刷新。
package server

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/t479842598/u1s12api-go/internal/config"
)

// handleGetSiteFeed GET /admin/api/sitefeed
// 返回两组条目（各 100 条倒序）+ 检查状态 + 版本对比。
func (s *Server) handleGetSiteFeed(w http.ResponseWriter, _ *http.Request) {
	cfg := s.getSettings()
	announcements, err := s.store.ListSitePosts("announcement", 100)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "查询公告失败: "+err.Error())
		return
	}
	changelog, err := s.store.ListSitePosts("changelog", 100)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "查询更新记录失败: "+err.Error())
		return
	}

	lastCheck := s.siteFeedLastCheck()
	intervalHours := cfg.SiteFeedCheckHours
	if intervalHours <= 0 {
		intervalHours = config.DefaultSiteFeedCheckHours
	}
	var nextCheck int64
	if lastCheck > 0 {
		nextCheck = lastCheck + int64(intervalHours)*3600
	}
	npmLatest, _ := s.store.GetSiteFeedState(siteFeedStateNpmLatest)
	npmCheckAt, _ := s.store.GetSiteFeedState(siteFeedStateCLICheckAt)
	npmCheckAtInt, _ := strconv.ParseInt(npmCheckAt, 10, 64)

	writeAPIData(w, http.StatusOK, map[string]any{
		"last_check_at":      lastCheck,
		"next_check_at":      nextCheck,
		"check_interval_h":   intervalHours,
		"bark_configured":    cfg.BarkKey != "",
		"local_version":      cfg.U1S1Version,
		"npm_version":        npmLatest,
		"npm_checked_at":     npmCheckAtInt,
		"announcements":      announcements,
		"changelog":          changelog,
		"announcement_count": mustCount(s, "announcement"),
		"changelog_count":    mustCount(s, "changelog"),
	})
}

// handleSiteFeedRefresh POST /admin/api/sitefeed/refresh — 立即执行一次检查。
func (s *Server) handleSiteFeedRefresh(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()
	res := s.checkSiteFeedAndNotify(ctx)
	if res.Error != "" {
		// 部分失败也返回结果（可能已抓到单边数据），带 error 字段
		writeAPIData(w, http.StatusBadGateway, map[string]any{"ok": false, "result": res})
		return
	}
	writeAPIData(w, http.StatusOK, map[string]any{"ok": true, "result": res})
}

func mustCount(s *Server, kind string) int64 {
	n, err := s.store.CountSitePostsSince(kind, 0)
	if err != nil {
		return 0
	}
	return n
}
