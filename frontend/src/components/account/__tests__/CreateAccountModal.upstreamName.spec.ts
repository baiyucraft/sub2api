import { beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, ref } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'

const {
  createAccountMock,
  showErrorMock,
  upstreamConfigsListMock,
  upstreamConfigKeysListMock
} = vi.hoisted(() => ({
  createAccountMock: vi.fn(),
  showErrorMock: vi.fn(),
  upstreamConfigsListMock: vi.fn(),
  upstreamConfigKeysListMock: vi.fn()
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: showErrorMock,
    showSuccess: vi.fn(),
    showInfo: vi.fn()
  })
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ isSimpleMode: true })
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      create: createAccountMock,
      checkMixedChannelRisk: vi.fn().mockResolvedValue({ has_risk: false })
    },
    settings: {
      getWebSearchEmulationConfig: vi.fn().mockResolvedValue({ enabled: false, providers: [] })
    },
    tlsFingerprintProfiles: {
      list: vi.fn().mockResolvedValue([])
    }
  }
}))

vi.mock('@/api/admin/upstreamConfigs', () => ({
  default: {
    list: upstreamConfigsListMock,
    listKeys: upstreamConfigKeysListMock
  }
}))

vi.mock('@/composables/useQuotaNotifyState', () => ({
  useQuotaNotifyState: () => ({
    globalEnabled: ref(false),
    state: {
      daily: { enabled: false, threshold: null, thresholdType: 'fixed' },
      weekly: { enabled: false, threshold: null, thresholdType: 'fixed' },
      total: { enabled: false, threshold: null, thresholdType: 'fixed' }
    },
    loadGlobalState: vi.fn(),
    writeToExtra: vi.fn()
  })
}))

vi.mock('@/composables/useModelWhitelist', async () => {
  const actual = await vi.importActual<typeof import('@/composables/useModelWhitelist')>(
    '@/composables/useModelWhitelist'
  )
  return {
    ...actual,
    fetchAntigravityDefaultMappings: vi.fn().mockResolvedValue([])
  }
})

vi.mock('@/composables/useAccountOAuth', () => ({
  useAccountOAuth: () => ({
    authUrl: ref(''),
    error: ref(''),
    loading: ref(false),
    sessionId: ref(''),
    generateAuthUrl: vi.fn(),
    completeAuth: vi.fn(),
    parseSessionKeys: vi.fn(),
    buildExtraInfo: vi.fn(),
    resetState: vi.fn()
  })
}))

vi.mock('@/composables/useOpenAIOAuth', () => ({
  useOpenAIOAuth: () => ({
    authUrl: ref(''),
    error: ref(''),
    loading: ref(false),
    sessionId: ref(''),
    generateAuthUrl: vi.fn(),
    resetState: vi.fn()
  })
}))

vi.mock('@/composables/useGeminiOAuth', () => ({
  useGeminiOAuth: () => ({
    authUrl: ref(''),
    error: ref(''),
    loading: ref(false),
    sessionId: ref(''),
    state: ref(''),
    getCapabilities: vi.fn().mockResolvedValue({}),
    generateAuthUrl: vi.fn(),
    exchangeAuthCode: vi.fn(),
    buildCredentials: vi.fn(),
    buildExtraInfo: vi.fn(),
    resetState: vi.fn()
  })
}))

vi.mock('@/composables/useAntigravityOAuth', () => ({
  useAntigravityOAuth: () => ({
    authUrl: ref(''),
    error: ref(''),
    loading: ref(false),
    sessionId: ref(''),
    state: ref(''),
    generateAuthUrl: vi.fn(),
    exchangeAuthCode: vi.fn(),
    validateRefreshToken: vi.fn(),
    buildCredentials: vi.fn(),
    resetState: vi.fn()
  })
}))

vi.mock('@/composables/useGrokOAuth', () => ({
  useGrokOAuth: () => ({
    authUrl: ref(''),
    error: ref(''),
    loading: ref(false),
    sessionId: ref(''),
    state: ref(''),
    generateAuthUrl: vi.fn(),
    exchangeAuthCode: vi.fn(),
    validateRefreshToken: vi.fn(),
    buildCredentials: vi.fn(),
    buildExtraInfo: vi.fn(),
    resetState: vi.fn()
  })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key })
  }
})

