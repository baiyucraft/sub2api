import { describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

const { getDimensions, getSnapshot, getMatrix, getModels, getErrors, getUsers, replace } = vi.hoisted(() => ({
  getDimensions: vi.fn(),
  getSnapshot: vi.fn(),
  getMatrix: vi.fn(),
  getModels: vi.fn(),
  getErrors: vi.fn(),
  getUsers: vi.fn(),
  replace: vi.fn(),
}))

const metric = {
  success_requests: 0,
  error_requests: 0,
  request_count: 0,
  token_count: 0,
  rpm: 0,
  tpm: 0,
  error_rate: 0,
  cache_rate: 0,
  cache_rate_numerator: 0,
  cache_rate_denominator: 0,
  ttft: { sample_count: 0, p50_ms: null, p95_ms: null, avg_ms: null },
  duration: { sample_count: 0, p50_ms: null, p95_ms: null, avg_ms: null },
}

const coverage = {
  requested_start: '2026-08-09T00:00:00Z',
  requested_end: '2026-08-10T00:00:00Z',
  coverage_start: '2026-08-09T00:00:00Z',
  data_through: '2026-08-09T00:00:00Z',
  computed_at: '2026-08-09T00:00:00Z',
  aggregation_lag_seconds: 0,
  coverage_complete: true,
  bucket_seconds: 300,
  bootstrap: null,
}

vi.mock('@/api/channelMonitorV2', () => ({ getDimensions, getSnapshot, getMatrix, getModels, getErrors, getUsers }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ isAdmin: false }) }))
vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: vi.fn(),
  }),
}))
vi.mock('@/utils/featureFlags', () => ({
  isChannelMonitorV1Mode: () => false,
  isChannelMonitorThroughputHidden: () => false,
}))
vi.mock('vue-router', () => ({
  useRoute: () => ({ query: {} }),
  useRouter: () => ({ replace }),
}))
vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key,
      te: () => false,
      locale: { value: 'en' },
    }),
  }
})

import ChannelStatusView from './ChannelStatusView.vue'

describe('ChannelStatusView unified range', () => {
  it('defaults to 90m and reloads the V2 data when the shared range changes', async () => {
    getDimensions.mockResolvedValue({ platforms: [], groups: [], models: [] })
    getSnapshot.mockResolvedValue({ config: { enabled: true, refresh_interval_seconds: 300 }, coverage, metrics: metric, health: { overall: 'unknown', error_rate: 'unknown', ttft: 'unknown', minimum_sample: 3 }, trend: [] })
    getMatrix.mockResolvedValue({ coverage, group_by: 'platform_group', items: [] })
    getModels.mockResolvedValue({ coverage, items: [] })
    getErrors.mockResolvedValue({ coverage, items: [] })
    getUsers.mockResolvedValue({ coverage, items: [] })

    const wrapper = mount(ChannelStatusView, {
      global: {
        stubs: {
          AppLayout: { template: '<main><slot /></main>' },
          FilterMultiSelect: { template: '<div />' },
          MetricCell: { template: '<div />' },
          MonitorRankBadge: { template: '<div />' },
          RelayPulseMatrix: { template: '<div />' },
          MonitorTrendChart: { template: '<div />' },
          Icon: { template: '<span />' },
        },
      },
    })
    await flushPromises()

    expect(getSnapshot).toHaveBeenCalledTimes(1)
    expect(getSnapshot.mock.calls[0][0]).toMatchObject({ range: '90m' })
    expect(wrapper.find('button.tab-active').text()).toContain('channelMonitorV2.ranges.90m')

    const rangeButtons = wrapper.findAll('button').filter((button) => button.text().includes('channelMonitorV2.ranges.24h'))
    expect(rangeButtons).toHaveLength(1)
    await rangeButtons[0].trigger('click')
    await flushPromises()

    expect(getSnapshot).toHaveBeenCalledTimes(2)
    expect(getSnapshot.mock.calls[1][0]).toMatchObject({ range: '24h' })
    expect(getMatrix.mock.calls[1][0]).toMatchObject({ range: '24h' })
    expect(getModels).toHaveBeenCalledTimes(2)
  })
})
