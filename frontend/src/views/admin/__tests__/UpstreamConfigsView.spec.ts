import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'

import UpstreamConfigsView from '../UpstreamConfigsView.vue'

const {
  listMock, createMock, updateMock, removeMock, testMock, syncKeysMock, syncAllKeysMock,
  getSettingsMock, updateSettingsMock, listSyncRunsMock, getSyncRunMock, listEventsMock,
  listIncidentsMock, getUsageTrendMock, proxiesMock, showErrorMock, showSuccessMock
} = vi.hoisted(() => ({
  listMock: vi.fn(),
  createMock: vi.fn(),
  updateMock: vi.fn(),
  removeMock: vi.fn(),
  testMock: vi.fn(),
  syncKeysMock: vi.fn(),
  syncAllKeysMock: vi.fn(),
  getSettingsMock: vi.fn(),
  updateSettingsMock: vi.fn(),
  listSyncRunsMock: vi.fn(),
  getSyncRunMock: vi.fn(),
  listEventsMock: vi.fn(),
  listIncidentsMock: vi.fn(),
  getUsageTrendMock: vi.fn(),
  proxiesMock: vi.fn(),
  showErrorMock: vi.fn(),
  showSuccessMock: vi.fn()
}))

vi.mock('@/api/admin/upstreamConfigs', () => ({
  default: {
    list: listMock,
    create: createMock,
    update: updateMock,
    remove: removeMock,
    test: testMock,
    syncKeys: syncKeysMock,
    syncAllKeys: syncAllKeysMock,
    getSettings: getSettingsMock,
    updateSettings: updateSettingsMock,
    listSyncRuns: listSyncRunsMock,
    getSyncRun: getSyncRunMock,
    listEvents: listEventsMock,
    listIncidents: listIncidentsMock,
    getUsageTrend: getUsageTrendMock
  }
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    proxies: {
      getAllWithCount: proxiesMock
    }
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: showErrorMock,
    showSuccess: showSuccessMock
  })
}))

vi.mock('@/composables/usePersistedPageSize', () => ({
  getPersistedPageSize: () => 20
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) =>
        params ? `${key}:${JSON.stringify(params)}` : key
    })
  }
})

const AppLayoutStub = defineComponent({
  template: '<div data-test="app-layout"><slot /></div>'
})

const TablePageLayoutStub = defineComponent({
  template: `
    <div data-test="table-page-layout">
      <div data-test="filters"><slot name="filters" /></div>
      <div data-test="table"><slot name="table" /></div>
      <div data-test="pagination"><slot name="pagination" /></div>
    </div>
  `
})

const DataTableStub = defineComponent({
  props: ['columns', 'data', 'rowKey', 'actionsCount'],
  template: `
    <div data-test="data-table" :data-row-key="rowKey" :data-actions-count="actionsCount">
      <div data-test="columns">{{ columns.map((column) => column.key).join(',') }}</div>
      <div v-for="row in data" :key="row.id" data-test="row">
        <slot name="cell-name" :row="row" :value="row.name" />
        <slot name="cell-provider" :row="row" :value="row.provider" />
        <slot name="cell-base_url" :row="row" :value="row.base_url" />
        <slot name="cell-balance" :row="row" />
        <slot name="cell-rates" :row="row" />
        <slot name="cell-auth_mode" :row="row" :value="row.auth_mode" />
        <slot name="cell-credentials" :row="row" />
        <slot name="cell-last_success_at" :row="row" :value="row.last_success_at" />
        <slot name="cell-actions" :row="row" />
      </div>
      <slot v-if="!data.length" name="empty" />
    </div>
  `
})

const PaginationStub = defineComponent({
  props: ['page', 'total', 'pageSize'],
  emits: ['update:page', 'update:pageSize'],
  template: `
    <div data-test="pagination-component">
      <button data-test="page-2" @click="$emit('update:page', 2)">page 2</button>
      <button data-test="page-size-50" @click="$emit('update:pageSize', 50)">size 50</button>
    </div>
  `
})

