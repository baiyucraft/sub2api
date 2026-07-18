/**
 * User-facing Channel Monitor API endpoints
 * Read-only views for end users to inspect channel availability/status.
 */

import { apiClient } from './client'
import type { Provider, MonitorStatus } from './admin/channelMonitor'

export type { Provider, MonitorStatus } from './admin/channelMonitor'

export interface UserMonitorExtraModel {
  model: string
  status: MonitorStatus
  latency_ms: number | null
}

export interface MonitorTimelinePoint {
  status: MonitorStatus
  latency_ms: number | null
  ping_latency_ms: number | null
  checked_at: string
}

export interface UserMonitorView {
  id: number
  name: string
  provider: Provider
  group_name: string
  primary_model: string
  primary_status: MonitorStatus
  primary_latency_ms: number | null
  primary_ping_latency_ms: number | null
  availability_7d: number
  extra_models: UserMonitorExtraModel[]
  timeline: MonitorTimelinePoint[]
  show_group_rate: boolean
  current_public_rate?: number | null
  rate_observed_since?: string | null
  rate_trend?: MonitorRateTrendPoint[]
}

export interface MonitorRateTrendPoint {
  observed_at: string
  rate: number
}

export interface UserMonitorListResponse {
  items: UserMonitorView[]
}

export interface UserMonitorModelDetail {
  model: string
  latest_status: MonitorStatus
  latest_latency_ms: number | null
  availability_7d: number
  availability_15d: number
  availability_30d: number
  avg_latency_7d_ms: number | null
}

export interface UserMonitorDetail {
  id: number
  name: string
  provider: Provider
  group_name: string
  models: UserMonitorModelDetail[]
  show_group_rate: boolean
  current_public_rate?: number | null
  rate_observed_since?: string | null
  rate_trend?: MonitorRateTrendPoint[]
}

export type MonitorRateRange = '24h' | '7d' | '30d'

/**
 * List all monitor views available to the current user.
 */
export async function list(options?: { signal?: AbortSignal; rateRange?: MonitorRateRange }): Promise<UserMonitorListResponse> {
  const { data } = await apiClient.get<UserMonitorListResponse>('/channel-monitors', {
    params: { rate_range: options?.rateRange || '24h' },
    signal: options?.signal,
  })
  return data
}

/**
 * Get detailed status (multi-window availability + latency) for a single monitor.
 */
export async function status(id: number, rateRange: MonitorRateRange = '24h'): Promise<UserMonitorDetail> {
  const { data } = await apiClient.get<UserMonitorDetail>(`/channel-monitors/${id}/status`, { params: { rate_range: rateRange } })
  return data
}

export const channelMonitorUserAPI = {
  list,
  status,
}

export default channelMonitorUserAPI
