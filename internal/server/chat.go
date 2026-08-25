package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/t479842598/u1s12api-go/internal/store"
	"github.com/t479842598/u1s12api-go/internal/upstream"
)

// contextWith 塞一个键值进 context。
func contextWith(r *http.Request, key, val any) context.Context {
	return context.WithValue(r.Context(), key, val)
}

// chatReq 只解出转发所需的最小字段。
type chatReq struct {
	Model         string          `json:"model"`
	Stream        bool            `json:"stream"`
	StreamOptions json.RawMessage `json:"stream_options"`
}

const maxRequestBody = 32 << 20 // 32MB

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	models, _, err := s.fetchModels(r.Context())
	if models == nil {
		if err == nil {
			err = fmt.Errorf("模型列表不可用")
		}
		writeOpenAIError(w, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}
	// err != nil 但返回了过期缓存时照常服务（上游短暂抖动）。
	data := make([]map[string]any, 0, len(models.Data))
	for _, m := range models.Data {
		data = append(data, map[string]any{
			"id":       m.ID,
			"object":   "model",
			"owned_by": "u1s1",
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

// fetchModels 拉取上游模型列表（5 分钟缓存；失败时回退过期缓存）。
func (s *Server) fetchModels(ctx context.Context) (*upstream.ModelsResponse, bool, error) {
	s.modelsMu.Lock()
	defer s.modelsMu.Unlock()
	const ttl = 5 * time.Minute
	if s.modelsCache != nil && time.Since(s.modelsCache.fetchedAt) < ttl {
		return s.modelsCache.resp, true, nil
	}
	cli := s.client()
	if cli == nil {
		if s.modelsCache != nil {
			return s.modelsCache.resp, true, fmt.Errorf("上游客户端不可用，返回过期缓存")
		}
		return nil, false, fmt.Errorf("上游客户端不可用（检查出口代理设置）")
	}
	ks, err := s.pool.Pick()
	if err != nil {
		if s.modelsCache != nil {
			return s.modelsCache.resp, true, nil
		}
		return nil, false, err
	}
	resp, ferr := cli.Models(ctx, ks.Key)
	if ferr != nil {
		var apiErr *upstream.APIError
		if asAPIError(ferr, &apiErr) {
			s.pool.ReportResult(ks.ID, apiErr.StatusCode, apiErr.Body)
		}
		if s.modelsCache != nil {
			return s.modelsCache.resp, true, nil
		}
		return nil, false, ferr
	}
	s.pool.ReportResult(ks.ID, respStatusCodeOK, "")
	s.modelsCache = &modelsCacheEntry{resp: resp, fetchedAt: time.Now()}
	return resp, false, nil
}

const respStatusCodeOK = 200

func asAPIError(err error, target **upstream.APIError) bool {
	if e, ok := err.(*upstream.APIError); ok {
		*target = e
		return true
	}
	return false
}

// handleChatCompletions OpenAI 兼容对话转发：
// 本地 key 鉴权 → 选池内 key → 注入指纹头转发 → 流式/非流式透传 + 计量落库。
func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	localKeyName, _ := r.Context().Value(apiKeyContextKey{}).(string)

	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBody))
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request", "读取请求体失败")
		return
	}
	var req chatReq
	if err := json.Unmarshal(body, &req); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_json", "请求体不是合法 JSON")
		return
	}
	if strings.TrimSpace(req.Model) == "" {
		writeOpenAIError(w, http.StatusBadRequest, "missing_model", "缺少 model 字段")
		return
	}
	forwardBody := body
	// 流式请求补 stream_options.include_usage=true —— 与官方 CLI 行为一致，
	// 同时让本网关能从最后一个 chunk 统计 token 用量。
	if req.Stream && len(req.StreamOptions) == 0 {
		var m map[string]any
		if json.Unmarshal(body, &m) == nil && m != nil {
			m["stream_options"] = map[string]any{"include_usage": true}
			if b, merr := json.Marshal(m); merr == nil {
				forwardBody = b
			}
		}
	}

	started := time.Now()
	maxAttempts := 3
	var lastErrBody string

	for attempt := 0; attempt < maxAttempts; attempt++ {
		ks, err := s.pool.Pick()
		if err != nil {
			writeOpenAIError(w, http.StatusServiceUnavailable, "no_available_key", err.Error())
			return
		}
		cli := s.client()
		if cli == nil {
			writeOpenAIError(w, http.StatusBadGateway, "client_unavailable", "上游客户端不可用（检查出口代理设置）")
			return
		}
		resp, cerr := cli.Chat(r.Context(), ks.Key, forwardBody)
		if cerr != nil {
			var apiErr *upstream.APIError
			if asAPIError(cerr, &apiErr) {
				lastErrBody = apiErr.Body
				s.pool.ReportResult(ks.ID, apiErr.StatusCode, apiErr.Body)
				logger.Warnf("chat 上游错误 key#%d status=%d body=%.200s", ks.ID, apiErr.StatusCode, apiErr.Body)
				// 可重试：key 级故障（无效/额度尽/限流）→ 换下一把；每次失败都落请求记录便于排查。
				if apiErr.StatusCode == 401 || apiErr.StatusCode == 402 || apiErr.StatusCode == 429 {
					s.recordRequest(localKeyName, req.Model, ks.ID, req.Stream, started,
						apiErr.StatusCode, 0, 0, 0, "error", truncate(apiErr.Body, 1000), clientIP(r))
					continue
				}
				s.recordRequest(localKeyName, req.Model, ks.ID, req.Stream, started,
					apiErr.StatusCode, 0, 0, 0, "error", truncate(apiErr.Body, 1000), clientIP(r))
				passthroughUpstreamError(w, apiErr)
				return
			}
			// 网络/代理层错误
			lastErrBody = cerr.Error()
			logger.Warnf("chat 网络错误 key#%d: %v", ks.ID, cerr)
			s.recordRequest(localKeyName, req.Model, ks.ID, req.Stream, started,
				0, 0, 0, 0, "error", truncate(cerr.Error(), 1000), clientIP(r))
			continue
		}

		// 成功拿到响应
		s.pool.ReportResult(ks.ID, resp.StatusCode, "")
		usageIn, usageOut, recErr := s.pipeResponse(w, r, resp, req.Stream)
		tokens := usageIn + usageOut
		if tokens > 0 {
			_ = s.store.TouchUpstreamKey(ks.ID, tokens)
		}
		status := "success"
		errMsg := ""
		if recErr != nil {
			status = "error"
			errMsg = truncate(recErr.Error(), 1000)
		}
		cost := s.estimateCost(req.Model, usageIn, usageOut)
		s.recordRequestFull(localKeyName, req.Model, ks.ID, req.Stream, started,
			resp.StatusCode, usageIn, usageOut, cost, status, errMsg, clientIP(r))
		logger.Infof("chat 完成 key#%d model=%s stream=%v status=%s in=%d out=%d dur=%dms ip=%s",
			ks.ID, req.Model, req.Stream, status, usageIn, usageOut,
			time.Since(started).Milliseconds(), clientIP(r))
		return
	}

	writeOpenAIError(w, http.StatusBadGateway, "all_keys_failed",
		fmt.Sprintf("所有 U1S1 Key 尝试均失败（最后错误: %.300s）", lastErrBody))
}

