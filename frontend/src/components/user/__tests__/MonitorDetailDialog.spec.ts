import { describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

const { status } = vi.hoisted(() => ({ status: vi.fn() }))
vi.mock('@/api/channelMonitor', () => ({ status }))
vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError: vi.fn() }),
}))
vi.mock('@/composables/useChannelMonitorFormat', () => ({
  useChannelMonitorFormat: () => ({
    statusLabel: (value: string) => value,
    statusBadgeClass: () => '',
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
  })
})
