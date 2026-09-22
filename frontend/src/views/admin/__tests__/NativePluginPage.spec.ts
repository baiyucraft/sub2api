import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const state = vi.hoisted(() => ({
  route: { params: { pluginKey: 'native.test' } },
  router: { push: vi.fn() },
  leaveGuard: undefined as undefined | (() => boolean | Promise<boolean>),
  plugin: {
    id: 7,
    plugin_key: 'native.test',
    name: 'Native Test',
    version: '1.0.0',
    description: 'test',
    author: 'test',
    manifest: {
      schema_version: 2,
      id: 'com.example.native-test',
      name: 'Native Test',
      version: '1.0.0',
      requires: { sub2api: '>=0.1.0', plugin_protocol: 1, transport_api: 1, admin_ui: 1 },
      capabilities: [],
      config_secrets: [],
      ui: { type: 'native', definition: 'ui/admin.json' },
    },
    binary_sha256: 'a'.repeat(64),
    signature_status: 'trusted' as const,
    state: 'disabled' as const,
    last_error: '',
    installed_at: '2026-09-22T00:00:00Z',
    updated_at: '2026-09-22T00:00:00Z',
    bindings: [],
    compatibility: {
      compatible: true,
      tested: true,
      status: 'compatible' as const,
      message: '',
      current_sub2api_version: '0.2.7-baiyu',
      required_sub2api_version: '>=0.1.0',
      recommended_sub2api_version: '',
      plugin_protocol: 1,
      transport_api: 1,
      ui_bridge: 0,
      admin_ui: 1,
    },
    runtime_healthy: false,
    runtime_message: 'disabled',
  },
  definition: {
    schema_version: 1,
    title: 'Native test',
    description: '',
    poll_interval_seconds: 2,
    layout: [
      { type: 'text_input', id: 'name', title: 'Name', bind: '/config/name', write: '/config/name' },
      { type: 'button', id: 'refresh', title: 'Refresh', action: 'config.refresh' },
      { type: 'button', id: 'harvest', title: 'Harvest', action: 'harvest', confirm: true },
    ],
  },
  config: { name: 'old', _host_secrets: {} },
}))

const {
  byKey, adminUI, getConfig, resources, status, saveConfig, action, saveSecrets, stepUpRun,
} = vi.hoisted(() => ({
  byKey: vi.fn(), adminUI: vi.fn(), getConfig: vi.fn(), resources: vi.fn(), status: vi.fn(),
  saveConfig: vi.fn(), action: vi.fn(), saveSecrets: vi.fn(),
  stepUpRun: vi.fn((fn: () => Promise<unknown>) => fn()),
}))

vi.mock('@/api/admin', () => ({
  adminAPI: { plugins: { byKey, adminUI, getConfig, resources, status, saveConfig, action, saveSecrets } },
}))
vi.mock('@/stores', () => ({ useAppStore: () => ({ showError: vi.fn(), showSuccess: vi.fn() }) }))
vi.mock('@/composables/useStepUp', () => ({
  useStepUp: () => ({ run: stepUpRun }),
  isStepUpBlocked: () => false,
  isStepUpCancelled: () => false,
  stepUpBlockReason: () => '',
}))
vi.mock('vue-i18n', async importOriginal => ({
  ...(await importOriginal<typeof import('vue-i18n')>()),
  useI18n: () => ({ t: (key: string) => key }),
}))
vi.mock('vue-router', () => ({
  useRoute: () => state.route,
  useRouter: () => state.router,
  onBeforeRouteLeave: (guard: () => boolean | Promise<boolean>) => { state.leaveGuard = guard },
  onBeforeRouteUpdate: (guard: () => boolean | Promise<boolean>) => { state.leaveGuard = guard },
}))

import NativePluginPage from '../NativePluginPage.vue'

const wrappers: VueWrapper[] = []

function mountPage(): VueWrapper {
  const wrapper = mount(NativePluginPage, {
    attachTo: document.body,
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        BaseDialog: { template: '<div><slot /></div>' },
        Icon: true,
        PluginSecretsDialog: true,
        TotpStepUpDialog: true,
      },
    },
  })
  wrappers.push(wrapper)
  return wrapper
}

describe('NativePluginPage', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    state.leaveGuard = undefined
    state.plugin = { ...state.plugin, state: 'disabled', runtime_healthy: false }
    state.config = { name: 'old', _host_secrets: {} }
    byKey.mockResolvedValue(state.plugin)
    adminUI.mockResolvedValue(state.definition)
    getConfig.mockResolvedValue(state.config)
    resources.mockResolvedValue({ accounts: [], groups: [], proxies: [] })
    status.mockResolvedValue({ healthy: false, message: 'disabled' })
    saveConfig.mockImplementation(async (_id: number, config: Record<string, unknown>) => ({ ...config, _host_secrets: {} }))
    action.mockResolvedValue({ action_id: 'a1', accepted: true, status: 'queued' })
    vi.spyOn(window, 'confirm').mockReturnValue(true)
  })

  afterEach(() => {
    for (const wrapper of wrappers.splice(0)) wrapper.unmount()
    vi.restoreAllMocks()
    vi.useRealTimers()
  })

  it('keeps configuration inputs editable while the plugin is disabled', async () => {
    const wrapper = mountPage()
    await flushPromises()
    const input = wrapper.get('input[type="text"]')
    expect(input.attributes('readonly')).toBeUndefined()
    await input.setValue('new')
    await wrapper.findAll('button').find(button => button.text() === 'common.save')?.trigger('click')
    await flushPromises()
    expect(saveConfig).toHaveBeenCalledWith(7, { name: 'new' })
    expect(wrapper.findAll('button').find(button => button.text() === 'Harvest')?.attributes('disabled')).toBeDefined()
  })

  it('confirms actions and sends one host-generated idempotency id', async () => {
    state.plugin = { ...state.plugin, state: 'enabled', runtime_healthy: true }
    byKey.mockResolvedValue(state.plugin)
    const wrapper = mountPage()
    await flushPromises()
    const harvest = wrapper.findAll('button').find(button => button.text().includes('Harvest'))
    expect(harvest).toBeDefined()
    await harvest!.trigger('click')
    await flushPromises()
    expect(window.confirm).toHaveBeenCalledWith('admin.plugins.nativeActionConfirm')
    expect(action).toHaveBeenCalledTimes(1)
    expect(action.mock.calls[0][1]).toEqual({
      action_id: expect.stringMatching(/^native-/), name: 'harvest', payload: {},
    })
  })

  it('protects unsaved drafts when refreshing or leaving the page', async () => {
    const wrapper = mountPage()
    await flushPromises()
    await wrapper.get('input[type="text"]').setValue('draft')
    vi.mocked(window.confirm).mockReturnValue(false)
    await wrapper.findAll('button').find(button => button.text() === 'common.refresh')?.trigger('click')
    expect(getConfig).toHaveBeenCalledTimes(1)
    expect(state.leaveGuard).toBeDefined()
    expect(await state.leaveGuard!()).toBe(false)
    vi.mocked(window.confirm).mockReturnValue(true)
    expect(await state.leaveGuard!()).toBe(true)
  })
})