// pipeResponse 把上游响应透传给客户端；流式时边转发边扫描用量 chunk。
// 返回 input/output tokens。出错时返回 err（此时可能已写入部分响应）。
func (s *Server) pipeResponse(w http.ResponseWriter, r *http.Request, resp *http.Response, stream bool) (int64, int64, error) {
	defer resp.Body.Close()

	ct := resp.Header.Get("Content-Type")
	if ct != "" {
		w.Header().Set("Content-Type", ct)
	}

	if !stream {
		data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		if err != nil {
			writeOpenAIError(w, http.StatusBadGateway, "upstream_read_error", "读取上游响应失败: "+err.Error())
			return 0, 0, err
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(data)))
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(data)
		in, out := parseUsageJSON(data)
		return in, out, nil
	}

	// 流式：SSE 逐行透传 + 扫描 usage
	flusher, canFlush := w.(http.Flusher)
	w.WriteHeader(resp.StatusCode)
	reader := bufio.NewReaderSize(resp.Body, 64<<10)
	var in, out int64
	var scanErr error
	buf := make([]byte, 0, 16<<10)
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			buf = append(buf[:0], line...)
			payload := sseDataPayload(string(line))
			if payload != "" && strings.Contains(payload, `"usage"`) && !strings.Contains(payload, `"usage":null`) {
				if i, o, ok := parseUsageSSE(payload); ok {
					in, out = i, o
				}
			}
			if _, werr := w.Write(buf); werr != nil {
				scanErr = werr
				break
			}
			if canFlush {
				flusher.Flush()
			}
		}
		if readErr != nil {
			if readErr != io.EOF {
				scanErr = readErr
			}
			break
		}
	}
	return in, out, scanErr
}

