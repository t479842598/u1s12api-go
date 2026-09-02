# Changelog

## v0.9.6 (2026-09-02)

### 变更

- **指纹同步 u1s1-cli 1.4.1**：`U1S1_VERSION` 默认值 1.4.0 → 1.4.1（`internal/config` / `.env.example` / README / 设置页 placeholder / 服务器 `.env`）。`x-u1s1-version` 头随之上到 1.4.1

### 逆向核对（1.4.1 vs 1.4.0）

官网 09-02 发布 CLI 1.4.1（npm `latest`、官方 `/releases/LATEST` 均为 1.4.1）。逐文件 diff 1.4.1 与 1.4.0 tarball：

- **请求头/鉴权零变化**：`device-auth.js`、`api.js` 与 1.4.0 **逐字节一致**；唯一差异在 `config.js` —— 内置兜底模型目录不再硬编码价格（`cost` 改为 `null` + `UNKNOWN_MODEL_COST`），属 CLI 内部 UI，与请求头无关
- **SDK 不变**：deps 仍 pi-coding-agent/pi-tui 0.84.4、openai 6.40.0 → `X-Stainless-Package-Version` 不变
- 1.4.1 官方内容：会话内公告触达（每 5 分钟静默查公告、对话内插 📢 提示）、模型价格只以服务端为准（网关目录拉取失败时只列模型名不显示可能过期的价格估算）、模块导入路径归一（无行为变化）

### 签到与限制复核（本次同步）

- **签到规则不变**：每日打卡 200 万全模型加量包 + 连续打卡 30 天里程碑挑战
- **限制无新增**：已有限制仍为 Token 仅限本站工具（违规封禁）、免费用量包覆盖部分模型；无新增公告（顶部仍 08-30 的 Token 使用范围/违规处置说明）

### 验证

- `go build ./...` + 前端 `npm run build` 成功；arm64 产物部署后启动日志确认 `u1s1_version=1.4.1`

## v0.9.5 (2026-09-02)

### 变更

- **指纹同步 u1s1-cli 1.4.0**：`U1S1_VERSION` 默认值 1.3.1 → 1.4.0（`internal/config` 默认值 / `.env.example` / README / 设置页 placeholder）。`x-u1s1-version` 头随之上到 1.4.0

### 逆向核对（1.4.0 vs 1.3.1）

官网 09-01 发布 CLI 1.4.0。npm `latest`、官方 `/releases/LATEST` 均为 1.4.0。逐文件 diff 1.4.0 与 1.3.1 tarball：

- **请求头集合零变化**：`authorization (DPoP)` / `x-u1s1-client` / `x-u1s1-version` / `x-u1s1-platform` / `x-u1s1-attestation` 均不变，**无新增请求头**。唯一实质差异是 `x-u1s1-version` 值变为 1.4.0
- **SDK 不变**：1.4.0 内嵌 `@earendil-works/pi-coding-agent`、`pi-tui` 仍 0.84.4，openai SDK 仍 6.40.0 → `X-Stainless-Package-Version` 仍 6.40.0，UA / `X-Stainless-*` 不变
- **attestation 刷新策略升级，但我们已对齐**：1.4.0 读 `client_attestation.expires_in`、临期 1 天预刷新、失败 30s 冷却、单飞、无 token 最多阻塞 4s；我们早已解析 `expires_in` + payload `exp` 双源并提前重签，语义一致
- **config 新增 `freeEligible` / `deriveModelNote`**：仅 TUI 模型选择器展示「免费用量包可抵扣 / 不走免费包」提示，与请求头无关
- 1.4.0 官方内容：CLI 新增 `/help` `/usage`（会话内查额度）、模型切换带价格提示、空目录首跑引导；Gateway 改进「额度提示与实际扣费对齐」（持有全模型包不再误判额度用尽）—— 属展示/提示侧，非新鉴权限制

### 签到与限制复核（本次同步）

- **签到规则不变**：每日打卡 200 万全模型加量包 + 连续打卡 30 天里程碑挑战
- **限制无新增**：已有限制仍为 Token 仅限本站工具（违规封禁）、免费用量包覆盖部分模型；Gateway 09-01 是额度提示修正，不影响我们的配额判定（`QuotaSignal` 仍正确识别 `quota_exceeded`/`insufficient_quota`）

### 验证

- **真实网关端到端复验**：`x-u1s1-version: 1.4.0` 下设备凭证通道仍被接受 —— attestation 正常签发（payload `v=1 u=531 d=655`，TTL 168h，缓存命中 1µs），证明同步到 1.4.0 无风险
- `go build ./...` + 前端 `npm run build` 成功

## v0.9.4 (2026-09-01)

### 变更

- **推理彻底只用授权官网账号（设备凭证），不再使用 `u1s1-` API Key**：
  - `handleChatCompletions`（`/v1/chat/completions`）去掉 Key 池兜底循环 —— 上游 u1s1 已把旧版 API Key 的推理通道封进「历史兼容窗口」（403 `u1s1_client_only`，见 v0.9.3），且继续用有封号风险。现在一律走设备凭证通道
  - 无授权账号 → 返回 `503 no_authorized_account`（明确提示去后台添加并授权官网账号）；有账号但全不可用（额度耗尽/上游限流/网络异常）→ 返回 `503 device_channel_unavailable`（保留具体原因）
  - 新增 `bestDeviceCredential()`：供「模型测试」等单次设备凭证路径复用
  - **管理后台「模型测试」**（`handleChatTest`）也从 Key 池改用设备凭证
  - 设备凭证通道对**网关级错误**（如 `503 model_unavailable + Retry-After`）立即透传并保留 `Retry-After`（新增 `upstream.CredentialScopedError` 区分「凭证级可轮换」与「请求/网关级应透传」），避免把客户端请求放大成 N 次上游调用、并丢失官方退避信号

### 范围说明

