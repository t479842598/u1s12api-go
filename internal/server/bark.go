// Bark 推送（iOS Bark App）。对齐 freebuff2api-go cli_watch 的实现：
// POST JSON 到 https://api.day.app/<key>/，禁用环境代理直连（trust_env=False 语义），
// barkPushFn 为测试可注入点。
package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	barkAPITemplate = "https://api.day.app/%s/"
	barkGroup       = "u1s1"
	barkIcon        = "https://u1s1.io/favicon.ico"
	barkSound       = "alarm"
	barkLevel       = "active"
	barkTimeout     = 10 * time.Second
)

// barkPushFn 测试可注入点：模拟推送结果。
var barkPushFn = barkPush

// barkPush 推送一条 Bark 通知。key 为空返回 (false, nil)；2xx 视为成功。
// url 非空时点击通知跳转该链接。
func barkPush(key, title, body, url string) (bool, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return false, nil
	}
	payload := map[string]any{
		"title": title,
		"body":  body,
		"group": barkGroup,
		"sound": barkSound,
		"icon":  barkIcon,
		"level": barkLevel,
	}
	if url != "" {
		payload["url"] = url
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return false, err
	}
	client := &http.Client{
		Timeout: barkTimeout,
		Transport: &http.Transport{
			Proxy: nil, // 禁用环境代理（http_proxy 等），直连推送
		},
	}
	resp, err := client.Post(fmt.Sprintf(barkAPITemplate, key), "application/json", bytes.NewReader(data))
	if err != nil {
		logger.Warnf("bark 推送失败: %v", err)
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		err := fmt.Errorf("bark 推送状态 %d", resp.StatusCode)
		logger.Warnf("bark 推送失败: %v", err)
		return false, err
	}
	return true, nil
}
