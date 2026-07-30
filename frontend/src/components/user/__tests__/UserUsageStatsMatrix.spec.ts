import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { ref } from 'vue'
import UserUsageStatsMatrix from '../UserUsageStatsMatrix.vue'
import type { BatchUserUsageStats, UserUsageWindow } from '@/api/admin/dashboard'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  const labels: Record<string, string> = {
    'admin.users.today': '今日',
    'admin.users.total': '30天',
    'admin.users.lifetime': '累计',
    'admin.users.usageStats.token': 'Token',
    'admin.users.usageStats.spend': '消费',
    'admin.users.usageStats.cost': '成本',
    'admin.users.usageStats.inputTokens': '输入 Token',
    'admin.users.usageStats.outputTokens': '输出 Token',
    'admin.users.usageStats.cacheCreationTokens': '缓存写入 Token',
    'admin.users.usageStats.cacheReadTokens': '缓存读取 Token',
    'admin.users.usageStats.totalTokens': '总 Token',
    'admin.users.usageStats.margin': '差额',
    'admin.users.usageStats.costHint': '成本口径说明',
    'admin.users.usageStats.lifetimePartial': '累计历史不完整',
    'admin.users.usageStats.lifetimeSince': '自 {date} 可追溯'
  }
  return {
    ...actual,
    useI18n: () => ({
      locale: ref('zh'),
      t: (key: string, params?: Record<string, string>) => {
        const value = labels[key] ?? key
        return Object.entries(params ?? {}).reduce(
          (text, [name, replacement]) => text.replace(`{${name}}`, replacement),
          value
        )
      }
    })
  }
})

const windowStats = (overrides: Partial<UserUsageWindow> = {}): UserUsageWindow => ({
  input_tokens: 1_000,
  output_tokens: 2_000,
  cache_creation_tokens: 3_000,
  cache_read_tokens: 4_000,
  total_tokens: 10_000,
  user_spend: 12.3456,
  account_cost: 8.1234,
  ...overrides
})

const stats = (overrides: Partial<BatchUserUsageStats> = {}): BatchUserUsageStats => ({
  user_id: 42,
  today: windowStats({ total_tokens: 24_800, user_spend: 0.26, account_cost: 0.18 }),
  last_30d: windowStats({ total_tokens: 7_200_000, user_spend: 73.5, account_cost: 48.1 }),
  lifetime: windowStats({ total_tokens: 52_400_000, user_spend: 522.34, account_cost: 346.22 }),
  lifetime_since: '2026-01-02T00:00:00Z',
  lifetime_complete: true,
  aggregation_status: 'available',
  observed_at: '2026-07-30T00:00:00Z',
  today_actual_cost: 0.26,
  total_actual_cost: 73.5,
  by_platform: [],
  ...overrides
})

describe('UserUsageStatsMatrix', () => {
  it('renders the compact today, 30-day, and lifetime matrix', () => {
    const wrapper = mount(UserUsageStatsMatrix, { props: { stats: stats() } })

    expect(wrapper.classes()).toContain('sm:w-[18rem]')
    expect(wrapper.get('[data-test="user-usage-window-today"]').text()).toContain('24.8K')
    expect(wrapper.get('[data-test="user-usage-window-last_30d"]').text()).toContain('7.2M')
    expect(wrapper.get('[data-test="user-usage-window-lifetime"]').text()).toContain('52.4M')
    expect(wrapper.text()).toContain('$0.2600')
    expect(wrapper.text()).toContain('$73.50')
    expect(wrapper.text()).toContain('$346.22')
  })

  it('shows exact token, spend, cost, and difference details on interaction', async () => {
    const wrapper = mount(UserUsageStatsMatrix, { props: { stats: stats() } })

    await wrapper.get('[data-test="user-usage-window-today"]').trigger('click')

    const details = wrapper.get('[data-test="user-usage-details-today"]')
    expect(details.classes()).toContain('!block')
    expect(details.text()).toContain('输入 Token')
    expect(details.text()).toContain('1,000')
    expect(details.text()).toContain('$0.2600')
    expect(details.text()).toContain('$0.1800')
    expect(details.text()).toContain('$0.0800')
    expect(details.text()).toContain('成本口径说明')
  })

  it('marks an incomplete lifetime with a restrained amber indicator and traceable date', () => {
    const wrapper = mount(UserUsageStatsMatrix, {
      props: {
        stats: stats({
          lifetime_complete: false,
          aggregation_status: 'partial'
        })
      }
    })

    const marker = wrapper.get('[data-test="user-usage-lifetime-partial"]')
    expect(marker.classes()).toContain('bg-amber-500')
    expect(marker.attributes('title')).toContain('2026')
    expect(wrapper.get('[data-test="user-usage-details-lifetime"]').text()).toContain('可追溯')
  })

  it('uses a stable three-row skeleton before the snapshot arrives', () => {
    const wrapper = mount(UserUsageStatsMatrix)

    expect(wrapper.attributes('data-test')).toBe('user-usage-stats-matrix')
    expect(wrapper.find('[aria-busy="true"]').exists()).toBe(true)
    expect(wrapper.findAll('[aria-busy="true"] > div')).toHaveLength(3)
  })

  it('renders unavailable snapshots as empty values instead of misleading zeroes', () => {
    const wrapper = mount(UserUsageStatsMatrix, {
      props: {
        stats: stats({ aggregation_status: 'unavailable' })
      }
    })

    const unavailable = wrapper.get('[data-test="user-usage-unavailable"]')
    expect(unavailable.text()).toContain('今日')
    expect(unavailable.text()).toContain('累计')
    expect(unavailable.text()).not.toContain('$0')
    expect(unavailable.findAll('span').filter((node) => node.text() === '—')).toHaveLength(9)
  })
})
