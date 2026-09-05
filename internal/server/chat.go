package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"regexp"
	"sort"
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
	forwardBody := normalizeChatRoles(body)
	// 流式请求补 stream_options.include_usage=true —— 与官方 CLI 行为一致，
	// 同时让本网关能从最后一个 chunk 统计 token 用量。
	if req.Stream && len(req.StreamOptions) == 0 {
		var m map[string]any
		if json.Unmarshal(forwardBody, &m) == nil && m != nil {
			m["stream_options"] = map[string]any{"include_usage": true}
			if b, merr := json.Marshal(m); merr == nil {
				forwardBody = b
			}
		}
	}

	// 设备凭证通道：(v0.9.4) 推理一律只用已授权官网账号（设备登录凭证），不再用旧版 u1s1- API Key。
	// 上游 u1s1 已关闭旧版 API Key 的推理通道（403 u1s1_client_only），继续用只会 403 并有账号封禁风险。
	started := time.Now()
	served, accountsExisted, devHint := s.tryDeviceChatCompletion(w, r, localKeyName, &req, forwardBody, started)
	if served {
		return
	}

	// 设备通道不可用：无论有无账号，都不回退 Key 池，返回清晰错误。
	if accountsExisted {
		// 有账号但全部不可用（额度耗尽 / 上游限流 / 网络异常 / 凭证失效）。
		logger.Warnf("设备通道不可用：%s", truncate(devHint, 200))
		writeOpenAIError(w, http.StatusServiceUnavailable, "device_channel_unavailable",
			truncate(devHint, 300)+"; 请稍后重试或检查官网账号额度")
		return
	}
	// 没有配置任何授权官网账号。
	logger.Warnf("未配置授权官网账号，推理请求被拒绝（u1s1 已禁止旧版 API Key 用于推理）")
	writeOpenAIError(w, http.StatusServiceUnavailable, "no_authorized_account",
		"未配置授权官网账号（设备登录凭证），无法调用模型。上游 u1s1 已禁止旧版 API Key 用于推理；请在后台添加并授权一个官网账号后重试")
}

// deviceQuotaSortKey 设备账号的剩余额度排序键（降序：额度最多者优先直接调用）。
// login_checkin_remaining 是登录打卡加量包剩余；未知(-1)视为最后（不清楚剩余，不优先）。
func deviceQuotaSortKey(a *store.Account) int64 {
	if a.LoginCheckinRemaining < 0 {
		return math.MaxInt64
	}
	return a.LoginCheckinRemaining
}

// markDeviceExhausted 记录某设备账号当日触发 quota_exceeded，冷却到次日北京时间 0 点。
func (s *Server) markDeviceExhausted(accountID int64, until time.Time) {
	s.deviceQuotaExhaustedMu.Lock()
	defer s.deviceQuotaExhaustedMu.Unlock()
	if s.deviceQuotaExhausted == nil {
		s.deviceQuotaExhausted = map[int64]time.Time{}
	}
	s.deviceQuotaExhausted[accountID] = until
}

// deviceIsExhausted 判断设备账号是否处于当日额度耗尽冷却期。
func (s *Server) deviceIsExhausted(accountID int64) bool {
	s.deviceQuotaExhaustedMu.Lock()
	defer s.deviceQuotaExhaustedMu.Unlock()
	until, ok := s.deviceQuotaExhausted[accountID]
	return ok && time.Now().Before(until)
}

