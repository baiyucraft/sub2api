import type { UpstreamProtectionGroupState, UpstreamTTFTModelObservation } from '@/types'

/** Presentation only: these categories never change the scheduler's state. */
export type TTFTModelDisplay = 'protected' | 'normal' | 'insufficient' | 'revalidating' | 'unavailable'
export type TTFTSummaryDisplay = TTFTModelDisplay | 'partial_insufficient' | 'partial_revalidating' | 'partial_unavailable' | 'disabled' | 'platform_not_selected'

const revalidationReasons = new Set([
  'bad_evidence_expired', 'recovery_samples_insufficient', 'recovery_sessions_insufficient',
  'waiting_revalidation', 'waiting_recovery_samples', 'waiting_recovery_sessions'
])
const severity: Record<TTFTModelDisplay, number> = { normal: 0, insufficient: 1, revalidating: 2, unavailable: 3, protected: 4 }

export function ttftModelDisplay(model: UpstreamTTFTModelObservation): TTFTModelDisplay {
  // New display_state overrides legacy execution labels, including a retained
  // degraded label after recovery. An epoch by itself does not imply recovery.
  const state = model.display_state ?? model.state
  if (['protected', 'degraded', 'draining', 'recovering'].includes(state)) return 'protected'
  if (state === 'normal' || state === 'healthy') return 'normal'
  if (model.state === 'unavailable') return 'unavailable'
  return revalidationReasons.has(model.recovery_reason ?? '') ? 'revalidating' : 'insufficient'
}

function aggregate(states: TTFTModelDisplay[]): TTFTSummaryDisplay {
  if (states.includes('protected')) return 'protected'
  if (states.includes('unavailable')) return states.some(state => state !== 'unavailable') ? 'partial_unavailable' : 'unavailable'
  if (states.includes('revalidating')) return states.some(state => state !== 'revalidating') ? 'partial_revalidating' : 'revalidating'
  if (states.includes('insufficient')) return states.includes('normal') ? 'partial_insufficient' : 'insufficient'
  return states.length ? 'normal' : 'insufficient'
}

export function ttftGroupDisplay(group: UpstreamProtectionGroupState): TTFTSummaryDisplay {
  if (group.platform_selected === false || group.ttft.state === 'platform_not_selected' || group.cache.state === 'platform_not_selected') return 'platform_not_selected'
  if (group.ttft.state === 'disabled') return 'disabled'
  const state = group.ttft.display_state ?? group.ttft.state
  if (['protected', 'degraded', 'draining', 'recovering'].includes(state) || group.ttft.models.some(model => ttftModelDisplay(model) === 'protected')) return 'protected'
  if (group.ttft.state === 'unavailable' || group.ttft.source === 'unavailable') return 'unavailable'
  return aggregate(group.ttft.models.map(ttftModelDisplay))
}

export function summarizeTTFTGroups(groups: UpstreamProtectionGroupState[]) {
  const selected = groups.filter(group => ttftGroupDisplay(group) !== 'platform_not_selected')
  const enabled = selected.filter(group => group.ttft.state !== 'disabled')
  const models = new Map<string, TTFTModelDisplay>()
  for (const group of enabled) {
    const unavailable = ttftGroupDisplay(group) === 'unavailable'
    for (const model of group.ttft.models) {
      const modelState = ttftModelDisplay(model)
      const state = unavailable && modelState !== 'protected' ? 'unavailable' : modelState
      const previous = models.get(model.model)
      if (previous == null || severity[state] > severity[previous]) models.set(model.model, state)
    }
  }
  const counts: Record<TTFTModelDisplay, number> = { normal: 0, insufficient: 0, revalidating: 0, unavailable: 0, protected: 0 }
  for (const state of models.values()) counts[state]++
  let state: TTFTSummaryDisplay
  if (!selected.length) state = groups.length ? 'platform_not_selected' : 'insufficient'
  else if (!enabled.length) state = 'disabled'
  else if (enabled.some(group => ttftGroupDisplay(group) === 'protected')) state = 'protected'
  else {
    const values = [...models.values()]
    // A missing/unavailable projection cannot be hidden by another group's
    // populated model list. Counts describe only the models actually present.
    for (const group of enabled) {
      if (ttftGroupDisplay(group) === 'unavailable') values.push('unavailable')
      else if (!group.ttft.models.length) values.push('insufficient')
    }
    state = aggregate(values)
  }
  return { state, counts }
}
