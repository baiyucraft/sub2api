import type { Account } from '@/types'

export const buildTTFTGuardDegradationKey = (account: Account): string => {
  const degradations = account.ttft_guard_degradations ?? []
  return JSON.stringify(
    degradations
      .map((item) => ({
        model: item.model,
        reason: item.reason,
        threshold_ms: item.threshold_ms,
        last_ttft_ms: item.last_ttft_ms,
        ewma_ms: item.ewma_ms,
        sample_count: item.sample_count,
        degraded_at: item.degraded_at,
        last_sample_at: item.last_sample_at,
        expires_at: item.expires_at,
        recovery_samples: item.recovery_samples,
        recovery_samples_required: item.recovery_samples_required
      }))
      .sort((a, b) => a.model.localeCompare(b.model))
  )
}

export const buildUpstreamHealthKey = (account: Account): string => {
  const health = account.upstream_health
  if (!health) return ''
  return JSON.stringify({
    key_id: health.key_id,
    status: health.status,
    observation_enabled: health.observation_enabled,
    reason: health.reason ?? '',
    last_probe_at: health.last_probe_at ?? '',
    last_probe_status: health.last_probe_status ?? '',
    last_evidence_at: health.last_evidence_at ?? '',
    consecutive_failures: health.consecutive_failures,
    last_traffic_status: health.last_traffic_status ?? '',
    recovery_samples: health.recovery_samples ?? 0,
    recovery_samples_required: health.recovery_samples_required ?? 0,
    history: (health.history ?? []).map(item => ({
      observed_at: item.observed_at,
      state: item.state,
      source: item.source,
      result: item.result,
      reason: item.reason ?? ''
    })),
    updated_at: health.updated_at
  })
}

export const mergeRuntimeFields = (oldAccount: Account, updatedAccount: Account): Account => ({
  ...updatedAccount,
  current_concurrency: updatedAccount.current_concurrency ?? oldAccount.current_concurrency,
  current_window_cost: updatedAccount.current_window_cost ?? oldAccount.current_window_cost,
  active_sessions: updatedAccount.active_sessions ?? oldAccount.active_sessions,
  ttft_guard_degradations:
    updatedAccount.platform === 'openai'
      ? updatedAccount.ttft_guard_degradations ?? oldAccount.ttft_guard_degradations
      : undefined,
  upstream_health: updatedAccount.upstream_health
    ? {
        ...updatedAccount.upstream_health,
        history: updatedAccount.upstream_health.history ?? oldAccount.upstream_health?.history
      }
    : oldAccount.upstream_health,
  available_actions: updatedAccount.available_actions ?? oldAccount.available_actions
})