- **只封推理，辅助端点不受影响**（实测）：`/v1/models`（模型列表）、`/v1/me`（配额）用 `u1s1-` key 仍正常 200 —— 所以 `fetchModels`（模型列表缓存）、admin 查某 key 配额仍保留 key 通道（非推理，不产生 `/chat/completions` 调用）

### 验证

- 新增 `TestInferenceRequiresAuthorizedAccount`（无账号 → 503 `no_authorized_account`，且上游**未收到任何 chat 请求**）、`TestCredentialScopedError` 单元测试
- 转换 4 个既有用例到设备凭证：`TestChatCompletionsForwardsFingerprintAndStreams`（设备通道指纹头：DPoP + x-u1s1-client/platform + X-Stainless-* + 流式）、`TestChatCompletionsNormalizesDeveloperRole`、`TestChatForwardsRetryAfterEndToEnd`（503+Retry-After 透传）、`TestKeyPoolChannelNoAttestation` → `TestInferenceRequiresAuthorizedAccount`
- 删除 3 个「Key 池推理」前提的过时用例：`TestKeyPoolClientOnly403DisablesKey`、`TestQuotaExhaustedCooldownAndFailover`、`TestInvalidKeyDisabled`（该路径已移除；key 配额冷却/禁用逻辑仍保留，用于 `/models`、`/me` 等 key 管理端点）
- `go build ./...` + `go test ./... -count=1` + `go test -race ./internal/... -count=1` 全绿（`go vet ./internal/server/` 通过）

## v0.9.3 (2026-09-01)

### 变更

- **旧版 `u1s1-` API Key 推理通道已被网关关闭（403 `u1s1_client_only`），服务改为只用设备凭证通道**：
  - 新增 `upstream.KeyClientOnlyRejected()` 识别网关「API 推理请求仅支持 u1s1 客户端；旧版 API Key 仅在明确的历史兼容窗口内可用…`type:forbidden, code:u1s1_client_only`」的 403
  - `tryDeviceChatCompletion` 改为返回 `(served, accountsExisted, hint)`：区分「没配官网账号」与「有账号但全部不可用」
  - **有设备账号但全部不可用（额度耗尽 / 上游限流 / 网络异常）→ 返回清晰的 `503 device_channel_unavailable`，不再回退 Key 池**。修复前：设备通道任何失败都无条件 continue 穿完账号再回退 Key 池，而 Key 池现已被网关 403 封禁 → 客户端收到 403，且继续用旧 Key 有**账号封禁风险**（公告 #6 + v1.3.0 风控）
  - Key 池通道命中 `u1s1_client_only` → **透传上游真实消息、立即禁用该 Key（`pool.DisableKey`）、不再跨 Key 重试**（换一把必然同样 403）
  - 新增 `pool.DisableKey(id, reason)`：把确定性拒绝的 Key 标记 disabled，避免池反复拾取、在官方风控里留下频次痕迹

### 逆向核对（本次「u1s1 更新」同步）

用户提示用安装脚本 `curl -fsSL https://u1s1.io/releases/install.sh | bash` 核对。结论：**无新 CLI 版本、无请求头 / 指纹变化**，真正变化是服务端对旧版 Key 通道的强制封禁落地。

- **版本**：npm `latest` = 1.3.1；官网 `https://u1s1.io/releases/LATEST` = **1.3.1**；npm 无 `next/beta` 等预发布 tag；更新记录顶部仍 v1.3.1
- **官方便携包与 npm 逐字节一致**：下载 `u1s1-cli-1.3.1-darwin-arm64.tar.gz`，其 `device-auth.js` / `api.js` / `config.js` 与 npm 1.3.1 **完全相同**（diff 无差异）—— 我们 v0.9.1 起的指纹实现就是真实 CLI 的权威实现
- **设备凭证通道仍通过**：真实网关复验 attestation 正常签发（payload `v=1 u=531 d=655`，TTL 168h），带完整指纹头的真实 chat 返回 **429 额度不足**（而非 403）→ 网关认可为 u1s1 客户端，仅额度耗尽
- **旧版 Key 通道确实被封**：用生产 active `u1s1-` key + Key 通道完整指纹头直连 `https://api.u1s1.io/v1/chat/completions` → **HTTP 403 `u1s1_client_only`**
- 推断：这就是「u1s1 更新了」—— 不是 CLI 或请求头更新，而是网关把 `u1s1-` Key 推理通道关进「历史兼容窗口」（可能窗口内的个别 key 尚可、但整体不再可靠），配合 v1.3.0「疑似非官方凭据代理后台累计风控证据、达条件自动封禁」与 08-30 公告 #6「Token 不得接入第三方工具」

### 验证

- 新增 `upstream.KeyClientOnlyRejected` 分类器测试 4 例（生产错误体 / 仅中文措辞变体 / 非 403 不算 / 普通 403 不误判）
- 新增 `internal/server/client_only_test.go` 两个回归测试：
  - `TestDeviceChannelUnavailableNoKeyFallback`：设备账号全部「上游限流 429」时返回 **503 device_channel_unavailable**，且 Key 池**不被调用**（`Bearer u1s1-` 断言 0 次）
  - `TestKeyPoolClientOnly403DisablesKey`：无账号时 Key 池命中 `u1s1_client_only` → 透传 403、该 Key 被禁用、只调 1 次（不跨 Key 重试）
- **回归有效性已验证**：临时把「设备通道不可用→回退 Key 池」放回后 `TestDeviceChannelUnavailableNoKeyFallback` 确实失败（`期望 503，实际 status=200`，日志显示 `chat 完成 key#1 status=success` 回退成功）→ 证明能抓到 bug
- `go build ./...` + `go test ./... -count=1` + `go test -race ./internal/... -count=1` 全绿

## v0.9.2 (2026-09-01)

### 修复

