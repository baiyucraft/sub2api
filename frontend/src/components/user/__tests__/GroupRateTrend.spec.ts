import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import GroupRateTrend from '@/components/user/monitor/GroupRateTrend.vue'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key }),
}))

describe('GroupRateTrend', () => {
  it('passes the localized time column to the compact trend chart', () => {
    const wrapper = mount(GroupRateTrend, {
      props: {
        item: {
          id: 1,
          name: 'cc-max',
          provider: 'openai',
          group_name: 'cc-max',
          primary_model: 'gpt-4o',
          primary_status: 'operational',
          primary_latency_ms: 100,
          primary_ping_latency_ms: 20,
          availability: 99,
          availability_7d: 99,
          extra_models: [],
          timeline: [],
          show_group_rate: true,
          current_public_rate: 0.03,
          rate_trend: [{ observed_at: '2026-07-18T01:02:03Z', rate: 0.03 }],
        },
      },
      global: {
        stubs: {
          TrendChart: {
            props: ['timeColumnLabel'],
            template: '<div data-test="trend-time-column">{{ timeColumnLabel }}</div>',
          },
        },
      },
    })

    expect(wrapper.get('[data-test="trend-time-column"]').text()).toBe('channelStatus.rateTrend.timeColumn')
  })
})
