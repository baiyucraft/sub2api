import { apiClient } from '../client'
import type { PaginatedResponse } from '@/types'

export type UpstreamProvider = 'sub2api' | 'newapi' | 'other'
export type UpstreamAuthMode = 'user_login' | 'manual_jwt'

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
  status: string
  last_seen_at?: string | null
  extra?: Record<string, unknown>
  created_at: string
  updated_at: string
}

export interface UpstreamConfig {
  id: number
  name: string
  provider: UpstreamProvider
  base_url: string
  auth_mode: UpstreamAuthMode
  credentials_status?: UpstreamCredentialsStatus
  extra?: Record<string, unknown>
  proxy_id?: number | null
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
  base_url: string
  auth_mode: UpstreamAuthMode
  proxy_id?: number | null
  status?: string
  credentials?: Record<string, string>
  extra?: Record<string, unknown>
}

export interface UpstreamSyncResult {
  config_id: number
  name: string
  success: boolean
  key_count: number
  updated_account_count: number
  error?: string
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

export async function syncKeys(id: number): Promise<{ keys: UpstreamKey[]; key_count?: number; updated_account_count?: number }> {
  const { data } = await apiClient.post<{ keys: UpstreamKey[]; key_count?: number; updated_account_count?: number }>(`/admin/upstream-configs/${id}/sync-keys`)
  return data
}

export async function syncAllKeys(): Promise<{ results: UpstreamSyncResult[] }> {
  const { data } = await apiClient.post<{ results: UpstreamSyncResult[] }>('/admin/upstream-configs/sync-keys')
  return data
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
  listKeys,
  createKey,
  removeKey
}
