import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import V1CacheRateSourceMappingPanel from '@/features/channel-monitor-v2/V1CacheRateSourceMappingPanel.vue'
import type { MonitorConfig } from '@/api/channelMonitorV2'

const { getConfig, updateConfig, getGroups, showSuccess, showError } = vi.hoisted(() => ({
  getConfig: vi.fn(),
  updateConfig: vi.fn(),
  getGroups: vi.fn(),
  showSuccess: vi.fn(),
  showError: vi.fn(),
}))

vi.mock('@/api/channelMonitorV2', async () => {
  const actual = await vi.importActual<typeof import('@/api/channelMonitorV2')>('@/api/channelMonitorV2')
  return { ...actual, getConfig, updateConfig }
})

vi.mock('@/api/admin', () => ({
  adminAPI: { groups: { getAllIncludingInactive: getGroups } },
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showSuccess, showError }),
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key }),
  }
})

function config(): MonitorConfig {
  return {
    version: 1,
    enabled: true,
    refresh_interval_seconds: 60,
    platforms: [],
    group_ids: [],
    v1_cache_rate_source_groups: { '1': 2 },
    health_thresholds: {
      minimum_sample: 50,
      warning_error_rate: 0.05,
      critical_error_rate: 0.2,
      target_ttft_ms: 3000,
      warning_ttft_ms: 3000,
      critical_ttft_ms: 10000,
      warning_cache_rate: 0.85,
      critical_cache_rate: 0.6,
      error_weight: 0.6,
      ttft_weight: 0.2,
      cache_weight: 0.2,
    },
  }
}

describe('V1CacheRateSourceMappingPanel', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    getConfig.mockResolvedValue(config())
    getGroups.mockResolvedValue([
      { id: 1, name: 'gpt-专属', platform: 'openai' },
      { id: 2, name: 'gpt-pro', platform: 'openai' },
      { id: 3, name: '其他', platform: 'openai' },
    ])
    updateConfig.mockImplementation(async (value: MonitorConfig) => JSON.parse(JSON.stringify(value)) as MonitorConfig)
  })

  it('loads mappings, adds a row, and saves through the shared monitor config API', async () => {
    const wrapper = mount(V1CacheRateSourceMappingPanel)
    await flushPromises()

    expect(wrapper.findAll('select')).toHaveLength(2)
    await wrapper.findAll('button')[1].trigger('click')
    expect(wrapper.findAll('select')).toHaveLength(4)

    const buttons = wrapper.findAll('button')
    await buttons[0].trigger('click')
    await flushPromises()
    expect(updateConfig).toHaveBeenCalledTimes(1)
    expect(updateConfig.mock.calls[0][0].v1_cache_rate_source_groups).toEqual({ '1': 2, '2': 1 })
    expect(showSuccess).toHaveBeenCalled()
  })
})
