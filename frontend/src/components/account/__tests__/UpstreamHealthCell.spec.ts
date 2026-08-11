import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import UpstreamHealthCell from '../UpstreamHealthCell.vue'
import type { Account } from '@/types'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key,
      te: (key: string) => key !== 'admin.upstreamManagement.health.reasons.unknown_reason'
    })
  }
})

const account = (overrides: Partial<Account> = {}): Account => ({
  id: 1,
  name: 'Transit-Key A',
  platform: 'openai',
  type: 'apikey',
  proxy_id: null,
  concurrency: 1,
  priority: 1,
  status: 'active',
  error_message: null,
  last_used_at: null,
  expires_at: null,
  auto_pause_on_expired: true,
  created_at: '2026-08-10T00:00:00Z',
  updated_at: '2026-08-10T00:00:00Z',
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

const stubs = {
  HelpTooltip: {
    template: '<div><slot name="trigger" /><div><slot /></div></div>'
  },
  Icon: true
}

describe('UpstreamHealthCell', () => {
  it('uses the transit-hub six-state color semantics and renders probe evidence', () => {
    const wrapper = mount(UpstreamHealthCell, {
      props: {
        account: account({
          upstream_health: {
            key_id: 9,
            status: 'degraded',
            observation_enabled: true,
            reason: 'authentication_failed',
            last_probe_at: '2026-08-10T01:00:00Z',
            last_probe_status: '401',
            last_evidence_at: '2026-08-10T01:01:00Z',
            last_traffic_status: '401',
            consecutive_failures: 2,
            recovery_samples: 0,
            recovery_samples_required: 3,
            updated_at: '2026-08-10T01:01:00Z',
            history: [
              { observed_at: '2026-08-10T00:59:00Z', state: 'healthy', source: 'probe', result: 'success', reason: 'probe_succeeded' },
              { observed_at: '2026-08-10T01:01:00Z', state: 'degraded', source: 'traffic', result: '401', reason: 'authentication_failed' }
            ]
          }
        })
      },
      global: { stubs }
    })

    const badge = wrapper.get('[data-upstream-health-state="degraded"]')
    expect(badge.classes()).toContain('bg-amber-100')
    expect(wrapper.text()).toContain('admin.upstreamManagement.health.degraded')
    expect(wrapper.text()).toContain('admin.upstreamManagement.health.reasons.authentication_failed')
    expect(wrapper.text()).toContain('401')
    expect(wrapper.findAll('[data-observation-state]').map(node => node.attributes('data-observation-state'))).toEqual(['healthy', 'degraded'])
  })

  it('opens the detailed history when the chart is clicked', async () => {
    const wrapper = mount(UpstreamHealthCell, {
      props: {
        account: account({
          upstream_health: {
            key_id: 9,
            status: 'healthy',
            observation_enabled: true,
            consecutive_failures: 0,
            updated_at: '2026-08-10T01:01:00Z',
            history: [{ observed_at: '2026-08-10T01:01:00Z', state: 'healthy', source: 'probe', result: 'success' }]
          }
        })
      },
      global: { stubs }
    })

    await wrapper.get('[data-upstream-health-history]').trigger('click')
    expect(wrapper.emitted('showHistory')).toHaveLength(1)
  })

  it('renders suspended health separately from TTFT model degradation', () => {
    const wrapper = mount(UpstreamHealthCell, {
      props: {
        account: account({
          upstream_health: {
            key_id: 9,
            status: 'suspended',
            observation_enabled: true,
            reason: 'capacity_limited',
            consecutive_failures: 3,
            updated_at: '2026-08-10T01:01:00Z'
          },
          ttft_guard_degradations: [{
            model: 'gpt-5.4-mini',
            reason: 'critical_sample',
            threshold_ms: 20_000,
            last_ttft_ms: 61_000,
            ewma_ms: 25_000,
            sample_count: 2,
            degraded_at: '2026-08-10T01:00:00Z',
            last_sample_at: '2026-08-10T01:01:00Z',
            expires_at: '2026-08-10T01:16:00Z',
            recovery_samples: 0,
            recovery_samples_required: 3
          }]
        })
      },
      global: { stubs }
    })

    expect(wrapper.get('[data-upstream-health-state="suspended"]').classes()).toContain('bg-red-100')
    expect(wrapper.text()).toContain('admin.upstreamManagement.health.temporarilyExcluded')
    expect(wrapper.text()).toContain('gpt-5.4-mini')
  })

  it('does not fabricate a healthy state when there is no health snapshot', () => {
    const wrapper = mount(UpstreamHealthCell, {
      props: { account: account() },
      global: { stubs }
    })

    expect(wrapper.get('[data-upstream-health-state="unobserved"]').text()).toContain('admin.upstreamManagement.health.noData')
    expect(wrapper.text()).not.toContain('admin.upstreamManagement.health.healthy')
  })
})
