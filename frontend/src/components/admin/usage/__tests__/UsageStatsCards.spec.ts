import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

import UsageStatsCards from '../UsageStatsCards.vue'
import AccountCostAmount from '../AccountCostAmount.vue'

const messages: Record<string, string> = {
  'usage.totalRequests': 'Total Requests',
  'usage.inSelectedRange': 'in selected range',
  'usage.totalTokens': 'Total Tokens',
  'usage.in': 'In',
  'usage.out': 'Out',
  'usage.cacheTotal': 'Cache',
  'usage.cacheBreakdown': 'Cache Token Breakdown',
  'usage.cacheCreationTokensLabel': 'Cache Creation',
  'usage.cacheReadTokensLabel': 'Cache Read',
  'usage.totalCost': 'Total Cost',
  'usage.accountCost': 'Cost',
  'admin.dashboard.accountCost': '成本',
  'usage.standardCost': 'Standard',
  'usage.avgDuration': 'Avg Duration',
}

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => messages[key] ?? key,
    }),
  }
})

const stats = {
  total_requests: 1,
  total_input_tokens: 100,
  total_output_tokens: 50,
  total_cache_tokens: 34,
  total_cache_creation_tokens: 12,
  total_cache_read_tokens: 22,
  total_tokens: 184,
  total_cost: 0.001,
  total_actual_cost: 0.001,
  total_account_cost: 0.001,
  average_duration_ms: 250,
}

describe('UsageStatsCards', () => {
  it('shows only the combined cost with a prefix, revealing addends on hover or focus', async () => {
    const wrapper = mount(UsageStatsCards, {
      props: { stats: { ...stats, total_account_cost: 2, total_extra_cost: 3 } },
      global: { stubs: { Icon: true } },
    })
    const amount = wrapper.getComponent(AccountCostAmount)
    expect(wrapper.text()).toContain('成本：')
    expect(amount.text()).toBe('$5.0000')
    expect(amount.find('[role="tooltip"]').exists()).toBe(false)

    await amount.trigger('mouseenter')
    const tooltip = amount.get('[role="tooltip"]')
    expect(tooltip.text()).toContain('$2.0000')
    expect(tooltip.text()).toContain('+')
    expect(tooltip.text()).toContain('$3.0000')
    expect(amount.attributes('aria-describedby')).toBe(tooltip.attributes('id'))
    await amount.trigger('mouseleave')
    expect(amount.find('[role="tooltip"]').exists()).toBe(false)
    await amount.trigger('focus')
    expect(amount.find('[role="tooltip"]').exists()).toBe(true)
    await amount.trigger('keydown', { key: 'Escape' })
    expect(amount.find('[role="tooltip"]').exists()).toBe(false)
    await amount.trigger('click')
    expect(amount.find('[role="tooltip"]').exists()).toBe(true)
    await amount.trigger('blur')
    expect(amount.find('[role="tooltip"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('keeps server totals, zero, legacy fallback and hidden account cost behavior', async () => {
    const wrapper = mount(UsageStatsCards, {
      props: { stats },
      global: { stubs: { Icon: true } },
    })
    expect(wrapper.getComponent(AccountCostAmount).text()).toBe('$0.0010')
    await wrapper.setProps({ stats: { ...stats, total_total_account_cost: 0, total_extra_cost: 3 } })
    expect(wrapper.getComponent(AccountCostAmount).text()).toBe('$0.0000')
    await wrapper.setProps({ showAccountCost: false })
    expect(wrapper.findComponent(AccountCostAmount).exists()).toBe(false)
    expect(wrapper.text()).not.toContain('成本：')
    wrapper.unmount()
  })

  it('shows cache token breakdown values', () => {
    const wrapper = mount(UsageStatsCards, {
      props: {
        stats,
      },
      global: {
        stubs: {
          Icon: true,
        },
      },
    })

    const text = wrapper.text()
    expect(text).toContain('Cache: 34')
    expect(text).toContain('Cache Token Breakdown')
    expect(text).toContain('Cache Creation')
    expect(text).toContain('12')
    expect(text).toContain('Cache Read')
    expect(text).toContain('22')
  })

  it('keeps the cache tooltip out of the layout while it is hidden', () => {
    const wrapper = mount(UsageStatsCards, {
      props: {
        stats,
      },
      global: {
        stubs: {
          Icon: true,
        },
      },
    })

    const tooltip = wrapper.findAll('span').find((el) => el.classes().includes('group-hover:block'))

    expect(tooltip).toBeDefined()
    // `opacity-0` hides the tooltip visually but keeps it in the layout, so its
    // fixed width still widens the document and causes horizontal scrolling on
    // narrow screens. `hidden` (display: none) takes it out of the flow.
    expect(tooltip?.classes()).toContain('hidden')
    expect(tooltip?.classes()).not.toContain('opacity-0')
  })
})
