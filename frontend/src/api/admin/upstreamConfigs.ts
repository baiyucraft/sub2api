import { apiClient } from '../client'
import type { PaginatedResponse } from '@/types'

export type UpstreamProvider = 'sub2api' | 'newapi' | 'other'
export type UpstreamAuthMode = 'user_login' | 'manual_jwt'
export type UpstreamTrendRange = '24h' | '7d' | '30d'

export interface UpstreamCredentialsStatus {
  has_login_email?: boolean
  has_login_password?: boolean
  has_access_token?: boolean
  has_refresh_token?: boolean
  has_newapi_login_username?: boolean
  has_newapi_login_password?: boolean
}

export interface UpstreamKeyStatus {
  has_key: boolean
  suffix?: string
}

export interface UpstreamKey {
  id: number
  upstream_config_id: number
  name: string
  key_status?: UpstreamKeyStatus
  remote_key_id?: number | null
  upstream_group_id?: number | null
  upstream_group_name?: string
  platform: string
  rate_multiplier?: number | null
  effective_cost_multiplier?: number | null
  status: string
  last_seen_at?: string | null
  missing_count?: number
  missing_since?: string | null
  extra?: Record<string, unknown>
  created_at: string
  updated_at: string
}

export interface UpstreamConfig {
  id: number
  name: string
  provider: UpstreamProvider
  site_url: string
  api_url?: string | null
  auth_mode: UpstreamAuthMode
  credentials_status?: UpstreamCredentialsStatus
  extra?: Record<string, unknown>
  proxy_id?: number | null
  clear_proxy?: boolean
  recharge_rate: number
  balance_to_cny_rate?: number | null
  clear_balance_to_cny_rate?: boolean
  status: string
  last_error?: string | null
  last_checked_at?: string | null
  last_success_at?: string | null
  created_at: string
  updated_at: string
  keys?: UpstreamKey[]
}

export interface UpstreamConfigPayload {
  name: string
  provider: UpstreamProvider
  site_url: string
  api_url?: string | null
  clear_api_url?: boolean
  auth_mode: UpstreamAuthMode
  proxy_id?: number | null
  recharge_rate: number
  balance_to_cny_rate?: number | null
  status?: string
  credentials?: Record<string, string>
  extra?: Record<string, unknown>
}

export interface UpstreamSyncResult {
  run_id?: number
  config_id: number
  name: string
  success: boolean
  key_count: number
  updated_account_count: number
  error?: string
  provider?: string
  status?: string
  stage?: string
  error_code?: string
  retryable?: boolean
  fallback_key_count?: number
  unresolved_key_count?: number
  warnings?: string[]
  duration_ms?: number
}

export interface UpstreamSettings {
  balance_low_threshold_cny: number
  sub2api_not_in_cn_confirmed: boolean
}

export interface UpstreamSyncRecord {
  id: number
  run_id: number
  config_id: number
  config_name: string
  provider: string
  status: string
  stage?: string
  error_code?: string
  safe_message?: string
  retryable: boolean
  http_status?: number | null
  remote_key_count: number
  persisted_key_count: number
  fallback_key_count: number
  unresolved_key_count: number
  updated_account_count: number
  warnings?: string[]
  duration_ms: number
  started_at: string
  finished_at: string
}

export interface UpstreamSyncRun {
  id: number
  trigger: string
  status: string
  total_configs: number
  success_configs: number
  partial_configs: number
  failed_configs: number
  started_at: string
  finished_at?: string | null
  results?: UpstreamSyncRecord[]
}

export interface UpstreamEvent {
  id: number
  config_id: number
  key_id?: number | null
  account_id?: number | null
  run_id?: number | null
  type: string
  severity: string
  message: string
  payload?: Record<string, unknown>
  created_at: string
}

export interface UpstreamIncident {
  id: number
  config_id: number
  type: string
  status: string
  metric_value?: number | null
  threshold_value?: number | null
  metadata?: Record<string, unknown>
  opened_at: string
  last_observed_at: string
  resolved_at?: string | null
}

export interface UpstreamBalanceSnapshot {
  id: number
  config_id: number
  run_id?: number | null
  provider: string
  balance_raw?: number | null
  used_raw?: number | null
  total_raw?: number | null
  balance_cny?: number | null
  used_cny?: number | null
  total_recharged_cny?: number | null
  currency_source: string
  currency_to_cny_rate?: number | null
  currency_rate_source: string
  metadata?: Record<string, unknown>
  observed_at: string
}

export interface UpstreamUsageTrendPoint {
  bucket: string
  requests: number
  upstream_base_cost: number
  upstream_cost: number
  billed_cost: number
  gross_profit: number
  unconverted_cost: number
}

export interface UpstreamUsageTrend {
  range: UpstreamTrendRange
  currency: string
  legacy_attributed_requests: number
  points: UpstreamUsageTrendPoint[]
}

export interface UpstreamOperationsList<T> {
  items: T[]
  total: number
}

export interface UpstreamKeyPayload {
  name?: string
  key: string
  platform?: string
  rate_multiplier?: number | null
}

export async function list(
  page = 1,
  pageSize = 20,
  filters?: { provider?: string; status?: string; search?: string }
): Promise<PaginatedResponse<UpstreamConfig>> {
  const { data } = await apiClient.get<PaginatedResponse<UpstreamConfig>>('/admin/upstream-configs', {
    params: { page, page_size: pageSize, ...filters }
  })
  return data
}

export async function getById(id: number): Promise<UpstreamConfig> {
  const { data } = await apiClient.get<UpstreamConfig>(`/admin/upstream-configs/${id}`)
  return data
}

