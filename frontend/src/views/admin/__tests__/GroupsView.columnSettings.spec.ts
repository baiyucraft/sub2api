import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import type { AdminGroup } from '@/types'
import GroupsView from '../GroupsView.vue'

const {
  listGroups,
  getAllGroups,
  getModelsListCandidates,
  getUsageSummary,
  getCapacitySummary,
  getBatchQualityStats,
  listAccounts,
  showError,
  showSuccess,
  isCurrentStep,
  nextStep,
} = vi.hoisted(() => ({
  listGroups: vi.fn(),
  getAllGroups: vi.fn(),
  getModelsListCandidates: vi.fn(),
  getUsageSummary: vi.fn(),
  getCapacitySummary: vi.fn(),
  getBatchQualityStats: vi.fn(),
  listAccounts: vi.fn(),
  showError: vi.fn(),
  showSuccess: vi.fn(),
  isCurrentStep: vi.fn(),
  nextStep: vi.fn(),
}))

const messages: Record<string, string> = {
  'admin.groups.columnSettings': 'Column Settings',
  'admin.groups.columns.name': 'Name',
  'admin.groups.columns.id': 'ID',
  'admin.groups.columns.platform': 'Platform',
  'admin.groups.columns.billingType': 'Billing Type',
  'admin.groups.columns.rateMultiplier': 'Rate Multiplier',
  'admin.groups.columns.type': 'Type',
  'admin.groups.columns.accounts': 'Accounts',
  'admin.groups.columns.capacity': 'Capacity',
  'admin.groups.columns.usage': 'Usage',
  'admin.groups.columns.realtimeQualityStats': '1H Quality',
  'admin.groups.columns.qualityStats': '24H Quality',
  'admin.groups.columns.status': 'Status',
  'admin.groups.columns.actions': 'Actions',
}

vi.mock('@/api/admin', () => ({
  adminAPI: {
    groups: {
      list: listGroups,
      getAll: getAllGroups,
      getModelsListCandidates,
      getUsageSummary,
      getCapacitySummary,
      getBatchQualityStats,
      create: vi.fn(),
      update: vi.fn(),
      delete: vi.fn(),
      updateSortOrder: vi.fn(),
    },
    accounts: {
      list: listAccounts,
    },
  },
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError,
    showSuccess,
  }),
}))

vi.mock('@/stores/onboarding', () => ({
  useOnboardingStore: () => ({
    isCurrentStep,
    nextStep,
  }),
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => messages[key] ?? key,
    }),
  }
})

const createGroup = (overrides: Partial<AdminGroup> = {}): AdminGroup => ({
  id: 1,
  name: 'Core Anthropic',
  description: null,
  platform: 'anthropic',
  rate_multiplier: 1,
  rpm_limit: 0,
  is_exclusive: false,
  status: 'active',
  subscription_type: 'standard',
  daily_limit_usd: null,
  weekly_limit_usd: null,
  monthly_limit_usd: null,
  allow_image_generation: false,
  image_rate_independent: false,
  image_rate_multiplier: 1,
  image_price_1k: null,
  image_price_2k: null,
  image_price_4k: null,
  claude_code_only: false,
  fallback_group_id: null,
  fallback_group_id_on_invalid_request: null,
  allow_messages_dispatch: false,
  default_mapped_model: '',
  messages_dispatch_model_config: undefined,
  require_oauth_only: false,
  require_privacy_set: false,
  created_at: '2026-07-01T00:00:00Z',
  updated_at: '2026-07-01T00:00:00Z',
  model_routing: null,
  model_routing_enabled: false,
  mcp_xml_inject: true,
  supported_model_scopes: [],
  account_count: 3,
  active_account_count: 2,
  rate_limited_account_count: 1,
  models_list_config: undefined,
  sort_order: 10,
  ...overrides,
})

const AppLayoutStub = {
  template: '<div><slot /></div>',
}

const TablePageLayoutStub = {
  template: `
    <div>
      <slot name="filters" />
      <slot name="table" />
      <slot name="pagination" />
    </div>
  `,
}

const DataTableStub = {
  props: ['columns', 'data'],
  emits: ['sort'],
  template: `
    <div>
      <div data-test="columns">{{ columns.map((col) => col.key).join(',') }}</div>
      <div data-test="rows">{{ data.map((row) => row.name).join(',') }}</div>
    </div>
  `,
}

