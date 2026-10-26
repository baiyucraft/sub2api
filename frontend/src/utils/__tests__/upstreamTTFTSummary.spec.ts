import { describe, expect, it } from 'vitest'
import type { UpstreamProtectionGroupState, UpstreamTTFTModelObservation } from '@/types'
import { summarizeTTFTGroups, ttftGroupDisplay, ttftModelDisplay } from '../upstreamTTFTSummary'

const model = (name: string, state: UpstreamTTFTModelObservation['state'], extra = {}): UpstreamTTFTModelObservation => ({
  model: name, state, sample_count: state === 'normal' ? 10 : 1, ewma_ms: 2000, last_ttft_ms: 2000,
  last_sample_at: null, expires_at: null, ...extra
})
const group = (id: number, models: UpstreamTTFTModelObservation[], state = 'insufficient') => ({
  group_id: id, platform_selected: true,
  ttft: { state, source: 'group', models }, cache: { state: 'normal' }
} as UpstreamProtectionGroupState)

describe('TTFT presentation summaries', () => {
  it('shows the actual mixed-model account without counting shared observations three times', () => {
    const models = [model('gpt-6.1-sol', 'normal'), model('gpt-5.6-sol', 'normal'), model('gpt-6-sol', 'insufficient')]
    const groups = [group(8, models), group(21, models), group(22, models)]
    expect(groups.map(ttftGroupDisplay)).toEqual(['partial_insufficient', 'partial_insufficient', 'partial_insufficient'])
    expect(summarizeTTFTGroups(groups)).toEqual({ state: 'partial_insufficient', counts: { normal: 2, insufficient: 1, revalidating: 0, unavailable: 0, protected: 0 } })
  })

  it('uses the unmet group policy when the same model is normal in another group', () => {
    const result = summarizeTTFTGroups([group(1, [model('same-model', 'normal')]), group(2, [model('same-model', 'insufficient')])])
    expect(result.state).toBe('insufficient')
    expect(result.counts.normal).toBe(0)
    expect(result.counts.insufficient).toBe(1)
  })

  it.each(['degraded', 'draining', 'recovering', 'protected'] as const)('preserves protection over partial samples and revalidation (%s)', state => {
    expect(summarizeTTFTGroups([group(1, [model('one', state), model('two', 'normal'), model('three', 'insufficient', { recovery_reason: 'bad_evidence_expired' })])]).state).toBe('protected')
  })

  it('distinguishes recovery verification from initial sample collection', () => {
    expect(ttftModelDisplay(model('one', 'insufficient', { recovery_reason: 'bad_evidence_expired', sample_count: 10 }))).toBe('revalidating')
    expect(ttftModelDisplay(model('one', 'insufficient', { recovery_reason: 'fresh_samples_insufficient', revalidation_epoch: 2 }))).toBe('insufficient')
    expect(ttftModelDisplay(model('one', 'normal', { recovery_reason: 'recovery_samples_insufficient', revalidation_epoch: 2 }))).toBe('normal')
    expect(summarizeTTFTGroups([group(1, [model('one', 'normal'), model('two', 'insufficient', { recovery_reason: 'recovery_sessions_insufficient' })])]).state).toBe('partial_revalidating')
  })

  it('honors the current display state over retained legacy execution labels', () => {
    expect(ttftModelDisplay(model('one', 'draining', { display_state: 'normal' }))).toBe('normal')
  })

  it('excludes disabled and unselected policies, including historical protected models', () => {
    const disabled = group(1, [model('one', 'protected')], 'disabled')
    const excluded = { ...group(2, [model('two', 'protected')]), platform_selected: false }
    expect(ttftGroupDisplay(disabled)).toBe('disabled')
    expect(summarizeTTFTGroups([disabled, excluded]).state).toBe('disabled')
    expect(summarizeTTFTGroups([excluded]).state).toBe('platform_not_selected')
    expect(summarizeTTFTGroups([disabled, excluded]).counts.protected).toBe(0)
  })

  it('never hides unavailable or empty projections behind a populated normal group', () => {
    expect(summarizeTTFTGroups([group(1, [], 'unavailable'), group(2, [model('one', 'normal')], 'normal')]).state).toBe('partial_unavailable')
    expect(summarizeTTFTGroups([group(1, [], 'normal')]).state).toBe('insufficient')
    expect(summarizeTTFTGroups([group(1, [])]).state).toBe('insufficient')
    expect(summarizeTTFTGroups([group(1, [model('one', 'normal')], 'normal')]).state).toBe('normal')
  })
})
