import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'

import UpstreamConfigsView from '../UpstreamConfigsView.vue'
import enUpstreamConfigs from '@/i18n/locales/en/admin/upstreamConfigs'
import zhUpstreamConfigs from '@/i18n/locales/zh/admin/upstreamConfigs'

const {
  listMock, createMock, updateMock, removeMock, testMock, syncKeysMock, syncAllKeysMock,
  getSettingsMock, updateSettingsMock, listSyncRunsMock, getSyncRunMock, listEventsMock,
  listIncidentsMock, listBalanceHistoryMock, getUsageTrendMock, proxiesMock, showErrorMock, showSuccessMock,
  listKeyRateTrendKeysMock, getKeyRateTrendMock
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
  listBalanceHistoryMock: vi.fn(),
  getUsageTrendMock: vi.fn(),
  listKeyRateTrendKeysMock: vi.fn(),
  getKeyRateTrendMock: vi.fn(),
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
    listBalanceHistory: listBalanceHistoryMock,
    getUsageTrend: getUsageTrendMock,
    listKeyRateTrendKeys: listKeyRateTrendKeysMock,
    getKeyRateTrend: getKeyRateTrendMock
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
  props: ['columns', 'data', 'rowKey'],
  template: `
    <div data-test="data-table" :data-row-key="rowKey">
      <div data-test="columns">{{ columns.map((column) => column.key).join(',') }}</div>
      <div data-test="actions-column-class">{{ columns.find((column) => column.key === 'actions')?.class }}</div>
      <div data-test="upstream-concurrency-header">
        <slot name="header-upstream_concurrency" :column="columns.find((column) => column.key === 'upstream_concurrency')" />
      </div>
      <div v-for="row in data" :key="row.id" data-test="row">
        <slot name="cell-name" :row="row" :value="row.name" />
        <slot v-if="columns.find((column) => column.key === 'provider')" name="cell-provider" :row="row" :value="row.provider" />
        <slot v-if="columns.find((column) => column.key === 'urls')" name="cell-urls" :row="row" :value="row.site_url" />
        <slot v-if="columns.find((column) => column.key === 'balance')" name="cell-balance" :row="row" />
        <slot v-if="columns.find((column) => column.key === 'upstream_concurrency')" name="cell-upstream_concurrency" :row="row" />
        <slot v-if="columns.find((column) => column.key === 'rates')" name="cell-rates" :row="row" />
        <slot v-if="columns.find((column) => column.key === 'auth_mode')" name="cell-auth_mode" :row="row" :value="row.auth_mode" />
        <slot v-if="columns.find((column) => column.key === 'credentials')" name="cell-credentials" :row="row" />
        <slot v-if="columns.find((column) => column.key === 'last_success_at')" name="cell-last_success_at" :row="row" :value="row.last_success_at" />
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
  props: ['show', 'title', 'width', 'closeOnEscape', 'showCloseButton'],
  emits: ['close'],
  template: `
    <div v-if="show" data-test="base-dialog" :data-width="width" :data-close-on-escape="closeOnEscape" :data-show-close-button="showCloseButton">
      <div data-test="dialog-title">{{ title }}</div>
      <button data-test="dialog-close" @click="$emit('close')">close</button>
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

const UpstreamActionMenuStub = defineComponent({
  props: ['show', 'anchorEl', 'config', 'width'],
  emits: ['close', 'test', 'dashboard', 'delete'],
  template: `
    <div v-if="show" data-test="upstream-action-menu" :data-width="width">
      <button data-test="menu-test" @click="$emit('test', config); $emit('close')">test</button>
      <button data-test="menu-dashboard" @click="$emit('dashboard', config); $emit('close')">dashboard</button>
      <button data-test="menu-delete" @click="$emit('delete', config); $emit('close')">delete</button>
    </div>
  `
})

const UpstreamCostTrendChartStub = defineComponent({
  props: ['points', 'loading'],
  template: '<div data-test="cost-trend-chart">{{ points.length }}</div>'
})

const UpstreamKeyRateTrendChartStub = defineComponent({
  props: ['points', 'loading'],
  template: '<div data-test="key-rate-trend-chart">{{ points.length }}</div>'
})

function upstreamConfig(overrides = {}) {
  return {
    id: 10,
    name: 'Sub2API Main',
    provider: 'sub2api',
    site_url: 'https://upstream.example.com',
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

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((done) => { resolve = done })
  return { promise, resolve }
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
        UpstreamActionMenu: UpstreamActionMenuStub,
        UpstreamCostTrendChart: UpstreamCostTrendChartStub,
        UpstreamKeyRateTrendChart: UpstreamKeyRateTrendChartStub,
        Icon: true,
        Teleport: true
      }
    }
  })
}