export async function create(payload: UpstreamConfigPayload): Promise<UpstreamConfig> {
  const { data } = await apiClient.post<UpstreamConfig>('/admin/upstream-configs', payload)
  return data
}

export async function update(id: number, payload: UpstreamConfigPayload): Promise<UpstreamConfig> {
  const { data } = await apiClient.put<UpstreamConfig>(`/admin/upstream-configs/${id}`, payload)
  return data
}

export async function remove(id: number): Promise<{ message: string }> {
  const { data } = await apiClient.delete<{ message: string }>(`/admin/upstream-configs/${id}`)
  return data
}

export async function test(id: number): Promise<{ ok: boolean }> {
  const { data } = await apiClient.post<{ ok: boolean }>(`/admin/upstream-configs/${id}/test`)
  return data
}

export async function syncKeys(id: number): Promise<{ keys: UpstreamKey[]; key_count?: number; updated_account_count?: number; result: UpstreamSyncResult }> {
  const { data } = await apiClient.post<{ keys: UpstreamKey[]; key_count?: number; updated_account_count?: number; result: UpstreamSyncResult }>(`/admin/upstream-configs/${id}/sync-keys`)
  return data
}

export async function syncAllKeys(): Promise<{ run_id?: number; results: UpstreamSyncResult[] }> {
  const { data } = await apiClient.post<{ run_id?: number; results: UpstreamSyncResult[] }>('/admin/upstream-configs/sync-keys')
  return data
}

export async function getSettings(): Promise<UpstreamSettings> {
  const { data } = await apiClient.get<UpstreamSettings>('/admin/upstream-settings')
  return data
}

export async function updateSettings(payload: UpstreamSettings): Promise<UpstreamSettings> {
  const { data } = await apiClient.put<UpstreamSettings>('/admin/upstream-settings', payload)
  return data
}

export async function listSyncRuns(limit = 50, offset = 0): Promise<UpstreamOperationsList<UpstreamSyncRun>> {
  const { data } = await apiClient.get<UpstreamOperationsList<UpstreamSyncRun> | UpstreamSyncRun[]>('/admin/upstream-sync-runs', {
    params: paginationParams(limit, offset)
  })
  return normalizeOperationsList(data)
}

export async function getSyncRun(runId: number): Promise<UpstreamSyncRun> {
  const { data } = await apiClient.get<UpstreamSyncRun>(`/admin/upstream-sync-runs/${runId}`)
  return data
}

export async function listEvents(configId: number, limit = 50, offset = 0): Promise<UpstreamOperationsList<UpstreamEvent>> {
  const { data } = await apiClient.get<UpstreamOperationsList<UpstreamEvent> | UpstreamEvent[]>('/admin/upstream-events', {
    params: { config_id: configId, ...paginationParams(limit, offset) }
  })
  return normalizeOperationsList(data)
}

export async function listIncidents(
  configId: number,
  status = 'open',
  limit = 50,
  offset = 0
): Promise<UpstreamOperationsList<UpstreamIncident>> {
  const { data } = await apiClient.get<UpstreamOperationsList<UpstreamIncident> | UpstreamIncident[]>('/admin/upstream-incidents', {
    params: { config_id: configId, status, ...paginationParams(limit, offset) }
  })
  return normalizeOperationsList(data)
}

export async function getUsageTrend(configId: number, range: UpstreamTrendRange): Promise<UpstreamUsageTrend> {
  const { data } = await apiClient.get<UpstreamUsageTrend>('/admin/upstream-configs/usage-trend', {
    params: { config_id: configId, range }
  })
  return data
}

export async function listBalanceHistory(
  configId: number,
  limit = 50,
  offset = 0
): Promise<UpstreamOperationsList<UpstreamBalanceSnapshot>> {
  const { data } = await apiClient.get<UpstreamOperationsList<UpstreamBalanceSnapshot> | UpstreamBalanceSnapshot[]>(
    `/admin/upstream-configs/${configId}/balance-history`,
    { params: paginationParams(limit, offset) }
  )
  return normalizeOperationsList(data)
}

function normalizeOperationsList<T>(data: UpstreamOperationsList<T> | T[]): UpstreamOperationsList<T> {
  if (Array.isArray(data)) return { items: data, total: data.length }
  return { items: data?.items || [], total: data?.total || 0 }
}

function paginationParams(limit: number, offset: number) {
  const pageSize = Math.max(1, limit)
  return { page: Math.floor(Math.max(0, offset) / pageSize) + 1, page_size: pageSize }
}

export async function listKeys(id: number): Promise<UpstreamKey[]> {
  const { data } = await apiClient.get<UpstreamKey[]>(`/admin/upstream-configs/${id}/keys`)
  return data
}

export async function createKey(id: number, payload: UpstreamKeyPayload): Promise<UpstreamKey> {
  const { data } = await apiClient.post<UpstreamKey>(`/admin/upstream-configs/${id}/keys`, payload)
  return data
}

export async function removeKey(id: number, keyId: number): Promise<{ message: string }> {
  const { data } = await apiClient.delete<{ message: string }>(`/admin/upstream-configs/${id}/keys/${keyId}`)
  return data
}

export default {
  list,
  getById,
  create,
  update,
  remove,
  test,
  syncKeys,
  syncAllKeys,
  getSettings,
  updateSettings,
  listSyncRuns,
  getSyncRun,
  listEvents,
  listIncidents,
  getUsageTrend,
  listBalanceHistory,
  listKeys,
  createKey,
  removeKey
}
