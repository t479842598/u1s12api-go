package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestNormalizeChatRoles(t *testing.T) {
	t.Run("developer→system 且其他字段不丢", func(t *testing.T) {
		in := `{"model":"gpt-5","messages":[{"role":"developer","content":"be terse","extra":1},{"role":"user","content":"hi"},{"role":"assistant","content":"ok"}],"temperature":0.3}`
		out := normalizeChatRoles([]byte(in))
		var m map[string]any
		if err := json.Unmarshal(out, &m); err != nil {
			t.Fatal(err)
		}
		msgs, _ := m["messages"].([]any)
		if len(msgs) != 3 {
			t.Fatalf("messages 长度错: %v", msgs)
		}
		first, _ := msgs[0].(map[string]any)
		if first["role"] != "system" {
			t.Fatalf("developer 未归一化: %v", first["role"])
		}
		if first["content"] != "be terse" || first["extra"] != float64(1) {
			t.Fatalf("同条消息的其他字段丢失: %v", first)
		}
		second, _ := msgs[1].(map[string]any)
		if second["role"] != "user" {
			t.Fatalf("user role 不应变: %v", second)
		}
		if m["temperature"] != 0.3 {
			t.Fatalf("顶层字段丢失: %v", m)
		}
	})

	t.Run("不含 developer 时原样返回", func(t *testing.T) {
		in := `{"model":"gpt-5","messages":[{"role":"user","content":"hi"}]}`
		if string(normalizeChatRoles([]byte(in))) != in {
			t.Fatal("无需归一化时 body 应原样返回")
		}
	})

	t.Run("非法 JSON / 无 messages / 空数组不报错", func(t *testing.T) {
		for _, in := range []string{`not-json`, `{}`, `{"messages":[]}`, `{"messages":"x"}`} {
			if string(normalizeChatRoles([]byte(in))) != in {
				t.Fatalf("输入 %q 应原样返回", in)
			}
		}
	})

	t.Run("单条消息解析失败不影响其他条", func(t *testing.T) {
		in := `{"messages":["broken",{"role":"developer","content":"x"}]}`
		out := string(normalizeChatRoles([]byte(in)))
		if !strings.Contains(out, `"role":"system"`) {
			t.Fatalf("应只改写 developer 那条: %s", out)
		}
		if !strings.Contains(out, `"broken"`) {
			t.Fatalf("broken 条不应被丢弃: %s", out)
		}
	})
}

// TestChatCompletionsNormalizesDeveloperRole 复现线上报障：上游严格反序列化
// 拒收 role=developer（400），网关必须在转发前归一化为 system，保证调用成功。
// (v0.9.4) 推理改用授权官网账号（设备凭证）验证。
func TestChatCompletionsNormalizesDeveloperRole(t *testing.T) {
	var sawDeveloper bool
	fx := setupTest(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, validUpstreamModelsResp)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), `"role":"developer"`) || strings.Contains(string(body), `"role": "developer"`) {
			sawDeveloper = true
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":{"message":"unknown variant developer"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`)
	})
	// 推理用授权官网账号（设备凭证）。
	mkDeviceAccount(t, fx, "dev@test.dev", "u1s1d-dev", 5_000_000)
	prepareDeviceChatFX(t, fx)

	reqBody := `{"model":"deepseek-v4-flash","messages":[{"role":"developer","content":"be short"},{"role":"user","content":"hi"}],"stream":false}`
	req, _ := http.NewRequest(http.MethodPost, fx.ts.URL+"/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Authorization", "Bearer sk-local-test")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if sawDeveloper {
		t.Fatalf("developer 被原样转发到了上游，应归一化为 system")
	}
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("应 200 成功，got %d body=%s", resp.StatusCode, data)
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Choices) == 0 || out.Choices[0].Message.Content != "ok" {
		t.Fatalf("响应内容异常: %+v", out)
	}
}
