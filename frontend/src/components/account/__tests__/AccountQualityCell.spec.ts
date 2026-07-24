import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'

import AccountQualityCell from '../AccountQualityCell.vue'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string, params: Record<string, string | number> = {}) => {
      const value = ({
        'admin.accounts.quality.last10': '近10',
        'admin.accounts.quality.last100': '近100',
        'admin.accounts.quality.firstTokenShort': '首字',
        'admin.accounts.quality.totalShort': '总',
        'admin.accounts.quality.activity.active': '活跃',
        'admin.accounts.quality.activity.idle': '未参与',
        'admin.accounts.quality.activity.unassigned': '未分组',
        'admin.accounts.quality.activity.paused': '暂停调度',
        'admin.accounts.quality.activity.counts': '{success}成/{failed}败',
        'admin.accounts.quality.activity.lastSuccessMinutes': '最近成功 {count} 分钟前',
        'admin.accounts.quality.activity.noSuccess24h': '24h无成功'
      }[key] ?? key)
      return Object.entries(params).reduce(
        (result, [name, replacement]) => result.replace(`{${name}}`, String(replacement)),
        value
      )
    }
  })
}))

const qualityPeriod = {
  last_10: {
    sample_count: 10,
    first_token_sample_count: 10,
    average_first_token_ms: 7700,
    average_duration_ms: 20000,
    quality_score: 73,
    quality_grade: 'A-',
    score_basis: 'ttft_duration' as const
  },
  last_100: {
    sample_count: 100,
    first_token_sample_count: 0,
    average_first_token_ms: null,
    average_duration_ms: 2800,
    quality_score: 69,
    quality_grade: 'B+',
    score_basis: 'duration_only' as const
  },
  window_hours: 24
}

describe('AccountQualityCell', () => {
  it('shows stable grade and latency columns for both windows', () => {
    const wrapper = mount(AccountQualityCell, { props: { stats: qualityPeriod } })

    expect(wrapper.classes()).toContain('min-w-[18.75rem]')
    expect(wrapper.text()).toContain('A- 73')
    expect(wrapper.text()).toContain('B+ 69')
    expect(wrapper.text()).toContain('首字7.7s')
    expect(wrapper.text()).toContain('总20s')
    expect(wrapper.find('[data-quality-grade="A-"]').classes()).toContain('bg-blue-100')
    expect(wrapper.find('[data-quality-grade="B+"]').classes()).toContain('bg-amber-100')
    expect(wrapper.find('.font-mono').exists()).toBe(true)
  })

  it('renders participation separately and supports scheduling overrides', async () => {
    const wrapper = mount(AccountQualityCell, {
      props: {
        stats: { ...qualityPeriod, window_hours: 1 },
        activity: {
          state: 'active',
          successful_request_count: 24,
          failed_request_count: 1,
          last_success_at: new Date(Date.now() - 5 * 60_000).toISOString(),
          last_error_at: null
        },
        showActivity: true
      }
    })

    expect(wrapper.text()).toContain('活跃')
    expect(wrapper.text()).toContain('24成/1败')
    expect(wrapper.text()).toContain('最近成功 5 分钟前')

    await wrapper.setProps({ activityStateOverride: 'unassigned' })
    expect(wrapper.text()).toContain('未分组')
    await wrapper.setProps({ activityStateOverride: 'paused' })
    expect(wrapper.text()).toContain('暂停调度')
  })

  it('uses neutral styling for idle or muted historical quality', () => {
    const wrapper = mount(AccountQualityCell, {
      props: {
        stats: qualityPeriod,
        activity: {
          state: 'idle',
          successful_request_count: 0,
          failed_request_count: 0,
          last_success_at: null,
          last_error_at: null
        },
        showActivity: true,
        muted: true
      }
    })

    expect(wrapper.find('[data-quality-activity="idle"]').classes()).toContain('bg-gray-100')
    expect(wrapper.find('[data-quality-grade="A-"]').classes()).toContain('bg-gray-100')
  })

  it('renders empty and failed snapshots without fabricating quality data', async () => {
    const wrapper = mount(AccountQualityCell)
    expect(wrapper.text()).toBe('-')

    await wrapper.setProps({ error: '加载失败' })
    expect(wrapper.text()).toBe('加载失败')
    expect(wrapper.find('[data-quality-grade]').exists()).toBe(false)
  })
})
