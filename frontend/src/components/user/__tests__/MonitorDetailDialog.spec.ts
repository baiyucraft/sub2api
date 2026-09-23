import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

const { status, showError } = vi.hoisted(() => ({ status: vi.fn(), showError: vi.fn() }))
vi.mock('@/api/channelMonitor', () => ({ status }))
vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError }),
}))
vi.mock('@/composables/useChannelMonitorFormat', () => ({
  useChannelMonitorFormat: () => ({
    statusLabel: (value: string) => value,
    statusBadgeClass: () => '',
    formatMonitorModel: (value: string) => value,
    formatLatency: (value: number | null) => value == null ? '-' : String(value),
    formatPercent: (value: number | null) => value == null ? '-' : `${value}%`,
  }),
}))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

import MonitorDetailDialog from '@/components/user/MonitorDetailDialog.vue'

describe('MonitorDetailDialog', () => {
  it('renders the 24-hour availability column and queries the selected range', async () => {
    status.mockResolvedValueOnce({
      id: 7,
      name: 'cc-max',
      provider: 'openai',
      group_name: 'cc-max',
      show_group_rate: false,
      models: [{
        model: 'gpt-4o',
        latest_status: 'operational',
        latest_latency_ms: 100,
        availability_24h: 99.5,
        availability_7d: 98,
        availability_15d: 97,
        availability_30d: 96,
        avg_latency_7d_ms: 110,
      }],
    })

    const wrapper = mount(MonitorDetailDialog, {
      props: { show: true, monitorId: 7, title: 'cc-max', range: '15d' },
      global: {
        stubs: {
          BaseDialog: { template: '<div><slot /><slot name="footer" /></div>' },
          TrendChart: true,
        },
      },
    })
    await flushPromises()

    expect(status).toHaveBeenCalledWith(7, '15d')
    expect(wrapper.text()).toContain('channelStatus.detailColumns.availability24h')
    expect(wrapper.text()).toContain('99.5%')
    expect(wrapper.get('[data-test="desktop-model-table"]').exists()).toBe(true)
    expect(wrapper.get('[data-test="mobile-model-metrics"]').exists()).toBe(true)
    expect(wrapper.get('[data-test="mobile-model-metrics"] dt').text()).toContain('channelStatus.detailColumns.latestLatency')
  })

  it('passes the real range and weighted average to the rate trend chart', async () => {
    status.mockResolvedValueOnce({
      id: 8,
      name: 'gpt',
      provider: 'openai',
      group_name: 'gpt',
      show_group_rate: true,
      current_public_rate: 0.03,
      average_public_rate: 0.02765,
      rate_observed_since: '2026-07-17T00:00:00Z',
      rate_range_start: '2026-07-18T00:00:00Z',
      rate_range_end: '2026-07-19T00:00:00Z',
      rate_trend: [{ observed_at: '2026-07-18T01:02:03Z', rate: 0.03 }],
      models: [],
    })

    const wrapper = mount(MonitorDetailDialog, {
      props: { show: true, monitorId: 8, title: 'gpt', range: '24h' },
      global: {
        stubs: {
          BaseDialog: { template: '<div><slot /><slot name="footer" /></div>' },
          TrendChart: {
            props: {
              timeColumnLabel: String,
              proportionalTime: Boolean,
              xMin: String,
              xMax: String,
            },
            template: '<div data-test="trend-time-column">{{ timeColumnLabel }}|{{ proportionalTime }}|{{ xMin }}|{{ xMax }}</div>',
          },
        },
      },
    })
    await flushPromises()

    expect(wrapper.get('[data-test="trend-time-column"]').text()).toContain('channelStatus.rateTrend.timeColumn|true')
    expect(wrapper.get('[data-test="trend-time-column"]').text()).toContain('2026-07-18T00:00:00Z|2026-07-19T00:00:00Z')
    expect(wrapper.text()).toContain('0.030x')
    expect(wrapper.text()).toContain('0.028x')
  })
})

beforeEach(() => { status.mockReset(); showError.mockReset() })
function deferred() {
  let resolve!: (value: unknown) => void
  let reject!: (value: unknown) => void
  const promise = new Promise((res, rej) => { resolve = res; reject = rej })
  return { promise, resolve, reject }
}
const detail = (model: string) => ({ models: [{ model, latest_status: 'operational' }] })
function open() {
  return mount(MonitorDetailDialog, { props: { show: true, monitorId: 1, title: 'Monitor', range: '24h' },
    global: { stubs: { BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /></div>' }, TrendChart: true } } })
}
describe('monitor detail request ownership', () => {
  it('keeps the new monitor response when the old response arrives last', async () => {
    const old = deferred()
    status.mockReturnValueOnce(old.promise).mockResolvedValueOnce(detail('new-model'))
    const w = open(); await w.setProps({ monitorId: 2 }); await flushPromises()
    old.resolve(detail('old-model')); await flushPromises()
    expect(w.text()).toContain('new-model'); expect(w.text()).not.toContain('old-model')
  })
  it('ignores a previous failure while the current monitor is loading', async () => {
    const old = deferred(); const current = deferred()
    status.mockReturnValueOnce(old.promise).mockReturnValueOnce(current.promise)
    const w = open(); await w.setProps({ show: false }); await w.setProps({ show: true })
    old.reject(new Error('old failure')); await flushPromises()
    expect(showError).not.toHaveBeenCalled(); expect(w.text()).toContain('common.loading')
    current.resolve(detail('current')); await flushPromises(); expect(w.text()).toContain('current')
  })
  it('reports a failure for the current monitor', async () => {
    status.mockRejectedValueOnce(new Error('current failure'))
    const w = open(); await flushPromises()
    expect(showError).toHaveBeenCalledWith('current failure')
    expect(w.text()).toContain('channelStatus.detailLoadError')
  })
})
