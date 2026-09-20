export const models = ['gpt-6-astra', 'gpt-5.6-sol', 'gpt-5.6-terra'] as const
export type Model = typeof models[number]
export type Plan = 'pro' | 'team'
export const secretFields = ['harvest_proxy_url', 'dial_proxy_url'] as const
export type SecretFlags = Record<typeof secretFields[number], boolean>
export interface ModelConfig { enabled: boolean; ticket_plan: Plan }
export interface AccountConfig { account_id: number; models: Record<Model, ModelConfig> }
export interface Config {
  version: 1
  enabled: boolean
  harvest_proxy_url: ''
  dial_proxy_url: ''
  accounts: AccountConfig[]
}
export interface AccountResource {
  id: number
  name: string
  platform: string
  account_type: string
  group_ids: number[]
  business_egress_configured: boolean
}
export interface Resources {
  accounts: AccountResource[]
  groups: Array<{ id: number; name: string }>
}
export interface Slot { expires_at: string; remaining_seconds: number; usable: boolean }
export type State = 'disabled' | 'global_disabled' | 'waiting' | 'queued' | 'harvesting' | 'ready' | 'error' | 'cooldown' | 'unavailable' | 'cancelled' | 'unknown'
export interface ModelStatus {
  state: State
  active: Slot | null
  ready: Slot | null
  strikes: number
  cooldown_until: string
  refreshing: boolean
  attempts: number
  last_error: string
}
export interface AccountStatus { account_id: number; models: Record<Model, ModelStatus> }
export interface Log { at: string; level: 'info' | 'warn' | 'error'; account_id: number; model: string; message: string }
export interface Status { running: boolean; accounts: AccountStatus[]; logs: Log[] }

export function record(input: unknown): Record<string, unknown> {
  return input && typeof input === 'object' && !Array.isArray(input) ? input as Record<string, unknown> : {}
}
const text = (value: unknown) => typeof value === 'string' ? value : ''
const count = (value: unknown) => typeof value === 'number' && Number.isFinite(value) ? Math.max(0, value) : 0
const id = (value: unknown): value is number => Number.isSafeInteger(value) && Number(value) > 0
const rows = (value: unknown) => Array.isArray(value) ? value.map(record) : []
const date = (value: unknown) => typeof value === 'string' && Number.isFinite(Date.parse(value)) ? new Date(value).toISOString() : ''

export function defaultAccount(account_id: number): AccountConfig {
  return { account_id, models: Object.fromEntries(models.map(m => [m, { enabled: false, ticket_plan: 'pro' }])) as AccountConfig['models'] }
}

export function normalizeConfig(input: unknown): Config {
  const value = record(input)
  const seen = new Set<number>()
  return {
    version: 1, enabled: value.enabled === true,
    harvest_proxy_url: '', dial_proxy_url: '',
    accounts: rows(value.accounts).filter(a => {
      if (!id(a.account_id) || seen.has(a.account_id)) return false
      seen.add(a.account_id)
      return true
    }).map(a => {
      const account = defaultAccount(a.account_id as number)
      for (const model of models) {
        const config = record(record(a.models)[model])
        account.models[model] = { enabled: config.enabled === true, ticket_plan: config.ticket_plan === 'team' ? 'team' : 'pro' }
      }
      return account
    }),
  }
}

export function normalizeSecretFlags(input: unknown): SecretFlags {
  const value = record(input)
  return { harvest_proxy_url: value.harvest_proxy_url === true, dial_proxy_url: value.dial_proxy_url === true }
}

export function normalizeResources(input: unknown): Resources {
  const value = record(input)
  return {
    accounts: rows(value.accounts).filter(a => id(a.id) && a.platform === 'openai' && ['oauth', 'setup-token'].includes(text(a.account_type))).map(a => ({
      id: a.id as number, name: text(a.name).slice(0, 160), platform: 'openai', account_type: text(a.account_type),
      group_ids: Array.isArray(a.group_ids) ? a.group_ids.filter(id) : [], business_egress_configured: a.business_egress_configured === true,
    })),
    groups: rows(value.groups).filter(g => id(g.id)).map(g => ({ id: g.id as number, name: text(g.name).slice(0, 160) })),
  }
}

// Retain only diagnostic categories. Free-form upstream errors, URLs, headers and
// ticket material must never enter reactive status, logs or rendered markup.
export const diagnosticCodes = ['model_mismatch', 'invalid_state', 'state_312', 'proxy_unavailable', 'harvest_failed', 'verification_failed', 'cancelled', 'started', 'completed', 'cooldown', 'config_saved', 'state_envelope', 'state_unavailable', 'upstream_rejected', 'harvest_persistence_failed', 'watchdog_persistence_failed', 'harvest_not_started', 'harvest_finalize_failed', 'harvest_persisted'] as const
export function diagnostic(value: unknown): string {
  return diagnosticCodes.includes(value as typeof diagnosticCodes[number]) ? String(value) : value ? 'redacted' : ''
}

function slot(input: unknown): Slot | null {
  if (!input || typeof input !== 'object' || Array.isArray(input)) return null
  const value = record(input)
  return { expires_at: date(value.expires_at), remaining_seconds: count(value.remaining_seconds), usable: value.usable === true || value.ticket_usable === true }
}

export function emptyModelStatus(): ModelStatus {
  return { state: 'unknown', active: null, ready: null, strikes: 0, cooldown_until: '', refreshing: false, attempts: 0, last_error: '' }
}

export function normalizeStatus(result: unknown): Status {
  const response = record(result)
  let value: Record<string, unknown> = {}
  try { value = record(typeof response.status_json === 'string' ? JSON.parse(response.status_json) : response.status_json) } catch { /* no raw payload retained */ }
  const states: State[] = ['disabled', 'global_disabled', 'waiting', 'queued', 'harvesting', 'ready', 'error', 'cooldown', 'unavailable', 'cancelled']
  return {
    running: response.healthy === true && value.running === true,
    accounts: rows(value.accounts).filter(a => id(a.account_id)).map(a => ({
      account_id: a.account_id as number,
      models: Object.fromEntries(models.map(model => {
        const m = record(record(a.models)[model])
        return [model, {
          state: states.includes(m.state as State) ? m.state : 'unknown', active: slot(m.active), ready: slot(m.ready),
          strikes: count(m.strikes), attempts: count(m.attempts), refreshing: m.refreshing === true,
          cooldown_until: date(m.cooldown_until || m.retry_after), last_error: diagnostic(m.error_code || m.last_error),
        }]
      })) as AccountStatus['models'],
    })),
    logs: rows(value.logs).slice(-200).map(log => ({
      at: date(log.at || log.timestamp), level: log.level === 'error' ? 'error' : log.level === 'warn' ? 'warn' : 'info',
      account_id: id(log.account_id) ? log.account_id : 0,
      model: models.includes(log.model as Model) ? String(log.model) : '',
      message: diagnostic(log.code || log.message),
    })),
  }
}
