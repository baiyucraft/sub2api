import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import UpstreamHealthCell from '../UpstreamHealthCell.vue'
import type { Account, UpstreamConfidenceDistribution } from '@/types'
import zh from '@/i18n/locales/zh/admin/upstreamManagement'
import en from '@/i18n/locales/en/admin/upstreamManagement'
import { formatDateTime } from '@/utils/format'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, values: Record<string, unknown> = {}) => {
        if (!key.includes('.distribution.')) return key
        let value: unknown = zh
        for (const part of key.replace('admin.', '').split('.')) value = (value as Record<string, unknown>)?.[part]
        return typeof value === 'string' ? value.replace(/\{(\w+)\}/g, (_, name: string) => String(values[name] ?? '')) : key
      },
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
  const distribution = (overrides: Partial<UpstreamConfidenceDistribution> = {}): UpstreamConfidenceDistribution => ({
    status: 'collecting', window_size: 128, attempted: 42, valid_samples: 40,
    cells: {
      gpt__screen067: { planned: 64, minimum: 39, completed: 21, valid: 20, counts: { '！': 14, '。': 6 } },
      gpt__screen101: { planned: 16, minimum: 10, completed: 7, valid: 7, counts: { uruguay: 5, __UNSEEN_IN_TRAINING__: 2 } },
      gpt__screen108: { planned: 48, minimum: 29, completed: 14, valid: 13, counts: { '47': 13 } }
    },
    matches: {}, scores: {}, thresholds: {}, claimed_model: 'gpt-6.1-sol',
    window_start: '2026-10-06T00:00:00Z', window_end: '2026-10-06T03:30:00Z',
    baseline_version: '4.5.4-predictive.20261003.1', protocol: 'responses', reasons: ['window_incomplete'], ...overrides
  })
  const mountDistribution = (value: UpstreamConfidenceDistribution, platform = 'openai') => mount(UpstreamHealthCell, {
    props: { account: account({ platform, upstream_health: {
      key_id: 9, status: 'healthy', observation_enabled: true, consecutive_failures: 0, updated_at: '2026-10-06T00:00:00Z',
      confidence_distribution: value,
      confidence_prompt_version: 'openai-juice-multiprobe-v2', confidence_valid_completed_24h: 5, confidence_score_24h: 99
    } }) }, global: { stubs }
  })

  it('shows collection progress without old ratios or premature candidate scores', () => {
    const wrapper = mountDistribution(distribution())
    expect(wrapper.get('[data-test="distribution-badge"]').text()).toBe('采集中 42/128')
    expect(wrapper.get('[data-test="distribution-badge"]').classes()).toContain('bg-gray-100')
    expect(wrapper.get('[data-test="health-confidence-row"]').classes()).toContain('flex-wrap')
    expect(wrapper.text()).toContain('40 / 42')
    expect(wrapper.text()).toContain('uruguay')
    expect(wrapper.text()).toContain('未见答案')
    expect(wrapper.text()).toContain('4.5.4-predictive.20261003.1')
    expect(wrapper.find('[data-test="confidence-badge"]').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('confidence24h')
    expect(wrapper.text()).not.toContain('0.0000')
    expect(wrapper.find('[data-test="distribution-reset"]').exists()).toBe(false)
  })

  it('explains pending configuration changes without changing the compact progress or showing a reset time', () => {
    const wrapper = mountDistribution(distribution({ attempted: 0, valid_samples: 0, series_reset: {
      pending: true, at: null, reasons: ['credential_changed', 'endpoint_changed'], previous_attempted: 113
    } }))
    expect(wrapper.get('[data-test="distribution-badge"]').text()).toBe('采集中 0/128')
    expect(wrapper.get('[data-test="distribution-badge"]').classes()).toContain('bg-gray-100')
    expect(wrapper.get('[data-test="distribution-reset-pending"]').text()).toBe('配置已变化，等待下一次探针重新累计')
    expect(wrapper.get('[data-test="distribution-reset-reasons"]').text()).toBe('鉴权凭据变化 · 请求端点变化')
    expect(wrapper.get('[data-test="distribution-reset-previous"]').text()).toBe('113 / 128')
    expect(wrapper.find('[data-test="distribution-reset-at"]').exists()).toBe(false)
  })

  it('shows the actual reset time and previous window after the first attempt in a new series', () => {
    const at = '2026-10-07T13:34:39Z'
    const wrapper = mountDistribution(distribution({ attempted: 1, valid_samples: 1, series_reset: {
      pending: false, at, reasons: ['legacy_identity_unverifiable'], previous_attempted: 18
    } }))
    expect(wrapper.get('[data-test="distribution-badge"]').text()).toBe('采集中 1/128')
    expect(wrapper.find('[data-test="distribution-reset-pending"]').exists()).toBe(false)
    expect(wrapper.get('[data-test="distribution-reset-at"]').text()).toBe(formatDateTime(at))
    expect(wrapper.get('[data-test="distribution-reset-reasons"]').text()).toBe('旧采样身份无法验证连续性')
    expect(wrapper.get('[data-test="distribution-reset-previous"]').text()).toBe('18 / 128')
  })

  it('uses a safe fallback for unknown reset reasons and tolerates an omitted reset timestamp', () => {
    const sensitive = 'api_key=private-value'
    const wrapper = mountDistribution(distribution({ series_reset: {
      pending: false, reasons: [sensitive, 'new_server_reason', 'model_changed'], previous_attempted: 128
    } }))
    expect(wrapper.get('[data-test="distribution-reset-at"]').text()).toBe('-')
    expect(wrapper.get('[data-test="distribution-reset-reasons"]').text()).toBe('其他请求配置变化 · Sol 有效模型变化')
    expect(wrapper.text()).not.toContain(sensitive)
    expect(wrapper.text()).not.toContain('new_server_reason')
  })

  it('translates every identity reset reason in both locales', () => {
    const reasons = ['binding_changed', 'protocol_changed', 'endpoint_changed', 'credential_changed',
      'model_changed', 'proxy_changed', 'headers_changed', 'contract_changed', 'baseline_changed', 'legacy_identity_unverifiable']
    const wrapper = mountDistribution(distribution({ series_reset: { pending: false, reasons, previous_attempted: 128 } }))
    for (const reason of reasons) {
      const zhMessage = zh.upstreamManagement.health.distribution.resetReasons[reason as keyof typeof zh.upstreamManagement.health.distribution.resetReasons]
      const enMessage = en.upstreamManagement.health.distribution.resetReasons[reason as keyof typeof en.upstreamManagement.health.distribution.resetReasons]
      expect(zhMessage).toBeTruthy()
      expect(enMessage).toBeTruthy()
      expect(wrapper.get('[data-test="distribution-reset-reasons"]').text()).toContain(zhMessage)
      expect(wrapper.text()).not.toContain(`resetReasons.${reason}`)
    }
  })

  it.each([
    { status: 'match' as const, closest_model: 'gpt-6.1-sol', label: 'Sol 匹配', color: 'bg-emerald-100' },
    { status: 'mismatch' as const, closest_model: 'gpt-6-astra', label: '疑似 Astra', color: 'bg-red-100' },
    { status: 'mismatch' as const, closest_model: 'other_known_external', label: '疑似 other', color: 'bg-red-100' },
    { status: 'insufficient' as const, closest_model: 'gpt-6-astra', label: '证据不足', color: 'bg-amber-100' }
  ])('renders $label while leaving health unchanged', ({ status, closest_model, label, color }) => {
    const wrapper = mountDistribution(distribution({ status, closest_model, attempted: 128, valid_samples: 121,
      matches: { 'gpt-6-astra': 0.57321, 'gpt-6.1-sol': 0.42679 }, reasons: status === 'insufficient' ? ['samples_incomplete'] : [] }))
    expect(wrapper.get('[data-test="distribution-badge"]').text()).toBe(label)
    expect(wrapper.get('[data-test="distribution-badge"]').classes()).toContain(color)
    expect(wrapper.get('[data-upstream-health-state="healthy"]').classes()).toContain('bg-emerald-100')
    expect(wrapper.get('[data-test="distribution-score-gpt-6-astra"]').text()).toBe('0.573210')
    expect(wrapper.text()).toContain('行为匹配分数')
    expect(wrapper.text()).toContain('不是模型身份概率')
    expect(wrapper.text()).not.toContain('57%')
    if (status === 'insufficient') expect(wrapper.text()).toContain('有效样本不足')
  })

  it('does not show an OpenAI distribution for another platform', () => {
    const wrapper = mountDistribution(distribution(), 'anthropic')
    expect(wrapper.find('[data-test="distribution-badge"]').exists()).toBe(false)
  })

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
        confidence_prompt_version: 'openai-juice-multiprobe-v2',
        confidence_valid_completed_24h: 4, confidence_valid_completed_7d: 10,
        confidence_current_success_24h: 3, confidence_current_success_7d: 8,
        confidence_evidence: { kind: 'juice', claimed_model: 'gpt-5.6-sol', requested_effort: 'high', expected_value: '40', observed_value: '40', classification: 'current_success' }
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

  it('renders mixed evidence and degrades the badge for a 24h hard anomaly', () => {
    const wrapper = mount(UpstreamHealthCell, {
      props: { account: account({ upstream_health: {
        key_id: 9, status: 'healthy', observation_enabled: true, consecutive_failures: 0,
        updated_at: '2026-08-24T01:01:00Z', confidence_score_24h: 50, confidence_score_7d: 50,
        confidence_sample_count_24h: 2, confidence_sample_count_7d: 2,
        confidence_valid_completed_24h: 2, confidence_valid_completed_7d: 2,
        confidence_current_success_24h: 1, confidence_current_success_7d: 1,
        confidence_mixed_24h: 1, confidence_mixed_7d: 1,
        confidence_output_rewrite_24h: 1, confidence_output_rewrite_7d: 1,
        confidence_prompt_version: 'openai-juice-multiprobe-v2',
        confidence_evidence: { kind: 'juice', claimed_model: 'gpt-5.6-sol', requested_effort: 'high', expected_value: '40', observed_value: '48', classification: 'mixed', mixed_models: ['gpt-5.6-luna'] }
      } }) },
      global: { stubs }
    })

    expect(wrapper.get('[data-test="confidence-badge"]').classes()).toContain('bg-red-100')
    expect(wrapper.text()).toContain('40')
    expect(wrapper.text()).toContain('48')
    expect(wrapper.text()).toContain('gpt-5.6-luna')
    expect(wrapper.text()).toContain('admin.upstreamManagement.health.confidenceStatuses.mixed')
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
