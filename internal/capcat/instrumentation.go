package capcat

import (
	"bytes"
	"compress/flate"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"time"

	"github.com/dop251/goja"
)

// solveInstrumentation 求解 instrumentation 挑战：解压 blob → 用 goja 以最小 DOM stub
// 执行其中的确定性算术程序 → 返回 {i: nonce, state: {…}, ts: now}（redeem 的 solutions 项）。
func solveInstrumentation(blob string) (json.RawMessage, error) {
	raw, err := base64.StdEncoding.DecodeString(blob)
	if err != nil {
		return nil, fmt.Errorf("instrumentation: base64 解码失败: %w", err)
	}
	js, err := inflateRaw(raw)
	if err != nil {
		return nil, fmt.Errorf("instrumentation: 解压失败: %w", err)
	}
	section, err := extractArithSection(string(js))
	if err != nil {
		return nil, fmt.Errorf("instrumentation: %w", err)
	}
	state, err := runArithSection(section)
	if err != nil {
		return nil, fmt.Errorf("instrumentation: %w", err)
	}
	nonceRe := regexp.MustCompile(`nonce: "([0-9a-f]+)"`)
	m := nonceRe.FindSubmatch(js)
	if m == nil {
		return nil, fmt.Errorf("instrumentation: blob 中未找到 nonce")
	}
	out := map[string]any{
		"i":     string(m[1]),
		"state": state,
		"ts":    time.Now().UnixMilli(),
	}
	rawOut, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("instrumentation: 序列化失败: %w", err)
	}
	return rawOut, nil
}

// inflateRaw deflate-raw（wbits=-15）解压。
func inflateRaw(src []byte) ([]byte, error) {
	r := flate.NewReader(bytes.NewReader(src))
	defer r.Close()
	return io.ReadAll(io.LimitReader(r, 2<<20))
}

var init4Re = regexp.MustCompile(`(?:var [A-Za-z0-9_$]+=\d+;){4}`)

// extractArithSection 截取算术段：从 4 个连续 var 初始化开始，到最后一个 return 语句结束。
func extractArithSection(js string) (string, error) {
	loc := init4Re.FindStringIndex(js)
	if loc == nil {
		return "", fmt.Errorf("未找到算术段起点（4 个 var 初始化）")
	}
	start := loc[0]
	ret := lastIndex(js, "return ")
	if ret < 0 {
		return "", fmt.Errorf("未找到算术段终点（return）")
	}
	end := indexAfter(js, ";", ret)
	if end < 0 {
		return "", fmt.Errorf("算术段终点不完整")
	}
	section := js[start:end]
	if len(section) == 0 || len(section) > 64<<10 {
		return "", fmt.Errorf("算术段长度异常: %d", len(section))
	}
	return section, nil
}

// runArithSection 用 goja 执行算术段（IIFE 包裹以支持其顶层 return），返回 4 个 state 值。
func runArithSection(section string) (map[string]int64, error) {
	script := fmt.Sprintf(`function makeNode(parent){
  return { parentNode: parent, innerText: '', children: [], style: {},
    appendChild: function(c){ c.parentNode = this; this.children.push(c); },
    removeChild: function(c){ this.children = this.children.filter(function(x){ return x !== c; }); },
    get lastElementChild(){ return this.children.length ? this.children[this.children.length-1] : null; } };
}
var document = { body: makeNode(null), createElement: function(){ return makeNode(null); } };
var navigator = { userAgent: %q };
(function(){ %s })()`, userAgent, section)

	vm := goja.New()
	v, err := vm.RunString(script)
	if err != nil {
		return nil, fmt.Errorf("goja 执行失败: %w", err)
	}
	obj := v.ToObject(vm)
	state := make(map[string]int64, 4)
	for _, key := range obj.Keys() {
		n := obj.Get(key).ToInteger()
		state[key] = n
	}
	if len(state) != 4 {
		return nil, fmt.Errorf("算术段返回字段数异常: %d", len(state))
	}
	return state, nil
}

func lastIndex(s, sub string) int {
	for i := len(s) - len(sub); i >= 0; i-- {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func indexAfter(s, sub string, from int) int {
	for i := from; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