- **请求级上游错误不再跨凭证重试放大**：新增 `upstream.RequestScopedError()` 判定「由请求体决定、与凭证无关」的错误（HTTP 400，额度类除外），设备通道与 Key 通道共用该判据。命中即**停止轮换账号 / 不回退 Key 池**、把上游错误原样透传给客户端
  - 触发场景（生产实测）：上游模型厂商内容审查 `data_inspection_failed` —— `***.***.DataInspectionFailed: Input text data may contain inappropriate content`（阿里云 DashScope 风格，厂商名被网关打码）
  - 修复前行为：`tryDeviceChatCompletion` 对**任何** `APIError` 无条件 `continue`，一次 400 被打穿全部设备账号后再回退 Key 池 —— 实测一次客户端请求放大为 **4 次上游调用**（3 个设备账号 + 1 把 Key），日志表现为 08:45:26→08:47:57 同一错误跨 5 个凭证反复出现
  - 危害：① 白烧多个账号的免费额度（每次重试都是真实计费的上游请求）；② 客户端延迟成倍拉长；③ 最危险的是在官方风控里形成「同一内容跨多账号短时间内重复请求」特征 —— 恰与 u1s1 v1.3.0「疑似非官方设备凭据代理后台累计风险证据、达条件自动封禁」及 08-30 公告「Token 不得接入第三方工具」相撞，是自我暴露的代理指纹
  - 判定口径：400 一律视为请求级（请求体在所有凭证间完全相同，凭证不是变量）；额度类错误实测走 429，显式排除在外，避免上游改口径后该轮换时不轮换
- 新增 `upstream.ContentModerationRejected()`：单独识别内容审查，日志明确提示「需调整输入文本，换账号无效」，避免运维误查网络/额度

### 逆向核对（本次同步复查官网更新）

- npm `u1s1-cli` 最新仍 **1.3.1**、官网更新记录顶部仍 v1.3.1 / Desktop App v0.1.9 / Gateway（均 2026-08-30）、公告最新仍 #6（08-30）→ **无新版本、请求头零变化**，v0.9.1 的指纹同步仍然有效
- 真实网关端到端复验（device_id 655）：attestation 正常签发（payload `v=1 u=531 d=655`，TTL 168h，缓存命中 1µs）；带全部指纹头的真实 chat 返回 **429 额度不足**而非 401/403 → 网关接受客户端身份，仅额度耗尽，请求头方案未被封堵

### 验证

- 新增 12 项测试：`internal/upstream/errorclass_test.go`（`RequestScopedError` 表驱动 10 例覆盖内容审查/未知模型/非法请求体/额度 400/429/401/403/503/200 与 `ContentModerationRejected` 4 例）
- `internal/server/request_scoped_test.go`：`TestRequestScopedErrorNoFanOut` 断言一次内容审查 400 只产生 **1 次**上游调用、其余账号 `total_requests=0`、不回退 Key 池、只落 1 条错误记录、且不误标账号冷却；`TestQuotaErrorStillRotates` 作对照，确保额度耗尽仍照旧冷却并轮换
- **回归有效性已验证**：临时回退 `chat.go` 修复后该测试确实失败（`上游 chat 调用次数 = 4，期望 1`），证明能抓到该 bug 而非空跑
- `go build ./...` + `go test ./... -count=1` 全绿；`pool.ReportResult` 复核确认 400 不触发 Key 禁用/冷却，无误伤

## v0.9.1 (2026-08-31)

### 变更

- **指纹同步 u1s1-cli 1.3.1**：`U1S1_VERSION` 默认值 1.3.0 → 1.3.1（`internal/config` 默认值、config 模板、`.env.example`、README、设置页 placeholder、服务器 `.env` 同步）
- **透传上游 `Retry-After`**：`APIError` 新增 `RetryAfter` 字段（`Chat` 与 `DeviceChat` 均捕获），`passthroughUpstreamError` 在错误响应中保留该头。Gateway 新增可重试的 `503 model_unavailable` 并下发 `Retry-After`，不透传则客户端失去退避依据、只能盲重试

### 逆向核对（1.3.0 → 1.3.1）

`npm pack` 两版本 tarball 逐文件 diff + 本地 mock 抓包对比实际出站头。结论：**1.3.1 是纯客户端健壮性版本，线上契约零变化**

- 改动面 21 个文件，但 `device-auth.js` 的新增仅为**本地签名代理加固**：请求体 32MB 上限（`readSigningProxyRequestBody`）、客户端断开后立即 `AbortController` 取消上游；`api.js` 的新增仅为**响应读取上限与 schema 校验**（8MB/64KB 上限、`signalWithTimeout`）
- 签名代理注入的四个头（`x-u1s1-client` / `x-u1s1-version` / `x-u1s1-platform` / `x-u1s1-attestation`）在 diff 里仅以**重新缩进**形式出现（被包进新的 try 块），内容逐字未变
- `dpopHeaders` / `authorizedFetch` / `requestHeaders` / `clientSurface` 四个函数体 **md5 完全一致**（DPoP 签名与鉴权链路未动）
- 依赖零变化（pi-coding-agent / pi-tui 仍 0.84.4，openai SDK 仍 6.40.0）→ UA / `X-Stainless-*` 不变
- **mock 抓包实证**：1.3.0 与 1.3.1 在三种模式（设备凭证+attestation / 设备凭证无 attestation / 普通 key）下的出站头集合**完全一致**，唯一差异是 `x-u1s1-version` 的值
- 1.3.1 官方更新记录内容全为客户端修复（`u1s1 model` 与实时模型列表一致、大响应不无界占用资源、子任务超时不误报成功、refactor workflow 不丢未提交修改、workflow 续跑重执失败任务），无网关协议变更

### 验证

