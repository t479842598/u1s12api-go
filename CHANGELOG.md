# Changelog

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