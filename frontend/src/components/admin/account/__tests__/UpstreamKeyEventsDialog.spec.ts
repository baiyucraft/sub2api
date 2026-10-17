import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import UpstreamKeyEventsDialog from '../UpstreamKeyEventsDialog.vue'

const { getKeyEvents, getKeyHealthTrend } = vi.hoisted(() => ({
  getKeyEvents: vi.fn(),
  getKeyHealthTrend: vi.fn()
}))

vi.mock('@/api/admin/upstreamManagement', () => ({
  default: { getKeyEvents, getKeyHealthTrend }
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key, te: () => true })
  }
})

const BaseDialogStub = defineComponent({
  props: ['show', 'title'],
  template: '<div v-if="show"><slot /><slot name="footer" /></div>'
})

const TrendStub = defineComponent({
  props: ['trend', 'metric', 'loading'],
  template: '<div data-test="health-trend">{{ metric }}</div>'
})

describe('UpstreamKeyEventsDialog', () => {
  beforeEach(() => {
    getKeyEvents.mockReset().mockResolvedValue({ items: [], total: 0, health_history: [] })
    getKeyHealthTrend.mockReset().mockImplementation((id: number, range: string) => Promise.resolve({
      key_id: id, range, start_at: '', end_at: '', bucket_seconds: 300, points: []
    }))
  })

  function mountDialog() {
    return mount(UpstreamKeyEventsDialog, {
      props: { show: true, keyId: 9, keyLabel: 'Key A' },
      global: { stubs: { BaseDialog: BaseDialogStub, UpstreamHealthTrendChart: TrendStub } }
    })
  }

  it('loads the default six-hour trend and switches all supported ranges', async () => {
    const wrapper = mountDialog()
    await flushPromises()
    expect(getKeyEvents).toHaveBeenCalledWith(9, { signal: expect.any(AbortSignal) })
    expect(getKeyHealthTrend).toHaveBeenCalledWith(9, '6h', { signal: expect.any(AbortSignal) })

    await wrapper.get('[data-test="health-range-30d"]').trigger('click')
    await flushPromises()
    expect(getKeyHealthTrend).toHaveBeenLastCalledWith(9, '30d', { signal: expect.any(AbortSignal) })
    wrapper.unmount()
  })

  it('switches between health and first-token metrics without another request', async () => {
    const wrapper = mountDialog()
    await flushPromises()
    const requests = getKeyHealthTrend.mock.calls.length
    await wrapper.get('[data-test="health-metric-ttft"]').trigger('click')
    expect(wrapper.get('[data-test="health-trend"]').text()).toBe('ttft')
    expect(getKeyHealthTrend).toHaveBeenCalledTimes(requests)
    wrapper.unmount()
  })

  it('cancels old key requests and ignores their late values after switching or closing', async () => {
    let resolveOld!: (value: unknown) => void
    getKeyEvents.mockReturnValueOnce(new Promise(resolve => { resolveOld = resolve }))
    const wrapper = mountDialog()
    await flushPromises()
    const oldSignal = getKeyEvents.mock.calls[0][1].signal
    await wrapper.setProps({ keyId: 10, keyLabel: 'Key B' })
    await flushPromises()
    expect(oldSignal.aborted).toBe(true)
    resolveOld({ items: [{ id: 1, message: 'wrong-key-result' }], health_history: [] })
    await flushPromises()
    expect(wrapper.text()).not.toContain('wrong-key-result')
    await wrapper.setProps({ show: false })
    wrapper.unmount()
  })

  it('retains successful evidence on refresh failure and preserves selected range/metric', async () => {
    getKeyEvents.mockResolvedValueOnce({ items: [{ id: 1, severity: 'info', message: 'last good evidence' }], health_history: [] })
    const wrapper = mountDialog()
    await flushPromises()
    await wrapper.get('[data-test="health-range-30d"]').trigger('click')
    await flushPromises()
    await wrapper.get('[data-test="health-metric-ttft"]').trigger('click')
    getKeyEvents.mockRejectedValueOnce(new Error('offline'))
    getKeyHealthTrend.mockRejectedValueOnce(new Error('offline'))
    await wrapper.get('[data-test="health-refresh"]').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('last good evidence')
    expect(wrapper.get('[data-test="health-trend"]').text()).toBe('ttft')
    expect(getKeyHealthTrend).toHaveBeenLastCalledWith(9, '30d', { signal: expect.any(AbortSignal) })
    wrapper.unmount()
  })

  it('invalidates an in-flight range response when the dialog closes and reopens', async () => {
    let resolveOld!: (value: unknown) => void
    getKeyHealthTrend.mockReturnValueOnce(new Promise(resolve => { resolveOld = resolve }))
    const wrapper = mountDialog()
    await flushPromises()
    const signal = getKeyHealthTrend.mock.calls[0][2].signal
    await wrapper.setProps({ show: false })
    expect(signal.aborted).toBe(true)
    await wrapper.setProps({ show: true, keyId: 11 })
    await flushPromises()
    resolveOld({ key_id: 9, range: '6h', points: [{ bucket: 'wrong-range' }] })
    await flushPromises()
    expect(wrapper.findComponent(TrendStub).props('trend').key_id).toBe(11)
    expect(wrapper.findComponent(TrendStub).props('trend').points).toEqual([])
    wrapper.unmount()
  })
})
