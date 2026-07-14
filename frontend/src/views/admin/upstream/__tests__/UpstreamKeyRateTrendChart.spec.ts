import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

vi.mock('vue-chartjs', () => ({
  Line: {
    name: 'Line',
    props: ['data', 'options'],
    template: '<div data-test="line-chart" />'
  }
}))
vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key })
  }
})

import UpstreamKeyRateTrendChart from '../UpstreamKeyRateTrendChart.vue'

describe('UpstreamKeyRateTrendChart', () => {
  it('renders one rate_multiplier series', () => {
    const wrapper = mount(UpstreamKeyRateTrendChart, {
      props: {
        points: [
          { bucket: '2026-07-14T00:00:00Z', rate_multiplier: 0.8 },
          { bucket: '2026-07-14T01:00:00Z', rate_multiplier: 1.2 }
        ]
      }
    })

    const chartData = wrapper.getComponent({ name: 'Line' }).props('data')
    expect(chartData.datasets).toHaveLength(1)
    expect(chartData.datasets[0]).toMatchObject({
      label: 'admin.upstreamConfigs.operations.rateSeries.rateMultiplier',
      data: [0.8, 1.2]
    })
  })
})