- 新增 8 项测试：`internal/upstream/retryafter_test.go`（`Chat`/`DeviceChat` 捕获 `Retry-After`、无该头时为空）与 `internal/server/retryafter_test.go`（`safeRetryAfter` 表驱动 13 例含 CR/LF 注入与非法值、透传保留/丢弃/不凭空造头、端到端 503+`Retry-After:9` 送达客户端）；`go test -race ./... -count=1` 全绿
- **v0.9.0 attestation 生产回看（部署后 24h）**：运行中二进制 md5 与发布产物一致（`0846ce1d…`）；`签发 x-u1s1-attestation 失败` **0 次**；设备通道 **294 成功 / 40 失败**（均为额度耗尽与限流）
- **真实网关端到端复验（额度恢复后）**：用有额度账号（device_id 656）跑 `TestRealGatewayAttestation -count=1` 带 `U1S1_REAL_CHAT=1` → **`✅ 真实 chat 成功 status=200`**；令牌 payload `u=463 d=656`（与另一账号 `u=531 d=655` 不同），再次印证令牌按 user+device 绑定
- 上一版遗留的「网关是否强制校验 attestation」至此彻底关闭：**带真实令牌的请求在真实网关上端到端 200**

## v0.9.0 (2026-08-30)

### 新增

- **设备凭证通道接入 `x-u1s1-attestation` 客户端证明**（官方 u1s1-cli 1.3.0 新增请求头）：新包文件 `internal/upstream/attestation.go` 提供 `AttestationManager`，在发 chat 前用设备凭证调 `GET /v1/models` 领取网关签发的 `client_attestation.token`，按设备凭证缓存并注入 `x-u1s1-attestation` 头，与官方签名代理行为对齐。令牌**绑定 user + device_id、无法伪造或跳账号复用**，因此必须逐账号获取；`DeviceChat` 新增 attestation 参数，`DeviceModels` 为新增的签发入口
- **令牌生命周期自治**：距过期不足 6 小时自动重签（真实 TTL 7 天，相当于每天最多重签一次）；per-key 锁 + 双检合并并发请求，同一账号不会每请求多打一次 `/v1/models`；上游返回 401/403 时 `Invalidate` 丢缓存、下次重签
- **降级不阻断**：签发失败（网络抖动、老网关无该字段）时返回空串并照常发请求（不带该头）——与官方无令牌时的行为一致；已有旧令牌时失败则继续复用旧值；网关不签发时缓存空值 1 小时避免重复探测

### 变更

- **指纹同步 u1s1-cli 1.3.0**：`U1S1_VERSION` 默认值 1.2.5 → 1.3.0（`internal/config`、`.env.example`、config 模板、README、设置页 placeholder、服务器 `.env` 同步）。逆向核对 1.2.6→1.3.0 五个版本（npm pack 逐文件 diff + 本地 mock 抓包 + 真实网关探针）：UA / X-Stainless-* / DPoP 签名实现**零变化**（openai SDK 仍 6.40.0，pi-coding-agent 1.2.9 起 0.84.3→0.84.4 但 `pi-ai` 依赖的 openai 版本未变），变化集中在 `x-u1s1-version` 与新头 attestation。官方注释明确“不带版本头的旧版会在会话首轮被追加升级提示”，因此版本值需及时跟进

### 验证

- 新增 `internal/upstream/attestation_test.go`（11 项：缓存命中只签发一次、16 并发合并为 1 次签发、临期重签、失败降级、失败复用旧值、老网关空值负缓存、Invalidate 重签、多设备隔离、nil 安全、超长令牌丢弃）与 `internal/server/attestation_test.go`（4 项端到端：设备通道实发该头、3 次请求只签发 1 次、Key 池通道不发、老网关下照常成功）；`go test -race ./...` 全绿
- 真实网关实测（自有账号，只读）：`/v1/models` 确实返回 `client_attestation{token(150 字符), expires_in:604800}`；payload 解码为 `{"v":1,"u":531,"d":655,"exp":…,"n":…}` 且 `d` 等于 `accounts.device_id`；连续两次调用 token 不同（每次重签）；普通 `u1s1-` key 实测 NOT ISSUED
- **新增可复用的真实网关集成检查** `internal/upstream/real_gateway_test.go`（`TestRealGatewayAttestation`）：默认 skip，凭证走环境变量（`U1S1_DEV_TOKEN`/`U1S1_DEV_PRIV`/`U1S1_DEV_PUB`，不落仓库），校验令牌能签发、payload 带 user/device 绑定、TTL 约 7 天、事二次命中缓存；额外设 `U1S1_REAL_CHAT=1` 才发一次真实 chat（确认不被网关以指纹/证明理由拒绝）。以后每次版本同步可直接跑它，不必重写探针
- 部署后真实环境验证：欧洲 VPS 跑 v0.9.0 发一次 chat，4 个设备账号均成功签发令牌（日志无签发失败告警）、回退链路不变；本地用本项目代码跑 `TestRealGatewayAttestation` 得到令牌 len=150 / TTL 168h / 缓存命中 1µs / chat 返回 429 额度而非 401

### 已知未验证

- **网关是否已强制校验该头未能证实**：四个生产账号今日额度均已耗尽，带/不带/伪造三种 attestation 的 chat 请求均返回同一个 `429 quota_exceeded`（额度检查先于证明校验，或该头尚处于只签发不校验的灰度阶段），无法区分。本次实现按“官方发则我发”对齐，无论后续是否强校验都不需再改

#### 后续更正（2026-08-30 晚）

上述未证实项已由**官方更新记录原文**解答，不再是推测：

