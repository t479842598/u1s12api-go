# u1s12api-go

将 [U1S1（有一说一）](https://u1s1.io) 官方网关包装为标准 OpenAI 兼容 API，支持多 Key 池轮询、免费额度监控与管理后台。架构对齐 [freebuff2api-go](https://github.com/t479842598/freebuff2api-go)。

## 特性

- **OpenAI 兼容**：`GET /v1/models`、`POST /v1/chat/completions`（流式 / 非流式）
- **请求头指纹模拟**：与官方 u1s1-cli 1.7.1 逐头逐字节对齐（HTTP/1.1 + 小写头名 + UA + X-Stainless-* + x-u1s1-version/client/platform/attestation），身份由部署机真实环境派生
- **官网账号 + 设备授权**：后台录入官网账号（邮箱+密码），发起设备授权领回 `u1s1d-` 设备凭证（DPoP 签名），网关即被识别为官方客户端，消耗「仅限 u1s1 客户端使用」的加量包
- **每日自动签到**：每天北京时间 0 点后用设备凭证调 `/v1/me` 自动领取每日打卡 200 万 Token 加量包
- **设备凭证优先转发**：有已授权账号时聊天转发优先走设备凭证通道（消耗客户端量包），失败回退 `u1s1-` Key 池
- **客户端证明（attestation）**：自动从 `/v1/models` 领取网关签发的 `x-u1s1-attestation` 令牌并按设备缓存（绑定 user+device、7 天有效、临期自动重签），与官方 1.3.0 客户端行为对齐；取不到时自动降级为不带该头，不阻断请求
- **多 Key 池**：轮询调度，单 Key 额度耗尽自动冷却至次日北京时间 0 点，401 自动禁用
- **配额查询**：一键查看每把 Key 的今日剩余额度 / 永久余额（调用上游 `/v1/me`）
- **配额自动刷新**：每天北京时间 0 点额度重置后自动全量刷新全部上游 Key 配额（`QUOTA_AUTO_REFRESH`，默认开启）
- **管理后台**：Dashboard / Key 管理 / 请求记录 / 运行日志 / 模型测试 / 设置
- **一键导入**：粘贴多行 `u1s1-xxx` 即完成批量导入
- **单二进制部署**：前端 embed，交叉编译 `GOOS=linux GOARCH=amd64 CGO_ENABLED=0`

## 快速开始

```bash
# 1. 构建
cd web && npm install && npm run build && cd ..
go build -o u1s12api .

# 2. 运行（首次启动自动生成 .env 与随机管理口令）
./u1s12api

# 3. 打开管理后台
open http://127.0.0.1:8080/admin/
```

## 配置（.env）

| 变量 | 默认值 | 说明 |
|---|---|---|
| `HOST` | `0.0.0.0` | 监听地址 |
| `PORT` | `8080` | 监听端口 |
| `ADMIN_PASSWORD` | 首次随机生成 | 管理后台登录口令 |
| `UPSTREAM_BASE_URL` | `https://api.u1s1.io/v1` | 上游网关 |
| `EGRESS_PROXY_URL` | 空（直连） | 出口代理 `http://`/`socks5://` |
| `FINGERPRINT_PROFILE` | `auto` | 头指纹档案 |
| `U1S1_VERSION` | `1.7.1` | x-u1s1-version 头（跟随官方 CLI npm latest） |
| `FINGERPRINT_NODE_VERSION` | 空 | 声称的 `x-stainless-runtime-version`；空=本机 node（须 ≥22.19.0）否则取真实发布值并持久化 |
| `LOG_LEVEL` | `info` | 日志级别 |
| `QUOTA_AUTO_REFRESH` | `true` | 北京时间 0 点后自动全量刷新上游 Key 配额 |

## 指纹档案

| 档案 | UA 示例 |
|---|---|
| `macos-arm64` | `pi (darwin 24.6.0; arm64)` |
| `macos-x64` | `pi (darwin 24.6.0; x64)` |
| `linux-x64` | `pi (linux 6.8.0-45-generic; x64)` |
| `linux-arm64` | `pi (linux 6.8.0-45-generic; arm64)` |
| `windows-x64` | `pi (win32 10.0.26100; x64)` |

选择 `auto`（默认）时，身份由**部署机真实环境**派生：`os.Hostname()` + `uname -r` 内核版本 +
GOARCH→node arch 映射，写入 `data/fingerprint.json`。只有 Node 版本是声称值（我们不是 Node 进程），
受官方 CLI `engines.node >= 22.19.0` 约束，且解析一次后长期沿用，不会每次重启漂移。

### 对齐的是哪个官方客户端

官方有两个发请求的入口，用的是**同一份 `device-auth.js`**（1.5.0→1.7.1 逐字节未变），
头集合与 DPoP 结构完全相同，真实差异只有两处。本项目对齐 **CLI（terminal）**：

| 项 | u1s1-cli 1.7.1 | 桌面客户端 0.1.15 | 本项目 |
|---|---|---|---|
| `x-u1s1-client` | `terminal` | `desktop` | **`terminal`** |
| 辅助端点 UA（`/models` `/me` `/auth/device/*`） | `node` | `undici` | **`node`**（会话中途刷新用 `undici`） |
| `x-u1s1-version` | `1.7.1` | `1.3.0`（内嵌 CLI 版本） | **`1.7.1`** |
| chat UA / `X-Stainless-*` / `x-u1s1-platform` / DPoP 结构 | 相同 | 相同 | 已对齐 |

**为什么不再对齐桌面端**（ADR 0001）：桌面端 0.1.9→0.1.11→0.1.15 三次都仍内嵌 CLI **1.3.0**，
而 npm CLI 已到 1.7.1 —— `desktop` + 新版本是现实中不存在的组合；且桌面端 Node 内嵌固定
v22.23.1，轮转多套 runtime 版本在 desktop 口径下同样不可能。CLI 通道下这些值都真实存在。

**为什么身份要取真实主机**（ADR 0002）：官方 CLI 报出的 hostname / platform / 内核版本 /
`device_name` 全部来自本机事实、互为佐证。轮转假档案会让这几项互相矛盾，而网关明确说它用
「组合证据」持续观察。已授权账号沿用**授权当时**的身份快照（`accounts.device_identity`），
后台切档案不影响它们 —— 真实世界里一台设备就是一个操作系统。

已复核到 **CLI 1.7.1** 与 **桌面端 0.1.15**（2026-09-04）：请求链路**零变化**
（`device-auth.js`/`config.js` 逐字节相同，`login.js` 只多一行日志）；1.7.1 唯一与本项目相关的
新增是 `api.js` 的 `AccessDeniedError` —— 官方把 **403 定性为「封禁/停用/设备不受信任，重登也没用」
并直接 `process.exit(1)`**，我们据此把 401 与 403 分流处置（403 停用账号 + Bark 告警 + 停止轮换）。
下次需要同步的触发条件：npm `u1s1-cli` 超过 1.7.1（那是 `x-u1s1-version` 的取值）、本脚本输出
出现新增/缺失的头、或官方 `engines.node` 下限变了。复核记录与跑法见脚本头注释。

除头集合外，本项目还复刻了官方三个**行为**特征：attestation 的 24h 提前刷新与失败后 30s 冷却
（官方 `device-auth.js` 的三个常量）、`device_name` 的官方格式、以及裸 fetch 的 UA 随
dispatcher 安装时机从 `node` 变 `undici`。

DPoP 证明与官方逐字节对齐（header 段的 JSON 是 ES256 签名输入的一部分）：

- header 段 jwk 键序 = Node `exportKey("jwk")` 的 `key_ops, ext, kty, x, y, crv`
- payload 段键序 = `jti, htm, htu, iat, ath`（Go 的 map 会被按字母排序，故显式拼 JSON）
- `jti` = 去掉连字符的 UUID v4（32 位小写 hex，第 13 位为 `4`、第 17 位为 `8|9|a|b`）

逐头核对与复现：`docs/repro/desktop-fingerprint-capture.mjs`（本地 mock 网关 + 官方签名代理，
不碰真实网关、不消耗额度；每次官方发版后跑一遍）。

### 已知无法对齐的残差

以下三项由 Go 的 `net/http` 与 `crypto/tls` 决定，头集合/大小写/值/协议已一致，但要消除它们
需要自研 HTTP/1.1 写器或走真 Node sidecar，收益未证实，暂不做（详见 `internal/upstream/wire.go` 包注释）：

1. **头顺序**：Go 按字母序写，undici 按插入序写。
2. **`Host` / `Content-Length` / `User-Agent` 的大小写**：Go 的 `Request.write` 硬写规范形式
   （其余头名已全部小写，含 `connection: keep-alive`，与官方一致）。
3. **TLS ClientHello**：Go crypto/tls 与 Node/BoringSSL 天然不同（JA3/JA4 层面）。

## 项目结构

```
u1s12api-go/
├── main.go                    # 入口：embed static + 启动 HTTP 服务
├── internal/
│   ├── config/                # .env 加载/热写回
│   ├── logging/               # 分级日志 + 内存环形缓冲
│   ├── fingerprint/           # 请求头指纹生成（对齐官方桌面客户端）
│   ├── upstream/              # 上游 HTTP 客户端 + Key 池轮询
│   ├── server/                # 路由 / 管理 API / 对话转发
│   └── store/                 # SQLite 持久化（modernc，无 CGO）
├── web/                       # React + Vite + shadcn/ui 管理后台
├── static/                    # 前端构建产物（go:embed）
└── deploy/u1s12api.service    # systemd 部署
```

## 交叉编译

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o u1s12api-linux .
```

## License

MIT
