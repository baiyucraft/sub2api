import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import UpstreamCacheGuardStates from '../UpstreamCacheGuardStates.vue'
import locale from '@/i18n/locales/en/admin/upstreamManagement'
import type { Account, UpstreamCacheGuardState, UpstreamCacheGuardProtectionSource } from '@/types'

vi.mock('vue-i18n', async () => ({ ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'), useI18n: () => ({
  t: (key: string, params: Record<string, unknown> = {}) => {
    let value: unknown = locale
    for (const part of key.replace('admin.', '').split('.')) value = (value as Record<string, unknown>)?.[part]
    return typeof value === 'string' ? value.replace(/\{(\w+)\}/g, (_, name: string) => String(params[name] ?? `{${name}}`)) : key
  },
  te: () => true
}) }))

const state = (overrides: Partial<UpstreamCacheGuardState> = {}): UpstreamCacheGuardState => ({
  group_id: 1, group_name: 'Low price', state: 'normal', rate_percent: 85.2,
  valid_requests: 21, reason: 'recovery_verified', recovery_valid_requests: 0,
  recovery_rate_percent: null,
  recovery_sessions: 0, recovery_started_at: null, cooldown_until: null,
  updated_at: null, ...overrides
})
const source = (overrides: Partial<UpstreamCacheGuardProtectionSource> = {}): UpstreamCacheGuardProtectionSource => ({
  group_id: 2, group_name: 'Source group', threshold_percent: 75,
  recovery_threshold_percent: 80, phase: 'trial', rate_percent: 40.5,
  valid_requests: 100, min_valid_requests: 20, recovery_valid_requests: 25,
  recovery_rate_percent: 83.3, recovery_sessions: 3,
  recovery_started_at: '2026-10-13T02:00:00Z', cooldown_until: null,
  updated_at: null, ...overrides
})
function render(states?: UpstreamCacheGuardState[]) {
  return mount(UpstreamCacheGuardStates, {
    props: { account: { upstream_cache_guard_states: states } as Account },
    global: {
      stubs: { HelpTooltip: { template: '<div><slot name="trigger"/><slot/></div>' } }
    }
  })
}