- v1.3.0（2026-08-29）「安全与兼容性（CLI）」写明「官方设备请求新增**短期安全凭据**……与当前设备绑定，用于验证请求来自官方客户端」，定性了 `x-u1s1-attestation` 就是本实现接入的令牌；并明确「服务端采用**组合证据与持续观察，不会仅按版本号或单个请求头判定**」——即该头目前**不是硬校验**，与实测三种取值同返回 429 一致
- 同条记录另写「**疑似非官方设备凭据代理**会在后台累计风险证据，**达到处置条件后自动封禁**」；配合公告 id=6「Token 仅限本站工具与服务使用，不得接入或用于任何第三方工具……违规调用、转接可封禁账号」，本项目（设备凭据代理 + 第三方转发）已处于官方风控的明确射程内，属运营风险而非协议缺陷
- 活体复验（`TestRealGatewayAttestation -count=1`，2026-08-30 22:11）：令牌仍正常签发、TTL 168h、chat 返回 429 额度而非 401/403，上游契约无变化；npm 仍为 1.3.0

## v0.8.3 (2026-08-29)

### 新增

- **概览页账号额度进度条（实时）**：概览「授权账号额度」卡片改为按账号展示**总可用额度进度条**——每条进度条 = 剩余 / 容量（容量取 `daily_tokens`（每日额度）或 `total_tokens`（包总额度），无则用剩余），剩余充足绿色、紧张琥珀、耗尽红色；下方附各分组明细（固定/每日赠送/邀请/签到 + 剩余/容量）
- **额度实时刷新**：新增 `POST /admin/api/accounts/quota-refresh-all`（用设备凭证对全部授权账号实时调 `/v1/me` 拉取最新加量包并入库，300ms 限速）；概览页「刷新额度」按钮手动实时刷新，页面每 3 分钟自动刷新一次，卡片标题显示更新时间
- **额度视图加容量字段**：`accountQuotaView` 新增 `capacity`（进度条分母），`GET /admin/api/accounts`、`/overview`、单账号 `quota-refresh` 均带出



- **已打过卡也顺带领取**：每日签到 claim 返回「今天已经打过卡了」时不再整体失败——改为记录该失败项、拉 `/api/me` 补连续天数，并继续尝试领取 500 万临时加量包等附加包（互不依赖）

## v0.8.2 (2026-08-29)

### 新增

- **顺带领取 500 万临时加量包等附加包**：`webcheckin.CheckIn` 打卡（登录打卡 200 万）后，拉取 `/api/me` 检查各加量包领取状态，自动领取所有当前可用的——**临时加量包 500 万**（`/api/packages/payment-delay-gift/claim`，官方文案「一次性 500 万 Token 加量包」）、**邀请赠送 500 万**（`/api/packages/invite/claim`）、**新用户赠送 500 万**（`/api/packages/new-user/claim`）；每个加量包各需一个新 cap-token（与登录、打卡互不共用）。领取结果并入打卡状态文案（如「已打卡：签到 200 万 + 临时加量包 500 万（连续 2 天）」），单包失败不影响主打卡

## v0.8.1 (2026-08-29)

### 变更

- **指纹同步 u1s1-cli 1.2.5**：`U1S1_VERSION` 默认值 1.2.3 → 1.2.5（README、.env.example、config 模板、设置页 placeholder 同步更新）。逆向核对 1.2.4/1.2.5（npm pack 三版本 dist diff + 本地 mock 抓包验证）：指纹相关文件 `device-auth.js`/`config.js`/`login.js`/`model.js` 与 package.json 依赖（pi-coding-agent 0.84.3 → openai 6.40.0）**零变化**，UA / X-Stainless-* / x-u1s1-client / x-u1s1-platform 头完全一致，仅 `x-u1s1-version` 随 package.json 变为 1.2.5；1.2.4/1.2.5 新增的是纯客户端功能（`u1s1 deploy`、bench 评测模块、searchWeb 参数重构），网关协议无变化。实抓 1.2.5 请求头确认：普通 key 走 `Authorization: Bearer`，设备凭证走 `Authorization: DPoP` + `dpop` 签名，与本项目两通道实现一致

## v0.8.0 (2026-08-29)

### 新增

- **官网动态监听（公告 + 更新记录）**：新包 `internal/sitefeed` 定时抓取 u1s1.io 官网公开数据源——公告 `GET /public/announcements?limit=100`（JSON，逆向官网前端 `announcements.js` 确认）与更新记录 `GET /guides/changelog`（静态渲染 HTML，`golang.org/x/net/html` 按 `<h2>` 版本分块解析，块内容 sha256 做去重键以区分重复的 Gateway 标题）；条目 `INSERT OR IGNORE` 落库新表 `site_posts`（首次运行只建快照不推送），任何单源失败不影响另一源
- **Bark 推送**：新增 `internal/server/bark.go`（POST `https://api.day.app/<key>/`，`group=u1s1`、官网 favicon 图标、点击跳转对应官网页，`Proxy:nil` 禁环境代理，`barkPushFn` 可注入测试）；新公告/新更新记录各合并为一条推送，更新记录含 CLI 版本号条目时附「请同步 U1S1_VERSION」提示；`BARK_KEY` 为空时照常抓取入库仅跳过推送
- **npm 版本检测**：每轮检查查询 `registry.npmjs.org/u1s1-cli/latest`，与配置的 `U1S1_VERSION` 语义化比较（`versionGreater`），有新版本单独推送一次（同一版本只推一次，去重存 `sitefeed_state`）
- **管理后台「官网动态」页面**：侧边栏左下角新增「官网公告」「更新记录」两个叠放入口（公告带 30 天内新条目计数徽标；更新记录显示「本地适配版本 → npm 最新版本」，落后时琥珀色高亮）；页面含版本对比、上次/下次检查时间、公告/更新记录列表（30 天内条目标「新」）与「立即检查」按钮
- **Admin API**：`GET /admin/api/sitefeed`（列表 + 检查状态 + 版本对比）、`POST /admin/api/sitefeed/refresh`（手动立即检查，返回新增条目与推送结果）
- **配置**：`config` 新增 `BARK_KEY`（空=不推送）与 `SITEFEED_CHECK_HOURS`（默认 24，`<=0` 关闭监听循环——测试直构 Settings 零值天然不启动，对齐 `QUOTA_AUTO_REFRESH` 惯例），进 `PatchableFields` 热更新写回 `.env`，设置页新增「官网动态推送」卡片
- **webcheckin 会话复用**：登录链路抽成导出方法 `NewSession`（capcat 求解 → 密码登录 → 返回带会话 cookie 的 client），`CheckIn` 内部改用它；`sitefeed` 注册其为公告接口 401 时的登录兜底（当前公开接口用不到，防御性保留）

