package upstream

import (
	"net/http"
	"testing"
)

// 生产实测错误体（2026-09-01，欧洲 VPS 日志）：上游模型厂商内容审查，厂商名被网关打码。
const moderationBody = `{"error":{"message":"<400> ***.***.DataInspectionFailed: Input text data may contain inappropriate content.","type":"data_inspection_failed","code":"data_inspection_failed","upstream_status":400}}`

const quotaBody429 = `{"error":{"message":"免费用量包余额不足","type":"insufficient_quota","code":"quota_exceeded"}}`

const clientOnly403Body = `{"error":{"message":"API 推理请求仅支持 u1s1 客户端；旧版 API Key 仅在明确的历史兼容窗口内可用。请升级并重新登录","type":"forbidden","code":"u1s1_client_only"}}`

func TestRequestScopedError(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"内容审查 400 属请求级", http.StatusBadRequest, moderationBody, true},
		{"未知模型 400 属请求级", http.StatusBadRequest, `{"error":{"code":"model_not_found"}}`, true},
		{"请求体非法 400 属请求级", http.StatusBadRequest, `{"error":{"type":"invalid_request_error"}}`, true},
		{"额度 400 不短路（仍应换账号）", http.StatusBadRequest, quotaBody429, false},
		{"额度 429 不短路", http.StatusTooManyRequests, quotaBody429, false},
		{"限流 429 不短路", http.StatusTooManyRequests, `{"error":{"message":"Provider returned error"}}`, false},
		{"401 不短路", http.StatusUnauthorized, `{"error":{"code":"invalid_api_key"}}`, false},
		{"403 不短路", http.StatusForbidden, `{"error":{"message":"forbidden"}}`, false},
		{"503 不短路", http.StatusServiceUnavailable, `{"error":{"code":"model_unavailable"}}`, false},
		{"200 不短路", http.StatusOK, `{}`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := RequestScopedError(c.status, c.body); got != c.want {
				t.Errorf("RequestScopedError(%d, %.60s) = %v，期望 %v", c.status, c.body, got, c.want)
			}
		})
	}
}

func TestContentModerationRejected(t *testing.T) {
	if !ContentModerationRejected(http.StatusBadRequest, moderationBody) {
		t.Error("生产实测的内容审查错误体应被识别")
	}
	// 大小写与打码变体
	if !ContentModerationRejected(http.StatusBadRequest, `{"error":{"message":"DataInspectionFailed"}}`) {
		t.Error("DataInspectionFailed 驼峰形态应被识别")
	}
	// 非 400 不算
	if ContentModerationRejected(http.StatusTooManyRequests, moderationBody) {
		t.Error("非 400 不应判为内容审查")
	}
	// 额度错误不应被误判为内容审查
	if ContentModerationRejected(http.StatusBadRequest, quotaBody429) {
		t.Error("额度类错误不应判为内容审查")
	}
}

func TestKeyClientOnlyRejected(t *testing.T) {
	if !KeyClientOnlyRejected(http.StatusForbidden, clientOnly403Body) {
		t.Error("生产实测的 u1s1_client_only 403 应被识别")
	}
	// 变体：仅中文措辞、未带 code
	if !KeyClientOnlyRejected(http.StatusForbidden, `{"error":{"message":"API 推理请求仅限 u1s1 客户端"}}`) {
		t.Error("仅限 u1s1 客户端 措辞应被识别")
	}
	// 非 403 不算
	if KeyClientOnlyRejected(http.StatusUnauthorized, clientOnly403Body) {
		t.Error("非 403 不应判为 u1s1_client_only")
	}
	// 设备通道的 403（令牌无效）与旧版 Key 通道 403 不同，不应误判
	if KeyClientOnlyRejected(http.StatusForbidden, `{"error":{"message":"invalid credential"}}`) {
		t.Error("普通 403 不应误判为旧版 Key 通道关闭")
	}
}