// sseDataPayload 提取一行 SSE 的 data 内容（非 data 行返回空）。
func sseDataPayload(line string) string {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "data:") {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(line, "data:"))
}

type usageShape struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	InputTokens      int64 `json:"input_tokens"`
	OutputTokens     int64 `json:"output_tokens"`
}

func tokensFrom(u usageShape) (int64, int64) {
	in := u.PromptTokens
	if in == 0 {
		in = u.InputTokens
	}
	out := u.CompletionTokens
	if out == 0 {
		out = u.OutputTokens
	}
	return in, out
}

func parseUsageJSON(data []byte) (int64, int64) {
	var wrapper struct {
		Usage usageShape `json:"usage"`
	}
	if json.Unmarshal(data, &wrapper) == nil {
		return tokensFrom(wrapper.Usage)
	}
	return 0, 0
}

func parseUsageSSE(payload string) (int64, int64, bool) {
	var chunk struct {
		Usage *usageShape `json:"usage"`
	}
	if json.Unmarshal([]byte(payload), &chunk) == nil && chunk.Usage != nil {
		in, out := tokensFrom(*chunk.Usage)
		return in, out, true
	}
	return 0, 0, false
}

// estimateCost 按模型价格估算成本（USD）。价格未知时返回 0。
func (s *Server) estimateCost(model string, inTok, outTok int64) float64 {
	s.modelsMu.Lock()
	mc := s.modelsCache
	s.modelsMu.Unlock()
	if mc == nil {
		return 0
	}
	for _, m := range mc.resp.Data {
		if m.ID == model {
			return float64(inTok)/1e6*m.Price.Input + float64(outTok)/1e6*m.Price.Output
		}
	}
	return 0
}

func (s *Server) recordRequest(keyName, model string, upstreamKeyID int64, stream bool, started time.Time,
	httpStatus int, inTok, outTok int64, cost float64, status, errMsg, ip string) {
	s.recordRequestFull(keyName, model, upstreamKeyID, stream, started, httpStatus, inTok, outTok, cost, status, errMsg, ip)
}

func (s *Server) recordRequestFull(keyName, model string, upstreamKeyID int64, stream bool, started time.Time,
	httpStatus int, inTok, outTok int64, cost float64, status, errMsg, ip string) {
	rec := &store.RequestRecord{
		TS:            time.Now().Unix(),
		APIKeyName:    keyName,
		Model:         model,
		UpstreamKeyID: upstreamKeyID,
		Stream:        stream,
		Status:        status,
		HTTPStatus:    httpStatus,
		InputTokens:   inTok,
		OutputTokens:  outTok,
		TotalTokens:   inTok + outTok,
		CostUSD:       cost,
		DurationMS:    time.Since(started).Milliseconds(),
		Error:         errMsg,
		ClientIP:      ip,
	}
	if _, err := s.store.InsertRequest(rec); err != nil {
		logger.Errorf("请求记录落库失败: %v", err)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// passthroughUpstreamError 原样透传上游错误状态码与错误体。
func passthroughUpstreamError(w http.ResponseWriter, apiErr *upstream.APIError) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(apiErr.StatusCode)
	_, _ = w.Write([]byte(apiErr.Body))
}