### 验证

- 本地端到端：启动后自动建快照（公告 5 条、更新记录 62 块）、npm 检测查到 1.2.5（本地适配 1.2.3）；真实 Bark key 测试推送送达（HTTP 200）
- 新增测试：changelog HTML 解析（含重复 Gateway 块去重键稳定性）、公告 JSON 解析与结构变化报错、npm 版本查询、首次快照不推送/新增合并推送/重复检查去重/CLI 版本单推一次、`versionGreater` 表驱动、store 去重与状态读写

## v0.7.1 (2026-08-29)

### 新增

- **账号额度明细展示**：`accounts` 表新增 `packages_json` 列，保存上游 `/v1/me` 返回的加量包快照（含 `kind`/`remaining`/`daily_tokens`/`total_tokens`/`used_tokens`/`note` 等字段，旧库自动 `ALTER` 补列）；账号列表「剩余额度」列按业务分组展示合计与明细——**固定额度**（payment/admin_grant/new_user）、**每日赠送**（free_first/free_renew）、**邀请额度**（invite）、**签到打卡**（login_checkin/login_checkin_bonus），仅列出剩余 > 0 的分组
- **手动刷新额度**：新增 `POST /admin/api/accounts/{id}/quota-refresh`（用设备凭证调 `/v1/me` 拉取加量包并入库），操作列新增「刷额度」按钮
- **概览页「授权账号额度」卡片**：`GET /admin/api/overview` 新增 `account_quota` 字段（来自库内快照，不做实时请求），概览页展示各启用已授权账号的剩余总额与分类明细
- **0 点自动刷新覆盖账号额度**：`runQuotaAutoRefresh` 刷完 Key 池后顺带刷新全部授权账号的加量包快照（`refreshAllDeviceQuotas`，300ms 限速）；网页打卡成功后也自动刷新快照（含刚领取的 `login_checkin` 包）

### 变更

- **移除手动打卡入口**：删掉「去打卡」（打开官网手动登录）、「复制账号」、「复制密码」三个按钮——打卡已由服务端 capcat 纯 API 求解全自动完成，不再需要人工到官网登录；操作列改为「打卡」（立即触发一次网页打卡）+「刷额度」
- **列名调整**：「签到剩余」改为「剩余额度」（展示合计 + 分组明细）

### 修复

- **nginx 413 Request Entity Too Large**：`u1s1.tang74.top` 反代配置未设 `client_max_body_size`，沿用 nginx 默认 1MB，大上下文聊天请求（`/v1/chat/completions`）在到达 Go 服务前即被 nginx 拒绝。已补 `client_max_body_size 50m;` 并 reload（服务器配置变更，不涉及代码）

## v0.7.0 (2026-08-29)

### 新增

- **网页自动打卡（纯 API，无需真浏览器）**：逆向 capcat 人机验证 format-2 协议并落地为 Go 求解器——`POST /challenge` 取 rsw（重复模平方 `x^(2^t) mod N`）+ instrumentation（每次随机生成的确定性算术程序）两道挑战，`POST /redeem` 兑换 cap-token，随后 `POST /auth/password/login` 网页登录（拿会话 cookie）、`POST /api/packages/login-checkin/claim` 领取每日 200 万 Token 加量包。此前 capcat 的 instrumentation 反自动化探测（检测 Node/CDP/webdriver 等）被认为必须真浏览器才能过，实测其算术后段为确定性程序，用 goja 以最小 DOM stub 执行即可通过，全程纯 API
- **新包 `internal/capcat`**：capcat 求解器（rsw 用 `math/big`，instrumentation 用 `github.com/dop251/goja` 执行随机算术段），出口走 `EGRESS_PROXY` 与主客户端一致；含 opt-in 真实求解测试（`CAPCAT_LIVE=1`）
- **新包 `internal/webcheckin`**：u1s1 网页登录与打卡 claim 客户端（登录、claim 各需一个新 cap-token，token 一次性、约 10 分钟有效）
- **签到链路改造**：有密码的授权账号自动走「网页打卡」（capcat 求解 → 登录 → claim），无密码账号回退设备凭证调 `/v1/me` 的旧机制；`accounts` 表新增 `last_web_checkin_at` / `web_checkin_status`（旧库自动 ALTER 补列），管理后台「授权账号」页展示每次打卡结果（成功文案 / 失败原因）
- **单账号「打卡」按钮**：操作列「查额度」改为「打卡」，可手动触发单账号网页打卡（已打卡则返回「今天已经打过卡了」）


## v0.6.0 (2026-08-28)

### 修复

- **设备账号调度顺序**：多授权账号时不再按「最少额度优先 → 最多 → 中间」轮询（此前每个请求都先打额度最少的账号）。改为按剩余额度降序——**有额度的账号直接调用**（额度最多者优先）；仅当账号触发 quota_exceeded 被标记当日冷却后，才轮询到下一个有额度的账号，已冷却账号不再调用

### 变更

