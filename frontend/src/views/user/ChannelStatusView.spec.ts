import { describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

const { list, status } = vi.hoisted(() => ({
  list: vi.fn(),
  status: vi.fn(),
}))

vi.mock('@/api/channelMonitor', () => ({ list, status }))
vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    cachedPublicSettings: { channel_monitor_enabled: false },
    showError: vi.fn(),
  }),
}))
vi.mock('@/composables/useAutoRefresh', () => ({
  useAutoRefresh: () => ({
    countdown: { value: 60 },
    enabled: { value: false },
    intervalSeconds: { value: 60 },
    intervals: [30, 60, 120],
    setEnabled: vi.fn(),
    setInterval: vi.fn(),
    start: vi.fn(),
    stop: vi.fn(),
  }),
}))
vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key }),
  }
})

import ChannelStatusView from './ChannelStatusView.vue'

describe('ChannelStatusView unified range', () => {
  it('defaults to 24h and refreshes list data once when the shared range changes', async () => {
    list.mockResolvedValue({ range: '24h', items: [] })

    const wrapper = mount(ChannelStatusView, {
      global: {
        stubs: {
          AppLayout: { template: '<main><slot /></main>' },
          MonitorHero: {
            props: ['range'],
            emits: ['update:range'],
            template: '<button data-test="range" @click="$emit(\'update:range\', \'15d\')">{{ range }}</button>',
          },
          MonitorCardGrid: {
            props: ['range'],
            template: '<div data-test="grid-range">{{ range }}</div>',
          },
          MonitorDetailDialog: {
            props: ['range'],
            template: '<div data-test="detail-range">{{ range }}</div>',
          },
        },
      },
    })
    await flushPromises()

    expect(list).toHaveBeenCalledTimes(1)
    expect(list.mock.calls[0][0]).toMatchObject({ range: '24h' })
    expect(wrapper.get('[data-test="grid-range"]').text()).toBe('24h')
    expect(wrapper.get('[data-test="detail-range"]').text()).toBe('24h')

    await wrapper.get('[data-test="range"]').trigger('click')
    await flushPromises()

    expect(list).toHaveBeenCalledTimes(2)
    expect(list.mock.calls[1][0]).toMatchObject({ range: '15d' })
    expect(wrapper.get('[data-test="grid-range"]').text()).toBe('15d')
    expect(wrapper.get('[data-test="detail-range"]').text()).toBe('15d')
    expect(status).not.toHaveBeenCalled()
  })
})
