# Changelog

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