describe('UpstreamConfigsView', () => {
  beforeEach(() => {
    localStorage.clear()
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
    listBalanceHistoryMock.mockReset()
    getUsageTrendMock.mockReset()
    listKeyRateTrendKeysMock.mockReset()
    getKeyRateTrendMock.mockReset()
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
    getSettingsMock.mockResolvedValue({ balance_low_threshold_cny: 10, sub2api_not_in_cn_confirmed: false })
    updateSettingsMock.mockResolvedValue({ balance_low_threshold_cny: 20, sub2api_not_in_cn_confirmed: true })
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
    listBalanceHistoryMock.mockResolvedValue({ items: [], total: 0 })
    getUsageTrendMock.mockResolvedValue({ range: '24h', currency: 'CNY', legacy_attributed_requests: 0, points: [] })
    listKeyRateTrendKeysMock.mockResolvedValue([{ key_id: 7, name: 'Primary', status: 'active', current_raw_rate: 1.2, current_effective_rate: 0.96 }])
    getKeyRateTrendMock.mockResolvedValue({
      range: '24h', config_id: 10, key_id: 7, key_name: 'Primary',
      current_raw_rate: 1.2, current_effective_rate: 0.96,
      points: [], changes: []
    })
  })

  afterEach(() => {
    localStorage.clear()
    vi.useRealTimers()
  })

  it('renders through admin layout, DataTable, and Pagination', async () => {
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.get('[data-test="app-layout"]').exists()).toBe(true)
    expect(wrapper.get('[data-test="table-page-layout"]').exists()).toBe(true)
    expect(wrapper.get('[data-test="data-table"]').attributes('data-row-key')).toBe('id')
    expect(wrapper.get('[data-test="data-table"]').attributes('data-actions-count')).toBeUndefined()
    expect(wrapper.get('[data-test="more-upstream-actions"]').element.parentElement?.className).toContain('min-w-[300px]')
    expect(wrapper.get('[data-test="actions-column-class"]').text()).toBe('min-w-[300px]')
    expect(wrapper.get('[data-test="columns"]').text()).toBe(
      'name,provider,urls,balance,upstream_concurrency,auth_mode,credentials,last_success_at,actions'
    )
    expect(wrapper.get('[data-test="upstream-concurrency-header"] span').attributes('title')).toBe(
      'admin.upstreamConfigs.concurrency.headerTitle'
    )
    expect(wrapper.text()).toContain('Sub2API Main')
    expect(wrapper.text()).toContain('https://upstream.example.com')
    expect(wrapper.get('[data-test="pagination-component"]').exists()).toBe(true)
  })

  it('uses localized balance and concurrency column labels', () => {
    expect(zhUpstreamConfigs.upstreamConfigs.columns.balance).toBe('余额')
    expect(zhUpstreamConfigs.upstreamConfigs.columns.upstreamConcurrency).toBe('并发')
    expect(enUpstreamConfigs.upstreamConfigs.columns.balance).toBe('Balance')
    expect(enUpstreamConfigs.upstreamConfigs.columns.upstreamConcurrency).toBe('Concurrency')
  })

  it('shows rates when toggled and restores the current-version preference', async () => {
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('button[title="admin.upstreamConfigs.columnSettings"]').trigger('click')
    const ratesToggle = wrapper.findAll('button').find((button) =>
      button.text().includes('admin.upstreamConfigs.columns.rates')
    )
    expect(ratesToggle).toBeTruthy()
    await ratesToggle!.trigger('click')
    await flushPromises()

    expect(wrapper.get('[data-test="columns"]').text()).toContain('rates')
    expect(localStorage.getItem('upstream-config-hidden-columns')).toBe('[]')
    expect(localStorage.getItem('upstream-config-hidden-columns-version')).toBe('1')

    wrapper.unmount()
    const remounted = mountView()
    await flushPromises()
    expect(remounted.get('[data-test="columns"]').text()).toContain('rates')
  })

  it('normalizes saved columns and never hides fixed columns', async () => {
    localStorage.setItem(
      'upstream-config-hidden-columns',
      JSON.stringify(['credentials', 'unknown', 'name', 'actions', 'provider'])
    )
    localStorage.setItem('upstream-config-hidden-columns-version', '1')

    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.get('[data-test="columns"]').text()).toBe(
      'name,urls,balance,upstream_concurrency,rates,auth_mode,last_success_at,actions'
    )
    expect(localStorage.getItem('upstream-config-hidden-columns')).toBe(
      JSON.stringify(['provider', 'credentials'])
    )
  })

  it('adds the default hidden rates column once when upgrading old preferences', async () => {
    localStorage.setItem('upstream-config-hidden-columns', JSON.stringify(['balance']))

    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.get('[data-test="columns"]').text()).not.toContain('balance')
    expect(wrapper.get('[data-test="columns"]').text()).not.toContain('rates')
    expect(localStorage.getItem('upstream-config-hidden-columns')).toBe(
      JSON.stringify(['balance', 'rates'])
    )
    expect(localStorage.getItem('upstream-config-hidden-columns-version')).toBe('1')
  })

  it('falls back to the default hidden rates column for invalid preferences', async () => {
    localStorage.setItem('upstream-config-hidden-columns', '{invalid')

    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.get('[data-test="columns"]').text()).not.toContain('rates')
    expect(localStorage.getItem('upstream-config-hidden-columns')).toBe(JSON.stringify(['rates']))
  })

  it('closes column settings when clicking outside', async () => {
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('button[title="admin.upstreamConfigs.columnSettings"]').trigger('click')
    expect(wrapper.text()).toContain('admin.upstreamConfigs.columns.rates')

    document.body.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await flushPromises()

    expect(wrapper.text()).not.toContain('admin.upstreamConfigs.columns.rates')
  })

  it('renders every upstream concurrency snapshot state', async () => {
    mockList([
      upstreamConfig({
        id: 1,
        extra: { upstream_concurrency_snapshot: { status: 'current', semantics: 'limited', limit: '9223372036854775807' } }
      }),
      upstreamConfig({
        id: 2,
        extra: { upstream_concurrency_snapshot: { status: 'current', semantics: 'unlimited', raw_value: 0 } }
      }),
      upstreamConfig({
        id: 3,
        provider: 'newapi',
        extra: { upstream_concurrency_snapshot: { status: 'current', semantics: 'provider_defined', raw_value: '0042' } }
      }),
      upstreamConfig({
        id: 4,
        extra: {
          upstream_concurrency_snapshot: {
            status: 'stale',
            semantics: 'limited',
            limit: '6000',
            observed_at: '2026-07-12T01:00:00Z',
            last_checked_at: '2026-07-13T02:00:00Z'
          }
        }
      }),
      upstreamConfig({
        id: 5,
        extra: { upstream_concurrency_snapshot: { status: 'unsupported' } }
      }),
      upstreamConfig({
        id: 6,
        extra: {
          upstream_concurrency_snapshot: {
            status: 'stale',
            last_checked_at: '2026-07-13T03:00:00Z'
          }
        }
      }),
      upstreamConfig({
        id: 7,
        extra: {
          upstream_concurrency_snapshot: {
            status: 'stale',
            semantics: 'unlimited',
            raw_value: '0',
            observed_at: '2026-07-12T01:00:00Z',
            last_checked_at: '2026-07-13T02:00:00Z'
          }
        }
      }),
      upstreamConfig({
        id: 8,
        provider: 'newapi',
        extra: {
          upstream_concurrency_snapshot: {
            status: 'stale',
            semantics: 'provider_defined',
            raw_value: '0042',
            observed_at: '2026-07-12T01:00:00Z',
            last_checked_at: '2026-07-13T02:00:00Z'
          }
        }
      })
    ])
    const wrapper = mountView()
    await flushPromises()

    const cells = wrapper.findAll('[data-test="upstream-concurrency"]')
    expect(cells.map((cell) => cell.text())).toEqual([
      'admin.upstreamConfigs.concurrency.limited:{"count":"9,223,372,036,854,775,807"}',
      'admin.upstreamConfigs.concurrency.unlimited',
      'admin.upstreamConfigs.concurrency.newapiReported:{"count":"0042"}',
      'admin.upstreamConfigs.concurrency.stale:{"value":"6,000"}',
      'admin.upstreamConfigs.concurrency.unsupported',
      'admin.upstreamConfigs.concurrency.initialFailure',
      'admin.upstreamConfigs.concurrency.stale:{"value":"admin.upstreamConfigs.concurrency.unlimited"}',
      'admin.upstreamConfigs.concurrency.stale:{"value":"admin.upstreamConfigs.concurrency.newapiReported:{\\"count\\":\\"0042\\"}"}'
    ])
    expect(cells[0].attributes('title')).toBe('admin.upstreamConfigs.concurrency.headerTitle')
    expect(cells[0].find('div > div').classes()).not.toContain('font-semibold')
    expect(cells[3].attributes('title')).toContain('admin.upstreamConfigs.concurrency.lastObservedAt')
    expect(cells[3].attributes('title')).toContain('admin.upstreamConfigs.concurrency.lastCheckedAt')
    expect(zhUpstreamConfigs.upstreamConfigs.concurrency.unsupported).toBe('--（未提供）')
    expect(zhUpstreamConfigs.upstreamConfigs.concurrency.initialFailure).toBe('--（同步失败）')
    expect(enUpstreamConfigs.upstreamConfigs.concurrency.unsupported).toBe('-- (Not provided)')
    expect(enUpstreamConfigs.upstreamConfigs.concurrency.initialFailure).toBe('-- (Sync failed)')
  })

  it('renders upstream balance from extra and opens sub2api dashboard URL', async () => {
    const openSpy = vi.spyOn(window, 'open').mockImplementation(() => null)
    mockList([upstreamConfig({
      site_url: 'https://upstream.example.com/base?x=1#frag',
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

    await wrapper.get('[data-test="more-upstream-actions"]').trigger('click')
    await wrapper.get('[data-test="menu-dashboard"]').trigger('click')

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
    expect(wrapper.text()).toContain('admin.upstreamConfigs.balance.totalQuota:{"amount":"¥72.00"}')
    expect(wrapper.text()).not.toContain('-4,826,010')
    expect(wrapper.text()).not.toContain('$')
  })

  it('uses the admin CNY override before the newapi provider display rate', async () => {
    mockList([upstreamConfig({
      provider: 'newapi',
      balance_to_cny_rate: 1,
      extra: {
        balance_cny: 0.0791904,
        total_recharged_cny: 73,
        upstream_provider_snapshot: {
          provider: 'newapi',
          balance_amount: 0.010848,
          used_amount: 9.989152,
          total_amount: 10,
          remain_quota_raw: 5424,
          used_quota_raw: 4994576,
          total_quota_raw: 5000000,
          quota_per_unit: 500000,
          currency: 'USD',
          usd_exchange_rate: 7.3
        }
      }
    })])

    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.text()).toContain('¥0.0108')
    expect(wrapper.text()).toContain('admin.upstreamConfigs.balance.totalQuota:{"amount":"¥10.00"}')
    expect(wrapper.text()).not.toContain('¥73.00')
  })

  it('restores the provider rate immediately after the admin override is cleared', async () => {
    mockList([upstreamConfig({
      provider: 'newapi',
      balance_to_cny_rate: null,
      extra: {
        balance_cny: 0.010848,
        currency_to_cny_rate: 1,
        currency_rate_source: 'admin_override',
        upstream_provider_snapshot: {
          provider: 'newapi',
          balance_amount: 0.010848,
          total_amount: 10,
          currency: 'USD',
          usd_exchange_rate: 7.3
        }
      }
    })])

    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.text()).toContain('¥0.0792')
    expect(wrapper.text()).toContain('admin.upstreamConfigs.balance.totalQuota:{"amount":"¥73.00"}')
  })

  it('uses the last trusted provider rate when a USD snapshot omits its rate', async () => {
    mockList([upstreamConfig({
      provider: 'newapi',
      balance_to_cny_rate: null,
      extra: {
        currency_to_cny_rate: 7.3,
        currency_rate_source: 'provider',
        upstream_provider_snapshot: {
          provider: 'newapi',
          balance_amount: 0.010848,
          total_amount: 10,
          currency: 'USD'
        }
      }
    })])

    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.text()).toContain('¥0.0792')
    expect(wrapper.text()).toContain('admin.upstreamConfigs.balance.totalQuota:{"amount":"¥73.00"}')
  })

  it('keeps zero balances and labels derived newapi totals as total quota', async () => {
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
    expect(wrapper.text()).toContain('admin.upstreamConfigs.balance.totalQuota:{"amount":"¥2.50"}')
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

    expect(wrapper.text()).toContain('admin.upstreamConfigs.balance.totalQuota:{"amount":"¥35.00"}')
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
    localStorage.setItem('upstream-config-hidden-columns', JSON.stringify([]))
    localStorage.setItem('upstream-config-hidden-columns-version', '1')
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
    await dialog.get('[data-test="upstream-site-url-input"]').setValue('https://new.example.com')
    await dialog.get('[data-test="upstream-api-url-input"]').setValue('https://api.new.example.com/v1')
    await dialog.get('[data-test="recharge-rate-input"]').setValue('1.2')
    await dialog.get('[data-test="balance-to-cny-rate-input"]').setValue('7.2')
    await wrapper.get('[data-test="proxy-pick"]').trigger('click')
    await dialog.get('[data-test="upstream-email-input"]').setValue('admin@example.com')
    await dialog.get('[data-test="upstream-password-input"]').setValue('secret-password')
    await wrapper.get('form#upstream-config-form').trigger('submit.prevent')
    await flushPromises()

    expect(createMock).toHaveBeenCalledWith(expect.objectContaining({
      name: 'New Upstream',
      site_url: 'https://new.example.com',
      api_url: 'https://api.new.example.com/v1',
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
    await dialog.get('[data-test="upstream-site-url-input"]').setValue('https://www.codexapis.com')
    await dialog.get('[data-test="upstream-username-input"]').setValue('owner@example.com')
    await dialog.get('[data-test="upstream-password-input"]').setValue('secret-password')
    await wrapper.get('form#upstream-config-form').trigger('submit.prevent')
    await flushPromises()

    expect(createMock).toHaveBeenCalledWith(expect.objectContaining({
      name: 'NewAPI Upstream',
      site_url: 'https://www.codexapis.com',
      provider: 'newapi',
      auth_mode: 'user_login',
      credentials: {
        newapi_login_username: 'owner@example.com',
        newapi_login_password: 'secret-password'
      }
    }))
    expect(syncKeysMock).toHaveBeenCalledWith(12)
  })

  it('clears an existing API URL while preserving the site URL', async () => {
    mockList([upstreamConfig({ api_url: 'https://api.upstream.example.com/v1' })])
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('[data-test="edit-upstream"]').trigger('click')
    await flushPromises()

    const dialog = wrapper.get('[data-test="base-dialog"]')
    expect((dialog.get('[data-test="upstream-site-url-input"]').element as HTMLInputElement).value).toBe('https://upstream.example.com')
    expect((dialog.get('[data-test="upstream-api-url-input"]').element as HTMLInputElement).value).toBe('https://api.upstream.example.com/v1')
    await dialog.get('[data-test="upstream-api-url-input"]').setValue('')
    await wrapper.get('form#upstream-config-form').trigger('submit.prevent')
    await flushPromises()

    expect(updateMock).toHaveBeenCalledWith(10, expect.objectContaining({
      site_url: 'https://upstream.example.com',
      api_url: null,
      clear_api_url: true
    }))
  })

  it('fills manual JWT fields from local token helper before saving', async () => {
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('button.btn-primary').trigger('click')
    await flushPromises()

    const dialog = wrapper.get('[data-test="base-dialog"]')
    await dialog.get('[data-test="upstream-name-input"]').setValue('JWT Upstream')
    await dialog.get('[data-test="upstream-site-url-input"]').setValue('https://jwt.example.com')

    await dialog.get('[data-test="upstream-auth-mode-manual_jwt"]').trigger('click')
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
      site_url: 'https://jwt.example.com',
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
    await dialog.get('[data-test="upstream-site-url-input"]').setValue('https://new.example.com')
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

    await wrapper.get('[data-test="more-upstream-actions"]').trigger('click')
    await wrapper.get('[data-test="menu-delete"]').trigger('click')
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

  it('opens the batch sync result dialog by run_id', async () => {
    syncAllKeysMock.mockResolvedValueOnce({
      run_id: 99,
      results: [{ config_id: 10, name: 'Sub2API Main', success: true, key_count: 3, updated_account_count: 1 }]
    })
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('button[title="admin.upstreamConfigs.actions.syncAll"]').trigger('click')
    await flushPromises()

    expect(getSyncRunMock).toHaveBeenCalledWith(99)
    expect(wrapper.get('[data-test="base-dialog"]').attributes('data-width')).toBe('extra-wide')
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

  it('loads a single key rate trend and switches ranges', async () => {
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('[data-test="open-rate-trend"]').trigger('click')
    await flushPromises()

    expect(listKeyRateTrendKeysMock).toHaveBeenCalledWith(10)
    expect(getKeyRateTrendMock).toHaveBeenLastCalledWith(10, 7, '24h')
    expect(wrapper.get('[data-test="key-rate-trend-chart"]').exists()).toBe(true)

    await wrapper.get('[data-test="rate-trend-range-7d"]').trigger('click')
    await flushPromises()
    expect(getKeyRateTrendMock).toHaveBeenLastCalledWith(10, 7, '7d')
  })

  it('submits NewAPI cookie authentication without exposing saved credentials', async () => {
    createMock.mockResolvedValueOnce(upstreamConfig({ id: 12, provider: 'newapi', auth_mode: 'cookie' }))
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('button.btn-primary').trigger('click')
    const dialog = wrapper.get('[data-test="base-dialog"]')
    await dialog.findAll('select')[0].setValue('newapi')
    await dialog.get('[data-test="upstream-auth-mode-cookie"]').trigger('click')
    await dialog.get('[data-test="upstream-name-input"]').setValue('NewAPI Cookie')
    await dialog.get('[data-test="upstream-site-url-input"]').setValue('https://newapi.example.com')
    await dialog.get('[data-test="upstream-newapi-user-id-input"]').setValue('7')
    await dialog.get('[data-test="upstream-newapi-cookie-input"]').setValue('session=secret')
    await dialog.find('form').trigger('submit')
    await flushPromises()

    expect(createMock).toHaveBeenCalledWith(expect.objectContaining({
      provider: 'newapi',
      auth_mode: 'cookie',
      credentials: {
        newapi_cookie: 'session=secret',
        newapi_user_id: '7'
      }
    }))
  })

  it('opens row cost trend with the row id and uses an extra-wide dialog', async () => {
    mockList([upstreamConfig({ id: 42 })])
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('[data-test="row-cost-trend"]').trigger('click')
    await flushPromises()

    expect(getUsageTrendMock).toHaveBeenCalledWith(42, '24h')
    expect(wrapper.get('[data-test="base-dialog"]').attributes('data-width')).toBe('extra-wide')
  })

  it('loads the complete upstream list while opening a row trend', async () => {
    listMock
      .mockReset()
      .mockResolvedValueOnce({
        items: [upstreamConfig({ id: 42, name: 'Visible Row' })],
        total: 1,
        page: 1,
        page_size: 20,
        pages: 1
      })
      .mockResolvedValueOnce({
      items: [upstreamConfig({ id: 42, name: 'Visible Row' }), upstreamConfig({ id: 43, name: 'Another Upstream' })],
      total: 2,
      page: 1,
      page_size: 200,
      pages: 1
      })
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('[data-test="row-cost-trend"]').trigger('click')
    await flushPromises()

    expect(listMock).toHaveBeenLastCalledWith(1, 200, {})
    expect(wrapper.get('[data-test="trend-upstream-select"]').findAll('option')).toHaveLength(2)
    expect(getUsageTrendMock).toHaveBeenCalledWith(42, '24h')
  })

  it('loads events and incidents for the selected upstream', async () => {
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('[data-test="open-upstream-events"]').trigger('click')
    await flushPromises()

    expect(listEventsMock).toHaveBeenCalledWith(10, 50, 0)
    expect(listIncidentsMock).toHaveBeenCalledWith(10, 'open', 50, 0)
    expect(listBalanceHistoryMock).toHaveBeenCalledWith(10, 50, 0)
    expect(wrapper.get('[data-test="base-dialog"]').attributes('data-width')).toBe('wide')
  })

  it('loads every operation config page and deduplicates ids', async () => {
    const firstPage = Array.from({ length: 200 }, (_, index) => upstreamConfig({ id: index + 1, name: `Upstream ${index + 1}` }))
    listMock
      .mockResolvedValueOnce({ items: [upstreamConfig()], total: 1, page: 1, page_size: 20, pages: 1 })
      .mockResolvedValueOnce({ items: firstPage, total: 201, page: 1, page_size: 200, pages: 2 })
      .mockResolvedValueOnce({ items: [upstreamConfig({ id: 200 }), upstreamConfig({ id: 201 })], total: 201, page: 2, page_size: 200, pages: 2 })
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('[data-test="open-cost-trend"]').trigger('click')
    await flushPromises()

    expect(listMock).toHaveBeenNthCalledWith(2, 1, 200, {})
    expect(listMock).toHaveBeenNthCalledWith(3, 2, 200, {})
    const options = wrapper.get('[data-test="trend-upstream-select"]').findAll('option')
    expect(options).toHaveLength(201)
  })

  it('renders newapi history as balance plus total used', async () => {
    mockList([upstreamConfig({ provider: 'newapi' })])
    listBalanceHistoryMock.mockResolvedValueOnce({
      items: [{
        id: 1,
        config_id: 10,
        provider: 'newapi',
        balance_cny: 0.010848,
        used_cny: 9.989152,
        total_recharged_cny: 10,
        currency_source: 'USD',
        currency_rate_source: 'admin_override',
        observed_at: '2026-07-11T06:30:40Z'
      }],
      total: 1
    })
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('[data-test="open-upstream-events"]').trigger('click')
    await flushPromises()

    expect(wrapper.text()).toContain('admin.upstreamConfigs.balance.totalUsed:{"amount":"¥9.9892"}')
    expect(wrapper.text()).not.toContain('admin.upstreamConfigs.balance.totalRecharged:{"amount":"¥10.00"}')
  })

  it('updates all upstream settings including the compliance declaration', async () => {
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('[data-test="open-upstream-settings"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[data-test="base-dialog"]').attributes('data-width')).toBe('normal')
    expect((wrapper.get('[data-test="sub2api-not-in-cn-confirmed"]').element as HTMLInputElement).checked).toBe(false)
    await wrapper.get('[data-test="low-balance-threshold-input"]').setValue('20')
    await wrapper.get('[data-test="sub2api-not-in-cn-confirmed"]').setValue(true)
    await wrapper.get('[data-test="upstream-settings-form"]').trigger('submit.prevent')
    await flushPromises()

    expect(updateSettingsMock).toHaveBeenCalledWith({
      balance_low_threshold_cny: 20,
      sub2api_not_in_cn_confirmed: true
    })
    expect(showSuccessMock).toHaveBeenCalledWith('admin.upstreamConfigs.messages.settingsSaved')
  })

  it('explicitly saves a false compliance declaration', async () => {
    getSettingsMock.mockResolvedValue({ balance_low_threshold_cny: 10, sub2api_not_in_cn_confirmed: true })
    updateSettingsMock.mockResolvedValueOnce({ balance_low_threshold_cny: 10, sub2api_not_in_cn_confirmed: false })
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('[data-test="open-upstream-settings"]').trigger('click')
    await flushPromises()
    const checkbox = wrapper.get('[data-test="sub2api-not-in-cn-confirmed"]')
    expect((checkbox.element as HTMLInputElement).checked).toBe(true)
    await checkbox.setValue(false)
    await wrapper.get('[data-test="upstream-settings-form"]').trigger('submit.prevent')
    await flushPromises()

    expect(updateSettingsMock).toHaveBeenCalledWith({
      balance_low_threshold_cny: 10,
      sub2api_not_in_cn_confirmed: false
    })
  })

  it('does not expose or submit stale settings when reloading fails', async () => {
    getSettingsMock
      .mockResolvedValueOnce({ balance_low_threshold_cny: 10, sub2api_not_in_cn_confirmed: true })
      .mockRejectedValueOnce(new Error('settings unavailable'))
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('[data-test="open-upstream-settings"]').trigger('click')
    await flushPromises()

    expect(wrapper.find('[data-test="upstream-settings-form"]').exists()).toBe(false)
    expect(wrapper.get('[data-test="upstream-settings-unavailable"]').exists()).toBe(true)
    expect(updateSettingsMock).not.toHaveBeenCalled()
  })

  it('prevents settings dialog close while saving', async () => {
    const pending = deferred<{ balance_low_threshold_cny: number; sub2api_not_in_cn_confirmed: boolean }>()
    updateSettingsMock.mockReturnValueOnce(pending.promise)
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('[data-test="open-upstream-settings"]').trigger('click')
    await flushPromises()
    await wrapper.get('[data-test="upstream-settings-form"]').trigger('submit.prevent')
    await wrapper.get('[data-test="dialog-close"]').trigger('click')

    expect(wrapper.get('[data-test="base-dialog"]').exists()).toBe(true)
    expect(wrapper.get('[data-test="base-dialog"]').attributes('data-close-on-escape')).toBe('false')
    expect(wrapper.get('[data-test="base-dialog"]').attributes('data-show-close-button')).toBe('false')

    pending.resolve({ balance_low_threshold_cny: 10, sub2api_not_in_cn_confirmed: false })
    await flushPromises()
  })
})
