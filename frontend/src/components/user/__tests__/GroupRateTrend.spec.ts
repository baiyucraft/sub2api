import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import GroupRateTrend from '@/components/user/monitor/GroupRateTrend.vue'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key }),
}))

describe('GroupRateTrend', () => {
  it('uses a proportional time axis and renders current and weighted average rates to three decimals', () => {
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
          average_public_rate: 0.02765,
          rate_observed_since: '2026-07-17T00:00:00Z',
          rate_range_start: '2026-07-18T00:00:00Z',
          rate_range_end: '2026-07-19T00:00:00Z',
          rate_trend: [
            { observed_at: '2026-07-18T01:02:03Z', rate: 0.03 },
            { observed_at: '2026-07-18T04:02:03Z', rate: 0.025 },
          ],
        },
      },
      global: {
        stubs: {
          TrendChart: {
            props: {
              timeColumnLabel: String,
              timestamps: Array,
              height: [String, Number],
              proportionalTime: Boolean,
              xMin: String,
              xMax: String,
              series: Array,
            },
            template: '<div data-test="trend-chart-props">{{ timeColumnLabel }}|{{ height }}|{{ proportionalTime }}|{{ xMin }}|{{ xMax }}|{{ timestamps.join(",") }}|{{ series[0].cubicInterpolationMode }}</div>',
          },
        },
      },
    })

    const chartProps = wrapper.get('[data-test="trend-chart-props"]').text()
    expect(chartProps).toContain('channelStatus.rateTrend.timeColumn|152|true')
    expect(chartProps).toContain('2026-07-18T00:00:00Z|2026-07-19T00:00:00Z')
    expect(chartProps).toContain('2026-07-19T00:00:00Z')
    expect(chartProps).toContain('|monotone')
    expect(wrapper.get('[data-test="current-public-rate"]').text()).toBe('0.030x')
    expect(wrapper.get('[data-test="average-public-rate"]').text()).toBe('0.028x')
    expect(wrapper.find('[data-test="rate-history-partial"]').exists()).toBe(false)
  })

  it('labels the average as partial when the observed history begins inside the selected range', () => {
    const wrapper = mount(GroupRateTrend, {
      props: {
        item: {
          id: 2,
          name: 'new group',
          provider: 'openai',
          group_name: 'new group',
          primary_model: 'gpt-4o',
          primary_status: 'operational',
          primary_latency_ms: 100,
          primary_ping_latency_ms: 20,
          availability: 99,
          availability_7d: 99,
          extra_models: [],
          timeline: [],
          show_group_rate: true,
          current_public_rate: 0.1234,
          average_public_rate: 0.1234,
          rate_observed_since: '2026-07-18T12:00:00Z',
          rate_range_start: '2026-07-18T00:00:00Z',
          rate_range_end: '2026-07-19T00:00:00Z',
          rate_trend: [{ observed_at: '2026-07-18T12:00:00Z', rate: 0.1234 }],
        },
      },
      global: { stubs: { TrendChart: true } },
    })

    expect(wrapper.get('[data-test="rate-history-partial"]').text()).toBe('channelStatus.rateTrend.historyIncomplete')
    expect(wrapper.text()).toContain('channelStatus.rateTrend.observedAverage')
  })
})