import CreateAccountModal from '../CreateAccountModal.vue'

const BaseDialogStub = defineComponent({
  name: 'BaseDialog',
  props: { show: Boolean },
  template: '<div v-if="show"><slot /><slot name="footer" /></div>'
})

const UpstreamKeySelectorStub = defineComponent({
  name: 'UpstreamKeySelector',
  props: {
    modelValue: { type: Number, default: null },
    platform: String,
    keys: { type: Array, default: () => [] },
    disabled: Boolean
  },
  emits: ['update:modelValue'],
  template: `
    <button
      v-if="keys.length"
      type="button"
      data-testid="select-first-upstream-key"
      :disabled="disabled"
      @click="$emit('update:modelValue', keys[0].id)"
    >
      select key
    </button>
  `
})

const activeConfig = (id: number, name: string) => ({
  id,
  name,
  provider: 'sub2api',
  site_url: `https://upstream-${id}.example.com`,
  auth_mode: 'user_login',
  status: 'active',
  created_at: '2026-07-10T00:00:00Z',
  updated_at: '2026-07-10T00:00:00Z'
})

const upstreamKey = (
  id: number,
  configID: number,
  name: string,
  status: 'active' | 'stale' = 'active',
  platform: string | null = 'anthropic'
) => ({
  id,
  upstream_config_id: configID,
  name,
  platform,
  status,
  created_at: '2026-07-10T00:00:00Z',
  updated_at: '2026-07-10T00:00:00Z'
})

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((resolvePromise) => {
    resolve = resolvePromise
  })
  return { promise, resolve }
}

function mountModal() {
  return mount(CreateAccountModal, {
    props: { show: false, proxies: [], groups: [] },
    global: {
      stubs: {
        BaseDialog: BaseDialogStub,
        ConfirmDialog: true,
        Select: true,
        PlatformIcon: true,
        Icon: true,
        ProxySelector: true,
        ProxyAdBanner: true,
        GroupSelector: true,
        UpstreamKeySelector: UpstreamKeySelectorStub,
        ModelWhitelistSelector: true,
        QuotaLimitCard: true,
        OAuthAuthorizationFlow: true
      }
    }
  })
}

async function mountOpenedModal() {
  const wrapper = mountModal()
  await wrapper.setProps({ show: true })
  await flushPromises()
  await wrapper.get('[data-testid="upstream-account-category"]').trigger('click')
  return wrapper
}