// bestDeviceCredential 返回当前额度最高的可用设备账号的凭证与 attestation 令牌。
// 供「模型测试」等需要单次设备凭证调用的路径使用；无可用账号时返回可读的拒绝原因。
func (s *Server) bestDeviceCredential(ctx context.Context) (attestation string, cred *upstream.DeviceCredential, err error) {
	accounts, err := s.store.ListAuthorizedEnabledAccounts()
	if err != nil || len(accounts) == 0 {
		return "", nil, fmt.Errorf("未配置授权官网账号（设备登录凭证），无法调用模型")
	}
	candidates := make([]*store.Account, 0, len(accounts))
	for _, a := range accounts {
		if s.deviceIsExhausted(a.ID) {
			continue
		}
		candidates = append(candidates, a)
	}
	if len(candidates) == 0 {
		return "", nil, fmt.Errorf("所有设备账号当日额度已耗尽（北京时间 0 点恢复）")
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return deviceQuotaSortKey(candidates[i]) > deviceQuotaSortKey(candidates[j])
	})
	for _, acc := range candidates {
		dc, cerr := s.accountCredential(acc)
		if cerr != nil {
			logger.Warnf("设备凭证解析失败 account=%s: %v", acc.Email, cerr)
			continue
		}
		return s.attest.Token(ctx, dc), dc, nil
	}
	return "", nil, fmt.Errorf("所有设备账号的设备凭证解析失败")
}

