import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import UpstreamHealthCell from '../UpstreamHealthCell.vue'
import type { Account } from '@/types'
import zh from '@/i18n/locales/zh/admin/upstreamManagement'
import en from '@/i18n/locales/en/admin/upstreamManagement'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, values: Record<string, unknown> = {}) => {
        if (!key.includes('.distribution.') && !key.includes('.attribution.') && !key.includes('.unified.')) return key
        let value: unknown = zh
        for (const part of key.replace('admin.', '').split('.')) value = (value as Record<string, unknown>)?.[part]
        return typeof value === 'string' ? value.replace(/\{(\w+)\}/g, (_, name: string) => String(values[name] ?? '')) : key
      },
      te: (key: string) => {
        if (key === 'admin.upstreamManagement.health.reasons.unknown_reason') return false
        if (/admin\.upstreamManagement\.health\.(attribution|unified|distribution)\.reasons\./.test(key)) {
          let value: unknown = zh
          for (const part of key.replace('admin.', '').split('.')) value = (value as Record<string, unknown>)?.[part]
          return typeof value === 'string'
        }
        return true
      }
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

type Snapshot = NonNullable<Account['upstream_health']>
const healthySnapshot = (extra: Record<string, unknown> = {}): Snapshot => ({
  key_id: 9, status: 'healthy', observation_enabled: true, consecutive_failures: 0,
  updated_at: '2026-10-06T00:00:00Z', ...extra
}) as Snapshot
const summary = (extra: Record<string, unknown> = {}) => ({
  status: 'collecting', inference: false, target_model: 'gpt-6.1-sol', effective_model: 'gpt-6.1-sol', fusion_version: 1,
  reason: 'insufficient_samples',
  methods: {
    distribution: { status: 'collecting', sample_count: 40, minimum_samples: 128,
      baseline_version: 'distribution-v1', weight: 3, effective_weight: 0, participating: false }
  }, ...extra
})
const mountSummary = (value: ReturnType<typeof summary>, platform = 'openai') => mount(UpstreamHealthCell, {
  props: { account: account({ platform, upstream_health: healthySnapshot({ confidence_summary: value }) }) }, global: { stubs }
})

