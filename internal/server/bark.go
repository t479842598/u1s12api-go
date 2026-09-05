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

	"github.com/t479842598/u1s12api-go/internal/store"
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

// alertDeviceNotTrusted 设备账号被网关判为不受信任（403）时告警。
//
// 为什么必须告警而不是静默换下一个账号：官方 1.7.1 把这类 403 定性为「重登也没用」，
// 只有人工去官网（https://u1s1.io/dashboard 提工单 / contact@u1s1.io）才能恢复。
// 不告警就等于账号悄悄死掉、直到全池耗尽才从 503 里看出来。
// 账号已被停用，因此不会被反复选中，也就不会刷屏。
func (s *Server) alertDeviceNotTrusted(email, body string) {
	key := strings.TrimSpace(s.getSettings().BarkKey)
	if key == "" {
		return
	}
	title := "u1s1 设备账号被拒绝(403)"
	msg := fmt.Sprintf("账号 %s 已被停用。网关原因：%s。需人工到 u1s1.io 处理（提工单或邮件 contact@u1s1.io）。",
		store.MaskEmail(email), truncate(body, 180))
	if ok, err := barkPushFn(key, title, msg, "https://u1s1.io/dashboard"); !ok {
		logger.Warnf("403 停用告警推送失败: %v", err)
	}
}

// alertDeviceNeedsRelogin 设备账号被网关要求重新登录（403 client_integrity_review）时告警。
//
// 与 alertDeviceNotTrusted 的区别：这类 403 官方文案就是「请升级并重新登录 u1s1」，
// 重新授权可恢复，所以告警引导去后台点「重新授权」，而不是只能官网申诉。
func (s *Server) alertDeviceNeedsRelogin(email, body string) {
	key := strings.TrimSpace(s.getSettings().BarkKey)
	if key == "" {
		return
	}
	title := "u1s1 账号需重新登录(403)"
	msg := fmt.Sprintf("账号 %s 被网关要求重新登录，已停用。请到后台「授权账号」点「重新授权」恢复。原因：%s",
		store.MaskEmail(email), truncate(body, 180))
	if ok, err := barkPushFn(key, title, msg, ""); !ok {
		logger.Warnf("403 需重新登录告警推送失败: %v", err)
	}
}
