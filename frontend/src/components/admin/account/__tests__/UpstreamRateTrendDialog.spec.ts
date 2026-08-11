import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import UpstreamRateTrendDialog from '../UpstreamRateTrendDialog.vue'

const { getKeyRateTrend } = vi.hoisted(() => ({
  getKeyRateTrend: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    upstreamConfigs: { getKeyRateTrend }
  }
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key })
}))

const BaseDialogStub = {
  props: ['show', 'title'],
  emits: ['close'],
  template: '<div v-if="show" data-test="dialog"><slot /></div>'
}

const TrendPanelStub = {
  props: ['trend', 'loading'],
  template: '<div data-test="trend-panel" :data-loading="String(loading)" :data-has-trend="String(Boolean(trend))" />'
}

const account = {
  id: 71,
  name: 'derived-key-account',
  upstream_config_id: 81,
  upstream_key_id: 91
} as any

describe('UpstreamRateTrendDialog', () => {
  beforeEach(() => {
    getKeyRateTrend.mockReset()
    getKeyRateTrend.mockResolvedValue({
      current_rate: 1.2,
      previous_rate: 1,
      points: [],
      changes: []
    })
  })

  const mountDialog = (props: Record<string, unknown> = {}) => mount(UpstreamRateTrendDialog, {
    props: { show: true, account, ...props },
    global: {
      stubs: {
        BaseDialog: BaseDialogStub,
        UpstreamKeyRateTrendPanel: TrendPanelStub
      }
    }
  })

  it('loads the bound config/key and supports all trend ranges', async () => {
    const wrapper = mountDialog()
    await flushPromises()

    expect(getKeyRateTrend).toHaveBeenCalledWith(81, 91, '24h')
    expect(wrapper.get('[data-test="trend-panel"]').attributes('data-has-trend')).toBe('true')

    await wrapper.findAll('button')[1].trigger('click')
    await flushPromises()
    expect(getKeyRateTrend).toHaveBeenLastCalledWith(81, 91, '7d')

    await wrapper.findAll('button')[2].trigger('click')
    await flushPromises()
    expect(getKeyRateTrend).toHaveBeenLastCalledWith(81, 91, '30d')
  })

  it('renders loading, empty and error states without stale requests', async () => {
    let resolveRequest!: (value: unknown) => void
    getKeyRateTrend.mockReturnValueOnce(new Promise((resolve) => { resolveRequest = resolve }))
    const wrapper = mountDialog()
    await Promise.resolve()
    expect(wrapper.get('[data-test="trend-panel"]').attributes('data-loading')).toBe('true')
    resolveRequest(null)
    await flushPromises()
    expect(wrapper.get('[data-test="trend-panel"]').attributes('data-has-trend')).toBe('false')

    getKeyRateTrend.mockRejectedValueOnce(new Error('probe unavailable'))
    await wrapper.findAll('button')[1].trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('probe unavailable')
  })
})
