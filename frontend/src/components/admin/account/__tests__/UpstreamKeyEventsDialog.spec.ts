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
    getKeyHealthTrend.mockReset().mockImplementation((_id: number, range: string) => Promise.resolve({
      key_id: 9, range, start_at: '', end_at: '', bucket_seconds: 300, points: []
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
    expect(getKeyEvents).toHaveBeenCalledWith(9)
    expect(getKeyHealthTrend).toHaveBeenCalledWith(9, '6h')

    await wrapper.get('[data-test="health-range-30d"]').trigger('click')
    await flushPromises()
    expect(getKeyHealthTrend).toHaveBeenLastCalledWith(9, '30d')
  })

  it('switches between health and first-token metrics without another request', async () => {
    const wrapper = mountDialog()
    await flushPromises()
    const requests = getKeyHealthTrend.mock.calls.length
    await wrapper.get('[data-test="health-metric-ttft"]').trigger('click')
    expect(wrapper.get('[data-test="health-trend"]').text()).toBe('ttft')
    expect(getKeyHealthTrend).toHaveBeenCalledTimes(requests)
  })
})