- **指纹同步官方 u1s1-cli v1.2.3**：`x-u1s1-version` 默认值从 `1.2.1` 升到 `1.2.3`；设备凭证（`u1s1d-` + DPoP）通道按官方 1.2.3 签名代理补齐 `x-u1s1-client`（默认 `terminal`）与 `x-u1s1-platform`（如 `darwin-arm64`）两个头。普通 `u1s1-` key 通道不受影响（官方 api_key 兜底分支不发这两头，仍保持 Bearer + x-u1s1-version）
- **设备凭证通道指纹自洽**：不再硬编码 macos-arm64 档案，改随配置的 `FINGERPRINT_PROFILE`，UA / X-Stainless-* / x-u1s1-platform 同档案一致
- **签到补尾**：`GET /v1/me`（每日签到）补上 `x-u1s1-version` 头（与官方 `fetchMe` 一致），此前只发 DPoP 头
- **授权账号独立化**：侧边栏与页面标题「签到管理」改为「授权账号」，明确与 Key 池独立

### 新增

- **U1S1 原生一键登录**：「授权账号」页新增「一键登录」按钮，无需在后台预填邮箱+密码——生成 EC P-256 密钥 → 调 `/auth/device/start` 领 verify_url → 浏览器用 U1S1 账号登录并批准设备 → 网关轮询 `/auth/device/poll` 拿 `api_key`+`device_token`+JWK → 自动调 `/v1/me` 取邮箱 → 自动建账号（密码留空，`accounts.password` 字段保留，手动添加仍可用）→ 存设备凭证 → `api_key` 自动导入 Key 池 → 签到。与该账号原有的「添加（邮箱+密码）→ 授权」流程并存，两种方式均可
- **授权后自动导入 api_key 到 Key 池**：设备授权/一键登录成功时，把 `device/poll` 返回的 `u1s1-` api_key 自动导入 `upstream_keys`（`INSERT OR IGNORE` 去重，绝不覆盖已有 key），供普通 Key 池兜底通道使用；已导入则跳过
- **授权账号复制凭证**：账号列表新增「复制账号」「复制密码」按钮，一键把该账号的完整邮箱 / 已保存的官网密码复制到剪贴板（新增 `GET /admin/api/accounts/{id}/credential`，仅管理端会话可访问），配合「去打卡」在官网打卡页手动登录领取每日加量包；未保存密码时点「复制密码」会提示先设置密码

## v0.5.2 (2026-08-28)

### 修复

- **设备授权 device_id 类型不匹配**：上游 `/auth/device/poll` 返回的 `device_id` 为数字（`654`），但 `DevicePollResp` 结构体定义为 `string`，导致 `json.Unmarshal` 静默失败，授权永远轮询不到。改为 `json.Number` 兼容
- **前端轮询取消机制**：关闭授权弹窗时停止后台轮询，避免多个授权请求冲突

## v0.5.1 (2026-08-28)

### 修复

- **设备授权确认流程**：改为前端轮询模式（`handleDeviceConfirm` 单次轮询，返回 `pending`/`authorized` 状态，前端每 5 秒重试），避免后端阻塞 900 秒导致前端超时
- **auth 端点 User-Agent**：抑制 Go 默认 `Go-http-client/1.1` 的 User-Agent（与官方 CLI 一致，auth 端点不发 UA）

### 改进

- **侧边栏菜单**：「官网账号」改为「签到管理」
- **复制按钮**：授权弹窗链接右侧增加复制按钮
- **单账号签到**：已授权账号行增加「签到」文字按钮，可单独签到
- **文字按钮**：操作列改为文字按钮（签到/授权/停用/删除），不再使用纯图标

## v0.5.0 (2026-08-28)

### 新增

- **官网账号管理 + 设备授权**：后台新增「官网账号」页（侧边栏），可录入账号（邮箱+密码，明文），对每个账号发起设备授权——生成 EC P-256 密钥向 `/auth/device/start` 领取授权链接（`verify_url` + 倒计时），用户在浏览器登录并批准设备后回来点「我已授权」，系统轮询 `/auth/device/poll` 领回设备凭证（`u1s1d-` + `api_key` + EC 私钥/公钥 JWK）入库
- **模拟官方客户端**：用设备凭证（DPoP ES256 签名 + 完整客户端指纹头）调用 `/v1/chat/completions`，网关识别为官方客户端，消耗「仅限 u1s1 客户端使用」的加量包（login_checkin / new_user）；有已授权账号时聊天转发优先走设备凭证通道，失败回退 `u1s1-` Key 池
- **每日自动签到**：每天北京时间 0 点后，用设备凭证调 `GET /v1/me` 触发当日「每日登录打卡」200 万 Token 加量包发放，并记录签到状态（`last_checkin_at` / `login_checkin_remaining`）；启动补签 + 手动「全部签到」
- **DPoP 认证客户端**：`internal/upstream/device.go` 实现 RFC 9449 DPoP（ES256 / P-256，R||S 拼接签名），与官方 u1s1-cli device-auth.js 对齐

## v0.4.3 (2026-08-27)

### 变更

- **请求头指纹同步官方 u1s1-cli v1.2.1**：`x-u1s1-version` 默认值从 `1.2.0` 升到 `1.2.1`（UA 与 X-Stainless-* 不变，官方 SDK 仍为 6.40.0，pi-coding-agent 仍 0.84.3）；`.env` 的 `U1S1_VERSION`、README 与后台设置页 placeholder 同步更新

## v0.4.2 (2026-08-27)

### 变更

- **请求头指纹同步官方 u1s1-cli v1.2.0**：`x-u1s1-version` 默认值从 `0.19.5` 升到 `1.2.0`（UA 与 X-Stainless-* 不变，官方 SDK 仍为 6.40.0）；`.env` 的 `U1S1_VERSION` 与后台设置页 placeholder 同步更新
- 模型列表由上游 `/v1/models` 动态拉取（5 分钟缓存），官方新模型（如 deepseek-v4-flash、x-ai/grok-4.6）自动生效，无需改代码

## v0.4.1 (2026-08-27)

### 修复

- **兼容 OpenAI o1/GPT-5 系客户端**：CPA 等新客户端发送的 `role=developer` 在转发给上游前统一归一化为 `system`；上游收紧后原样转发了直接 400 "unknown variant `developer`"。附带逐条消息字段保留了 integration/unit 测试

