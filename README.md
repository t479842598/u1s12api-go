# u1s12api-go

将 [U1S1（有一说一）](https://u1s1.io) 官方网关包装为标准 OpenAI 兼容 API，支持多 Key 池轮询、免费额度监控与管理后台。架构对齐 [freebuff2api-go](https://github.com/t479842598/freebuff2api-go)。

## 特性

- **OpenAI 兼容**：`GET /v1/models`、`POST /v1/chat/completions`（流式 / 非流式）
- **请求头指纹模拟**：与官方 u1s1-cli 0.19.5 完全一致（UA + X-Stainless-* + x-u1s1-version）
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
| `U1S1_VERSION` | `0.19.5` | x-u1s1-version 头 |
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

选择 `auto` 时，首次启动随机选取一个档案并持久化到 `data/fingerprint.json`。

## 项目结构

```
u1s12api-go/
├── main.go                    # 入口：embed static + 启动 HTTP 服务
├── internal/
│   ├── config/                # .env 加载/热写回
│   ├── logging/               # 分级日志 + 内存环形缓冲
│   ├── fingerprint/           # 请求头指纹生成（对齐官方 CLI）
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