const SelectStub = defineComponent({
  props: ['modelValue', 'options'],
  emits: ['update:modelValue', 'change'],
  template: `
    <select
      :value="modelValue"
      @change="$emit('update:modelValue', $event.target.value); $emit('change', $event.target.value, null)"
    >
      <option v-for="option in options" :key="option.value" :value="option.value">{{ option.label }}</option>
    </select>
  `
})

const BaseDialogStub = defineComponent({
  props: ['show', 'title'],
  emits: ['close'],
  template: `
    <div v-if="show" data-test="base-dialog">
      <div data-test="dialog-title">{{ title }}</div>
      <slot />
      <slot name="footer" />
    </div>
  `
})

const ConfirmDialogStub = defineComponent({
  props: ['show', 'title', 'message'],
  emits: ['confirm', 'cancel'],
  template: `
    <div v-if="show" data-test="confirm-dialog">
      <div>{{ title }}</div>
      <div>{{ message }}</div>
      <button data-test="confirm-delete" @click="$emit('confirm')">confirm</button>
      <button data-test="cancel-delete" @click="$emit('cancel')">cancel</button>
    </div>
  `
})

const ProxySelectorStub = defineComponent({
  props: ['modelValue', 'proxies', 'disabled'],
  emits: ['update:modelValue'],
  template: `
    <div data-test="proxy-selector">
      <button type="button" data-test="proxy-none" @click="$emit('update:modelValue', null)">none</button>
      <button type="button" data-test="proxy-pick" @click="$emit('update:modelValue', 7)">proxy</button>
    </div>
  `
})

const UpstreamOperationsDrawerStub = defineComponent({
  props: ['show', 'title', 'subtitle'],
  emits: ['close'],
  template: `
    <div v-if="show" data-test="upstream-operations-drawer">
      <div data-test="operations-title">{{ title }}</div>
      <slot name="toolbar" />
      <slot />
      <slot name="footer" />
    </div>
  `
})

const UpstreamCostTrendChartStub = defineComponent({
  props: ['points', 'loading'],
  template: '<div data-test="cost-trend-chart">{{ points.length }}</div>'
})

function upstreamConfig(overrides = {}) {
  return {
    id: 10,
    name: 'Sub2API Main',
    provider: 'sub2api',
    base_url: 'https://upstream.example.com',
    auth_mode: 'manual_jwt',
    credentials_status: {
      has_access_token: true,
      has_refresh_token: false
    },
    proxy_id: null,
    recharge_rate: 1,
    balance_to_cny_rate: null,
    status: 'active',
    last_success_at: null,
    last_error: null,
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    ...overrides
  }
}

function mockList(items = [upstreamConfig()], total = items.length) {
  listMock.mockResolvedValue({
    items,
    total,
    page: 1,
    page_size: 20,
    pages: 1
  })
}

function mountView() {
  return mount(UpstreamConfigsView, {
    global: {
      stubs: {
        AppLayout: AppLayoutStub,
        TablePageLayout: TablePageLayoutStub,
        DataTable: DataTableStub,
        Pagination: PaginationStub,
        Select: SelectStub,
        BaseDialog: BaseDialogStub,
        ConfirmDialog: ConfirmDialogStub,
        ProxySelector: ProxySelectorStub,
        UpstreamOperationsDrawer: UpstreamOperationsDrawerStub,
        UpstreamCostTrendChart: UpstreamCostTrendChartStub,
        Icon: true,
        Teleport: true
      }
    }
  })
}

