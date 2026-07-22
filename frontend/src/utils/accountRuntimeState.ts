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

export const mergeRuntimeFields = (oldAccount: Account, updatedAccount: Account): Account => ({
  ...updatedAccount,
  current_concurrency: updatedAccount.current_concurrency ?? oldAccount.current_concurrency,
  current_window_cost: updatedAccount.current_window_cost ?? oldAccount.current_window_cost,
  active_sessions: updatedAccount.active_sessions ?? oldAccount.active_sessions,
  ttft_guard_degradations:
    updatedAccount.platform === 'openai'
      ? updatedAccount.ttft_guard_degradations ?? oldAccount.ttft_guard_degradations
      : undefined
})
