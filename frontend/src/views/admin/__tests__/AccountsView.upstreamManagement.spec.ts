import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import AccountsView from '../AccountsView.vue'

const {
  ordinaryList,
  upstreamList,
  upstreamListWithEtag,
  getTTFTGuard,
  getProbeModels,
  getBatchTodayStats,
  getUpstreamBillingProbeSettings
} = vi.hoisted(() => ({
  ordinaryList: vi.fn(),
  upstreamList: vi.fn(),
  upstreamListWithEtag: vi.fn(),
  getTTFTGuard: vi.fn(),
  getProbeModels: vi.fn(),
  getBatchTodayStats: vi.fn(),
  getUpstreamBillingProbeSettings: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      list: ordinaryList,
      listWithEtag: vi.fn(),
      getBatchTodayStats,
      getBatchQualityStats: vi.fn().mockResolvedValue({ stats: {} }),
      getUpstreamBillingProbeSettings,
      batchDelete: vi.fn(),
      batchClearError: vi.fn(),
      batchRefresh: vi.fn(),
      bulkUpdate: vi.fn()
    },
    proxies: { getAll: vi.fn().mockResolvedValue([]) },
    groups: { getAll: vi.fn().mockResolvedValue([]) },
    upstreamConfigs: {}
  }
}))

vi.mock('@/api/admin/upstreamManagement', () => ({
  default: {
    listAccounts: upstreamList,
    listAccountsWithEtag: upstreamListWithEtag,
    getTTFTGuard,
    updateTTFTGuard: vi.fn(),
    getProbeModels,
    updateProbeModels: vi.fn(),
    probeKey: vi.fn(),
    setKeyObservation: vi.fn(),
    listKeyEvents: vi.fn()
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: vi.fn(),
    showSuccess: vi.fn(),
    showInfo: vi.fn()
  })
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ token: 'test-token' })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key })
  }
})

const AccountTableActionsStub = {
  props: ['showCreate'],
  template: '<div data-test="table-actions" :data-show-create="String(showCreate)"><slot name="after" /></div>'
}

const AccountBulkActionsBarStub = {
  props: ['showDelete', 'showRefreshToken', 'showBillingProbe'],
  template: `
    <div
      data-test="bulk-actions"
      :data-show-delete="String(showDelete)"
      :data-show-refresh-token="String(showRefreshToken)"
      :data-show-billing-probe="String(showBillingProbe)"
    />
  `
}

const AccountTableFiltersStub = {
  props: ['mode'],
  template: `
    <div
      data-test="filters"
      :data-mode="mode"
    />
  `
}

const mountView = () => mount(AccountsView, {
  props: { scope: 'upstream' },
  global: {
    stubs: {
      AppLayout: { template: '<div><slot /></div>' },
      TablePageLayout: { template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>' },
      DataTable: { props: ['data'], template: '<div data-test="data-table" :data-count="data.length" />' },
      Pagination: true,
      ConfirmDialog: true,
      AccountTableActions: AccountTableActionsStub,
      AccountTableFilters: AccountTableFiltersStub,
      AccountBulkActionsBar: AccountBulkActionsBarStub,
      AccountActionMenu: true,
      ImportDataModal: true,
      ReAuthAccountModal: true,
      AccountTestModal: true,
      AccountStatsModal: true,
      ScheduledTestsPanel: true,
      SyncFromCrsModal: true,
      TempUnschedStatusModal: true,
      ErrorPassthroughRulesModal: true,
      TLSFingerprintProfilesModal: true,
      CreateAccountModal: true,
      EditAccountModal: true,
      BulkEditAccountModal: true,
      UpstreamKeyEventsDialog: true,
      PlatformTypeBadge: true,
      AccountCapacityCell: true,
      AccountStatusIndicator: true,
      AccountQualityCell: true,
      AccountTodayStatsCell: true,
      AccountGroupsCell: true,
      AccountUsageCell: true,
      Toggle: true,
      Icon: true
    }
  }
})

describe('admin AccountsView upstream management mode', () => {
  beforeEach(() => {
    localStorage.clear()
    ordinaryList.mockReset()
    upstreamList.mockReset()
    upstreamListWithEtag.mockReset()
    getTTFTGuard.mockReset()
    getProbeModels.mockReset()
    getBatchTodayStats.mockReset()
    getUpstreamBillingProbeSettings.mockReset()

    upstreamList.mockResolvedValue({
      items: [{
        id: 71,
        name: 'derived-key-account',
        platform: 'openai',
        type: 'apikey',
        status: 'active',
        schedulable: true,
        upstream_config_id: 81,
        upstream_key_id: 91,
        upstream_config_name: 'Transit Hub',
        upstream_key_name: 'Key A',
        upstream_key_masked: 'sk-abc...7890',
        available_actions: ['edit', 'test', 'probe_key'],
        created_at: '2026-08-10T00:00:00Z',
        updated_at: '2026-08-10T00:00:00Z'
      }],
      total: 1,
      page: 1,
      page_size: 20,
      pages: 1
    })
    upstreamListWithEtag.mockResolvedValue({ notModified: true, etag: 'upstream-etag', data: null })
    getTTFTGuard.mockResolvedValue({ enabled: false, degradation_ttft_seconds: 20, min_samples: 5 })
    getProbeModels.mockResolvedValue({ models: { openai: 'gpt-5.4-mini', anthropic: 'claude-haiku-4-5', gemini: 'gemini-2.5-flash' } })
    getBatchTodayStats.mockResolvedValue({ stats: {} })
    getUpstreamBillingProbeSettings.mockResolvedValue({ enabled: true, interval_minutes: 30 })
  })

  it('uses the dedicated upstream API and hides ordinary-only mutations', async () => {
    const wrapper = mountView()
    await flushPromises()

    expect(upstreamList).toHaveBeenCalledWith(expect.objectContaining({
      page: 1,
      page_size: 20,
      scope: 'upstream'
    }))
    expect(ordinaryList).not.toHaveBeenCalled()
    expect(getTTFTGuard).toHaveBeenCalledTimes(1)
    expect(getProbeModels).toHaveBeenCalledTimes(1)
    expect(upstreamList).toHaveBeenCalledWith(expect.not.objectContaining({
      type: expect.anything(),
      privacy_mode: expect.anything(),
      upstream_config_id: expect.anything(),
      upstream_key_id: expect.anything()
    }))

    expect(wrapper.get('[data-test="filters"]').attributes('data-mode')).toBe('upstream')
    expect(wrapper.get('[data-test="table-actions"]').attributes('data-show-create')).toBe('false')
    expect(wrapper.get('[data-test="bulk-actions"]').attributes('data-show-delete')).toBe('false')
    expect(wrapper.get('[data-test="bulk-actions"]').attributes('data-show-refresh-token')).toBe('false')
    expect(wrapper.get('[data-test="bulk-actions"]').attributes('data-show-billing-probe')).toBe('false')
    expect(wrapper.get('[data-test="data-table"]').attributes('data-count')).toBe('1')
    expect(wrapper.text()).toContain('admin.upstreamManagement.ttftGuard.title')
    expect(wrapper.text()).toContain('admin.upstreamManagement.probeModels.title')

    wrapper.unmount()
  })
})