describe('UpstreamConfigsView', () => {
  beforeEach(() => {
    vi.useRealTimers()
    listMock.mockReset()
    createMock.mockReset()
    updateMock.mockReset()
    removeMock.mockReset()
    testMock.mockReset()
    syncKeysMock.mockReset()
    syncAllKeysMock.mockReset()
    getSettingsMock.mockReset()
    updateSettingsMock.mockReset()
    listSyncRunsMock.mockReset()
    getSyncRunMock.mockReset()
    listEventsMock.mockReset()
    listIncidentsMock.mockReset()
    getUsageTrendMock.mockReset()
    proxiesMock.mockReset()
    showErrorMock.mockReset()
    showSuccessMock.mockReset()
    mockList()
    proxiesMock.mockResolvedValue([
      {
        id: 7,
        name: 'HK Proxy',
        protocol: 'http',
        host: '127.0.0.1',
        port: 7890
      }
    ])
    createMock.mockResolvedValue(upstreamConfig({ id: 11 }))
    updateMock.mockResolvedValue(upstreamConfig({ id: 10 }))
    removeMock.mockResolvedValue({ message: 'ok' })
    testMock.mockResolvedValue({ ok: true })
    syncKeysMock.mockResolvedValue({ keys: [{ id: 1 }], key_count: 1, updated_account_count: 2 })
    syncAllKeysMock.mockResolvedValue({ results: [{ config_id: 10, name: 'Sub2API Main', success: true, key_count: 3, updated_account_count: 1 }] })
    getSettingsMock.mockResolvedValue({ balance_low_threshold_cny: 10 })
    updateSettingsMock.mockResolvedValue({ balance_low_threshold_cny: 20 })
    listSyncRunsMock.mockResolvedValue({ items: [], total: 0 })
    getSyncRunMock.mockResolvedValue({
      id: 99,
      trigger: 'manual_batch',
      status: 'succeeded',
      total_configs: 1,
      success_configs: 1,
      partial_configs: 0,
      failed_configs: 0,
      started_at: '2026-07-10T00:00:00Z',
      finished_at: '2026-07-10T00:00:01Z',
      results: []
    })
    listEventsMock.mockResolvedValue({ items: [], total: 0 })
    listIncidentsMock.mockResolvedValue({ items: [], total: 0 })
    getUsageTrendMock.mockResolvedValue({ range: '24h', currency: 'CNY', legacy_attributed_requests: 0, points: [] })
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('renders through admin layout, DataTable, and Pagination', async () => {
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.get('[data-test="app-layout"]').exists()).toBe(true)
    expect(wrapper.get('[data-test="table-page-layout"]').exists()).toBe(true)
    expect(wrapper.get('[data-test="data-table"]').attributes('data-row-key')).toBe('id')
    expect(wrapper.get('[data-test="data-table"]').attributes('data-actions-count')).toBe('5')
    expect(wrapper.get('[data-test="columns"]').text()).toContain('actions')
    expect(wrapper.get('[data-test="columns"]').text()).toContain('balance')
    expect(wrapper.text()).toContain('Sub2API Main')
    expect(wrapper.text()).toContain('https://upstream.example.com')
    expect(wrapper.get('[data-test="pagination-component"]').exists()).toBe(true)
  })

  it('renders upstream balance from extra and opens sub2api dashboard URL', async () => {
    const openSpy = vi.spyOn(window, 'open').mockImplementation(() => null)
    mockList([upstreamConfig({
      base_url: 'https://upstream.example.com/base?x=1#frag',
      extra: {
        sub2api_balance: 12.3456,
        sub2api_total_recharged: 169.17,
        sub2api_user_email: 'owner@example.com',
        sub2api_balance_synced_at: '2026-07-09T01:00:00Z'
      }
    })])
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.text()).toContain('12.3456')
    expect(wrapper.text()).toContain('admin.upstreamConfigs.balance.totalRecharged:{"amount":"¥169.17"}')

    await wrapper.get('[data-test="open-upstream-dashboard"]').trigger('click')

    expect(openSpy).toHaveBeenCalledWith('https://upstream.example.com/dashboard', '_blank', 'noopener,noreferrer')
    openSpy.mockRestore()
  })

  it('renders newapi quota snapshot as money, not raw negative quota', async () => {
    mockList([upstreamConfig({
      provider: 'newapi',
      extra: {
        upstream_provider_snapshot: {
          version: 1,
          provider: 'newapi',
          synced_at: '2026-07-09T01:00:00Z',
          email: 'owner@example.com',
          quota: 86995,
          quota_raw: 86995,
          used_quota: 4913005,
          used_quota_raw: 4913005,
          remain_quota: 86995,
          remain_quota_raw: 86995,
          total_quota: 5000000,
          total_quota_raw: 5000000,
          balance_amount: 0.17399,
          used_amount: 9.82601,
          total_amount: 10,
          currency: 'USD',
          currency_symbol: '$',
          quota_display_type: 'USD',
          quota_per_unit: 500000,
          usd_exchange_rate: 7.2
        }
      }
    })])

    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.text()).toContain('¥1.2527')
    expect(wrapper.text()).toContain('admin.upstreamConfigs.balance.totalRecharged:{"amount":"¥72.00"}')
    expect(wrapper.text()).not.toContain('-4,826,010')
    expect(wrapper.text()).not.toContain('$')
  })

  it('keeps zero balances and falls back to balance plus used for newapi total recharged', async () => {
    mockList([upstreamConfig({
      provider: 'newapi',
      extra: {
        upstream_provider_snapshot: {
          version: 1,
          provider: 'newapi',
          balance_amount: 0,
          used_amount: 2.5,
          currency: 'CNY'
        }
      }
    })])

    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.text()).toContain('0.00')
    expect(wrapper.text()).toContain('admin.upstreamConfigs.balance.totalRecharged:{"amount":"¥2.50"}')
  })

  it('falls back to converted total quota for legacy newapi snapshots', async () => {
    mockList([upstreamConfig({
      provider: 'newapi',
      extra: {
        upstream_provider_snapshot: {
          version: 1,
          provider: 'newapi',
          balance_amount: 1,
          total_quota: 2500000,
          quota_per_unit: 500000,
          quota_display_type: 'USD',
          usd_exchange_rate: 7
        }
      }
    })])

    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.text()).toContain('admin.upstreamConfigs.balance.totalRecharged:{"amount":"¥35.00"}')
  })

  it('does not label newapi balances as CNY without an explicit exchange rate', async () => {
    mockList([upstreamConfig({
      provider: 'newapi',
      extra: {
        upstream_provider_snapshot: {
          balance_amount: 2,
          total_amount: 10,
          currency: 'USD'
        }
      }
    })])

    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.text()).not.toContain('¥2.00')
    expect(wrapper.text()).not.toContain('¥10.00')
  })

  it('highlights low CNY balance and renders raw and cost rate summaries', async () => {
    mockList([upstreamConfig({
      recharge_rate: 2,
      keys: [
        { id: 1, rate_multiplier: 0.8, effective_cost_multiplier: 0.35 },
        { id: 2, rate_multiplier: 1.2, effective_cost_multiplier: 0.55 }
      ],
      extra: {
        balance_cny: 5,
        total_recharged_cny: 100
      }
    })])

    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.text()).toContain('admin.upstreamConfigs.balance.lowBalance')
    expect(wrapper.text()).toContain('admin.upstreamConfigs.rates.raw:{"value":"0.8 - 1.2"}')
    expect(wrapper.text()).toContain('admin.upstreamConfigs.rates.cost:{"value":"0.35 - 0.55"}')
  })

  it('wires pagination events to upstream list API', async () => {
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('[data-test="page-2"]').trigger('click')
    await flushPromises()

    expect(listMock).toHaveBeenLastCalledWith(2, 20, { provider: '', search: '' })

    await wrapper.get('[data-test="page-size-50"]').trigger('click')
    await flushPromises()

    expect(listMock).toHaveBeenLastCalledWith(1, 50, { provider: '', search: '' })
  })

  it('debounces search and resets to the first page', async () => {
    vi.useFakeTimers()
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('[data-test="page-2"]').trigger('click')
    await flushPromises()
    await wrapper.get('input').setValue('kedaya')
    vi.advanceTimersByTime(300)
    await flushPromises()

    expect(listMock).toHaveBeenLastCalledWith(1, 20, { provider: '', search: 'kedaya' })
  })

  it('opens create dialog and submits through BaseDialog footer', async () => {
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('button.btn-primary').trigger('click')
    await flushPromises()

    expect(wrapper.get('[data-test="dialog-title"]').text()).toBe('admin.upstreamConfigs.dialog.createTitle')

    const dialog = wrapper.get('[data-test="base-dialog"]')
    await dialog.get('[data-test="upstream-name-input"]').setValue('New Upstream')
    await dialog.get('[data-test="upstream-base-url-input"]').setValue('https://new.example.com')
    await dialog.get('[data-test="recharge-rate-input"]').setValue('1.2')
    await dialog.get('[data-test="balance-to-cny-rate-input"]').setValue('7.2')
    await wrapper.get('[data-test="proxy-pick"]').trigger('click')
    await dialog.get('[data-test="upstream-email-input"]').setValue('admin@example.com')
    await dialog.get('[data-test="upstream-password-input"]').setValue('secret-password')
    await wrapper.get('form#upstream-config-form').trigger('submit.prevent')
    await flushPromises()

    expect(createMock).toHaveBeenCalledWith(expect.objectContaining({
      name: 'New Upstream',
      base_url: 'https://new.example.com',
      provider: 'sub2api',
      proxy_id: 7,
      recharge_rate: 1.2,
      balance_to_cny_rate: 7.2,
      credentials: {
        sub2api_login_email: 'admin@example.com',
        sub2api_login_password: 'secret-password'
      }
    }))
    expect(syncKeysMock).toHaveBeenCalledWith(11)
    expect(showSuccessMock).toHaveBeenCalledWith('admin.upstreamConfigs.messages.savedAndSynced:{"keys":1,"accounts":2}')
  })

  it('submits newapi username/password and syncs after save', async () => {
    createMock.mockResolvedValueOnce(upstreamConfig({ id: 12, provider: 'newapi' }))
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('button.btn-primary').trigger('click')
    await flushPromises()

    const dialog = wrapper.get('[data-test="base-dialog"]')
    await dialog.find('select').setValue('newapi')
    await flushPromises()

    await dialog.get('[data-test="upstream-name-input"]').setValue('NewAPI Upstream')
    await dialog.get('[data-test="upstream-base-url-input"]').setValue('https://www.codexapis.com')
    await dialog.get('[data-test="upstream-username-input"]').setValue('owner@example.com')
    await dialog.get('[data-test="upstream-password-input"]').setValue('secret-password')
    await wrapper.get('form#upstream-config-form').trigger('submit.prevent')
    await flushPromises()

    expect(createMock).toHaveBeenCalledWith(expect.objectContaining({
      name: 'NewAPI Upstream',
      base_url: 'https://www.codexapis.com',
      provider: 'newapi',
      auth_mode: 'user_login',
      credentials: {
        newapi_login_username: 'owner@example.com',
        newapi_login_password: 'secret-password'
      }
    }))
    expect(syncKeysMock).toHaveBeenCalledWith(12)
  })

  it('fills manual JWT fields from local token helper before saving', async () => {
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('button.btn-primary').trigger('click')
    await flushPromises()

    const dialog = wrapper.get('[data-test="base-dialog"]')
    await dialog.get('[data-test="upstream-name-input"]').setValue('JWT Upstream')
    await dialog.get('[data-test="upstream-base-url-input"]').setValue('https://jwt.example.com')

    const selects = dialog.findAll('select')
    await selects[1].setValue('manual_jwt')
    await flushPromises()

    await wrapper.get('[data-test="open-token-assistant"]').trigger('click')
    await flushPromises()

    const accessToken = 'eyJhbGciOiJIUzI1NiJ9.eyJleHAiOjE3ODM1OTgxNTR9.signature'
    const refreshToken = `rt_${'b'.repeat(64)}`
    await wrapper.get('[data-test="token-paste-input"]').setValue([
      `auth_token\t${accessToken}`,
      'auth_user\t{"id":27,"email":"owner@example.com","total_recharged":169.169316}',
      `refresh_token\t${refreshToken}`,
      'token_expires_at\t1783598152643'
    ].join('\n'))
    await wrapper.get('[data-test="apply-token-candidates"]').trigger('click')
    await flushPromises()

    const textareas = wrapper.get('form#upstream-config-form').findAll('textarea')
    expect((textareas[0].element as HTMLTextAreaElement).value).toBe(accessToken)
    expect((textareas[1].element as HTMLTextAreaElement).value).toBe(refreshToken)

    await wrapper.get('form#upstream-config-form').trigger('submit.prevent')
    await flushPromises()

    expect(createMock).toHaveBeenCalledWith(expect.objectContaining({
      name: 'JWT Upstream',
      base_url: 'https://jwt.example.com',
      provider: 'sub2api',
      auth_mode: 'manual_jwt',
      credentials: {
        sub2api_access_token: accessToken,
        sub2api_refresh_token: refreshToken
      }
    }))
    expect(syncKeysMock).toHaveBeenCalledWith(11)
  })

  it('keeps save success visible when the post-save sync fails', async () => {
    syncKeysMock.mockRejectedValueOnce({
      response: {
        data: {
          detail: 'refresh token invalid'
        }
      }
    })
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('button.btn-primary').trigger('click')
    await flushPromises()

    const dialog = wrapper.get('[data-test="base-dialog"]')
    await dialog.get('[data-test="upstream-name-input"]').setValue('New Upstream')
    await dialog.get('[data-test="upstream-base-url-input"]').setValue('https://new.example.com')
    await dialog.get('[data-test="upstream-email-input"]').setValue('admin@example.com')
    await dialog.get('[data-test="upstream-password-input"]').setValue('secret-password')
    await wrapper.get('form#upstream-config-form').trigger('submit.prevent')
    await flushPromises()

    expect(createMock).toHaveBeenCalledTimes(1)
    expect(syncKeysMock).toHaveBeenCalledWith(11)
    expect(showErrorMock).toHaveBeenCalledWith('admin.upstreamConfigs.messages.savedButSyncFailed:{"error":"refresh token invalid"}')
  })

  it('uses ConfirmDialog for delete', async () => {
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('button[aria-label="common.delete"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[data-test="confirm-dialog"]').exists()).toBe(true)

    await wrapper.get('[data-test="confirm-delete"]').trigger('click')
    await flushPromises()

    expect(removeMock).toHaveBeenCalledWith(10)
  })

  it('syncs all upstream configs from toolbar', async () => {
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('button[title="admin.upstreamConfigs.actions.syncAll"]').trigger('click')
    await flushPromises()

    expect(syncAllKeysMock).toHaveBeenCalledTimes(1)
    expect(showSuccessMock).toHaveBeenCalledWith('admin.upstreamConfigs.messages.syncAllSuccess:{"success":1,"keys":3}')
    expect(listMock).toHaveBeenCalledTimes(2)
  })

  it('opens the batch sync result drawer by run_id', async () => {
    syncAllKeysMock.mockResolvedValueOnce({
      run_id: 99,
      results: [{ config_id: 10, name: 'Sub2API Main', success: true, key_count: 3, updated_account_count: 1 }]
    })
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('button[title="admin.upstreamConfigs.actions.syncAll"]').trigger('click')
    await flushPromises()

    expect(getSyncRunMock).toHaveBeenCalledWith(99)
    expect(wrapper.get('[data-test="upstream-operations-drawer"]').exists()).toBe(true)
    expect(wrapper.get('[data-test="sync-run-detail"]').exists()).toBe(true)
  })

  it('loads cost trend and switches between 24h, 7d, and 30d', async () => {
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('[data-test="open-cost-trend"]').trigger('click')
    await flushPromises()
    expect(getUsageTrendMock).toHaveBeenLastCalledWith(10, '24h')

    await wrapper.get('[data-test="trend-range-7d"]').trigger('click')
    await flushPromises()
    expect(getUsageTrendMock).toHaveBeenLastCalledWith(10, '7d')

    await wrapper.get('[data-test="trend-range-30d"]').trigger('click')
    await flushPromises()
    expect(getUsageTrendMock).toHaveBeenLastCalledWith(10, '30d')
  })

  it('loads events and incidents for the selected upstream', async () => {
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('[data-test="open-upstream-events"]').trigger('click')
    await flushPromises()

    expect(listEventsMock).toHaveBeenCalledWith(10, 50, 0)
    expect(listIncidentsMock).toHaveBeenCalledWith(10, 'open', 50, 0)
  })

  it('updates the low balance threshold from upstream settings', async () => {
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('[data-test="open-upstream-settings"]').trigger('click')
    await flushPromises()
    await wrapper.get('[data-test="low-balance-threshold-input"]').setValue('20')
    await wrapper.get('[data-test="upstream-settings-form"]').trigger('submit.prevent')
    await flushPromises()

    expect(updateSettingsMock).toHaveBeenCalledWith({ balance_low_threshold_cny: 20 })
    expect(showSuccessMock).toHaveBeenCalledWith('admin.upstreamConfigs.messages.settingsSaved')
  })
})