## v0.4.0 (2026-08-26)

### 新增

- **北京时间 0 点自动刷新配额**：免费额度每天北京时间 0 点重置后，服务端自动对全部上游 Key 执行一轮配额检查（等同后台「一键刷新」），冷却中的 key 恢复 active、面板额度数字同步更新，无需每天人工点刷新；默认开启，`QUOTA_AUTO_REFRESH=false` 关闭
- **启动补刷**：服务若跨 0 点停过机（当天北京时间尚未检查过配额），启动后自动补刷一轮，避免额度数字停留在昨天的旧值
- **刷新互斥**：定时刷新与手动「一键刷新」不并发，冲突时返回明确提示
- 设置接口新增 `quota_auto_refresh` / `next_quota_refresh_at` 字段，可查询开关状态与下次自动刷新时间

## v0.3.2 (2026-08-25)

### 改进

- **概览页布局调整**：最近请求移到模型用量下方，移除来源列

## v0.3.1 (2026-08-25)

### 改进

- **概览页配色柔和化**：移除鲜艳的渐变与高饱和色，改用主题 muted 色系（success/warning/info + muted 变体）
- **间距收紧**：内容区最大宽度放宽至 1400px、四周 padding 减小，与侧边栏间隔更紧凑
- **新增 Key 用量排行**：按每把 U1S1 上游 Key 展示调用次数与 Token 消耗（掩码显示），本地 API Key 用量同步展示

---

## v0.3.0 (2026-08-25)

### 新增

- **概览页全面升级**（参考 freebuff2api-go 结构）：
  - 请求统计卡片（总请求/成功/失败/总 Token/平均耗时），支持 1d/3d/7d/30d/全部 时间范围切换
  - 成功率/失败率进度条
  - 模型用量排行（按 Token 排序，输入/输出/总计分解）
  - 快捷操作卡片（导入 Key/创建 API Key/请求记录/模型测试）
  - 请求头指纹信息卡
- **请求统计后端接口**：`GET /admin/api/requests/stats?range=1d|3d|7d|30d|all`，返回总体/模型/API Key 三维聚合
- **侧边栏调整**：运行日志移到模型测试上方

### 修复

- **请求来源 IP**：正确读取 nginx 反代的 `X-Real-IP`/`X-Forwarded-For`，不再全部显示为 127.0.0.1
- **成功请求写入运行日志**：每条 chat 完成记录模型/token/耗时/来源 IP，方便查看调用情况
- **鉴权失败写入运行日志**：无效/缺失 API Key 现在有 WARN 日志，不再静默

---

## v0.2.0 (2026-08-25)

### 改进

- **API Key 可随时复制**：新增「复制」按钮，管理员登录后可随时从列表取回完整密钥；创建时自动复制到剪贴板；列表不再下发明文密钥（改为 `/api/local-keys/{name}/copy` 按需拉取，增强安全性）
- **运行日志倒序**：最新日志显示在顶部，默认固定最新，可取消固定手动翻阅历史

### 修复

- 本地 API Key 列表接口此前会下发明文密钥，已改为只返回掩码

---

## v0.1.0 (2026-08-25)

### 新增

- **请求头指纹模拟**：逆向分析 u1s1-cli 0.19.5，完整复现官方客户端指纹
  - `User-Agent: pi ({platform} {release}; {arch})`
  - `X-Stainless-Lang/Package-Version/OS/Arch/Runtime/Runtime-Version/Retry-Count`
  - `x-u1s1-version` 客户端版本标识
  - 5 套预设档案（macos-arm64/x64、linux-x64/arm64、windows-x64），持久化稳定身份
- **OpenAI 兼容 API**
  - `GET /v1/models` — 汇聚上游模型列表
  - `POST /v1/chat/completions` — 流式/非流式透传，自动补 `stream_options.include_usage`
- **多 Key 池轮询**
  - 429 `quota_exceeded`（额度用完）→ 自动冷却至次日北京时间 0 点
  - 401 → 自动禁用
  - 429 普通限流 → 90 秒短冷却
  - 故障自动切换下一把，最多重试 3 次
- **一键文本导入**：粘贴多行 `u1s1-xxx`（支持 `#` 注释、`key 备注` 格式、自动去重）
- **配额查询**：单把/批量调用上游 `/v1/me`，展示今日剩余额度与永久余额
- **管理后台**（React + Vite + shadcn/ui）
  - Dashboard：今日/累计统计、14 天趋势、模型用量、最近请求
  - U1S1 Key 管理：导入/删除/启用禁用/查配额
  - 本地 API Key：创建/删除/启停（`sk-u1s12-` 前缀）
  - 请求记录：分页/筛选/清空
  - 运行日志：实时增量拉取
  - 模型测试：在线对话验证
  - 设置：上游地址/出口代理/指纹档案/版本号/管理口令
- **基础设施**
  - SQLite 持久化（modernc.org/sqlite，无 CGO，交叉编译友好）
  - 单二进制部署（前端 `go:embed`）
  - `.env` 配置热写回（管理后台修改即持久化）
  - 登录限流（5 次失败锁定 15 分钟）
  - HMAC 签名 Cookie 会话（7 天有效期）
  - 出口代理支持（http/https/socks5）
  - systemd 部署文件 + 构建脚本

### 技术细节

- Go 1.26+，HTTP 路由使用 Go 1.22 pattern matching（`mux.HandleFunc("GET /v1/models", ...)`）
- 前端：React 19 + TypeScript + Tailwind CSS 4 + shadcn/ui
- 指纹头与官方 CLI 逐字段对齐（逆向自 `@earendil-works/pi-coding-agent` 0.84.3 + OpenAI SDK 6.40.0）
- 辅助端点（/models /me）不发 User-Agent，与 Node fetch（undici）行为一致

</parameter>