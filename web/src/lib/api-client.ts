import type {
  ApiResponse,
  SessionInfo,
  OverviewData,
  ModelsData,
  UpstreamKeyItem,
  UpstreamKeysData,
  BulkImportResult,
  TextImportResult,
  CheckAllResult,
  LocalKeyItem,
  LocalKeysData,
  RequestsData,
  SettingsData,
  ChatTestResult,
  LogsData,
} from "@/types"

const API_BASE = "/admin/api"

class ApiClientError extends Error {
  status: number
  data: { detail?: string }

  constructor(status: number, data: { detail?: string }) {
    super(data.detail || "请求失败")
    this.status = status
    this.data = data
  }
}

export { ApiClientError }

async function request<T>(path: string, options?: RequestInit): Promise<T> {
  const headers: Record<string, string> = {
    ...(options?.headers as Record<string, string>),
  }

  if (options?.body && typeof options.body === "string") {
    headers["Content-Type"] = "application/json"
  }

  const res = await fetch(`${API_BASE}${path}`, {
    ...options,
    headers,
    credentials: "same-origin",
  })

  const json = await res.json()

  if (!res.ok) {
    throw new ApiClientError(res.status, json)
  }

  const wrapped = json as ApiResponse<T>
  return wrapped.data
}

export const api = {
  // Auth
  session: () => request<SessionInfo>("/session"),
  login: (key: string) =>
    request<unknown>("/login", { method: "POST", body: JSON.stringify({ key }) }),
  logout: () => request<unknown>("/logout", { method: "POST" }),

  // Overview & models
  overview: () => request<OverviewData>("/overview"),
  models: () => request<ModelsData>("/models"),

  // U1S1 上游 Key
  u1s1Keys: () => request<UpstreamKeysData>("/u1s1-keys"),
  importU1s1Keys: (items: { key: string; note?: string }[]) =>
    request<BulkImportResult>("/u1s1-keys", {
      method: "POST",
      body: JSON.stringify({ items }),
    }),
  importU1s1KeysText: (text: string) =>
    request<TextImportResult>("/u1s1-keys/import-text", {
      method: "POST",
      body: JSON.stringify({ text }),
    }),
  deleteU1s1Key: (id: number) =>
    request<unknown>(`/u1s1-keys/${id}`, { method: "DELETE" }),
  setU1s1KeyStatus: (id: number, status: "active" | "disabled") =>
    request<unknown>(`/u1s1-keys/${id}/status`, {
      method: "PUT",
      body: JSON.stringify({ status }),
    }),
  checkU1s1Quota: (id: number) =>
    request<UpstreamKeyItem>(`/u1s1-keys/${id}/quota`, { method: "POST" }),
  checkAllQuotas: () =>
    request<CheckAllResult>("/u1s1-keys/check-all", { method: "POST" }),

  // 本地 API Key
  localKeys: () => request<LocalKeysData>("/local-keys"),
  createLocalKey: (name: string, note: string) =>
    request<LocalKeyItem>("/local-keys", {
      method: "POST",
      body: JSON.stringify({ name, note }),
    }),
  updateLocalKey: (
    name: string,
    fields: { note?: string; enabled?: boolean },
  ) =>
    request<unknown>(`/local-keys/${encodeURIComponent(name)}`, {
      method: "PUT",
      body: JSON.stringify(fields),
    }),
  deleteLocalKey: (name: string) =>
    request<unknown>(`/local-keys/${encodeURIComponent(name)}`, {
      method: "DELETE",
    }),
  copyLocalKey: (name: string) =>
    request<{ name: string; key: string }>(
      `/local-keys/${encodeURIComponent(name)}/copy`,
      { method: "POST" },
    ),

  // 请求记录
  requests: (params?: {
    limit?: number
    offset?: number
    model?: string
    status?: string
    api_key_name?: string
  }) => {
    const sp = new URLSearchParams()
    if (params?.limit) sp.set("limit", String(params.limit))
    if (params?.offset) sp.set("offset", String(params.offset))
    if (params?.model) sp.set("model", params.model)
    if (params?.status) sp.set("status", params.status)
    if (params?.api_key_name) sp.set("api_key_name", params.api_key_name)
    const qs = sp.toString()
    return request<RequestsData>(`/requests${qs ? `?${qs}` : ""}`)
  },
  clearRequests: () => request<unknown>("/requests", { method: "DELETE" }),

  // 请求统计
  requestStats: (range: string = "all") =>
    request<import("@/types").RequestStats>(
      `/requests/stats?range=${range}`,
    ),

  // 设置
  settings: () => request<SettingsData>("/settings"),
  saveSettings: (patch: {
    upstream_base_url?: string
    egress_proxy?: string
    fingerprint_profile?: string
    u1s1_version?: string
    admin_password?: string
  }) =>
    request<{ ok: boolean; fingerprint_profile_applied: string }>("/settings", {
      method: "PUT",
      body: JSON.stringify(patch),
    }),

  // 模型测试
  chatTest: (model: string, prompt: string) =>
    request<ChatTestResult>("/chat-test", {
      method: "POST",
      body: JSON.stringify({ model, prompt }),
    }),

  // 运行日志
  logs: (params?: { since_id?: number; limit?: number; level?: string }) => {
    const sp = new URLSearchParams()
    if (params?.since_id) sp.set("since_id", String(params.since_id))
    if (params?.limit) sp.set("limit", String(params.limit))
    if (params?.level) sp.set("level", params.level)
    const qs = sp.toString()
    return request<LogsData>(`/logs${qs ? `?${qs}` : ""}`)
  },

  // 出口代理连通性测试
  proxyTest: (egress_proxy: string) =>
    request<{ ok: boolean; status?: number; error?: string }>("/proxy-test", {
      method: "POST",
      body: JSON.stringify({ egress_proxy }),
    }),
}
