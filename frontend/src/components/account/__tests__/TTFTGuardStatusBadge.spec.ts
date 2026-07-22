import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import TTFTGuardStatusBadge from '../TTFTGuardStatusBadge.vue'
import type { AccountTTFTGuardDegradation } from '@/types'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) =>
        params ? `${key} ${JSON.stringify(params)}` : key
    })
  }
})

const makeDegradation = (
  overrides: Partial<AccountTTFTGuardDegradation> = {}
): AccountTTFTGuardDegradation => ({
  model: 'gpt-5.4-mini',
  reason: 'consecutive_elevated',
  threshold_ms: 20_000,
  last_ttft_ms: 34_700,
  ewma_ms: 28_120,
  sample_count: 7,
  degraded_at: '2026-07-22T10:00:00Z',
  last_sample_at: '2026-07-22T10:09:41Z',
  expires_at: '2026-07-22T10:24:41Z',
  recovery_samples: 1,
  recovery_samples_required: 3,
  ...overrides
})

const mountBadge = (degradations: AccountTTFTGuardDegradation[]) =>
  mount(TTFTGuardStatusBadge, {
    props: { degradations },
    global: {
      stubs: {
        Icon: true,
        HelpTooltip: {
          props: ['triggerClass'],
          template: '<div class="help-tooltip" :data-trigger-class="triggerClass"><slot name="trigger" /><div class="ttft-tooltip"><slot /></div></div>'
        }
      }
    }
  })

describe('TTFTGuardStatusBadge', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-07-22T10:10:11Z'))
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('以单行紧凑标签显示模型和已降级时长', () => {
    const wrapper = mountBadge([makeDegradation()])
    const badge = wrapper.find('span.inline-flex')

    expect(badge.text()).toContain('gpt-5.4-mini')
    expect(badge.text()).toContain('10m11s')
    expect(badge.attributes('aria-label')).toBe('gpt-5.4-mini · 10m11s')
    expect(badge.classes()).toContain('whitespace-nowrap')
    expect(badge.find('.truncate').text()).toBe('gpt-5.4-mini')
    expect(wrapper.find('.help-tooltip').attributes('data-trigger-class')).toBe('max-w-full min-w-0')

    wrapper.unmount()
  })

  it('hover 详情包含原因、指标、恢复进度、过期时间和探测说明', () => {
    const wrapper = mountBadge([makeDegradation()])
    const tooltip = wrapper.find('.ttft-tooltip')

    expect(tooltip.text()).toContain('admin.accounts.status.ttftGuard.consecutiveElevated')
    expect(tooltip.text()).toContain('34.7s')
    expect(tooltip.text()).toContain('28.1s')
    expect(tooltip.text()).toContain('20s')
    expect(tooltip.text()).toContain('"count":7')
    expect(tooltip.text()).toContain('"current":1')
    expect(tooltip.text()).toContain('"required":3')
    expect(tooltip.text()).toContain('admin.accounts.status.ttftGuard.expiresAt')
    expect(tooltip.text()).toContain('admin.accounts.status.ttftGuard.probeHint')

    wrapper.unmount()
  })

  it.each([
    ['critical_sample', 'criticalSample'],
    ['ewma', 'ewmaReason'],
    ['future_reason', 'unknownReason']
  ])('映射 %s 原因到 %s 文案', (reason, key) => {
    const wrapper = mountBadge([makeDegradation({ reason })])
    expect(wrapper.text()).toContain(`admin.accounts.status.ttftGuard.${key}`)
    wrapper.unmount()
  })

  it('支持多个模型并在状态异步出现后启动计时', async () => {
    const wrapper = mountBadge([])
    await wrapper.setProps({
      degradations: [
        makeDegradation(),
        makeDegradation({ model: 'gpt-5.4-with-a-very-long-canonical-model-name' })
      ]
    })

    expect(wrapper.findAll('.help-tooltip')).toHaveLength(2)
    expect(wrapper.findAll('.truncate')).toHaveLength(2)

    vi.advanceTimersByTime(1000)
    await wrapper.vm.$nextTick()
    expect(wrapper.text()).toContain('10m12s')

    wrapper.unmount()
  })
})
