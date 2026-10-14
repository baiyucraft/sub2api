import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import AccountsView from '../AccountsView.vue'
import { adminAPI } from '@/api/admin'

const {
  ordinaryList,
  upstreamList,
  upstreamListWithEtag,
  getSettings,
  getProbeModelCandidates,
  getBatchTodayStats,
  getBatchQualityStats,
  getBatchUsage,
  getUpstreamBillingProbeSettings,
  probeKey,
  setKeyObservation,
  showError
} = vi.hoisted(() => ({
  ordinaryList: vi.fn(),
  upstreamList: vi.fn(),
  upstreamListWithEtag: vi.fn(),
  getSettings: vi.fn(),
  getProbeModelCandidates: vi.fn(),
  getBatchTodayStats: vi.fn(),
  getBatchQualityStats: vi.fn(),
  getBatchUsage: vi.fn(),
  getUpstreamBillingProbeSettings: vi.fn(),
  probeKey: vi.fn(),
  setKeyObservation: vi.fn(),
  showError: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      list: ordinaryList,
      listWithEtag: vi.fn(),
      getBatchTodayStats,
      getBatchQualityStats,
      getBatchUsage,
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
    showError,
    showSuccess: vi.fn(),
    showInfo: vi.fn()
  })
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ token: 'test-token' })
}))

vi.mock('vue-router', () => ({
  useRoute: () => ({ query: {} })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) => {
        if (key === 'admin.upstreamManagement.actions.probeFailedWithDetail') return `${params?.message}: ${params?.detail}`
        if (key === 'admin.upstreamManagement.actions.probeModel') return `model ${params?.model}`
        if (key === 'admin.upstreamManagement.actions.probeHttpStatus') return `HTTP ${params?.status}`
        if (key === 'admin.upstreamManagement.actions.probeDetailSeparator') return '; '
        if (key === 'admin.upstreamManagement.health.reasons.probe_model_unsupported') return 'unsupported probe model'
        return key
      }
    })
  }
})

const AccountTableActionsStub = {
  props: ['showCreate', 'loading'],
  template: '<div data-test="table-actions" :data-show-create="String(showCreate)"><button data-test="refresh" :disabled="loading" @click="$emit(\'refresh\')" /><slot name="afterRefresh" /><slot name="after" /></div>'
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
  props: ['mode', 'filters'],
  template: `
    <div
      data-test="filters"
      :data-mode="mode"
    >
      <button
        data-test="quality-filter-1h-a"
        @click="$emit('update:filters', Object.assign({}, filters, { quality_filter: '1h-A' })); $emit('change')"
      />
      <button
        data-test="quality-filter-clear"
        @click="$emit('update:filters', Object.assign({}, filters, { quality_filter: '' })); $emit('change')"
      />
    </div>
  `
}

const AccountUsageCellStub = {
  props: ['account', 'requestBatchedUsage'],
  mounted() {
    this.requestBatchedUsage?.(this.account)
  },
  template: '<div data-test="usage-cell" />'
}