describe('CreateAccountModal upstream account name', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    createAccountMock.mockResolvedValue({})
    upstreamConfigsListMock.mockResolvedValue({
      items: [activeConfig(1, '钧澈')],
      total: 1,
      page: 1,
      page_size: 200
    })
    upstreamConfigKeysListMock.mockResolvedValue([upstreamKey(10, 1, '特惠')])
  })

  it('previews the generated name and submits an empty name for backend generation', async () => {
    const wrapper = await mountOpenedModal()

    expect(wrapper.find('[data-testid="account-name-input"]').exists()).toBe(false)
    await wrapper.get('[data-testid="upstream-config-select"]').setValue('1')
    await flushPromises()
    await wrapper.get('[data-testid="upstream-key-selector"]').trigger('click')

    expect(wrapper.get('[data-testid="upstream-account-name-preview"]').attributes('value')).toBe('钧澈-特惠')

    await wrapper.get('#create-account-form').trigger('submit')
    await flushPromises()

    expect(createAccountMock).toHaveBeenCalledWith(expect.objectContaining({
      name: '',
      type: 'apikey',
      upstream_config_id: 1,
      upstream_key_id: 10,
      credentials: expect.not.objectContaining({
        pool_mode: expect.anything()
      })
    }))
  })

  it('submits pool mode settings for an upstream-bound account', async () => {
    const wrapper = await mountOpenedModal()

    await wrapper.get('[data-testid="pool-mode-toggle"]').trigger('click')
    await wrapper.get('[data-testid="pool-mode-retry-count"]').setValue('5')
    await wrapper.get('[data-testid="pool-mode-retry-status-codes"]').setValue('429, 503')
    await wrapper.get('[data-testid="upstream-config-select"]').setValue('1')
    await flushPromises()
    await wrapper.get('[data-testid="upstream-key-selector"]').trigger('click')
    await wrapper.get('#create-account-form').trigger('submit')
    await flushPromises()

    expect(createAccountMock).toHaveBeenCalledWith(expect.objectContaining({
      type: 'apikey',
      upstream_config_id: 1,
      upstream_key_id: 10,
      credentials: expect.objectContaining({
        pool_mode: true,
        pool_mode_retry_count: 5,
        pool_mode_retry_status_codes: [429, 503]
      })
    }))
  })

  it('rejects a key without a real upstream name', async () => {
    upstreamConfigKeysListMock.mockResolvedValue([upstreamKey(10, 1, '\u3000\u00a0')])
    const wrapper = await mountOpenedModal()

    await wrapper.get('[data-testid="upstream-config-select"]').setValue('1')
    await flushPromises()
    await wrapper.get('[data-testid="upstream-key-selector"]').trigger('click')
    await wrapper.get('#create-account-form').trigger('submit')
    await flushPromises()

    expect(createAccountMock).not.toHaveBeenCalled()
    expect(showErrorMock).toHaveBeenCalledWith('上游 Key 缺少名称，请先在上游命名并同步')
  })

  it('only allows selecting active upstream keys', async () => {
    upstreamConfigKeysListMock.mockResolvedValue([
      upstreamKey(10, 1, '已失效 Key', 'stale'),
      upstreamKey(11, 1, '可用 Key')
    ])
    const wrapper = await mountOpenedModal()

    await wrapper.get('[data-testid="upstream-config-select"]').setValue('1')
    await flushPromises()

    const selector = wrapper.getComponent(UpstreamKeySelectorStub)
    expect(selector.props('keys')).toEqual([
      expect.objectContaining({ id: 11, status: 'active' })
    ])

    await wrapper.get('[data-testid="upstream-key-selector"]').trigger('click')
    await wrapper.get('#create-account-form').trigger('submit')
    await flushPromises()

    expect(createAccountMock).toHaveBeenCalledWith(expect.objectContaining({
      upstream_key_id: 11
    }))
  })

  it('only offers active keys assigned to the current platform', async () => {
    upstreamConfigKeysListMock.mockResolvedValue([
      upstreamKey(10, 1, '当前平台 Key'),
      upstreamKey(11, 1, '其他平台 Key', 'active', 'openai'),
      upstreamKey(12, 1, '未知平台 Key', 'active', null),
      upstreamKey(13, 1, '当前平台失效 Key', 'stale')
    ])
    const wrapper = await mountOpenedModal()

    await wrapper.get('[data-testid="upstream-config-select"]').setValue('1')
    await flushPromises()

    const selector = wrapper.getComponent(UpstreamKeySelectorStub)
    expect(selector.props('platform')).toBe('anthropic')
    expect(selector.props('keys')).toEqual([
      expect.objectContaining({ id: 10, platform: 'anthropic', status: 'active' })
    ])

    await wrapper.get('[data-testid="upstream-key-selector"]').trigger('click')
    await wrapper.get('#create-account-form').trigger('submit')
    await flushPromises()

    expect(createAccountMock).toHaveBeenCalledWith(expect.objectContaining({
      platform: 'anthropic',
      upstream_key_id: 10
    }))
  })

  it('blocks submission when only stale upstream keys exist', async () => {
    upstreamConfigKeysListMock.mockResolvedValue([
      upstreamKey(10, 1, '已失效 Key', 'stale')
    ])
    const wrapper = await mountOpenedModal()

    await wrapper.get('[data-testid="upstream-config-select"]').setValue('1')
    await flushPromises()

    expect(wrapper.getComponent(UpstreamKeySelectorStub).props('keys')).toEqual([])
    expect(wrapper.find('[data-testid="select-first-upstream-key"]').exists()).toBe(false)

    await wrapper.get('#create-account-form').trigger('submit')
    await flushPromises()

    expect(createAccountMock).not.toHaveBeenCalled()
    expect(showErrorMock).toHaveBeenCalledWith('请选择上游 Key')
  })

  it('ignores a stale key response after switching configs', async () => {
    const first = deferred<ReturnType<typeof upstreamKey>[]>()
    const second = deferred<ReturnType<typeof upstreamKey>[]>()
    upstreamConfigsListMock.mockResolvedValue({
      items: [activeConfig(1, '旧渠道'), activeConfig(2, '新渠道')],
      total: 2,
      page: 1,
      page_size: 200
    })
    upstreamConfigKeysListMock.mockImplementation((configID: number) =>
      configID === 1 ? first.promise : second.promise
    )
    const wrapper = await mountOpenedModal()

    const select = wrapper.get('[data-testid="upstream-config-select"]')
    await select.setValue('1')
    await select.setValue('2')
    second.resolve([upstreamKey(20, 2, '新Key')])
    await flushPromises()
    await wrapper.get('[data-testid="upstream-key-selector"]').trigger('click')
    expect(wrapper.get('[data-testid="upstream-account-name-preview"]').attributes('value')).toBe('新渠道-新Key')

    first.resolve([upstreamKey(10, 1, '旧Key')])
    await flushPromises()

    expect(wrapper.get('[data-testid="upstream-account-name-preview"]').attributes('value')).toBe('新渠道-新Key')
  })

  it('blocks submission while upstream keys are loading', async () => {
    const pending = deferred<ReturnType<typeof upstreamKey>[]>()
    upstreamConfigKeysListMock.mockReturnValue(pending.promise)
    const wrapper = await mountOpenedModal()

    await wrapper.get('[data-testid="upstream-config-select"]').setValue('1')
    expect(wrapper.get('button[form="create-account-form"]').attributes('disabled')).toBeDefined()

    await wrapper.get('#create-account-form').trigger('submit')
    expect(createAccountMock).not.toHaveBeenCalled()
    expect(showErrorMock).toHaveBeenCalledWith('上游配置或 Key 正在加载，请稍候')

    pending.resolve([upstreamKey(10, 1, '特惠')])
    await flushPromises()
  })

  it('clears a selected key when the platform no longer matches', async () => {
    const wrapper = await mountOpenedModal()

    await wrapper.get('[data-testid="upstream-config-select"]').setValue('1')
    await flushPromises()
    await wrapper.get('[data-testid="upstream-key-selector"]').trigger('click')
    expect(wrapper.get('[data-testid="upstream-account-name-preview"]').attributes('value')).toBe('钧澈-特惠')

    await wrapper.get('[data-testid="platform-openai"]').trigger('click')
    await flushPromises()

    expect(wrapper.get('[data-testid="upstream-account-name-preview"]').attributes('value')).toBe('')
  })

  it('ignores a stale config response after the dialog is reopened', async () => {
    const first = deferred<{
      items: ReturnType<typeof activeConfig>[]
      total: number
      page: number
      page_size: number
    }>()
    const second = deferred<{
      items: ReturnType<typeof activeConfig>[]
      total: number
      page: number
      page_size: number
    }>()
    upstreamConfigsListMock
      .mockReturnValueOnce(first.promise)
      .mockReturnValueOnce(second.promise)
    const wrapper = mountModal()

    await wrapper.setProps({ show: true })
    await wrapper.setProps({ show: false })
    await wrapper.setProps({ show: true })
    second.resolve({ items: [activeConfig(2, '新渠道')], total: 1, page: 1, page_size: 200 })
    await flushPromises()
    await wrapper.get('[data-testid="upstream-account-category"]').trigger('click')

    first.resolve({ items: [activeConfig(1, '旧渠道')], total: 1, page: 1, page_size: 200 })
    await flushPromises()

    const options = wrapper.findAll('[data-testid="upstream-config-select"] option')
    expect(options.some((option) => option.text().includes('新渠道'))).toBe(true)
    expect(options.some((option) => option.text().includes('旧渠道'))).toBe(false)
  })
})
