import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import AccountsView from '../AccountsView.vue'

const {
  ordinaryList,
  upstreamList,
  upstreamListWithEtag,
  getSettings,
  getProbeModelCandidates,
  getBatchTodayStats,
  getUpstreamBillingProbeSettings,
  probeKey,
  setKeyObservation
} = vi.hoisted(() => ({
  ordinaryList: vi.fn(),
  upstreamList: vi.fn(),
  upstreamListWithEtag: vi.fn(),
  getSettings: vi.fn(),
  getProbeModelCandidates: vi.fn(),
  getBatchTodayStats: vi.fn(),
  getUpstreamBillingProbeSettings: vi.fn(),
  probeKey: vi.fn(),
  setKeyObservation: vi.fn()
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
    getSettings,
    updateSettings: vi.fn(),
    getProbeModelCandidates,
    probeKey,
    setKeyObservation,
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
  template: '<div data-test="table-actions" :data-show-create="String(showCreate)"><slot name="afterRefresh" /><slot name="after" /></div>'
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
      DataTable: {
        props: ['data', 'columns'],
        template: `
          <div data-test="data-table" :data-count="data.length" :data-columns="columns.map((column) => column.key).join(',')">
            <div v-for="row in data" :key="row.id"><slot name="cell-actions" :row="row" /></div>
          </div>
        `
      },
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
      UpstreamManagementSettingsDialog: true,
      PlatformTypeBadge: true,
      AccountCapacityCell: true,
      AccountStatusIndicator: true,
      UpstreamHealthCell: true,
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
    getSettings.mockReset()
    getProbeModelCandidates.mockReset()
    getBatchTodayStats.mockReset()
    getUpstreamBillingProbeSettings.mockReset()
    probeKey.mockReset()
    setKeyObservation.mockReset()

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
        available_actions: ['edit', 'test', 'probe_key', 'toggle_observation'],
        upstream_health: { observation_enabled: false },
        created_at: '2026-08-10T00:00:00Z',
        updated_at: '2026-08-10T00:00:00Z'
      }],
      total: 1,
      page: 1,
      page_size: 20,
      pages: 1
    })
    upstreamListWithEtag.mockResolvedValue({ notModified: true, etag: 'upstream-etag', data: null })
    getSettings.mockResolvedValue({
      ttft_guard: { enabled: false, degradation_ttft_seconds: 20, min_samples: 5 },
      probe_models: { openai: 'gpt-5.4-mini', anthropic: 'claude-haiku-4-5', gemini: 'gemini-2.5-flash' }
    })
    getProbeModelCandidates.mockResolvedValue({ candidates: { openai: [], anthropic: [], gemini: [] } })
    getBatchTodayStats.mockResolvedValue({ stats: {} })
    getUpstreamBillingProbeSettings.mockResolvedValue({ enabled: true, interval_minutes: 30 })
    probeKey.mockResolvedValue({ observation_enabled: false })
    setKeyObservation.mockResolvedValue({ observation_enabled: true })
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
    expect(getSettings).not.toHaveBeenCalled()
    expect(getProbeModelCandidates).not.toHaveBeenCalled()
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
    const dataTable = wrapper.get('[data-test="data-table"]')
    const columns = dataTable.attributes('data-columns').split(',')
    expect(dataTable.attributes('data-count')).toBe('1')
    expect(columns).not.toContain('upstream_source')
    expect(columns).toContain('model_mapping')
    expect(columns).toContain('upstream_health')
    expect(columns).toContain('quality_stats')
    expect(columns).toContain('today_stats')
    expect(columns.slice(columns.indexOf('schedulable'), columns.indexOf('today_stats') + 1)).toEqual([
      'schedulable',
      'status',
      'upstream_health',
      'quality_stats',
      'today_stats'
    ])
    expect(wrapper.find('[data-test="upstream-management-settings"]').exists()).toBe(true)
    expect(wrapper.text()).not.toContain('admin.upstreamManagement.ttftGuard.title')
    expect(wrapper.text()).not.toContain('admin.upstreamManagement.probeModels.title')

    wrapper.unmount()
  })

  it('keeps explicit upstream column choices after the visibility migration', async () => {
    localStorage.setItem('upstream-account-hidden-columns', JSON.stringify([
      'upstream_health',
      'quality_stats',
      'today_stats'
    ]))
    localStorage.setItem('upstream-account-hidden-columns-version', 'health-quality-today-visible-v1')

    const wrapper = mountView()
    await flushPromises()

    const columns = wrapper.get('[data-test="data-table"]').attributes('data-columns').split(',')
    expect(columns).not.toContain('upstream_health')
    expect(columns).not.toContain('quality_stats')
    expect(columns).not.toContain('today_stats')

    wrapper.unmount()
  })

  it('keeps the probe label while replacing its icon during a probe', async () => {
    let resolveProbe: ((value: { observation_enabled: boolean }) => void) | undefined
    probeKey.mockReturnValue(new Promise(resolve => { resolveProbe = resolve }))

    const wrapper = mountView()
    await flushPromises()

    const action = wrapper.get('[data-test="upstream-probe-action"]')
    expect(action.text()).toBe('admin.upstreamManagement.actions.probe')
    expect(action.attributes('title')).toBe('admin.upstreamManagement.actions.probeTip')
    expect(action.get('icon-stub').attributes('name')).toBe('play')

    await action.trigger('click')

    expect(action.attributes('disabled')).toBeDefined()
    expect(action.text()).toBe('admin.upstreamManagement.actions.probe')
    expect(action.get('icon-stub').attributes('name')).toBe('refresh')
    expect(action.get('icon-stub').classes()).toContain('animate-spin')

    resolveProbe?.({ observation_enabled: false })
    await flushPromises()
    wrapper.unmount()
  })

  it('shows observe and cancel as distinct automatic observation states', async () => {
    const wrapper = mountView()
    await flushPromises()

    const action = wrapper.get('[data-test="upstream-observation-action"]')
    expect(action.text()).toBe('admin.upstreamManagement.actions.observation')
    expect(action.attributes('title')).toBe('admin.upstreamManagement.actions.observationTip')
    expect(action.get('icon-stub').attributes('name')).toBe('eye')
    expect(action.classes()).toContain('text-sky-600')

    await action.trigger('click')
    await flushPromises()

    expect(setKeyObservation).toHaveBeenCalledWith(91, true)
    expect(action.text()).toBe('admin.upstreamManagement.actions.cancelObservation')
    expect(action.attributes('title')).toBe('admin.upstreamManagement.actions.cancelObservationTip')
    expect(action.get('icon-stub').attributes('name')).toBe('eyeOff')
    expect(action.classes()).toContain('text-rose-600')

    setKeyObservation.mockResolvedValueOnce({ observation_enabled: false })
    await action.trigger('click')
    await flushPromises()

    expect(setKeyObservation).toHaveBeenLastCalledWith(91, false)
    expect(action.text()).toBe('admin.upstreamManagement.actions.observation')
    wrapper.unmount()
  })
})