const SelectStub = {
  props: ['modelValue', 'options', 'placeholder'],
  emits: ['update:modelValue', 'change'],
  template: `
    <select
      :value="modelValue"
      @change="$emit('update:modelValue', $event.target.value); $emit('change')"
    >
      <option v-for="option in options" :key="String(option.value)" :value="option.value">
        {{ option.label }}
      </option>
    </select>
  `,
}

const BaseDialogStub = {
  props: ['show'],
  template: '<div v-if="show"><slot /><slot name="footer" /></div>',
}

const IconStub = {
  props: ['name'],
  template: '<span data-test="icon">{{ name }}</span>',
}

const mountView = async () => {
  const wrapper = mount(GroupsView, {
    global: {
      stubs: {
        AppLayout: AppLayoutStub,
        TablePageLayout: TablePageLayoutStub,
        DataTable: DataTableStub,
        Pagination: true,
        BaseDialog: BaseDialogStub,
        ConfirmDialog: true,
        EmptyState: true,
        Select: SelectStub,
        PlatformIcon: true,
        Icon: IconStub,
        GroupCapacityBadge: true,
        GroupRateMultipliersModal: true,
        GroupRPMOverridesModal: true,
        VueDraggable: { template: '<div><slot /></div>' },
      },
    },
  })
  await flushPromises()
  return wrapper
}

const columnKeys = (wrapper: ReturnType<typeof mount>) =>
  wrapper.get('[data-test="columns"]').text().split(',').filter(Boolean)

const openColumnSettings = async (wrapper: ReturnType<typeof mount>) => {
  await wrapper.get('button[title="Column Settings"]').trigger('click')
}

const clickColumnToggle = async (wrapper: ReturnType<typeof mount>, label: string) => {
  const button = wrapper
    .findAll('button')
    .find((item) => item.text().includes(label))
  expect(button, `column toggle ${label}`).toBeTruthy()
  await button!.trigger('click')
  await flushPromises()
}

