import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import TrendChart from '../TrendChart.vue'

vi.mock('vue-chartjs', () => ({
  Line: {
    name: 'MockTrendLine',
    props: ['data', 'options'],
    template: '<div data-test="mock-trend-line" />'
  }
}))

describe('TrendChart', () => {
  it('renders a stable empty state and loading skeleton without a chart', () => {
    const empty = mount(TrendChart, {
      props: {
        timestamps: [],
        series: [],
        emptyText: '暂无数据',
        chartLabel: '趋势图'
      }
    })
    expect(empty.get('[data-test="trend-chart"]').attributes('style')).toContain('height: 18rem')
    expect(empty.get('[data-test="trend-chart-empty"]').text()).toBe('暂无数据')
    expect(empty.find('[data-test="mock-trend-line"]').exists()).toBe(false)

    const loading = mount(TrendChart, {
      props: {
        timestamps: [],
        series: [],
        loading: true,
        loadingText: '加载中',
        emptyText: '暂无数据',
        chartLabel: '趋势图'
      }
    })
    expect(loading.find('[data-test="trend-chart-empty"]').exists()).toBe(false)
    expect(loading.find('.sr-only').text()).toBe('加载中')
  })

  it('uses stepped datasets, zero baseline, full timestamp tooltip and request footer', () => {
    const wrapper = mount(TrendChart, {
      props: {
        timestamps: ['2026-07-18T01:02:03Z'],
        series: [{
          label: '倍率',
          data: [0.03],
          tone: 'primary',
          stepped: 'before'
        }],
        emptyText: '暂无数据',
        chartLabel: '倍率趋势',
        valueFormatter: (value: number) => `${value.toFixed(2)}x`,
        tooltipFooter: (index: number) => `请求数：${index + 4}`,
        zeroBaseline: true
      }
    })

    const line = wrapper.getComponent({ name: 'MockTrendLine' })
    const data = line.props('data') as { datasets: Array<Record<string, unknown>> }
    const options = line.props('options') as any
    expect(data.datasets[0].stepped).toBe('before')
    expect(data.datasets[0].pointRadius).toBe(2.5)
    expect(options.scales.y.beginAtZero).toBe(true)
    expect(options.plugins.tooltip.callbacks.label({ dataset: { label: '倍率' }, parsed: { y: 0.03 } })).toBe('倍率：0.03x')
    expect(options.plugins.tooltip.callbacks.footer([{ dataIndex: 0 }])).toBe('请求数：4')
    expect(options.plugins.tooltip.callbacks.title([{ dataIndex: 0 }])).toContain('2026')
  })

  it('maps timestamps to a proportional linear axis without changing the default category mode', () => {
    const timestamps = [
      '2026-07-18T00:00:00Z',
      '2026-07-18T01:00:00Z',
      '2026-07-18T10:00:00Z',
    ]
    const wrapper = mount(TrendChart, {
      props: {
        timestamps,
        series: [{ label: '倍率', data: [0.1, 0.2, 0.3] }],
        proportionalTime: true,
        xMin: '2026-07-17T00:00:00Z',
        xMax: '2026-07-19T00:00:00Z',
        yGrace: '10%',
        emptyText: '暂无数据',
        chartLabel: '倍率趋势',
      },
    })

    const line = wrapper.getComponent({ name: 'MockTrendLine' })
    const data = line.props('data') as { labels: number[] }
    const options = line.props('options') as any
    expect(data.labels[1] - data.labels[0]).toBe(60 * 60 * 1000)
    expect(data.labels[2] - data.labels[1]).toBe(9 * 60 * 60 * 1000)
    expect(options.scales.x.type).toBe('linear')
    expect(options.scales.x.min).toBe(Date.parse('2026-07-17T00:00:00Z'))
    expect(options.scales.x.max).toBe(Date.parse('2026-07-19T00:00:00Z'))
    expect(options.scales.y.grace).toBe('10%')

    const category = mount(TrendChart, {
      props: {
        timestamps: [timestamps[0]],
        series: [{ label: '普通趋势', data: [1] }],
        emptyText: '暂无数据',
        chartLabel: '普通趋势',
      },
    })
    const categoryOptions = category.getComponent({ name: 'MockTrendLine' }).props('options') as any
    expect(categoryOptions.scales.x.type).toBe('category')
  })
})
