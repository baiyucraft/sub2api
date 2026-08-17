import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

import UpstreamImagePricingSummary from '../UpstreamImagePricingSummary.vue'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key })
  }
})

describe('UpstreamImagePricingSummary', () => {
  it('renders a centered 1K/2K/4K matrix and keeps calculation details in hover titles', () => {
    const wrapper = mount(UpstreamImagePricingSummary, {
      props: {
        pricing: {
          supported: true,
          status: 'available',
          stale: false,
          currency: 'USD',
          rate_independent: false,
          effective_rate_multiplier: 0.8,
          final_cost_1k: 0,
          final_cost_2k: 0.024,
          final_cost_4k: 0.048,
          observed_at: '2026-08-10T00:00:00Z'
        }
      }
    })

    expect(wrapper.find('[data-test="image-pricing-rate-mode"]').exists()).toBe(false)
    expect(wrapper.find('[data-test="image-pricing-status"]').exists()).toBe(false)
    expect(wrapper.get('[data-test="image-cost-1k"]').text()).toContain('$0.000')
    expect(wrapper.get('[data-test="image-cost-2k"]').text()).toContain('$0.024')
    expect(wrapper.get('[data-test="image-cost-4k"]').text()).toContain('$0.048')
    expect(wrapper.get('[data-test="image-cost-1k"]').attributes('title')).toContain(
      'admin.accounts.upstreamImagePricing.costTooltipShared'
    )
  })

  it('renders independent, stale and missing tiers without treating them as zero', () => {
    const wrapper = mount(UpstreamImagePricingSummary, {
      props: {
        pricing: {
          supported: true,
          status: 'partial',
          stale: true,
          currency: 'CNY',
          rate_independent: true,
          effective_rate_multiplier: 1.25,
          final_cost_1k: 0.01,
          final_cost_2k: null,
          final_cost_4k: undefined,
          observed_at: null
        }
      }
    })

    expect(wrapper.find('[data-test="image-pricing-rate-mode"]').exists()).toBe(false)
    expect(wrapper.get('[data-test="image-pricing-status"]').attributes('aria-label')).toBe(
      'admin.accounts.upstreamImagePricing.statusStale'
    )
    expect(wrapper.get('[data-test="image-cost-1k"]').text()).toContain('CNY 0.010')
    expect(wrapper.get('[data-test="image-cost-2k"]').text()).toContain('—')
    expect(wrapper.get('[data-test="image-cost-4k"]').text()).toContain('—')
    expect(wrapper.get('[data-test="image-cost-1k"]').attributes('title')).toContain(
      'admin.accounts.upstreamImagePricing.costTooltipIndependent'
    )
  })
})