describe('admin GroupsView column settings', () => {
  beforeEach(() => {
    localStorage.clear()

    listGroups.mockReset()
    getAllGroups.mockReset()
    getModelsListCandidates.mockReset()
    getUsageSummary.mockReset()
    getCapacitySummary.mockReset()
    getBatchQualityStats.mockReset()
    listAccounts.mockReset()
    showError.mockReset()
    showSuccess.mockReset()
    isCurrentStep.mockReset()
    nextStep.mockReset()

    listGroups.mockResolvedValue({
      items: [createGroup()],
      total: 1,
      page: 1,
      page_size: 20,
      pages: 1,
    })
    getAllGroups.mockResolvedValue([])
    getModelsListCandidates.mockResolvedValue([])
    getUsageSummary.mockResolvedValue([])
    getCapacitySummary.mockResolvedValue([])
    getBatchQualityStats.mockResolvedValue({ notModified: false, etag: '"groups-v1"', data: { stats: {} } })
    listAccounts.mockResolvedValue({ items: [], total: 0, page: 1, page_size: 20, pages: 0 })
    isCurrentStep.mockReturnValue(false)
  })

  afterEach(() => {
    localStorage.clear()
  })

  it('hides the id column by default while keeping other group columns visible', async () => {
    const wrapper = await mountView()

    expect(columnKeys(wrapper)).toEqual([
      'name',
      'platform',
      'billing_type',
      'rate_multiplier',
      'is_exclusive',
      'account_count',
      'capacity',
      'usage',
      'status',
      'actions',
    ])
    expect(JSON.parse(localStorage.getItem('group-hidden-columns')!)).toEqual([
      'id',
      'quality_stats_1h',
      'quality_stats',
    ])
    expect(localStorage.getItem('group-column-settings-version')).toBe('3')
  })

  it('applies saved hidden columns on mount and ignores unknown keys', async () => {
    localStorage.setItem(
      'group-hidden-columns',
      JSON.stringify([
        'usage',
        'capacity',
        'quality_stats_1h',
        'quality_stats',
        'removed_column',
        'name',
        'actions',
      ]),
    )
    localStorage.setItem('group-column-settings-version', '3')

    const wrapper = await mountView()

    expect(columnKeys(wrapper)).toEqual([
      'name',
      'id',
      'platform',
      'billing_type',
      'rate_multiplier',
      'is_exclusive',
      'account_count',
      'status',
      'actions',
    ])
  })

  it('auto-hides id for existing saved column prefs after version bump', async () => {
    localStorage.setItem('group-hidden-columns', JSON.stringify(['usage']))
    // No version key -> migrate through v2/v3 and hide id plus both quality columns.

    const wrapper = await mountView()

    expect(columnKeys(wrapper)).toEqual([
      'name',
      'platform',
      'billing_type',
      'rate_multiplier',
      'is_exclusive',
      'account_count',
      'capacity',
      'status',
      'actions',
    ])
    expect(JSON.parse(localStorage.getItem('group-hidden-columns')!)).toEqual(
      expect.arrayContaining(['usage', 'id', 'quality_stats_1h', 'quality_stats']),
    )
    expect(localStorage.getItem('group-column-settings-version')).toBe('3')
  })

  it('toggles a column and persists hidden column keys', async () => {
    const wrapper = await mountView()

    await openColumnSettings(wrapper)
    await clickColumnToggle(wrapper, 'Usage')

    expect(columnKeys(wrapper)).toEqual([
      'name',
      'platform',
      'billing_type',
      'rate_multiplier',
      'is_exclusive',
      'account_count',
      'capacity',
      'status',
      'actions',
    ])
    expect(JSON.parse(localStorage.getItem('group-hidden-columns')!)).toEqual(
      expect.arrayContaining(['id', 'usage']),
    )
  })

  it('can show the id column from column settings', async () => {
    const wrapper = await mountView()

    await openColumnSettings(wrapper)
    await clickColumnToggle(wrapper, 'ID')

    expect(columnKeys(wrapper)).toEqual([
      'name',
      'id',
      'platform',
      'billing_type',
      'rate_multiplier',
      'is_exclusive',
      'account_count',
      'capacity',
      'usage',
      'status',
      'actions',
    ])
    expect(JSON.parse(localStorage.getItem('group-hidden-columns')!)).toEqual([
      'quality_stats_1h',
      'quality_stats',
    ])
  })

  it('skips quality fetches until a quality column is shown and reuses the current snapshot', async () => {
    localStorage.setItem(
      'group-hidden-columns',
      JSON.stringify(['billing_type', 'usage', 'capacity', 'quality_stats_1h', 'quality_stats']),
    )
    localStorage.setItem('group-column-settings-version', '3')

    const wrapper = await mountView()

    expect(getUsageSummary).not.toHaveBeenCalled()
    expect(getCapacitySummary).not.toHaveBeenCalled()
    expect(getBatchQualityStats).not.toHaveBeenCalled()

    await openColumnSettings(wrapper)
    await clickColumnToggle(wrapper, 'Usage')
    expect(getUsageSummary).toHaveBeenCalledTimes(1)
    expect(getCapacitySummary).not.toHaveBeenCalled()
    expect(getBatchQualityStats).not.toHaveBeenCalled()

    await clickColumnToggle(wrapper, 'Capacity')
    expect(getUsageSummary).toHaveBeenCalledTimes(1)
    expect(getCapacitySummary).toHaveBeenCalledTimes(1)
    expect(getBatchQualityStats).not.toHaveBeenCalled()

    await clickColumnToggle(wrapper, '1H Quality')
    expect(getBatchQualityStats).toHaveBeenCalledWith(
      [1],
      expect.objectContaining({ etag: null, signal: expect.any(AbortSignal) }),
    )

    await clickColumnToggle(wrapper, '1H Quality')
    getBatchQualityStats.mockResolvedValueOnce({ notModified: true, etag: null, data: null })
    await clickColumnToggle(wrapper, '1H Quality')
    expect(getBatchQualityStats).toHaveBeenNthCalledWith(
      2,
      [1],
      expect.objectContaining({ etag: '"groups-v1"', signal: expect.any(AbortSignal) }),
    )

    await clickColumnToggle(wrapper, '24H Quality')
    expect(getBatchQualityStats).toHaveBeenCalledTimes(2)
  })
})
