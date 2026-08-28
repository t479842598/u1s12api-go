// 全局类型定义（与后端 /admin/api 契约一一对应）。

export interface ApiResponse<T> {
  data: T
  detail?: string
}

export interface SessionInfo {
  authenticated: boolean
}

// ---- 概览 ----

export interface UsageSummary {
  requests: number
  input_tokens: number
  output_tokens: number
  total_tokens: number
  cost_usd: number
}

export interface KeyStats {
  total: number
  active?: number
  cooldown?: number
  disabled?: number
}

export interface DailyUsage {
  date: string
  requests: number
  total_tokens: number
  cost_usd: number
}

export interface ModelUsage {
  model: string
  requests: number
  total_tokens: number
}

export interface RecentRequest {
  id: number
  ts: number
  api_key_name: string
  model: string
  stream: boolean
  status: string
  http_status: number
  total_tokens: number
  cost_usd: number
  duration_ms: number
  error: string
  client_ip: string
}

export interface FingerprintInfo {
  profile: string
  label: string
  user_agent: string
  runtime: string
}

export interface OverviewData {
  today: UsageSummary
  totals: UsageSummary
  keys: KeyStats
  daily: DailyUsage[]
  models: ModelUsage[]
  recent: RecentRequest[]
  fingerprint: FingerprintInfo
  upstream_base_url: string
  u1s1_version: string
  announcement?: unknown
}

// ---- 上游模型 ----

export interface UpstreamModelPrice {
  input: number
  output: number
  cache_read?: number
}

export interface UpstreamModel {
  id: string
  name: string
  reasoning: boolean
  context_length: number
  max_tokens: number
  price: UpstreamModelPrice
}

export interface ModelsData {
  models: UpstreamModel[]
  features?: Record<string, unknown>
  announcement?: unknown
  cached: boolean
}

// ---- 上游 U1S1 Key ----

export type UpstreamKeyStatus = "active" | "cooldown" | "disabled"

export interface UpstreamKeyItem {
  id: number
  key_masked: string
  note: string
  status: UpstreamKeyStatus
  cooldown_until: number
  last_error: string
  email: string
  tokens_per_usd: number
  daily_free_remaining_usd: number
  remaining_usd: number
  free_claim: string
  quota_checked_at: number
  total_requests: number
  total_tokens: number
  created_at: number
  last_used_at: number
}

export interface UpstreamKeysData {
  keys: UpstreamKeyItem[]
  stats: KeyStats
}

export interface BulkImportResult {
  added: number
  skipped: number
  invalid: number
}

export interface TextImportResult extends BulkImportResult {
  invalid_lines: string[]
}

export interface CheckAllResult {
  results: { id: number; ok: boolean; error?: string }[]
  ok: number
  total: number
}

// ---- 本地 API Key ----

export interface LocalKeyItem {
  id: number
  name: string
  key: string // 创建响应里是完整 key，列表为空串
  key_masked: string
  note: string
  enabled: boolean
  total_requests: number
  total_tokens: number
  created_at: number
  last_used_at: number
}

export interface LocalKeysData {
  keys: LocalKeyItem[]
}

// ---- 请求统计 ----

export interface RequestStatsEntry {
  count: number
  prompt_tokens: number
  completion_tokens: number
  total_tokens: number
}

export interface RequestStats {
  total: number
  success: number
  error: number
  total_tokens: number
  avg_duration_ms: number
  by_model: Record<string, RequestStatsEntry>
  by_api_key: Record<string, RequestStatsEntry>
  by_upstream_key: Record<string, RequestStatsEntry>
}

export type StatsRange = "1d" | "3d" | "7d" | "30d" | "all"

// ---- 请求记录 ----

export interface RequestRecord {
  id: number
  ts: number
  api_key_name: string
  model: string
  upstream_key_id: number
  stream: boolean
  status: string
  http_status: number
  input_tokens: number
  output_tokens: number
  total_tokens: number
  cost_usd: number
  duration_ms: number
  error: string
  client_ip: string
}

export interface RequestsData {
  items: RequestRecord[]
  total: number
}

// ---- 设置 ----

export interface ProfileSummary {
  id: string
  label: string
  user_agent: string
  runtime: string
}

export interface SettingsData {
  host: string
  port: number
  has_password: boolean
  upstream_base_url: string
  egress_proxy: string
  fingerprint_profile: string
  u1s1_version: string
  log_level: string
  profiles: ProfileSummary[]
  current_profile: string
}

// ---- 模型测试 ----

export interface ChatTestResult {
  content: string
  model: string
  input_tokens: number
  output_tokens: number
  duration_ms: number
}

// ---- 运行日志 ----

export interface LogEntry {
  id: number
  ts: string
  level: string
  name: string
  msg: string
}

export interface LogsData {
  items: LogEntry[]
}

// ---- 官网账号（设备授权 + 签到） ----

export interface AccountItem {
  id: number
  email: string
  email_masked: string
  note: string
  enabled: boolean
  has_password: boolean
  authorized: boolean
  device_token_masked: string
  api_key_masked: string
  device_id: string
  device_name: string
  last_checkin_at: number
  login_checkin_remaining: number
  last_web_checkin_at: number
  web_checkin_status?: string
  total_requests: number
  total_tokens: number
  created_at: number
  updated_at: number
}

export interface AccountsData {
  accounts: AccountItem[]
}

export interface DeviceStartResult {
  account_id: number
  verify_url: string
  expires_in: number
  interval: number
}

export interface DeviceConfirmResult {
  status: string
  authorized: boolean
  device_id: string
  api_key: string
  device_token: string
}

// U1S1 一键登录：无需预填邮箱密码，授权后自动建号 + api_key 入池

export interface OneClickStartResult {
  session_id: string
  verify_url: string
  expires_in: number
  interval: number
}

export interface OneClickConfirmResult {
  status: string
  authorized: boolean
  account_id: number
  email: string
  device_id: string
  api_key: string
  device_token: string
}

export interface CheckinAllResult {
  ok: number
  total: number
  results: { account_id: number; email: string; ok: boolean; remaining: number; error?: string }[]
}
