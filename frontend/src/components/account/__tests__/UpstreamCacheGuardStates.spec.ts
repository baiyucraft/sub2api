import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import UpstreamCacheGuardStates from '../UpstreamCacheGuardStates.vue'
import locale from '@/i18n/locales/en/admin/upstreamManagement'
import type { Account, UpstreamCacheGuardState, UpstreamCacheGuardSummary } from '@/types'

vi.mock('vue-i18n', async () => ({ ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'), useI18n: () => ({
  t: (key: string, params: Record<string, unknown> = {}) => {
    let value: unknown = locale
    for (const part of key.replace('admin.', '').split('.')) value = (value as Record<string, unknown>)?.[part]
    return typeof value === 'string' ? value.replace(/\{(\w+)\}/g, (_, name: string) => String(params[name] ?? `{${name}}`)) : key
  },
  te: () => true
}) }))

const summary = (overrides: Partial<UpstreamCacheGuardSummary> = {}): UpstreamCacheGuardSummary => ({
  state: 'normal', rate_percent: 93.2, valid_requests: 42, window_minutes: 60,
  reason: 'healthy', recovery_valid_requests: 0, recovery_rate_percent: null,
  recovery_sessions: 0, recovery_started_at: null, cooldown_until: null,
  updated_at: null, ...overrides
})
const group = (overrides: Partial<UpstreamCacheGuardState> = {}): UpstreamCacheGuardState => ({
  group_id: 1, group_name: 'Low price', state: 'normal', rate_percent: 93.2,
  valid_requests: 42, reason: 'healthy', recovery_valid_requests: 0,
  recovery_rate_percent: null, recovery_sessions: 0, recovery_started_at: null,
  cooldown_until: null, updated_at: null, ...overrides
})
function render(value?: UpstreamCacheGuardSummary, states: UpstreamCacheGuardState[] = []) {
  return mount(UpstreamCacheGuardStates, {
    props: { account: { upstream_cache_guard: value, upstream_cache_guard_states: states } as Account },
    global: { stubs: { HelpTooltip: { template: '<div><slot name="trigger"/><slot/></div>' } } }
  })
}

describe('UpstreamCacheGuardStates', () => {
  it('hides the badge when globally off', () => {
    expect(render(undefined, [group()]).find('[data-test="cache-guard-state"]').exists()).toBe(false)
  })
  it('shows one account badge and separate group execution details', () => {
    const wrapper = render(summary(), [group(), group({ group_id: 2, group_name: 'Other', state: 'disabled', trial_participant: false })])
    expect(wrapper.findAll('[data-test="cache-guard-state"]')).toHaveLength(1)
    expect(wrapper.get('[data-test="cache-guard-state"]').text()).toBe('Normal · 93.2%')
    expect(wrapper.get('[data-cache-guard-group="1"]').text()).toContain('Low price:Normal')
    expect(wrapper.get('[data-cache-guard-group="2"]').text()).toContain('Other:Off')
    expect(wrapper.text()).toContain('Window: 60 minutes')
    expect(wrapper.text()).not.toContain('Linked protection')
  })
  it('distinguishes unknown from a measured zero and preserves recovery progress', () => {
    const unknown = render(summary({ state: 'unavailable', rate_percent: null, reason: 'snapshot_unavailable' }))
    expect(unknown.get('[data-test="cache-guard-state"]').text()).toBe('Temporarily unavailable · Unknown')
    expect(unknown.text()).toContain('Shared state temporarily unavailable')
    const recovery = render(summary({ state: 'recovering', rate_percent: 0, recovery_rate_percent: 82.5, recovery_valid_requests: 25, recovery_sessions: 3, recovery_started_at: '2026-10-13T02:00:00Z' }), [group({ state: 'recovering', trial_participant: true })])
    expect(recovery.get('[data-test="cache-guard-state"]').text()).toBe('Recovery trial · 0.0%')
    expect(recovery.text()).toContain('Current recovery continuation cache rate: 82.5%')
    expect(recovery.text()).toContain('In this recovery trial')
  })
  it.each(['observing', 'protected', 'insufficient'] as const)('shows %s in a single badge', (state) => {
    expect(render(summary({ state, rate_percent: null })).get('[data-test="cache-guard-state"]').text()).toContain('Unknown')
  })
})