describe('UpstreamCacheGuardStates', () => {
  it('does not show a badge when protection is off or absent', () => {
    expect(render().find('[data-test="cache-guard-states"]').exists()).toBe(false)
    expect(render([]).find('[data-test="cache-guard-states"]').exists()).toBe(false)
    expect(render([state({ state: 'disabled' })]).find('[data-test="cache-guard-states"]').exists()).toBe(false)
  })
  it('shows separate group states and distinguishes unknown from zero', () => {
    const wrapper = render([state({ rate_percent: null, state: 'insufficient' }), state({ group_id: 2, group_name: 'Other', rate_percent: 0, state: 'protected', reason: 'low_cache_rate' })])
    expect(wrapper.get('[data-cache-guard-state="insufficient"]').text()).toContain('Low price · Insufficient samples · Unknown')
    expect(wrapper.get('[data-cache-guard-state="protected"]').text()).toContain('Other · Protected · 0.0%')
    expect(wrapper.text()).toContain('Below protection threshold')
    expect(wrapper.text()).not.toContain('fallback')
  })
  it('shows recovery cohort counts without adding fallback counters', () => {
    const wrapper = render([state({ state: 'recovering', rate_percent: 70, recovery_rate_percent: 82.5, recovery_valid_requests: 25, recovery_sessions: 3, recovery_started_at: '2026-10-13T02:00:00Z' })])
    expect(wrapper.text()).toContain('Current recovery continuation cache rate: 82.5%')
    expect(wrapper.get('[data-cache-guard-state="recovering"]').text()).toContain('70.0%')
    expect(wrapper.text()).toContain('Current recovery valid requests: 25')
    expect(wrapper.text()).toContain('Current recovery sessions: 3')
  })
  it('shows linked protection while keeping local samples and source recovery separate', () => {
    const wrapper = render([state({
      state: 'protected', rate_percent: 91.2, valid_requests: 42,
      reason: 'linked_group_protection', native_state: 'normal', native_reason: 'healthy',
      linked_protection: { protected: true, reason: 'cache_threshold_cascade', primary_source_group_id: 2, sources: [source()] }
    })])
    expect(wrapper.get('[data-cache-guard-state="protected"]').text()).toContain('91.2%')
    expect(wrapper.get('[data-test="linked-protection"]').text()).toBe('Linked protection')
    expect(wrapper.text()).toContain('Valid requests: 42')
    expect(wrapper.text()).toContain('Independent state in this group: Normal')
    expect(wrapper.text()).toContain('The same upstream account is protected by another group')
    const linked = wrapper.get('[data-cache-guard-source="2"]')
    expect(linked.text()).toContain('Linked source group: Source group')
    expect(linked.text()).toContain('Source protection threshold: 75.0%')
    expect(linked.text()).toContain('Source valid requests: 100')
    expect(linked.text()).toContain('Source recovery continuation cache rate: 83.3%')
    expect(linked.text()).toContain('Source recovery valid requests: 25')
    expect(linked.text()).toContain('Source recovery sessions: 3')
    expect(linked.text()).toContain('more than 20 valid requests')
    expect(wrapper.text()).not.toContain('Current recovery valid requests: 25')
    expect(wrapper.text()).toContain('A high cache rate here does not clear source protection')
  })
  it('keeps local unknown samples and shows each source with its own progress', () => {
    const wrapper = render([state({
      state: 'protected', rate_percent: null, valid_requests: 0,
      reason: 'linked_group_protection', native_state: 'insufficient', native_reason: 'insufficient_samples',
      linked_protection: {
        protected: true, reason: 'cache_threshold_cascade', primary_source_group_id: 2,
        sources: [source({ rate_percent: 0, recovery_rate_percent: null, recovery_valid_requests: 0 }), source({ group_id: 3, group_name: undefined, phase: 'cooling', recovery_started_at: null })]
      }
    })])
    expect(wrapper.get('[data-cache-guard-state="protected"]').text()).toContain('Unknown')
    expect(wrapper.text()).toContain('Valid requests: 0')
    expect(wrapper.get('[data-cache-guard-source="2"]').text()).toContain('Source valid continuation cache rate: 0.0%')
    expect(wrapper.get('[data-cache-guard-source="2"]').text()).toContain('Source recovery continuation cache rate: Unknown')
    expect(wrapper.get('[data-cache-guard-source="3"]').text()).toContain('Linked source group: Group #3')
    expect(wrapper.get('[data-cache-guard-source="3"]').text()).toContain('Source state: Cooling down')
    expect(wrapper.get('[data-cache-guard-source="3"]').text()).not.toContain('Source recovery valid requests:')
  })
  it('labels linked observe results without claiming scheduling protection', () => {
    const wrapper = render([state({
      state: 'observing', reason: 'linked_group_protection',
      linked_protection: { protected: true, reason: 'cache_threshold_cascade', primary_source_group_id: 2, sources: [source()] }
    })])
    expect(wrapper.get('[data-test="linked-protection"]').text()).toBe('Linked observation')
    expect(wrapper.text()).toContain('without changing scheduling')
    expect(wrapper.text()).toContain('Low-cache observations for the same upstream account in another group')
    expect(wrapper.text()).not.toContain('does not clear source protection')
  })
  it.each([
    ['healthy', 'Normal window cache performance'],
    ['insufficient_samples', 'Insufficient valid continuation samples'],
    ['snapshot_unavailable', 'Shared state temporarily unavailable'],
    ['below_threshold', 'Below protection threshold'],
    ['waiting_recovery_validation', 'Waiting for independent recovery validation']
  ])('translates API reason %s', (reason, expected) => {
    expect(render([state({ reason })]).text()).toContain(expected)
  })
})
