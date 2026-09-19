import { apiClient } from '../client'

export type CodexTicketPlan = 'pro' | 'team'
export type CodexTicketModel = 'gpt-6-astra' | 'gpt-5.6-sol' | 'gpt-5.6-terra'
export type CodexTicketState = 'disabled' | 'global_disabled' | 'waiting' | 'harvesting' | 'ready' | 'error'

export interface CodexAccountTicketWatchdog {
  enabled: boolean
  trigger_count: number
  last_reason?: 'model_mismatch' | 'state_312'
  last_triggered_at?: string
}

export interface CodexAccountTicketSlot {
  ready?: boolean
  remaining_seconds?: number
  issued_at?: string
  captured_at?: string
  expires_at?: string
  ticket_usable?: boolean
  version?: number
  length?: number
}

export interface CodexAccountTicketModelStatus {
  model: string
  ticket_plan: CodexTicketPlan
  target_length: number
  enabled: boolean
  state: CodexTicketState
  active?: CodexAccountTicketSlot | null
  ready?: CodexAccountTicketSlot | null
  strikes: number
  refreshing: boolean
  retry_after?: string
  last_error: string
  attempts: number
  watchdog: CodexAccountTicketWatchdog

  // Legacy single-ticket response fields remain optional during rolling upgrades.
  remaining_seconds?: number
  ticket_usable?: boolean
  captured_at?: string
  expires_at?: string
}

export interface CodexAccountTicketStatus {
  global_enabled: boolean
  proxy_configured: boolean
  proxy_display: string
  fixed_proxy_configured: boolean
  models?: Partial<Record<CodexTicketModel, CodexAccountTicketModelStatus>> & Record<string, CodexAccountTicketModelStatus>

  // Legacy account-level shape accepted for backwards-compatible reads.
  enabled?: boolean
  model?: string
  ticket_plan?: CodexTicketPlan
  target_length?: number
  state?: CodexTicketState
  remaining_seconds?: number
  ticket_usable?: boolean
  captured_at?: string
  expires_at?: string
  refreshing?: boolean
  retry_after?: string
  last_error?: string
  attempts?: number
  watchdog?: CodexAccountTicketWatchdog
}

export interface CodexAccountTicketModelSettings {
  enabled: boolean
  ticket_plan: CodexTicketPlan
}

export interface CodexAccountTicketSettings {
  models?: Record<CodexTicketModel, CodexAccountTicketModelSettings>

  // Legacy writes are still representable for callers that have not migrated yet.
  enabled?: boolean
  model?: string
  ticket_plan?: CodexTicketPlan
}

export async function getCodexAccountTicket(accountId: number): Promise<CodexAccountTicketStatus> {
  const { data } = await apiClient.get<CodexAccountTicketStatus>(`/admin/accounts/${accountId}/codex-ticket`)
  return data
}

export async function saveCodexAccountTicket(accountId: number, settings: CodexAccountTicketSettings): Promise<CodexAccountTicketStatus> {
  const { data } = await apiClient.put<CodexAccountTicketStatus>(`/admin/accounts/${accountId}/codex-ticket`, settings)
  return data
}

export async function harvestCodexAccountTicket(accountId: number, model: CodexTicketModel): Promise<CodexAccountTicketStatus> {
  const { data } = await apiClient.post<CodexAccountTicketStatus>(`/admin/accounts/${accountId}/codex-ticket/harvest`, { model })
  return data
}
