// 与上游 U1S1 网关的兼容层：请求体在进入上游前做最小必要改写，
// 不改语义，仅规避上游 JSON 反序列化的限制。
package server

import (
	"encoding/json"
)

// normalizeChatRoles 把 messages[].role 中的 "developer" 改写为 "system"。
//
// 背景：OpenAI o1/GPT-5 系列客户端（含 CPA 等代理）会把系统提示发为
// role=developer，但上游目前只接受 system/user/assistant/tool/latest_reminder
// 五种枚举，收到 developer 直接报 HTTP 400 "unknown variant `developer`"。
// developer 与 system 语义等价（OpenAI 官方文档即如此描述），直接归一化。
// 解析失败或字段缺失时原样返回，不改变既有错误路径。
func normalizeChatRoles(body []byte) []byte {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		return body
	}
	msgs, ok := root["messages"]
	if !ok {
		return body
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(msgs, &arr); err != nil || len(arr) == 0 {
		return body
	}
	changed := false
	for i, raw := range arr {
		var msg map[string]json.RawMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			continue
		}
		var role string
		if err := json.Unmarshal(msg["role"], &role); err != nil {
			continue
		}
		if role != "developer" {
			continue
		}
		msg["role"] = json.RawMessage(`"system"`)
		buf, err := json.Marshal(msg)
		if err != nil {
			continue
		}
		arr[i] = buf
		changed = true
	}
	if !changed {
		return body
	}
	out, err := json.Marshal(arr)
	if err != nil {
		return body
	}
	root["messages"] = out
	newBody, err := json.Marshal(root)
	if err != nil {
		return body
	}
	return newBody
}