describe('UpstreamHealthCell', () => {
  it('renders exactly one confidence entry, with method contributions and collapsed diagnostics', () => {
    const wrapper = mountSummary(summary())
    expect(wrapper.get('[data-test="unified-confidence-badge"]').text()).toBe('采集中')
    expect(wrapper.get('[data-test="confidence-progress-distribution"]').text()).toBe('40 / 128')
    expect(wrapper.get('[data-test="confidence-weight-distribution"]').text()).toBe('0 / 3')
    expect(wrapper.findAll('[data-test="unified-confidence-badge"]')).toHaveLength(1)
    expect(wrapper.find('[data-test="distribution-badge"]').exists()).toBe(false)
    expect(wrapper.find('[data-test="attribution-badge"]').exists()).toBe(false)
    expect(wrapper.find('[data-test="confidence-badge"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('不参与判断')
    expect(wrapper.text()).toContain('不是准确率或身份概率')
    expect(wrapper.get('[data-test="unified-confidence-technical"]').attributes('open')).toBeUndefined()
    expect(wrapper.text()).toContain('distribution-v1')
    expect(wrapper.text()).not.toContain('标点')
    expect(wrapper.text()).not.toContain('答案分布')
  })

  it.each([
    { status: 'collecting', label: '采集中', color: 'bg-gray-100' },
    { status: 'insufficient', label: '证据不足', color: 'bg-amber-100' },
    { status: 'match', label: '匹配', color: 'bg-emerald-100' },
    { status: 'suspected', label: '疑似 · Luna', color: 'bg-red-100' }
  ])('uses the server $status verdict without changing health', ({ status, label, color }) => {
    const wrapper = mountSummary(summary({ status, candidate_model: 'gpt-6-luna' }))
    expect(wrapper.get('[data-test="unified-confidence-badge"]').text()).toBe(label)
    expect(wrapper.get('[data-test="unified-confidence-badge"]').classes()).toContain(color)
    expect(wrapper.get('[data-upstream-health-state="healthy"]').classes()).toContain('bg-emerald-100')
    expect(wrapper.get('[data-test="unified-confidence-badge"]').attributes('data-confidence-status')).toBe(status)
  })

  it('does not invent an exact model name for the external bucket', () => {
    expect(mountSummary(summary({ status: 'suspected', candidate_model: 'other_known_external' }))
      .get('[data-test="unified-confidence-badge"]').text()).toBe('疑似 · 其他模型')
  })

  it('shows qualified contributions and consecutive rounds without averaging candidate scores', () => {
    const wrapper = mountSummary(summary({ status: 'match', target_model: 'gpt-5.4', effective_model: 'gpt-5.4',
      inference: true, reason: 'reference_consensus', methods: {
        modeltrace_numeric: { status: 'match', sample_count: 3, minimum_samples: 3, baseline_version: 'numeric-v1',
          weight: 2, effective_weight: 2, participating: true, candidate_model: 'gpt-5.4', confirmed_rounds: 2, current_sample_count: 1,
          candidates: [{ model_id: 'gpt-5.4', display_name: 'GPT-5.4', score: 0.98 }] },
        chanshui_behavioral: { status: 'match', sample_count: 3, minimum_samples: 3, baseline_version: 'behavior-v1',
          weight: 1, effective_weight: 1, participating: true, candidate_model: 'gpt-5.4', confirmed_rounds: 2 }
      }
    }))
    expect(wrapper.get('[data-test="confidence-weight-modeltrace_numeric"]').text()).toBe('2 / 2')
    expect(wrapper.get('[data-test="confidence-weight-chanshui_behavioral"]').text()).toBe('1 / 1')
    expect(wrapper.get('[data-test="confidence-candidate-modeltrace_numeric"]').text()).toBe('gpt-5.4')
    expect(wrapper.text()).toContain('连续确认 2 / 2 轮')
    expect(wrapper.text()).toContain('本轮采集 1 / 3')
    expect(wrapper.get('[data-test="unified-confidence-inference"]').text()).toBe('综合推断')
    expect(wrapper.get('[data-test="confidence-effective-model"]').text()).toBe('gpt-5.4')
    expect(wrapper.text()).not.toContain('0.98')
    expect(wrapper.text()).not.toContain('98%')
  })

  it('leaves a server conflict insufficient even when an individual method supports the target', () => {
    const wrapper = mountSummary(summary({ status: 'insufficient', reason: 'methods_conflict', methods: {
      distribution: { status: 'match', sample_count: 128, minimum_samples: 128, weight: 3,
        effective_weight: 3, participating: true, candidate_model: 'gpt-6.1-sol' }
    } }))
    expect(wrapper.get('[data-test="unified-confidence-badge"]').text()).toBe('证据不足')
    expect(wrapper.get('[data-test="unified-confidence-reason"]').text()).toContain('方法结论冲突')
  })

  it('retains the server verdict during a new incomplete round', async () => {
    const wrapper = mountSummary(summary({ status: 'match' }))
    const row = wrapper.get('[data-test="confidence-method-distribution"]').element
    await wrapper.setProps({ account: account({ upstream_health: healthySnapshot({ confidence_summary: summary({ status: 'match', methods: {
      distribution: { status: 'match', sample_count: 128, minimum_samples: 128, weight: 3, effective_weight: 3,
        participating: true, candidate_model: 'gpt-6.1-sol' },
      modeltrace_numeric: { status: 'collecting', sample_count: 1, minimum_samples: 3, weight: 2,
        effective_weight: 0, participating: false, confirmed_rounds: 0, baseline_version: 'numeric-v1' }
    } }) }) }) })
    expect(wrapper.get('[data-test="unified-confidence-badge"]').text()).toBe('匹配')
    expect(wrapper.get('[data-test="confidence-method-distribution"]').element).toBe(row)
    expect(wrapper.get('[data-test="confidence-weight-modeltrace_numeric"]').text()).toBe('0 / 2')
  })

  it('reports protocol anomalies independently from the model verdict on other platforms', () => {
    const wrapper = mountSummary(summary({ status: 'match', target_model: 'claude-sonnet-4-6',
      effective_model: 'claude-sonnet-4-6', protocol: 'anthropic_messages', observed_protocol: 'anthropic_messages',
      protocol_status: 'mismatch', protocol_reason: 'protocol_mismatch', methods: {} }), 'anthropic')
    expect(wrapper.get('[data-test="unified-confidence-badge"]').text()).toBe('匹配')
    expect(wrapper.get('[data-test="attribution-protocol-status"]').text()).toBe('协议不匹配')
    expect(wrapper.get('[data-test="attribution-observed-protocol"]').text()).toBe('anthropic_messages')
    expect(wrapper.get('[data-upstream-health-state="healthy"]').exists()).toBe(true)
  })

  it('keeps missing baseline and mapping mismatch explanations clear', () => {
    const wrapper = mountSummary(summary({ status: 'insufficient', reason: 'model_mapping_mismatch',
      target_model: 'gpt-6.1-sol', effective_model: 'gpt-6-sol', methods: {} }))
    expect(wrapper.get('[data-test="unified-confidence-reason"]').text()).toContain('目标与实际发送型号不一致')
    expect(wrapper.get('[data-test="confidence-effective-model"]').text()).toBe('gpt-6-sol')
    expect(mountSummary(summary({ status: 'insufficient', reason: 'no_applicable_baseline' })).text()).toContain('暂无适用于目标型号和协议的基线')
  })

  it('never exposes an unknown raw reason', () => {
    const secret = 'api_key=private-value'
    const wrapper = mountSummary(summary({ reason: secret }))
    expect(wrapper.text()).not.toContain(secret)
    expect(wrapper.get('[data-test="unified-confidence-reason"]').text()).toContain('当前证据原因暂不可识别')
  })

  it('does not show a second badge from legacy confidence fields', () => {
    const wrapper = mount(UpstreamHealthCell, { props: { account: account({ upstream_health: healthySnapshot({
      confidence_score_24h: 99, confidence_prompt_version: 'openai-juice-multiprobe-v2', confidence_valid_completed_24h: 10
    }) }) }, global: { stubs } })
    expect(wrapper.find('[data-test="unified-confidence-badge"]').exists()).toBe(false)
    expect(wrapper.find('[data-test="confidence-badge"]').exists()).toBe(false)
    expect(wrapper.get('[data-upstream-health-state="healthy"]').exists()).toBe(true)
  })

  it.each(['healthy', 'degraded', 'suspended', 'observing', 'recovering', 'disabled'])('preserves %s health independently', status => {
    const wrapper = mount(UpstreamHealthCell, { props: { account: account({ upstream_health: healthySnapshot({ status }) }) }, global: { stubs } })
    expect(wrapper.get(`[data-upstream-health-state="${status}"]`).exists()).toBe(true)
  })

  it('opens health history and preserves observation ordering', async () => {
    const wrapper = mount(UpstreamHealthCell, { props: { account: account({ upstream_health: healthySnapshot({ history: [
      { observed_at: '2026-08-10T00:59:00Z', state: 'healthy', source: 'probe', result: 'success' },
      { observed_at: '2026-08-10T01:01:00Z', state: 'degraded', source: 'traffic', result: '401' }
    ] }) }) }, global: { stubs } })
    expect(wrapper.findAll('[data-observation-state]').map(node => node.attributes('data-observation-state'))).toEqual(['healthy', 'degraded'])
    await wrapper.get('[data-upstream-health-history]').trigger('click')
    expect(wrapper.emitted('showHistory')).toHaveLength(1)
  })

  it('keeps both locales aligned for the four-state result', () => {
    expect(Object.keys(zh.upstreamManagement.health.unified.statuses)).toEqual(Object.keys(en.upstreamManagement.health.unified.statuses))
  })
})
