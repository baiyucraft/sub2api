import { apiClient } from '../client'
import type { Account, PaginatedResponse, UpstreamHealthObservation } from '@/types'

export interface TTFTGuardSettings {
  enabled: boolean
  degradation_ttft_seconds: number
  min_samples: number
}

export interface ProbeGuardSettings {
  enabled: boolean
  suspend_after_failures: number
  recovery_successes: number
  custom_error_codes_enabled: boolean
  custom_error_codes: number[]
}

export interface ProbeModels { models: Record<string, string> }

export interface ProbePlatformDescriptor {
  id: string
  label: string
  models: string[]
  probe_supported: boolean
  probe_reason?: string
}

export interface UpstreamManagementSettings {
  ttft_guard: TTFTGuardSettings
  probe_guard: ProbeGuardSettings
  probe_models: Record<string, string>
  probe_interval_seconds: number
  model_alias_rules?: Record<string, string>
  confidence_probe: UpstreamConfidenceProbeSettings
}

export interface UpstreamConfidenceProbeSettings {
  enabled: boolean
  reasoning_effort: 'high'
  long_context_enabled: boolean
  long_context_max_tokens: number
  quality_degrade_threshold: number
  prompt_version: string
}

export interface ProbeModelCandidates {
  candidates: Record<string, string[]>
  platforms: ProbePlatformDescriptor[]
}

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
  last_failure_source?: string
  last_failure_class?: string
  suspension_source?: string
  updated_at: string
  history?: UpstreamHealthObservation[]
  confidence_score_24h?: number
  confidence_score_7d?: number
  confidence_sample_count_24h?: number
  confidence_sample_count_7d?: number
  confidence_last_score?: number
  confidence_last_probe_at?: string
  confidence_status?: string
  confidence_requested_effort?: string
  confidence_reasoning_tokens?: number
  confidence_breakdown?: Record<string, number>
  confidence_prompt_version?: string
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

export type UpstreamHealthTrendRange = '6h' | '24h' | '7d' | '30d'

export interface UpstreamHealthTrendPoint {
  bucket: string
  state: UpstreamHealthObservation['state']
  state_counts: Partial<Record<UpstreamHealthObservation['state'], number>>
  ttft_p50_ms?: number
  ttft_p95_ms?: number
  duration_avg_ms?: number
  sample_count: number
  ttft_sample_count: number
  primary_source?: string
  latest_reason?: string
  latest_result?: string
}

export interface UpstreamHealthTrend {
  key_id: number
  range: UpstreamHealthTrendRange
  start_at: string
  end_at: string
  bucket_seconds: number
  points: UpstreamHealthTrendPoint[]
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

export async function getSettings(): Promise<UpstreamManagementSettings> {
  const { data } = await apiClient.get<UpstreamManagementSettings>('/admin/upstream-management/settings')
  return data
}

export async function updateSettings(payload: UpstreamManagementSettings): Promise<UpstreamManagementSettings> {
  const { data } = await apiClient.put<UpstreamManagementSettings>('/admin/upstream-management/settings', payload)
  return data
}

export async function getProbeSettings(): Promise<UpstreamManagementSettings> {
  const { data } = await apiClient.get<UpstreamManagementSettings>('/admin/upstream-management/probe-settings')
  return data
}

export async function updateProbeSettings(payload: UpstreamManagementSettings): Promise<UpstreamManagementSettings> {
  const { data } = await apiClient.put<UpstreamManagementSettings>('/admin/upstream-management/probe-settings', payload)
  return data
}

export async function getProbeModelCandidates(): Promise<ProbeModelCandidates> {
  const { data } = await apiClient.get<ProbeModelCandidates>('/admin/upstream-management/probe-model-candidates')
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

export async function getKeyEvents(id: number): Promise<{ items: UpstreamKeyEvent[]; total: number; health_history: UpstreamHealthObservation[] }> {
  const { data } = await apiClient.get<{ items: UpstreamKeyEvent[]; total: number; health_history: UpstreamHealthObservation[] }>(`/admin/upstream-management/keys/${id}/events`)
  return data
}

export async function getKeyHealthTrend(id: number, range: UpstreamHealthTrendRange): Promise<UpstreamHealthTrend> {
  const { data } = await apiClient.get<UpstreamHealthTrend>(`/admin/upstream-management/keys/${id}/health-trend`, {
    params: { range }
  })
  return data
}

export default {
  listAccounts, listAccountsWithEtag, getTTFTGuard, updateTTFTGuard, getProbeModels, updateProbeModels,
  getSettings, updateSettings, getProbeSettings, updateProbeSettings, getProbeModelCandidates,
  setKeyObservation, probeKey, getKeyEvents, getKeyHealthTrend
}
