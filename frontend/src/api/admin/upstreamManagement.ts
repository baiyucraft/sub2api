import { apiClient } from '../client'
import type { Account, PaginatedResponse } from '@/types'

export interface TTFTGuardSettings {
  enabled: boolean
  degradation_ttft_seconds: number
  min_samples: number
}

export interface ProbeModels { models: Record<string, string> }

export interface UpstreamHealthSnapshot {
  key_id: number
  status: 'healthy' | 'degraded' | 'suspended' | 'observing' | 'recovering' | 'disabled'
  observation_enabled: boolean
  reason?: string
  last_probe_at?: string
  last_probe_status?: string
  last_evidence_at?: string
  last_traffic_status?: string
  consecutive_failures: number
  recovery_samples?: number
  recovery_samples_required?: number
  updated_at: string
}

export interface UpstreamKeyEvent {
  id: number
  config_id: number
  key_id?: number
  account_id?: number
  type: string
  severity: string
  message: string
  payload?: Record<string, unknown>
  created_at: string
}

export async function listAccounts(params: Record<string, unknown> = {}): Promise<PaginatedResponse<Account>> {
  const { data } = await apiClient.get<PaginatedResponse<Account>>('/admin/upstream-management/accounts', { params })
  return data
}

export interface UpstreamAccountListWithEtagResult {
  notModified: boolean
  etag: string | null
  data: PaginatedResponse<Account> | null
}

export async function listAccountsWithEtag(
  params: Record<string, unknown> = {},
  options?: { signal?: AbortSignal; etag?: string | null }
): Promise<UpstreamAccountListWithEtagResult> {
  const headers: Record<string, string> = {}
  if (options?.etag) headers['If-None-Match'] = options.etag

  const response = await apiClient.get<PaginatedResponse<Account>>('/admin/upstream-management/accounts', {
    params,
    headers,
    signal: options?.signal,
    validateStatus: status => (status >= 200 && status < 300) || status === 304
  })
  const etag = typeof response.headers?.etag === 'string' ? response.headers.etag : null
  return response.status === 304
    ? { notModified: true, etag, data: null }
    : { notModified: false, etag, data: response.data }
}

export async function getTTFTGuard(): Promise<TTFTGuardSettings> {
  const { data } = await apiClient.get<TTFTGuardSettings>('/admin/upstream-management/ttft-guard')
  return data
}

export async function updateTTFTGuard(payload: TTFTGuardSettings): Promise<TTFTGuardSettings> {
  const { data } = await apiClient.put<TTFTGuardSettings>('/admin/upstream-management/ttft-guard', payload)
  return data
}

export async function getProbeModels(): Promise<ProbeModels> {
  const { data } = await apiClient.get<ProbeModels>('/admin/upstream-management/probe-models')
  return data
}

export async function updateProbeModels(models: Record<string, string>): Promise<ProbeModels> {
  const { data } = await apiClient.put<ProbeModels>('/admin/upstream-management/probe-models', { models })
  return data
}

export async function setKeyObservation(id: number, enabled: boolean): Promise<UpstreamHealthSnapshot> {
  const { data } = await apiClient.put<UpstreamHealthSnapshot>(`/admin/upstream-management/keys/${id}/observation`, { enabled })
  return data
}

export async function probeKey(id: number): Promise<UpstreamHealthSnapshot> {
  const { data } = await apiClient.post<UpstreamHealthSnapshot>(`/admin/upstream-management/keys/${id}/probe`)
  return data
}

export async function getKeyEvents(id: number): Promise<{ items: UpstreamKeyEvent[]; total: number }> {
  const { data } = await apiClient.get<{ items: UpstreamKeyEvent[]; total: number }>(`/admin/upstream-management/keys/${id}/events`)
  return data
}

export default {
  listAccounts, listAccountsWithEtag, getTTFTGuard, updateTTFTGuard, getProbeModels, updateProbeModels,
  setKeyObservation, probeKey, getKeyEvents
}
