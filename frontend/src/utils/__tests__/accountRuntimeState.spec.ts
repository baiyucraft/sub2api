import { describe, expect, it } from 'vitest'
import { buildTTFTGuardDegradationKey, mergeRuntimeFields } from '../accountRuntimeState'
import type { Account, AccountTTFTGuardDegradation } from '@/types'

const degradation = (model: string): AccountTTFTGuardDegradation => ({
  model,
  reason: 'ewma',
  threshold_ms: 20_000,
  last_ttft_ms: 24_000,
  ewma_ms: 21_000,
  sample_count: 5,
  degraded_at: '2026-07-22T10:00:00Z',
  last_sample_at: '2026-07-22T10:01:00Z',
  expires_at: '2026-07-22T10:16:00Z',
  recovery_samples: 0,
  recovery_samples_required: 3
})

const account = (overrides: Partial<Account> = {}): Account => ({
  id: 1,
  name: 'openai',
  platform: 'openai',
  type: 'oauth',
  proxy_id: null,
  concurrency: 1,
  priority: 1,
  status: 'active',
  error_message: null,
  last_used_at: null,
  expires_at: null,
  auto_pause_on_expired: true,
  created_at: '2026-07-22T00:00:00Z',
  updated_at: '2026-07-22T00:00:00Z',
  schedulable: true,
  rate_limited_at: null,
  rate_limit_reset_at: null,
  overload_until: null,
  temp_unschedulable_until: null,
  temp_unschedulable_reason: null,
  session_window_start: null,
  session_window_end: null,
  session_window_status: null,
  ...overrides
})

describe('accountRuntimeState', () => {
  it('降级签名不受后端模型数组顺序影响，但会响应指标变化', () => {
    const first = account({ ttft_guard_degradations: [degradation('gpt-b'), degradation('gpt-a')] })
    const reordered = account({ ttft_guard_degradations: [degradation('gpt-a'), degradation('gpt-b')] })
    const changed = account({
      ttft_guard_degradations: [degradation('gpt-a'), { ...degradation('gpt-b'), recovery_samples: 1 }]
    })

    expect(buildTTFTGuardDegradationKey(first)).toBe(buildTTFTGuardDegradationKey(reordered))
    expect(buildTTFTGuardDegradationKey(first)).not.toBe(buildTTFTGuardDegradationKey(changed))
  })

  it('局部更新缺少运行态时保留降级状态，显式空数组则清除', () => {
    const current = account({
      current_concurrency: 2,
      ttft_guard_degradations: [degradation('gpt-5.4-mini')]
    })
    const partial = account({ name: 'renamed' })

    expect(mergeRuntimeFields(current, partial).ttft_guard_degradations).toEqual(
      current.ttft_guard_degradations
    )
    expect(mergeRuntimeFields(current, partial).current_concurrency).toBe(2)
    expect(
      mergeRuntimeFields(current, account({ ttft_guard_degradations: [] })).ttft_guard_degradations
    ).toEqual([])
    expect(
      mergeRuntimeFields(current, account({ platform: 'anthropic' })).ttft_guard_degradations
    ).toBeUndefined()
  })
})
