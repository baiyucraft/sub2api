import { defineComponent } from 'vue'
import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import UpstreamHealthTrendChart from '../UpstreamHealthTrendChart.vue'
import type { UpstreamHealthTrend } from '@/api/admin/upstreamManagement'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string, params?: Record<string, unknown>) => params ? `${key}:${JSON.stringify(params)}` : key,
    te: () => true
  })
}))

const TrendChartStub = defineComponent({
  name: 'TrendChart',
  props: ['timestamps', 'series', 'valueFormatter', 'tooltipFooter', 'yMin', 'yMax', 'showLegend'],
  template: '<div data-test="trend-chart-stub" />'
})

const trend: UpstreamHealthTrend = {
  key_id: 9,
  range: '6h',
  start_at: '2026-08-10T00:00:00Z',
  end_at: '2026-08-10T06:00:00Z',
  bucket_seconds: 300,
  points: [
    {
      bucket: '2026-08-10T00:00:00Z', state: 'healthy', state_counts: { healthy: 2 },
      ttft_p50_ms: 1200, ttft_p95_ms: 1800, duration_avg_ms: 2400,
      sample_count: 2, ttft_sample_count: 2, primary_source: 'probe', latest_reason: 'probe_succeeded'
    },
    {
      bucket: '2026-08-10T00:05:00Z', state: 'suspended', state_counts: { suspended: 1 },
      sample_count: 1, ttft_sample_count: 0, primary_source: 'business', latest_result: '401'
    }
  ]
}

describe('UpstreamHealthTrendChart', () => {
  it('renders health as a labelled stepped series', () => {
    const wrapper = mount(UpstreamHealthTrendChart, {
      props: { trend, metric: 'health' },
      global: { stubs: { TrendChart: TrendChartStub } }
    })
    const chart = wrapper.getComponent(TrendChartStub)
    expect(chart.props('timestamps')).toHaveLength(2)
    expect(chart.props('series')[0]).toMatchObject({ stepped: 'after', pointStyle: 'rect' })
    expect(chart.props('valueFormatter')(5)).toContain('admin.upstreamManagement.health.healthy')
    expect(chart.props('yMin')).toBe(0)
    expect(chart.props('yMax')).toBe(5)
  })

  it('renders TTFT P50 and an explicit marker for failed buckets', () => {
    const wrapper = mount(UpstreamHealthTrendChart, {
      props: { trend, metric: 'ttft' },
      global: { stubs: { TrendChart: TrendChartStub } }
    })
    const chart = wrapper.getComponent(TrendChartStub)
    expect(chart.props('series')[0].data).toEqual([1200, null])
    expect(chart.props('series')[1].data).toEqual([null, 0])
    expect(chart.props('series')[1]).toMatchObject({ pointStyle: 'triangle', showLine: false })
    expect(chart.props('tooltipFooter')(0).join(' ')).toContain('1.8 s')
  })
})