const mountView = (scope: 'all' | 'ordinary' | 'upstream' = 'upstream') => mount(AccountsView, {
  props: { scope },
  global: {
    stubs: {
      AppLayout: { template: '<div><slot /></div>' },
      TablePageLayout: { template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>' },
      Teleport: true,
      DataTable: {
        name: 'DataTable',
        props: ['data', 'columns', 'sortStorageKey', 'loading'],
        template: `
          <div
            data-test="data-table"
            :data-count="data.length"
            :data-columns="columns.map((column) => column.key).join(',')"
            :data-sort-storage-key="sortStorageKey"
          >
            <button data-test="sort-status" @click="$emit('sort', 'status', 'desc')">sort</button>
            <div v-if="loading" data-test="skeleton" />
            <template v-else><div v-for="row in data" :key="row.id" :data-account-row="row.id">
              <slot name="cell-name" :row="row" :value="row.name" />
              <slot name="cell-rate_multiplier" :row="row" :value="row.rate_multiplier" />
              <slot name="cell-usage" :row="row" />
              <slot name="cell-status" :row="row" />
              <slot name="cell-actions" :row="row" />
            </div></template>
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
      AccountUsageCell: AccountUsageCellStub,
      AutoRefreshCountdownLabel: {
        props: ['enabled'],
        template: '<span>{{ enabled ? "admin.accounts.autoRefreshCountdown" : "admin.accounts.autoRefresh" }}</span>'
      },
      UpstreamModelMappingCell: true,
      Toggle: true,
      Icon: true
    }
  }
})

function deferred<T = any>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>(done => { resolve = done })
  return { promise, resolve }
}

describe('admin AccountsView upstream management mode', () => {
  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
  })
  beforeEach(() => {
    localStorage.clear()
    ordinaryList.mockReset()
    vi.mocked(adminAPI.accounts.listWithEtag).mockReset()
    upstreamList.mockReset()
    upstreamListWithEtag.mockReset()
    getSettings.mockReset()
    getProbeModelCandidates.mockReset()
    getBatchTodayStats.mockReset()
    getBatchQualityStats.mockReset()
    getBatchUsage.mockReset()
    getUpstreamBillingProbeSettings.mockReset()
    probeKey.mockReset()
    setKeyObservation.mockReset()
    showError.mockReset()
    vi.mocked(adminAPI.proxies.getAll).mockResolvedValue([])
    vi.mocked(adminAPI.groups.getAll).mockResolvedValue([])

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
        rate_multiplier: 0.8,
        upstream_image_pricing: {
          supported: true,
          status: 'available',
          stale: false,
          currency: 'USD',
          rate_independent: false,
          effective_rate_multiplier: 0.8,
          final_cost_1k: 0.012,
          final_cost_2k: 0.024,
          final_cost_4k: 0.048,
          pricing_source: 'mixed',
          defaulted_tiers: ['4K'],
          observed_at: '2026-08-10T00:00:00Z'
        },
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
    ordinaryList.mockResolvedValue({
      items: [{
        id: 11,
        name: 'ordinary-account',
        platform: 'openai',
        type: 'apikey',
        status: 'active',
        schedulable: true,
        created_at: '2026-08-10T00:00:00Z',
        updated_at: '2026-08-10T00:00:00Z'
      }],
      total: 1,
      page: 1,
      page_size: 20,
      pages: 1
    })
    upstreamListWithEtag.mockResolvedValue({ notModified: true, etag: 'upstream-etag', data: null })
    vi.mocked(adminAPI.accounts.listWithEtag).mockResolvedValue({ notModified: true, etag: 'ordinary-etag', data: null })
    getSettings.mockResolvedValue({
      ttft_guard: { enabled: false, degradation_ttft_seconds: 20, min_samples: 5 },
      probe_models: { openai: 'gpt-5.4-mini', anthropic: 'claude-haiku-4-5', gemini: 'gemini-2.5-flash' }
    })
    getProbeModelCandidates.mockResolvedValue({ candidates: { openai: [], anthropic: [], gemini: [] } })
    getBatchTodayStats.mockResolvedValue({ stats: {} })
    getBatchQualityStats.mockResolvedValue({ data: { stats: {} }, notModified: false })
    getBatchUsage.mockResolvedValue({ usage: {}, errors: {} })
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
    }), expect.objectContaining({ signal: expect.any(AbortSignal) }))
    expect(ordinaryList).not.toHaveBeenCalled()
    expect(getSettings).not.toHaveBeenCalled()
    expect(getProbeModelCandidates).not.toHaveBeenCalled()
    expect(upstreamList).toHaveBeenCalledWith(expect.not.objectContaining({
      type: expect.anything(),
      privacy_mode: expect.anything(),
      upstream_config_id: expect.anything(),
      upstream_key_id: expect.anything()
    }), expect.objectContaining({ signal: expect.any(AbortSignal) }))

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
    expect(columns).toContain('priority')
    expect(columns).toContain('rate_multiplier')
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

  it('forwards and clears the upstream quality filter when the selector changes', async () => {
    vi.useFakeTimers()
    const wrapper = mountView()
    await flushPromises()
    upstreamList.mockClear()

    await wrapper.get('[data-test="quality-filter-1h-a"]').trigger('click')
    await vi.advanceTimersByTimeAsync(300)
    await flushPromises()
    expect(upstreamList).toHaveBeenLastCalledWith(expect.objectContaining({ quality_filter: '1h-A' }), expect.objectContaining({ signal: expect.any(AbortSignal) }))

    await wrapper.get('[data-test="quality-filter-clear"]').trigger('click')
    await vi.advanceTimersByTimeAsync(300)
    await flushPromises()
    expect(upstreamList).toHaveBeenLastCalledWith(expect.objectContaining({ quality_filter: '' }), expect.objectContaining({ signal: expect.any(AbortSignal) }))

    wrapper.unmount()
    vi.useRealTimers()
  })

  it('shows the upstream image capability badge and costs only in upstream scope', async () => {
    const wrapper = mountView('upstream')
    await flushPromises()
    expect(wrapper.text()).toContain('admin.accounts.upstreamImagePricing.badge')
    expect(wrapper.get('[data-test="image-cost-1k"]').text()).toContain('$0.012')
    expect(wrapper.get('[data-test="image-cost-2k"]').text()).toContain('$0.024')
    expect(wrapper.get('[data-test="image-cost-4k"]').text()).toContain('$0.048')
    expect(wrapper.find('[data-test="image-pricing-rate-mode"]').exists()).toBe(false)
    expect(wrapper.get('[data-test="image-cost-1k"]').attributes('title')).toContain(
      'admin.accounts.upstreamImagePricing.costTooltipShared'
    )
    expect(wrapper.text()).toContain('0.80x')
    wrapper.unmount()

    const ordinary = mountView('ordinary')
    await flushPromises()
    expect(ordinary.find('[data-test="upstream-image-pricing-summary"]').exists()).toBe(false)
    ordinary.unmount()
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

  it('keeps upstream priority and multiplier visibility choices after remounting', async () => {
    localStorage.setItem('upstream-account-hidden-columns', JSON.stringify([
      'priority',
      'rate_multiplier'
    ]))
    localStorage.setItem('upstream-account-hidden-columns-version', 'health-quality-today-visible-v1')

    const wrapper = mountView('upstream')
    await flushPromises()

    const columns = wrapper.get('[data-test="data-table"]').attributes('data-columns').split(',')
    expect(columns).not.toContain('priority')
    expect(columns).not.toContain('rate_multiplier')
    expect(JSON.parse(localStorage.getItem('upstream-account-hidden-columns') || '[]')).toEqual([
      'priority',
      'rate_multiplier'
    ])
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

  it('shows sanitized probe classification metadata after a manual probe failure', async () => {
    probeKey.mockRejectedValue({
      reason: 'UPSTREAM_KEY_PROBE_FAILED',
      message: 'upstream key probe failed',
      metadata: {
        probe_reason: 'probe_model_unsupported',
        protocol: 'gemini_stream_generate_content',
        model: 'gemini-3.7-flash',
        http_status: '404'
      }
    })

    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('[data-test="upstream-probe-action"]').trigger('click')
    await flushPromises()

    expect(showError).toHaveBeenCalledWith(
      'upstream key probe failed: unsupported probe model; model gemini-3.7-flash; HTTP 404'
    )
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

  it('reloads isolated column settings when switching between ordinary and upstream scopes', async () => {
    localStorage.setItem('account-hidden-columns', JSON.stringify(['today_stats']))
    localStorage.setItem('account-hidden-columns-version', 'quality-stats-merged-v3')
    localStorage.setItem('upstream-account-hidden-columns', JSON.stringify(['upstream_health']))
    localStorage.setItem('upstream-account-hidden-columns-version', 'health-quality-today-visible-v1')

    const wrapper = mountView('ordinary')
    await flushPromises()

    let columns = wrapper.get('[data-test="data-table"]').attributes('data-columns').split(',')
    expect(columns).not.toContain('today_stats')
    expect(columns).not.toContain('upstream_health')

    await wrapper.setProps({ scope: 'upstream' })
    await flushPromises()

    columns = wrapper.get('[data-test="data-table"]').attributes('data-columns').split(',')
    expect(columns).toContain('today_stats')
    expect(columns).not.toContain('upstream_health')
    expect(JSON.parse(localStorage.getItem('account-hidden-columns') || '[]')).toEqual(['today_stats'])
    expect(JSON.parse(localStorage.getItem('upstream-account-hidden-columns') || '[]')).toEqual(['upstream_health'])

    await wrapper.setProps({ scope: 'ordinary' })
    await flushPromises()

    columns = wrapper.get('[data-test="data-table"]').attributes('data-columns').split(',')
    expect(columns).not.toContain('today_stats')
    expect(columns).not.toContain('upstream_health')
    wrapper.unmount()
  })

  it('persists real column toggle interactions independently across scope switches', async () => {
    const wrapper = mountView('ordinary')
    await flushPromises()

    await wrapper.find('button[title="admin.accounts.moreActions"]').trigger('click')
    await wrapper.get('[data-test="column-toggle-status"]').trigger('click')
    expect(JSON.parse(localStorage.getItem('account-hidden-columns') || '[]')).toContain('status')

    await wrapper.setProps({ scope: 'upstream' })
    await flushPromises()
    let columns = wrapper.get('[data-test="data-table"]').attributes('data-columns').split(',')
    expect(columns).toContain('status')

    await wrapper.find('button[title="admin.accounts.moreActions"]').trigger('click')
    await wrapper.get('[data-test="column-toggle-upstream_health"]').trigger('click')
    expect(JSON.parse(localStorage.getItem('upstream-account-hidden-columns') || '[]')).toContain('upstream_health')

    await wrapper.setProps({ scope: 'ordinary' })
    await flushPromises()
    columns = wrapper.get('[data-test="data-table"]').attributes('data-columns').split(',')
    expect(columns).not.toContain('status')
    expect(JSON.parse(localStorage.getItem('upstream-account-hidden-columns') || '[]')).toContain('upstream_health')
    wrapper.unmount()
  })

  it('reloads isolated sorting and auto-refresh preferences on every scope switch', async () => {
    localStorage.setItem('account-table-sort', JSON.stringify({ key: 'status', order: 'desc' }))
    localStorage.setItem('upstream-account-table-sort', JSON.stringify({ key: 'id', order: 'asc' }))
    localStorage.setItem('account-auto-refresh', JSON.stringify({ enabled: true, interval_seconds: 5 }))
    localStorage.setItem('upstream-account-auto-refresh', JSON.stringify({ enabled: false, interval_seconds: 30 }))

    const wrapper = mountView('ordinary')
    await flushPromises()

    expect(wrapper.get('[data-test="data-table"]').attributes('data-sort-storage-key')).toBe('account-table-sort')
    expect(ordinaryList).toHaveBeenLastCalledWith(1, 20, expect.objectContaining({
      sort_by: 'status',
      sort_order: 'desc'
    }), expect.objectContaining({ signal: expect.any(AbortSignal) }))
    expect(wrapper.text()).toContain('admin.accounts.autoRefreshCountdown')

    await wrapper.setProps({ scope: 'upstream' })
    await flushPromises()

    expect(wrapper.get('[data-test="data-table"]').attributes('data-sort-storage-key')).toBe('upstream-account-table-sort')
    expect(upstreamList).toHaveBeenLastCalledWith(expect.objectContaining({
      sort_by: 'id',
      sort_order: 'asc'
    }), expect.objectContaining({ signal: expect.any(AbortSignal) }))
    expect(wrapper.text()).toContain('admin.accounts.autoRefresh')
    expect(wrapper.text()).not.toContain('admin.accounts.autoRefreshCountdown')

    await wrapper.get('[data-test="sort-status"]').trigger('click')
    expect(JSON.parse(localStorage.getItem('upstream-account-table-sort') || '{}')).toEqual({
      key: 'status',
      order: 'desc'
    })
    expect(JSON.parse(localStorage.getItem('account-table-sort') || '{}')).toEqual({
      key: 'status',
      order: 'desc'
    })
    wrapper.unmount()
  })

  it('refreshes list, today stats and quality stats on separate due intervals', async () => {
    vi.useFakeTimers()
    localStorage.setItem('upstream-account-auto-refresh', JSON.stringify({ enabled: true, interval_seconds: 5 }))

    const wrapper = mountView('upstream')
    await flushPromises()
    expect(getBatchTodayStats).toHaveBeenCalledTimes(1)
    expect(getBatchQualityStats).toHaveBeenCalledTimes(1)

    await vi.advanceTimersByTimeAsync(5_000)
    await flushPromises()
    expect(upstreamListWithEtag).toHaveBeenCalledTimes(1)
    expect(getBatchTodayStats).toHaveBeenCalledTimes(1)
    expect(getBatchQualityStats).toHaveBeenCalledTimes(1)

    await vi.advanceTimersByTimeAsync(25_000)
    await flushPromises()
    expect(getBatchTodayStats).toHaveBeenCalledTimes(2)
    expect(getBatchQualityStats).toHaveBeenCalledTimes(1)

    await vi.advanceTimersByTimeAsync(30_000)
    await flushPromises()
    expect(getBatchTodayStats).toHaveBeenCalledTimes(3)
    expect(getBatchQualityStats).toHaveBeenCalledTimes(2)

    wrapper.unmount()
    vi.useRealTimers()
  })

  it('submits visible usage accounts as one parent-owned batch', async () => {
    vi.useFakeTimers()
    ordinaryList.mockResolvedValueOnce({
      items: [
        { id: 101, name: 'oauth-a', platform: 'openai', type: 'oauth', status: 'active', schedulable: true },
        { id: 102, name: 'oauth-b', platform: 'grok', type: 'oauth', status: 'active', schedulable: true },
        { id: 103, name: 'api-key', platform: 'openai', type: 'apikey', status: 'active', schedulable: true }
      ],
      total: 3,
      page: 1,
      page_size: 20,
      pages: 1
    })

    const wrapper = mountView('ordinary')
    await flushPromises()
    await vi.runOnlyPendingTimersAsync()
    await flushPromises()

    expect(getBatchUsage).toHaveBeenCalledTimes(1)
    expect(getBatchUsage).toHaveBeenCalledWith([101, 102], false)
    wrapper.unmount()
    vi.useRealTimers()
  })

  it('uses unified protection for upstream rows in the all-account list and refreshes an open dialog in place', async () => {
    const ordinary = { id: 11, name: 'ordinary-account', platform: 'openai', type: 'apikey', status: 'active', schedulable: true, created_at: '2026-08-10T00:00:00Z', updated_at: '2026-08-10T00:00:00Z' }
    const bound = {
      ...ordinary, id: 71, name: 'bound-upstream-account', upstream_config_id: 81, upstream_key_id: 91,
      upstream_protection: {
        cache: null,
        ttft_models: [{ model: 'gpt-real', state: 'expired', ewma_ms: 35000, last_ttft_ms: 36000, sample_count: 5, last_sample_at: null, expires_at: null }],
        groups: []
      },
      ttft_guard_degradations: [{ model: 'gpt-real', group_id: 7 }]
    }
    ordinaryList.mockResolvedValue({ items: [ordinary, bound], total: 2, page: 1, page_size: 20, pages: 1 })
    const wrapper = mountView('all')
    await flushPromises()
    const ordinaryRow = wrapper.get('[data-account-row="11"]')
    const upstreamRow = wrapper.get('[data-account-row="71"]')
    expect(ordinaryRow.find('[data-test="protection-ttft-summary"]').exists()).toBe(false)
    expect(ordinaryRow.findComponent({ name: 'AccountStatusIndicator' }).props('hideProtection')).toBe(false)
    expect(upstreamRow.findComponent({ name: 'AccountStatusIndicator' }).props('hideProtection')).toBe(true)
    await upstreamRow.get('[data-test="protection-ttft-summary"]').trigger('click')
    const openedDialog = wrapper.get('[data-test="upstream-protection-details"]').element
    expect(wrapper.get('[data-test="protection-ttft-detail"]').text()).toContain('35.0s')
    const pending = deferred()
    vi.mocked(adminAPI.accounts.listWithEtag).mockReturnValueOnce(pending.promise)
    await wrapper.get('[data-test="refresh"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-test="skeleton"]').exists()).toBe(false)
    expect(wrapper.get('[data-test="upstream-protection-details"]').element).toBe(openedDialog)
    const updatedBound = { ...bound, upstream_protection: {
      ...bound.upstream_protection,
      ttft_models: [{ ...bound.upstream_protection.ttft_models[0], state: 'observed', ewma_ms: 4000, sample_count: 6 }]
    } }
    pending.resolve({ notModified: false, etag: 'protection-updated', data: { items: [ordinary, updatedBound], total: 2, pages: 1 } })
    await flushPromises()
    expect(ordinaryList).toHaveBeenCalledTimes(1)
    expect(adminAPI.accounts.listWithEtag).toHaveBeenCalledWith(1, 20, expect.objectContaining({ lite: '1' }), expect.objectContaining({ signal: expect.any(AbortSignal) }))
    expect(wrapper.get('[data-test="upstream-protection-details"]').element).toBe(openedDialog)
    expect(wrapper.get('[data-test="protection-ttft-detail"]').text()).toContain('4.0s')
    expect(wrapper.get('[data-test="protection-ttft-summary"]').text()).toContain('state.observed')
    await wrapper.get('[data-test="protection-close"]').trigger('click')
    expect(wrapper.find('[data-test="upstream-protection-details"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('keeps mounted rows during manual refresh and applies cache guard changes without updated_at changing', async () => {
    const wrapper = mountView()
    await flushPromises()
    const table = wrapper.findComponent({ name: 'DataTable' })
    const oldCell = wrapper.get('[data-test="usage-cell"]').element
    const row = (table.props('data') as any[])[0]
    const pending = deferred()
    upstreamListWithEtag.mockReturnValueOnce(pending.promise)

    await wrapper.get('[data-test="refresh"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-test="skeleton"]').exists()).toBe(false)
    expect(wrapper.get('[data-test="usage-cell"]').element).toBe(oldCell)
    expect(upstreamList).toHaveBeenCalledTimes(1)
    expect(upstreamListWithEtag).toHaveBeenCalledTimes(1)
    expect((upstreamListWithEtag.mock.calls[0][1] as any).signal.aborted).toBe(false)

    pending.resolve({ notModified: false, etag: 'cache-changed', data: {
      items: [{ ...row, upstream_cache_guard: { state: 'protected', rate_percent: 50, valid_requests: 21 }, upstream_cache_guard_states: [{ group_id: 7, state: 'protected', valid_requests: 21, rate_percent: 50 }], upstream_protection: { ttft_models: [{ model: 'gpt-real', state: 'insufficient', sample_count: 1 }], groups: [] } }],
      total: 1, pages: 1
    } })
    await flushPromises()
    const updated = (table.props('data') as any[])[0]
    expect(updated.updated_at).toBe(row.updated_at)
    expect(updated.priority).toBe(row.priority)
    expect(updated.upstream_cache_guard.state).toBe('protected')
    expect(updated.upstream_cache_guard_states[0].state).toBe('protected')
    expect(updated.upstream_protection.ttft_models[0].state).toBe('insufficient')
    expect(wrapper.get('[data-test="usage-cell"]').element).toBe(oldCell)
    expect(upstreamListWithEtag.mock.calls[0][1].signal.aborted).toBe(false)

    upstreamListWithEtag.mockResolvedValueOnce({ notModified: false, etag: 'cache-reverted', data: {
      items: [{ ...row, upstream_cache_guard: { state: 'normal', rate_percent: 93.2, valid_requests: 21 }, upstream_cache_guard_states: [{ group_id: 7, state: 'normal', valid_requests: 21, rate_percent: 93.2 }], upstream_protection: { ttft_models: [{ model: 'gpt-real', state: 'normal', sample_count: 5 }], groups: [] } }],
      total: 1, pages: 1
    } })
    await wrapper.get('[data-test="refresh"]').trigger('click')
    await flushPromises()
    const reverted = (table.props('data') as any[])[0]
    expect(reverted.updated_at).toBe(row.updated_at)
    expect(reverted.upstream_cache_guard.state).toBe('normal')
    expect(reverted.upstream_cache_guard_states[0].state).toBe('normal')
    expect(reverted.upstream_protection.ttft_models[0].state).toBe('normal')
    expect(wrapper.get('[data-test="usage-cell"]').element).toBe(oldCell)
    wrapper.unmount()
  })

  it('shares an automatic list refresh with a manual click and starts forced usage before slow statistics and settings finish', async () => {
    vi.useFakeTimers()
    const initial = await upstreamList()
    initial.items[0].type = 'oauth'
    upstreamList.mockClear().mockResolvedValue(initial)
    const wrapper = mountView()
    await flushPromises()
    await vi.advanceTimersByTimeAsync(0)
    await flushPromises()
    getBatchUsage.mockClear()
    const pendingList = deferred()
    const pendingToday = deferred()
    const pendingQuality = deferred()
    const pendingSettings = deferred()
    upstreamListWithEtag.mockReturnValueOnce(pendingList.promise)
    getBatchTodayStats.mockReturnValueOnce(pendingToday.promise)
    getBatchQualityStats.mockReturnValueOnce(pendingQuality.promise)
    getUpstreamBillingProbeSettings.mockReturnValueOnce(pendingSettings.promise)
    const automatic = (wrapper.vm as any).$.setupState.refreshAccountsIncrementally()
    await wrapper.get('[data-test="refresh"]').trigger('click')
    await flushPromises()
    expect(upstreamListWithEtag).toHaveBeenCalledTimes(1)

    pendingList.resolve({ notModified: true, etag: 'same', data: null })
    await flushPromises()
    await vi.advanceTimersByTimeAsync(0)
    await flushPromises()
    expect(getBatchUsage).toHaveBeenCalledWith([71], true)
    expect(wrapper.find('[data-test="skeleton"]').exists()).toBe(false)
    expect(wrapper.get('[data-test="refresh"]').attributes('disabled')).toBeDefined()

    pendingToday.resolve({ stats: {} })
    pendingQuality.resolve({ data: { stats: {} } })
    pendingSettings.resolve({ enabled: true })
    await automatic
    await flushPromises()
    expect(wrapper.get('[data-test="refresh"]').attributes('disabled')).toBeUndefined()
    wrapper.unmount()
  })

  it('aborts the automatic HTTP request on a sort change and ignores its late result', async () => {
    const wrapper = mountView()
    await flushPromises()
    const pending = deferred()
    upstreamListWithEtag.mockReturnValueOnce(pending.promise)
    const refresh = (wrapper.vm as any).$.setupState.refreshAccountsIncrementally()
    const signal = upstreamListWithEtag.mock.calls[0][1].signal
    const nextList = deferred()
    upstreamList.mockReturnValueOnce(nextList.promise)
    await wrapper.get('[data-test="sort-status"]').trigger('click')
    expect(signal.aborted).toBe(true)
    expect(wrapper.find('[data-test="skeleton"]').exists()).toBe(true)
    nextList.resolve({ items: [{ id: 99, name: 'sorted-current', platform: 'openai', type: 'apikey' }], total: 1, pages: 1 })
    await flushPromises()
    pending.resolve({ notModified: false, etag: 'old', data: { items: [{ id: 1, name: 'obsolete' }], total: 500, pages: 25 } })
    await refresh
    await flushPromises()
    expect(wrapper.text()).toContain('sorted-current')
    expect(wrapper.text()).not.toContain('obsolete')
    expect(wrapper.get('[data-test="data-table"]').attributes('data-count')).toBe('1')
    wrapper.unmount()
  })

  it('passes initial-load cancellation through and rejects late results after a sort change', async () => {
    const pending = deferred()
    upstreamList.mockReturnValueOnce(pending.promise)
    const wrapper = mountView()
    await flushPromises()
    const firstSignal = upstreamList.mock.calls[0][1].signal
    expect(wrapper.find('[data-test="skeleton"]').exists()).toBe(true)
    await wrapper.get('[data-test="sort-status"]').trigger('click')
    await flushPromises()
    expect(firstSignal.aborted).toBe(true)
    pending.resolve({ items: [{ id: 1, name: 'obsolete-load' }], total: 1, pages: 1 })
    await flushPromises()
    expect(wrapper.text()).toContain('derived-key-account')
    expect(wrapper.text()).not.toContain('obsolete-load')
    wrapper.unmount()
  })

  it('finishes ordinary in-flight usage before exactly one forced followup and shares forced in-flight requests', async () => {
    vi.useFakeTimers()
    const initial = await upstreamList()
    initial.items[0].type = 'oauth'
    upstreamList.mockClear().mockResolvedValue(initial)
    const ordinaryUsage = deferred()
    const forcedUsage = deferred()
    getBatchUsage.mockReturnValueOnce(ordinaryUsage.promise).mockReturnValueOnce(forcedUsage.promise)
    const wrapper = mountView()
    await flushPromises()
    await vi.advanceTimersByTimeAsync(0)
    await flushPromises()
    expect(getBatchUsage).toHaveBeenCalledWith([71], false)

    await wrapper.get('[data-test="refresh"]').trigger('click')
    await flushPromises()
    await wrapper.get('[data-test="refresh"]').trigger('click')
    await flushPromises()
    await vi.advanceTimersByTimeAsync(0)
    expect(getBatchUsage).toHaveBeenCalledTimes(1)
    ordinaryUsage.resolve({ usage: { 71: { source: 'passive' } }, errors: {} })
    await flushPromises()
    await vi.advanceTimersByTimeAsync(0)
    await flushPromises()
    expect(getBatchUsage).toHaveBeenCalledTimes(2)
    expect(getBatchUsage).toHaveBeenLastCalledWith([71], true)

    await wrapper.get('[data-test="refresh"]').trigger('click')
    await flushPromises()
    await vi.advanceTimersByTimeAsync(0)
    expect(getBatchUsage).toHaveBeenCalledTimes(2)
    forcedUsage.resolve({ usage: { 71: { source: 'active' } }, errors: {} })
    await flushPromises()
    await vi.advanceTimersByTimeAsync(0)
    expect(getBatchUsage).toHaveBeenCalledTimes(2)
    wrapper.unmount()
  })

  it('reuses an upstream usage request when its account leaves and returns to the visible page', async () => {
    vi.useFakeTimers()
    const initial = await upstreamList()
    initial.items[0].type = 'oauth'
    initial.pages = 2
    initial.total = 21
    upstreamList.mockClear().mockResolvedValue(initial)
    const pending = deferred()
    getBatchUsage.mockReturnValueOnce(pending.promise)
    const wrapper = mountView()
    await flushPromises()
    await vi.advanceTimersByTimeAsync(0)
    await flushPromises()
    expect(getBatchUsage).toHaveBeenCalledWith([71], false)

    upstreamList.mockResolvedValueOnce({ ...initial, items: [{ ...initial.items[0], id: 72 }] })
    ;(wrapper.vm as any).$.setupState.handlePageChange(2)
    await flushPromises()
    await vi.advanceTimersByTimeAsync(0)
    await flushPromises()
    expect(getBatchUsage).toHaveBeenCalledTimes(2)
    expect(getBatchUsage).toHaveBeenLastCalledWith([72], false)

    ;(wrapper.vm as any).$.setupState.handlePageChange(1)
    await flushPromises()
    await vi.advanceTimersByTimeAsync(0)
    await flushPromises()
    expect(getBatchUsage).toHaveBeenCalledTimes(2)
    pending.resolve({ usage: { 71: { source: 'passive' } }, errors: {} })
    await flushPromises()
    expect((wrapper.vm as any).$.setupState.usageBatchByAccountId['71'].source).toBe('passive')
    wrapper.unmount()
  })

  it('keeps the same row object for unchanged snapshots and retains it when a background refresh fails', async () => {
    const wrapper = mountView()
    await flushPromises()
    const table = wrapper.findComponent({ name: 'DataTable' })
    const rows = table.props('data') as any[]
    upstreamListWithEtag.mockResolvedValueOnce({ notModified: false, etag: 'identical', data: {
      items: JSON.parse(JSON.stringify(rows)), total: 1, pages: 1
    } })
    await wrapper.get('[data-test="refresh"]').trigger('click')
    await flushPromises()
    expect((table.props('data') as any[])[0]).toBe(rows[0])
    const error = vi.spyOn(console, 'error').mockImplementation(() => {})
    upstreamListWithEtag.mockRejectedValueOnce(new Error('network unavailable'))
    await wrapper.get('[data-test="refresh"]').trigger('click')
    await flushPromises()
    expect((table.props('data') as any[])[0]).toBe(rows[0])
    expect(wrapper.find('[data-test="skeleton"]').exists()).toBe(false)
    expect(error).toHaveBeenCalled()
    expect(showError).toHaveBeenCalledWith('admin.accounts.failedToLoad')
    wrapper.unmount()
  })

  it('reports a failed manual retry after initial loading failed without leaving an unhandled rejection', async () => {
    vi.spyOn(console, 'error').mockImplementation(() => {})
    upstreamList.mockRejectedValueOnce(new Error('initial load unavailable'))
    const wrapper = mountView()
    await flushPromises()
    expect(showError).toHaveBeenCalledWith('admin.accounts.failedToLoad')
    showError.mockClear()
    upstreamList.mockRejectedValueOnce(new Error('manual retry unavailable'))
    await expect((wrapper.vm as any).$.setupState.handleManualRefresh()).resolves.toBeUndefined()
    await flushPromises()
    expect(showError).toHaveBeenCalledTimes(1)
    expect(showError).toHaveBeenCalledWith('admin.accounts.failedToLoad')
    expect(wrapper.get('[data-test="refresh"]').attributes('disabled')).toBeUndefined()
    expect(upstreamListWithEtag).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('keeps automatic refresh failures quiet and suppresses automatic requests while manual statistics are pending', async () => {
    vi.useFakeTimers()
    vi.spyOn(document, 'hidden', 'get').mockReturnValue(false)
    localStorage.setItem('upstream-account-auto-refresh', JSON.stringify({ enabled: true, interval_seconds: 5 }))
    const wrapper = mountView()
    await flushPromises()
    vi.spyOn(console, 'error').mockImplementation(() => {})
    upstreamListWithEtag.mockRejectedValueOnce(new Error('automatic failure'))
    await (wrapper.vm as any).$.setupState.refreshAccountsIncrementally()
    expect(showError).not.toHaveBeenCalled()

    const today = deferred()
    getBatchTodayStats.mockReturnValueOnce(today.promise)
    await wrapper.get('[data-test="refresh"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[data-test="refresh"]').attributes('disabled')).toBeDefined()
    expect(upstreamListWithEtag).toHaveBeenCalledTimes(2)
    await vi.advanceTimersByTimeAsync(6000)
    expect(upstreamListWithEtag).toHaveBeenCalledTimes(2)
    today.resolve({ stats: {} })
    await flushPromises()
    wrapper.unmount()
  })

  it('cancels an incremental refresh on unmount and does not start statistics for its late response', async () => {
    const wrapper = mountView()
    await flushPromises()
    const pending = deferred()
    upstreamListWithEtag.mockReturnValueOnce(pending.promise)
    const refresh = (wrapper.vm as any).$.setupState.refreshAccountsIncrementally()
    const signal = upstreamListWithEtag.mock.calls[0][1].signal
    const todayCalls = getBatchTodayStats.mock.calls.length
    wrapper.unmount()
    expect(signal.aborted).toBe(true)
    pending.resolve({ notModified: true, etag: 'late', data: null })
    await refresh
    expect(getBatchTodayStats).toHaveBeenCalledTimes(todayCalls)
  })
})