// tryDeviceChatCompletion 尝试用已授权官网账号的设备凭证（DPoP + 指纹头）转发对话。
//
// 返回三元组：
//
//	served        —— 已写出响应（成功，或已被请求级错误透传），调用方直接返回；
//	accountsExisted —— 是否存在已授权账号（区分「没配账号」与「有账号但全不可用」）；
//	hint          —— 当 accountsExisted 但未 served 时的人类可读原因，供调用方构造清晰错误。
//
// 多账号调度策略：按剩余额度降序——**有额度的账号直接调用**（额度最多者优先）；
// 仅当其触发 quota_exceeded（当日额度耗尽）被标记冷却后，本请求切到下一个
// 有额度的账号，后续请求不再调用已冷却账号。
//
// 重要（v0.9.3）：上游已关闭旧版 u1s1- API Key 推理通道（403 u1s1_client_only），
// 全部设备账号不可用时应返回 accountsExisted=true，让调用方返回清晰的设备通道错误，
// **而不是回退 Key 池**（回退只会 403，且有账号封禁风险）。
func (s *Server) tryDeviceChatCompletion(w http.ResponseWriter, r *http.Request, localKeyName string, req *chatReq, forwardBody []byte, started time.Time) (served bool, accountsExisted bool, hint string) {
	accounts, err := s.store.ListAuthorizedEnabledAccounts()
	if err != nil || len(accounts) == 0 {
		return false, false, ""
	}
	lastHint := "设备通道不可用"

	// 过滤已标记当日额度耗尽的账号。
	candidates := make([]*store.Account, 0, len(accounts))
	for _, a := range accounts {
		if s.deviceIsExhausted(a.ID) {
			continue
		}
		candidates = append(candidates, a)
	}
	if len(candidates) == 0 {
		return false, true, "所有设备账号当日额度已耗尽（北京时间 0 点恢复），请稍后重试"
	}

	// 尝试顺序：按剩余额度降序——额度最多的账号直接调用；
	// 触发 quota_exceeded 被标记冷却后，轮询到下一个有额度的账号。
	sort.SliceStable(candidates, func(i, j int) bool {
		return deviceQuotaSortKey(candidates[i]) > deviceQuotaSortKey(candidates[j])
	})
	order := candidates

	dc := s.deviceClient()
	for _, acc := range order {
		if s.deviceIsExhausted(acc.ID) {
			continue // 本请求内可能刚被标记
		}
		cred, cerr := s.accountCredential(acc)
		if cerr != nil {
			logger.Warnf("设备凭证解析失败 account=%s: %v", acc.Email, cerr)
			continue
		}
		// v1.3.0：官方签名代理会向设备凭证请求注入网关签发的 x-u1s1-attestation。
		// 取不到令牌时返回空串、照常发请求（降级不阻断）。
		att := s.attest.Token(r.Context(), cred)
		resp, derr := dc.DeviceChat(r.Context(), cred, forwardBody, att)
		if derr != nil {
			var apiErr *upstream.APIError
			if asAPIError(derr, &apiErr) {
				logger.Warnf("设备通道上游错误 account=%s status=%d body=%.200s", acc.Email, apiErr.StatusCode, apiErr.Body)
				// 403：官方 1.7.1 定性为「封禁/停用/设备不受信任」，重登也没用（CLI 直接 exit(1)）。
				// 对应动作：停用该账号 + Bark 告警 + 透传原因 + **停止换下一个账号重试同一请求**。
				// 例外：2026-09-05 起上游新增的「客户端完整性审查」（client_integrity_review）
				// 文案是「请升级并重新登录 u1s1」——重新授权可恢复，所以标记 relogin 供后台引导。
				if upstream.DeviceNotTrusted(apiErr.StatusCode, apiErr.Body) {
					s.attest.Invalidate(cred)
					if upstream.IntegrityReviewRelogin(apiErr.StatusCode, apiErr.Body) {
						reason := fmt.Sprintf("网关要求重新登录（403 完整性审查）：%s", truncate(apiErr.Body, 200))
						if derr := s.store.MarkAccountNeedsRelogin(acc.ID, reason); derr != nil {
							logger.Warnf("标记需重新登录账号失败 account=%s: %v", acc.Email, derr)
						}
						logger.Warnf("设备账号被网关要求重新登录（403），已停用并标记 relogin account=%s：%s", acc.Email, truncate(apiErr.Body, 200))
						s.alertDeviceNeedsRelogin(acc.Email, apiErr.Body)
					} else {
						reason := fmt.Sprintf("网关拒绝（403）：%s", truncate(apiErr.Body, 200))
						if derr := s.store.DisableAccountByGateway(acc.ID, reason); derr != nil {
							logger.Warnf("停用不受信任账号失败 account=%s: %v", acc.Email, derr)
						}
						logger.Warnf("设备账号被网关判为不受信任（403），已停用 account=%s：%s", acc.Email, truncate(apiErr.Body, 200))
						s.alertDeviceNotTrusted(acc.Email, apiErr.Body)
					}
					s.recordRequest(localKeyName, req.Model, 0, req.Stream, started, apiErr.StatusCode, 0, 0, 0, "error", truncate(apiErr.Body, 1000), clientIP(r))
					passthroughUpstreamError(w, apiErr)
					return true, true, ""
				}
				// 401：设备被移除/换过钥匙。丢令牌缓存 + 标记需重新授权（不动 enabled），继续下一个账号。
				if upstream.DeviceCredentialRetired(apiErr.StatusCode) {
					s.attest.Invalidate(cred)
					if derr := s.store.MarkAccountUnauthorized(acc.ID, "设备被网关移除或更换过钥匙，需重新授权"); derr != nil {
						logger.Warnf("标记账号需重新授权失败 account=%s: %v", acc.Email, derr)
					}
					logger.Warnf("设备凭证已失效（401），标记需重新授权 account=%s", acc.Email)
					s.recordRequest(localKeyName, req.Model, 0, req.Stream, started, apiErr.StatusCode, 0, 0, 0, "error", truncate(apiErr.Body, 1000), clientIP(r))
					lastHint = fmt.Sprintf("设备账号 %s 的凭证已失效（需在后台重新授权）", acc.Email)
					continue
				}
				s.recordRequest(localKeyName, req.Model, 0, req.Stream, started, apiErr.StatusCode, 0, 0, 0, "error", truncate(apiErr.Body, 1000), clientIP(r))
				// 请求级错误（内容审查 / 未知模型 / 请求体非法）：由请求体决定，换账号必然同样失败。
				// 原先这里无条件 continue，一次 400 会打穿全部设备账号再回退 Key 池，
				// 白烧额度、拉长延迟，并在官方风控里留下「同内容跨多账号重复请求」特征。
				// 现在立即透传、停止轮换、不回退 Key 池（同一请求体，Key 池也必然失败）。
				if upstream.RequestScopedError(apiErr.StatusCode, apiErr.Body) {
					if upstream.ContentModerationRejected(apiErr.StatusCode, apiErr.Body) {
						logger.Warnf("设备通道被上游内容审查拒绝（请求级，停止轮换）account=%s：需调整输入文本，换账号无效", acc.Email)
					}
					passthroughUpstreamError(w, apiErr)
					return true, true, ""
				}
				// 网关级错误（如 503 model_unavailable + Retry-After）：与用哪把凭证无关，换账号无益，
				// 立即透传并保留 Retry-After，让客户端按官方退避时长重试（u1s1-cli 1.3.1 同期服务端变更）。
				if !upstream.CredentialScopedError(apiErr.StatusCode, apiErr.Body) {
					logger.Warnf("设备通道网关级错误（停止轮换，透传）account=%s status=%d：%s", acc.Email, apiErr.StatusCode, truncate(apiErr.Body, 120))
					passthroughUpstreamError(w, apiErr)
					return true, true, ""
				}
				if upstream.QuotaSignal(apiErr.StatusCode, apiErr.Body) {
					// 当日额度耗尽：标记该设备冷却，切到下一个有额度的账号。
					s.markDeviceExhausted(acc.ID, upstream.NextBeijingMidnight(time.Now()))
					logger.Infof("设备账号当日额度耗尽，标记冷却 account=%s（北京时间 0 点恢复）", acc.Email)
					lastHint = fmt.Sprintf("设备账号 %s 当日额度耗尽（北京时间 0 点恢复）", acc.Email)
				} else {
					lastHint = fmt.Sprintf("设备账号 %s 上游返回 %d：%s", acc.Email, apiErr.StatusCode, truncate(apiErr.Body, 120))
				}
				continue
			}
			logger.Warnf("设备通道网络错误 account=%s: %v", acc.Email, derr)
			s.recordRequest(localKeyName, req.Model, 0, req.Stream, started, 0, 0, 0, 0, "error", truncate(derr.Error(), 1000), clientIP(r))
			lastHint = fmt.Sprintf("设备账号 %s 网络异常：%v", acc.Email, derr)
			continue
		}

		// 成功透传。
		usageIn, usageOut, recErr := s.pipeResponse(w, r, resp, req.Stream)
		tokens := usageIn + usageOut
		if tokens > 0 {
			_ = s.store.TouchAccount(acc.ID, tokens)
		}
		status := "success"
		errMsg := ""
		if recErr != nil {
			status = "error"
			errMsg = truncate(recErr.Error(), 1000)
		}
		cost := s.estimateCost(req.Model, usageIn, usageOut)
		s.recordRequestFull(localKeyName, req.Model, 0, req.Stream, started, resp.StatusCode, usageIn, usageOut, cost, status, errMsg, clientIP(r))
		logger.Infof("设备通道 chat 完成 account=%s model=%s stream=%v status=%s in=%d out=%d",
			acc.Email, req.Model, req.Stream, status, usageIn, usageOut)
		return true, true, ""
	}

	// 全部设备账号失败/已耗尽 → accountsExisted=true，由调用方决定（不自动回退 Key 池，
	// 因为上游已对旧版 u1s1- Key 关闭推理通道，回退只会有 403 + 封号风险）。
	return false, true, lastHint
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

// retryAfterDelayRE / retryAfterDateRE 对应 RFC 9110 的两种合法 Retry-After 形式。
var (
	retryAfterDelayRE = regexp.MustCompile(`^\d{1,10}$`)
	retryAfterDateRE  = regexp.MustCompile(`^[A-Za-z]{3}, \d{2} [A-Za-z]{3} \d{4} \d{2}:\d{2}:\d{2} GMT$`)
)

// safeRetryAfter 判定上游 Retry-After 能否安全透传：只接受 delay-seconds 或 HTTP-date，
// 其余（含 CR/LF、控制字符、超长、垃圾值）一律丢弃，避免响应头注入与客户端被误导。
func safeRetryAfter(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if retryAfterDelayRE.MatchString(v) || retryAfterDateRE.MatchString(v) {
		return v
	}
	return ""
}

// passthroughUpstreamError 把上游错误原样透传给客户端。
// 保留 Retry-After：Gateway 对可重试的 503 model_unavailable 会下发该退避头（u1s1-cli 1.3.1 同期服务端变更）。
func passthroughUpstreamError(w http.ResponseWriter, apiErr *upstream.APIError) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if ra := safeRetryAfter(apiErr.RetryAfter); ra != "" {
		w.Header().Set("Retry-After", ra)
	}
	w.WriteHeader(apiErr.StatusCode)
	_, _ = w.Write([]byte(apiErr.Body))
}
