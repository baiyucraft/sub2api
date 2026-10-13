import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, shallowMount } from '@vue/test-utils'
import ChannelStatusV1View from '../ChannelStatusV1View.vue'

const mocks = vi.hoisted(() => ({ list: vi.fn(), status: vi.fn() }))
vi.mock('@/api/channelMonitor', () => mocks)
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ cachedPublicSettings: { channel_monitor_enabled: true }, showError: vi.fn() }) }))
vi.mock('vue-i18n', async () => ({
  ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'),
  useI18n: () => ({ t: (key: string) => key })
}))
enableAutoUnmount(afterEach)
afterEach(() => { vi.useRealTimers(); localStorage.clear() })

function listResult(range: string, availability: number) {
  return { range, items: [{ id: 1, name: 'Monitor', provider: 'openai', primary_model: 'model', primary_status: 'operational', availability }] }
}
beforeEach(() => {
  vi.useFakeTimers()
  vi.resetAllMocks()
  localStorage.clear()
  localStorage.setItem('channel-status-auto-refresh', JSON.stringify({ enabled: true, interval_seconds: 30 }))
  mocks.list.mockImplementation(async ({ range }) => listResult(range, 90))
})

function mountView() {
  return shallowMount(ChannelStatusV1View, {
    global: { stubs: {
      AppLayout: { template: '<div><slot /></div>' },
      MonitorHero: { props: ['loading'], emits: ['update:range', 'refresh'], template: `<div :data-loading="loading">
        <button class="one" @click="$emit('update:range', '1h')">1h</button>
        <button class="seven" @click="$emit('update:range', '7d')">7d</button>
        <button class="refresh" @click="$emit('refresh')">Refresh</button>
      </div>` },
      MonitorCardGrid: { props: ['groups'], template: '<output>{{ groups[0]?.items[0]?.availability }}</output>' }
    } }
  })
}

describe('channel monitor availability refresh for the selected range', () => {
  it.each([['one', '1h'], ['seven', '7d']])('refreshes %s availability on timer ticks', async (tab, range) => {
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('.' + tab).trigger('click')
    await flushPromises()
    expect(wrapper.get('output').text()).toBe('90')
    mocks.list.mockImplementation(async ({ range }) => listResult(range, 98))
    await vi.advanceTimersByTimeAsync(30000)
    await flushPromises()
    expect(wrapper.get('output').text()).toBe('98')
    expect(mocks.list).toHaveBeenLastCalledWith(expect.objectContaining({ range }))
    expect(mocks.status).not.toHaveBeenCalled()
    expect(wrapper.get('[data-loading]').attributes('data-loading')).toBe('false')
  })

  it('refreshes the default range through the aggregate list', async () => {
    const wrapper = mountView()
    await flushPromises()
    await vi.advanceTimersByTimeAsync(30000)
    await flushPromises()
    expect(mocks.list).toHaveBeenCalledTimes(2)
    expect(mocks.status).not.toHaveBeenCalled()
    expect(wrapper.get('output').text()).toBe('90')
  })

  it('still refreshes availability on manual reload', async () => {
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('.one').trigger('click')
    await flushPromises()
    mocks.list.mockImplementation(async ({ range }) => listResult(range, 98))
    await wrapper.get('.refresh').trigger('click')
    await flushPromises()
    expect(wrapper.get('output').text()).toBe('98')
  })
})
