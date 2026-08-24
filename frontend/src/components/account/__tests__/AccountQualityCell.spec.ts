import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'

import AccountQualityCell from '../AccountQualityCell.vue'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string, params: Record<string, string | number> = {}) => {
      const value = ({
        'admin.accounts.quality.activity.active': '活跃',
        'admin.accounts.quality.activity.idle': '未参与',
        'admin.accounts.quality.activity.unassigned': '未分组',
        'admin.accounts.quality.activity.paused': '暂停调度',
        'admin.accounts.quality.activity.title': '{state}，成功 {success}，失败 {failed}，最近成功 {lastSuccess}',
        'admin.accounts.quality.activity.lastSuccessMinutes': '{count}m',
        'admin.accounts.quality.activity.over24h': '>24h',
        'admin.accounts.quality.scoreTitle': '{grade} {score}，样本 {count}，首字样本 {firstCount}',
        'admin.accounts.quality.latencyTitle': '首字 {firstToken}，总耗时 {duration}',
        'admin.accounts.quality.cacheRateTitle': '缓存率 {rate}（缓存读 {numerator} / 输入总量 {denominator}）',
        'admin.accounts.quality.cacheWeighted': '缓存率参与评分（首字 50% / 总耗时 15% / 缓存率 35%）'
      }[key] ?? key)
      return Object.entries(params).reduce(
        (result, [name, replacement]) => result.replace(`{${name}}`, String(replacement)),
        value
      )
    }
  })
}))

const qualityStats = {
  recent_1h: {
    sample_count: 10,
    first_token_sample_count: 10,
    average_first_token_ms: 7700,
    average_duration_ms: 20000,
    cache_rate: 64,
    cache_rate_numerator: 640,
    cache_rate_denominator: 1000,
    quality_score: 73,
    quality_grade: 'A-',
    score_basis: 'ttft_duration' as const
  },
  recent_24h: {
    sample_count: 142,
    first_token_sample_count: 0,
    average_first_token_ms: null,
    average_duration_ms: 2800,
    cache_rate: null,
    cache_rate_numerator: 0,
    cache_rate_denominator: 0,
    quality_score: 69,
    quality_grade: 'B+',
    score_basis: 'duration_only' as const
  },
  activity: {
    state: 'active' as const,
    successful_request_count: 24,
    failed_request_count: 1,
    last_success_at: new Date(Date.now() - 5 * 60_000).toISOString(),
    last_error_at: null
  },
  score_version: 4 as const
}

describe('AccountQualityCell', () => {
  it('shows both full windows in a stable compact three-row grid', () => {
    const wrapper = mount(AccountQualityCell, { props: { stats: qualityStats } })

    expect(wrapper.classes()).toContain('w-[17rem]')
    expect(wrapper.findAll('[data-quality-window]')).toHaveLength(2)
    expect(wrapper.text()).toContain('活跃')
    expect(wrapper.text()).toContain('24/1')
    expect(wrapper.text()).toContain('5m')
    expect(wrapper.text()).toContain('1HA- 73')
    expect(wrapper.text()).toContain('24HB+ 69')
    expect(wrapper.text()).toContain('A- 73')
    expect(wrapper.text()).toContain('B+ 69')
    expect(wrapper.text()).toContain('7.7s / 20s')
    expect(wrapper.text()).toContain('64%')
    expect(wrapper.find('[data-quality-cache-rate]').attributes('title')).toContain('缓存读 640 / 输入总量 1000')
    expect(wrapper.text()).toContain('24H')
    expect(wrapper.text()).toContain('n10')
    expect(wrapper.text()).toContain('n142')
    expect(wrapper.text()).not.toContain('首字')
    expect(wrapper.text()).not.toContain('样本')
    expect(wrapper.find('[data-quality-grade="A-"]').classes()).toContain('bg-blue-100')
    expect(wrapper.find('[data-quality-grade="B+"]').classes()).toContain('bg-amber-100')
    expect(wrapper.find('.font-mono').exists()).toBe(true)
  })

  it('renders participation separately and supports scheduling overrides', async () => {
    const wrapper = mount(AccountQualityCell, {
      props: {
        stats: qualityStats
      }
    })

    expect(wrapper.text()).toContain('活跃')
    expect(wrapper.text()).toContain('24/1')
    expect(wrapper.text()).toContain('5m')

    await wrapper.setProps({ activityStateOverride: 'unassigned' })
    expect(wrapper.text()).toContain('未分组')
    await wrapper.setProps({ activityStateOverride: 'paused' })
    expect(wrapper.text()).toContain('暂停调度')
  })

  it('uses neutral styling for idle or muted historical quality', () => {
    const wrapper = mount(AccountQualityCell, {
      props: {
        stats: {
          ...qualityStats,
          activity: {
            state: 'idle',
            successful_request_count: 0,
            failed_request_count: 0,
            last_success_at: null,
            last_error_at: null
          }
        },
        muted: true
      }
    })

    expect(wrapper.find('[data-quality-activity="idle"]').classes()).toContain('bg-gray-100')
    expect(wrapper.find('[data-quality-window="1H"]').classes()).not.toContain('opacity-60')
    expect(wrapper.find('[data-quality-window="24H"]').classes()).toContain('opacity-60')
    expect(wrapper.find('[data-quality-window="24H"] [data-quality-grade="B+"]').classes()).toContain('bg-gray-100')
  })

  it('renders empty and failed snapshots without fabricating quality data', async () => {
    const wrapper = mount(AccountQualityCell)
    expect(wrapper.text()).toBe('-')

    await wrapper.setProps({ error: '加载失败' })
    expect(wrapper.text()).toBe('加载失败')
    expect(wrapper.find('[data-quality-grade]').exists()).toBe(false)
  })
})
