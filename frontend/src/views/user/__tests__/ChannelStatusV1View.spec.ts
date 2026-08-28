import { describe, expect, it, beforeEach, vi } from 'vitest'
import { defineComponent, h, ref } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'

const list = vi.fn()
const showError = vi.fn()
let autoRefreshCallback: ((silent?: boolean) => Promise<void>) | undefined

vi.mock('@/api/channelMonitor', () => ({
  list,
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    cachedPublicSettings: { channel_monitor_enabled: true },
    showError,
  }),
}))

vi.mock('@/composables/useAutoRefresh', () => ({
  useAutoRefresh: (options: { onRefresh: () => Promise<void> }) => {
    autoRefreshCallback = options.onRefresh
    return {
      enabled: ref(true),
      intervalSeconds: ref(60),
      countdown: ref(60),
      fetching: ref(false),
      intervals: [30, 60, 120],
      setEnabled: vi.fn(),
      setInterval: vi.fn(),
      start: vi.fn(),
      stop: vi.fn(),
    }
  },
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key }),
}))

vi.mock('@/components/layout/AppLayout.vue', () => ({
  default: defineComponent({
    name: 'AppLayout',
    setup: (_, { slots }) => () => h('div', slots.default?.()),
  }),
}))

vi.mock('@/components/user/monitor/MonitorHero.vue', () => ({
  default: defineComponent({ name: 'MonitorHero', template: '<div />' }),
}))

vi.mock('@/components/user/MonitorDetailDialog.vue', () => ({
  default: defineComponent({ name: 'MonitorDetailDialog', template: '<div />' }),
}))

const MonitorCardGridStub = defineComponent({
  name: 'MonitorCardGrid',
  props: ['groups', 'loading'],
  template: '<div data-test="monitor-grid">{{ groups.length }}</div>',
})

const item = {
  id: 7,
  name: 'managed-openai',
  provider: 'openai',
  group_name: 'managed-openai',
  primary_model: 'gpt-4o',
  primary_status: 'operational',
  primary_latency_ms: 120,
  primary_ttft_ms: 30,
  primary_ping_latency_ms: 20,
  availability: 100,
  availability_7d: 100,
  extra_models: [],
  timeline: [],
  show_group_rate: false,
}

describe('ChannelStatusV1View refresh behavior', () => {
  beforeEach(() => {
    list.mockReset()
    showError.mockReset()
    autoRefreshCallback = undefined
  })

  it('keeps the last real channel state and stays silent when auto refresh fails', async () => {
    list.mockResolvedValueOnce({ range: '24h', items: [item] })
    const ChannelStatusV1View = (await import('../ChannelStatusV1View.vue')).default
    const wrapper = mount(ChannelStatusV1View, {
      global: {
        stubs: { MonitorCardGrid: MonitorCardGridStub },
      },
    })

    await flushPromises()
    const grid = wrapper.getComponent(MonitorCardGridStub)
    expect(grid.props('groups')).toHaveLength(1)
    expect(grid.props('groups')[0].items[0].primary_status).toBe('operational')

    list.mockRejectedValueOnce(new Error('server is restarting'))
    await autoRefreshCallback?.(true)

    expect(showError).not.toHaveBeenCalled()
    expect(grid.props('groups')[0].items[0].primary_status).toBe('operational')
    expect(grid.props('groups')[0].items[0].id).toBe(7)

    list.mockRejectedValueOnce(new Error('server is still restarting'))
    wrapper.findComponent({ name: 'MonitorHero' }).vm.$emit('refresh')
    await flushPromises()
    expect(showError).toHaveBeenCalledTimes(1)
    expect(grid.props('groups')[0].items[0].primary_status).toBe('operational')
    wrapper.unmount()
  })
})
