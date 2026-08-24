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

  it('健康列不展示 TTFT 模型临时排除', () => {
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
    expect(wrapper.text()).not.toContain('gpt-5.4-mini')
  })

  it('does not fabricate a healthy state when there is no health snapshot', () => {
    const wrapper = mount(UpstreamHealthCell, {
      props: { account: account() },
      global: { stubs }
    })

    expect(wrapper.get('[data-upstream-health-state="unobserved"]').text()).toContain('admin.upstreamManagement.health.noData')
    expect(wrapper.text()).not.toContain('admin.upstreamManagement.health.healthy')
  })

  it('renders OpenAI health and Juice confidence badges in the same row', () => {
    const wrapper = mount(UpstreamHealthCell, {
      props: { account: account({ upstream_health: {
        key_id: 9, status: 'healthy', observation_enabled: true, consecutive_failures: 0,
        updated_at: '2026-08-24T01:01:00Z', confidence_score_24h: 75, confidence_score_7d: 80,
        confidence_sample_count_24h: 4, confidence_sample_count_7d: 10,
        confidence_status: 'current_success', confidence_requested_effort: 'high',
        confidence_breakdown: { valid_completed: 1, current_success: 1 },
        confidence_prompt_version: 'openai-juice-high-v1'
      } }) },
      global: { stubs }
    })

    const row = wrapper.get('[data-test="health-confidence-row"]')
    expect(row.find('[data-upstream-health-state="healthy"]').exists()).toBe(true)
    expect(row.find('[data-test="confidence-badge"]').exists()).toBe(true)
    expect(wrapper.text()).toContain('high')
    expect(wrapper.text()).toContain('current_success')
  })

  it('does not render confidence placeholders for non-OpenAI accounts', () => {
    const wrapper = mount(UpstreamHealthCell, {
      props: { account: account({ platform: 'anthropic', upstream_health: {
        key_id: 9, status: 'healthy', observation_enabled: true, consecutive_failures: 0,
        updated_at: '2026-08-24T01:01:00Z', confidence_sample_count_24h: 2,
        confidence_sample_count_7d: 2, confidence_score_24h: 100, confidence_score_7d: 100
      } }) },
      global: { stubs }
    })
    expect(wrapper.find('[data-test="confidence-badge"]').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('admin.upstreamManagement.health.confidenceLabel')
  })

  it('does not render confidence when OpenAI has no valid completed samples', () => {
    const wrapper = mount(UpstreamHealthCell, {
      props: { account: account({ upstream_health: {
        key_id: 9, status: 'healthy', observation_enabled: true, consecutive_failures: 0,
        updated_at: '2026-08-24T01:01:00Z', confidence_sample_count_24h: 0,
        confidence_sample_count_7d: 0, confidence_status: 'data_insufficient'
      } }) },
      global: { stubs }
    })
    expect(wrapper.find('[data-test="confidence-badge"]').exists()).toBe(false)
    expect(wrapper.find('[data-upstream-health-state="unobserved"]').exists()).toBe(false)
  })
})
