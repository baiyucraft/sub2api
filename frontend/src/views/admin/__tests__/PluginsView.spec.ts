import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import PluginsView from '../PluginsView.vue'
import PluginSecretsDialog from '@/components/admin/plugins/PluginSecretsDialog.vue'

const {
  listPlugins,
  uploadPlugin,
  enablePlugin,
  savePluginConfig,
  createUISession,
  stepUpRun,
  upgradePlugin,
  disablePlugin,
  getResources,
  runAction,
  getConfig,
  getStatus,
  saveSecrets,
} = vi.hoisted(() => ({
  listPlugins: vi.fn(),
  uploadPlugin: vi.fn(),
  enablePlugin: vi.fn(),
  savePluginConfig: vi.fn(),
  createUISession: vi.fn(),
  stepUpRun: vi.fn((action: () => Promise<unknown>) => action()),
  upgradePlugin: vi.fn(), disablePlugin: vi.fn(), getResources: vi.fn(), runAction: vi.fn(), getConfig: vi.fn(), getStatus: vi.fn(),
  saveSecrets: vi.fn(),
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    plugins: {
      list: listPlugins,
      upload: uploadPlugin,
      enable: enablePlugin,
      disable: disablePlugin,
      upgrade: upgradePlugin,
      resources: getResources,
      action: runAction,
      status: getStatus,
      remove: vi.fn(),
      getConfig,
      saveConfig: savePluginConfig,
      saveSecrets,
      test: vi.fn().mockResolvedValue({ success: true, message: 'ok', latency_ms: 1 }),
      createUISession,
    },
  },
}))

vi.mock('@/stores', () => ({
  useAppStore: () => ({
    showError: vi.fn(),
    showSuccess: vi.fn(),
    showInfo: vi.fn(),
  }),
}))

vi.mock('@/composables/useStepUp', () => ({
  useStepUp: () => ({ run: stepUpRun }),
  isStepUpBlocked: () => false,
  isStepUpCancelled: () => false,
  stepUpBlockReason: () => '',
}))

vi.mock('vue-router', () => ({
  useRouter: () => ({ push: vi.fn() }),
}))

vi.mock('vue-i18n', async (importOriginal) => ({
  ...(await importOriginal<typeof import('vue-i18n')>()),
  useI18n: () => ({ t: (key: string) => key }),
}))

const plugin = {
  id: 7,
  plugin_key: 'local.test.transport',
  name: 'Test Transport',
  version: '1.0.0',
  description: '',
  author: 'test',
  manifest: {
    schema_version: 1,
    id: 'local.test.transport',
    name: 'Test Transport',
    version: '1.0.0',
    requires: {
      sub2api: '>=0.1.0',
      plugin_protocol: 1,
      transport_api: 1,
      ui_bridge: 1,
    },
    capabilities: [],
    ui: { entrypoint: 'ui/index.html' },
  },
  binary_sha256: 'a'.repeat(64),
  signature_status: 'trusted' as const,
  state: 'disabled' as const,
  last_error: '',
  installed_at: '2026-08-22T00:00:00Z',
  updated_at: '2026-08-22T00:00:00Z',
  bindings: [
    {
      id: 1,
      plugin_id: 7,
      capability: 'openai.oauth.outbound_transport.v1',
      platform: 'openai',
      account_type: 'oauth',
      enabled: false,
      rollout_percent: 100,
    },
  ],
  compatibility: {
    compatible: true,
    tested: true,
    status: 'compatible' as const,
    message: '',
    current_sub2api_version: '0.1.0',
    required_sub2api_version: '>=0.1.0',
    recommended_sub2api_version: '0.1.0',
    plugin_protocol: 1,
    transport_api: 1,
    ui_bridge: 1,
    admin_ui: 0,
  },
  runtime_healthy: false,
  runtime_message: '',
}

const wrappers: VueWrapper[] = []
function mountView() {
  const wrapper = mount(PluginsView, {
    attachTo: document.body,
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        BaseDialog: { template: '<div><slot /></div>' },
        Icon: true,
        TotpStepUpDialog: true,
      },
    },
  })
  wrappers.push(wrapper)
  return wrapper
}

async function openFrame() {
  const wrapper = mountView()
  await flushPromises()
  await wrapper.findAll('button').find(b => b.text().includes('admin.plugins.configure'))!.trigger('click')
  await flushPromises()
  const frame = wrapper.get('iframe').element as HTMLIFrameElement
  const post = vi.spyOn(frame.contentWindow!, 'postMessage').mockImplementation(() => {})
  const send = (type: string, extra: Record<string, unknown> = {}, origin = 'null') => window.dispatchEvent(new MessageEvent('message', {
    origin, source: frame.contentWindow, data: { source: 'sub2api-plugin-ui', bridge_token: 'bridge', request_id: `request-${type}`, type, ...extra },
  }))
  return { wrapper, frame, post, send }
}

describe('管理员插件页二次验证', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    stepUpRun.mockImplementation((action: () => Promise<unknown>) => action())
    listPlugins.mockResolvedValue([plugin])
    uploadPlugin.mockResolvedValue(plugin)
    enablePlugin.mockResolvedValue(plugin)
    savePluginConfig.mockResolvedValue({ enabled: true })
    upgradePlugin.mockResolvedValue(plugin)
    getResources.mockResolvedValue({ accounts: [], groups: [], proxies: [] })
    getConfig.mockResolvedValue({ legacy_config: true })
    getStatus.mockResolvedValue({ healthy: false, message: 'disabled' })
    saveSecrets.mockResolvedValue({ endpoint: '', token: '', _host_secrets: { endpoint: true, token: false } })
    runAction.mockResolvedValue({ action_id: 'a1', accepted: true, status: 'queued' })
    createUISession.mockResolvedValue({
      url: '/api/v1/plugin-ui/token/index.html#bridge_token=bridge',
      bridge_token: 'bridge',
      ui_bridge_version: 1,
      expires_at: '2026-08-22T01:00:00Z',
    })
  })

  afterEach(() => {
    for (const wrapper of wrappers.splice(0)) wrapper.unmount()
    vi.restoreAllMocks()
    vi.useRealTimers()
  })

  it('启用插件通过 step-up 控制器执行', async () => {
    const wrapper = mountView()
    await flushPromises()

    const button = wrapper.findAll('button').find((item) => item.text().includes('admin.plugins.enable'))
    expect(button).toBeDefined()
    await button!.trigger('click')
    await flushPromises()

    expect(stepUpRun).toHaveBeenCalledTimes(1)
    expect(enablePlugin).toHaveBeenCalledWith(7, 100, false)
  })

  it('上传插件通过 step-up 控制器执行', async () => {
    const wrapper = mountView()
    await flushPromises()
    const input = wrapper.get('input[type="file"]')
    Object.defineProperty(input.element, 'files', {
      configurable: true,
      value: [new File(['plugin'], 'transport.s2plugin', { type: 'application/zip' })],
    })

    await input.trigger('change')
    await flushPromises()

    expect(stepUpRun).toHaveBeenCalledTimes(1)
    expect(uploadPlugin).toHaveBeenCalledTimes(1)
  })

  it('upgrades a running plugin without disabling it or uploading a second installation', async () => {
    listPlugins.mockResolvedValue([{ ...plugin, state: 'enabled', runtime_healthy: true }])
    const wrapper = mountView()
    await flushPromises()
    await wrapper.findAll('button').find(b => b.text() === 'admin.plugins.upgrade')!.trigger('click')
    const input = wrapper.get('input[type="file"]')
    const file = new File(['upgrade'], 'state.s2plugin')
    Object.defineProperty(input.element, 'files', { configurable: true, value: [file] })
    await input.trigger('change')
    await flushPromises()
    expect(upgradePlugin).toHaveBeenCalledWith(7, file)
    expect(disablePlugin).not.toHaveBeenCalled()
    expect(uploadPlugin).not.toHaveBeenCalled()
    expect(stepUpRun).toHaveBeenCalledTimes(1)
  })

  it('retains v1 config and status responses in an opaque-origin sandbox', async () => {
    const { frame, send, post } = await openFrame()
    expect(frame.getAttribute('sandbox')).toBe('allow-scripts')
    send('config.load')
    send('plugin.status')
    await flushPromises()
    expect(post).toHaveBeenCalledWith(expect.objectContaining({ type: 'config.load.result', ok: true, config: { legacy_config: true } }), '*')
    expect(post).toHaveBeenCalledWith(expect.objectContaining({ type: 'plugin.status.result', ok: true, result: { healthy: false, message: 'disabled' } }), '*')
    expect(stepUpRun).not.toHaveBeenCalled()
    send('config.save', { config: { legacy_config: false } })
    await flushPromises()
    expect(savePluginConfig).toHaveBeenCalledWith(7, { legacy_config: false })
  })

  it('exposes only catalog metadata while stopped and blocks actions before step-up', async () => {
    getResources.mockResolvedValue({
      accounts: [{ id: 3, name: 'A', platform: 'openai', account_type: 'oauth', group_ids: [2], business_egress_configured: true, credentials: { access_token: 'SECRET' }, extra: { state: 'SECRET' } }],
      groups: [{ id: 2, name: 'G', secret: 'SECRET' }],
      proxies: [{ id: 9, name: 'P', protocol: 'http', host: 'proxy.example', port: 8080, username: 'SECRET', password: 'SECRET' }],
      token: 'SECRET',
    })
    const { send, post } = await openFrame()
    send('plugin.resources')
    send('plugin.action', { action_id: 'a1', name: 'harvest', payload: { account_id: 3, model: 'gpt-6-astra' } })
    await flushPromises()
    expect(getResources).toHaveBeenCalledWith(7)
    expect(JSON.stringify(post.mock.calls)).not.toContain('SECRET')
    expect(post).toHaveBeenCalledWith(expect.objectContaining({ type: 'plugin.action.result', ok: false }), '*')
    expect(runAction).not.toHaveBeenCalled()
    expect(stepUpRun).not.toHaveBeenCalled()
  })

  it('runs a validated action with step-up and rechecks the current runtime', async () => {
    listPlugins.mockResolvedValue([{ ...plugin, state: 'enabled', runtime_healthy: true }])
    const { send, post } = await openFrame()
    send('plugin.action', { action_id: 'a1', name: 'cancel', payload: { account_id: 3, model: 'gpt-6-astra' } })
    await flushPromises()
    expect(stepUpRun).toHaveBeenCalledTimes(1)
    expect(runAction).toHaveBeenCalledWith(7, { action_id: 'a1', name: 'cancel', payload: { account_id: 3, model: 'gpt-6-astra' } })
    expect(post).toHaveBeenCalledWith(expect.objectContaining({ ok: true, result: expect.objectContaining({ accepted: true }) }), '*')
    listPlugins.mockResolvedValue([plugin])
    send('plugin.action', { action_id: 'a2', name: 'harvest', payload: { account_id: 3, model: 'gpt-6-astra' } })
    await flushPromises()
    expect(runAction).toHaveBeenCalledTimes(1)
  })

  it('rejects forged messages and malformed actions without network calls', async () => {
    listPlugins.mockResolvedValue([{ ...plugin, state: 'enabled', runtime_healthy: true }])
    const { send, frame } = await openFrame()
    send('plugin.resources', {}, 'https://attacker.example')
    send('plugin.resources', { bridge_token: 'wrong' })
    window.dispatchEvent(new MessageEvent('message', { source: window, origin: 'null', data: { source: 'sub2api-plugin-ui', bridge_token: 'bridge', type: 'plugin.resources', request_id: 'bad' } }))
    send('plugin.action', { action_id: 'bad id', name: 'harvest', payload: {} })
    await flushPromises()
    expect(frame.contentWindow).not.toBe(window)
    expect(getResources).not.toHaveBeenCalled()
    expect(runAction).not.toHaveBeenCalled()
  })

  it('drops late responses after iframe navigation even when a request id is reused', async () => {
    let complete!: (value: unknown) => void
    getResources.mockReturnValueOnce(new Promise(resolve => { complete = resolve }))
    const { send, wrapper, post } = await openFrame()
    await wrapper.get('iframe').trigger('load')
    send('plugin.resources', { request_id: 'same' })
    await wrapper.get('iframe').trigger('load')
    send('plugin.resources', { request_id: 'same' })
    await flushPromises()
    complete({ accounts: [{ id: 99, name: 'OLD', platform: 'openai', account_type: 'oauth' }] })
    await flushPromises()
    expect(post).toHaveBeenCalledTimes(1)
    expect(JSON.stringify(post.mock.calls)).not.toContain('OLD')
  })

  it('never forwards raw API exception text to the iframe', async () => {
    getResources.mockRejectedValue(new Error('http://user:SECRET@proxy.example raw STATE'))
    const { send, post } = await openFrame()
    send('plugin.resources')
    await flushPromises()
    expect(post).toHaveBeenCalledWith(expect.objectContaining({ ok: false, error: 'admin.plugins.bridgeRequestFailed' }), '*')
    expect(JSON.stringify(post.mock.calls)).not.toContain('SECRET')
  })

  it('shows additional host SDK requirements without requiring them on old plugins', async () => {
    listPlugins.mockResolvedValue([{ ...plugin, manifest: { ...plugin.manifest, requires: { ...plugin.manifest.requires, host_service_api: 1, host_features: ['account.read', 'state.cas'] } } }])
    const wrapper = mountView()
    await flushPromises()
    expect(wrapper.get('[data-testid="plugin-minimum-sdk"]').text()).toContain('Host Service API 1')
    expect(wrapper.text()).toContain('state.cas')
  })

  function declareSecrets() {
    listPlugins.mockResolvedValue([{ ...plugin, manifest: { ...plugin.manifest, config_secrets: ['endpoint', 'token'] } }])
    // A stale or faulty backend must not make the host reflect a declared secret.
    getConfig.mockResolvedValue({ endpoint: 'OLD_PRIVATE', token: 'OLD_PRIVATE', enabled: false, _host_secrets: { endpoint: true, token: false } })
  }

  it('redacts declared fields on load and save and strips ordinary secret edits', async () => {
    declareSecrets()
    savePluginConfig.mockResolvedValue({ enabled: true, endpoint: 'OLD_PRIVATE', token: 'OLD_PRIVATE', _host_secrets: { endpoint: true, token: false, other: 'PRIVATE' } })
    const { send, post } = await openFrame()
    send('config.load')
    await flushPromises()
    send('config.save', { config: { enabled: true, endpoint: 'FORGED_PRIVATE', token: '', _host_secrets: { endpoint: false } } })
    await flushPromises()
    expect(savePluginConfig).toHaveBeenCalledWith(7, { enabled: true, endpoint: '', token: '' })
    expect(JSON.stringify(post.mock.calls)).not.toContain('PRIVATE')
    expect(post).toHaveBeenLastCalledWith(expect.objectContaining({ type: 'config.save.result', config: { enabled: true, endpoint: '', token: '', _host_secrets: { endpoint: true, token: false } } }), '*')
  })

  it('rejects secret editing for legacy plugins and rejects every iframe-supplied parameter', async () => {
    const legacy = await openFrame()
    legacy.send('plugin.secrets.edit')
    await flushPromises()
    expect(legacy.wrapper.findComponent(PluginSecretsDialog).exists()).toBe(false)
    expect(getConfig).not.toHaveBeenCalled()
    legacy.wrapper.unmount()
    declareSecrets()
    const { send, wrapper } = await openFrame()
    for (const key of ['values', 'value', 'config', 'field', 'payload']) {
      send('plugin.secrets.edit', { [key]: 'FORGED_PRIVATE' })
      await flushPromises()
    }
    expect(wrapper.findComponent(PluginSecretsDialog).exists()).toBe(false)
    expect(saveSecrets).not.toHaveBeenCalled()
    expect(getConfig).not.toHaveBeenCalled()
  })

  it('opens a host-owned empty editor, keeps without PUT, and returns only flags', async () => {
    declareSecrets()
    const { send, wrapper, post } = await openFrame()
    send('plugin.secrets.edit')
    await flushPromises()
    const dialog = wrapper.getComponent(PluginSecretsDialog)
    expect(dialog.props('configured')).toEqual({ endpoint: true, token: false })
    expect(dialog.findAll('input')).toHaveLength(0)
    expect(wrapper.html()).not.toContain('OLD_PRIVATE')
    expect(stepUpRun).not.toHaveBeenCalled()
    await dialog.get('form').trigger('submit')
    await flushPromises()
    expect(saveSecrets).not.toHaveBeenCalled()
    expect(post).toHaveBeenLastCalledWith(expect.objectContaining({ type: 'plugin.secrets.edit.result', ok: true, result: { configured: { endpoint: true, token: false } } }), '*')
  })

  it('writes only trusted replacements and clear operations under step-up, never returns credentials', async () => {
    declareSecrets()
    let submitted: Record<string, string> = {}
    saveSecrets.mockImplementation(async (_id: number, values: Record<string, string>) => {
      submitted = { ...values }
      return { endpoint: 'NEW_PRIVATE', token: 'PRIVATE', _host_secrets: { endpoint: true, token: false, other: 'PRIVATE' } }
    })
    const { send, wrapper, post } = await openFrame()
    send('plugin.secrets.edit')
    await flushPromises()
    const dialog = wrapper.getComponent(PluginSecretsDialog)
    await dialog.get('[data-secret-mode="endpoint"]').setValue('replace')
    await dialog.get('[data-secret-value="endpoint"]').setValue('NEW_PRIVATE')
    await dialog.get('[data-secret-mode="token"]').setValue('clear')
    send('config.save', { config: { enabled: true } })
    await flushPromises()
    expect(savePluginConfig).not.toHaveBeenCalled()
    await dialog.get('form').trigger('submit')
    await flushPromises()
    expect(stepUpRun).toHaveBeenCalledTimes(1)
    expect(saveSecrets).toHaveBeenCalledTimes(1)
    expect(saveSecrets.mock.calls[0][0]).toBe(7)
    expect(submitted).toEqual({ endpoint: 'NEW_PRIVATE', token: '' })
    expect(JSON.stringify(post.mock.calls)).not.toContain('PRIVATE')
    expect(post).toHaveBeenLastCalledWith(expect.objectContaining({ type: 'plugin.secrets.edit.result', result: { configured: { endpoint: true, token: false } } }), '*')
    expect(wrapper.findComponent(PluginSecretsDialog).exists()).toBe(false)
  })

  it('cancels without a write and renders only generic sensitive API errors', async () => {
    declareSecrets()
    const { send, wrapper, post } = await openFrame()
    send('plugin.secrets.edit')
    await flushPromises()
    await wrapper.get('[data-testid="secrets-cancel"]').trigger('click')
    expect(saveSecrets).not.toHaveBeenCalled()
    expect(post).toHaveBeenLastCalledWith(expect.objectContaining({ ok: false, code: 'cancelled' }), '*')
    send('plugin.secrets.edit')
    await flushPromises()
    saveSecrets.mockRejectedValue(new Error('http://user:PRIVATE@proxy'))
    const dialog = wrapper.getComponent(PluginSecretsDialog)
    await dialog.get('[data-secret-mode="token"]').setValue('clear')
    await dialog.get('form').trigger('submit')
    await flushPromises()
    expect(wrapper.text()).toContain('admin.plugins.secretsSaveFailed')
    expect(wrapper.html()).not.toContain('PRIVATE')
    expect(JSON.stringify(post.mock.calls)).not.toContain('PRIVATE')
  })

  it('does not write after iframe navigation while step-up is pending', async () => {
    declareSecrets()
    let resume!: () => Promise<unknown>
    stepUpRun.mockImplementationOnce((action: () => Promise<unknown>) => new Promise((resolve, reject) => {
      resume = () => Promise.resolve().then(action).then(resolve, reject)
    }))
    const { send, wrapper, post } = await openFrame()
    await wrapper.get('iframe').trigger('load')
    send('plugin.secrets.edit')
    await flushPromises()
    const dialog = wrapper.getComponent(PluginSecretsDialog)
    await dialog.get('[data-secret-mode="token"]').setValue('clear')
    await dialog.get('form').trigger('submit')
    await flushPromises()
    await wrapper.get('iframe').trigger('load')
    await resume()
    await flushPromises()
    expect(saveSecrets).not.toHaveBeenCalled()
    expect(post).not.toHaveBeenCalled()
    expect(wrapper.findComponent(PluginSecretsDialog).exists()).toBe(false)
  })

  it('keeps interactive secret requests beyond 30s and closes them at the bounded timeout', async () => {
    vi.useFakeTimers()
    declareSecrets()
    const { send, wrapper } = await openFrame()
    send('plugin.secrets.edit')
    await flushPromises()
    await vi.advanceTimersByTimeAsync(31_000)
    expect(wrapper.findComponent(PluginSecretsDialog).exists()).toBe(true)
    await vi.advanceTimersByTimeAsync(10 * 60_000)
    expect(wrapper.findComponent(PluginSecretsDialog).exists()).toBe(false)
    expect(saveSecrets).not.toHaveBeenCalled()
  })

  it('discards a late secret-save response after navigation even when the request id is reused', async () => {
    declareSecrets()
    let complete!: (value: unknown) => void
    saveSecrets.mockReturnValueOnce(new Promise(resolve => { complete = resolve }))
    const { send, wrapper, post } = await openFrame()
    await wrapper.get('iframe').trigger('load')
    send('plugin.secrets.edit')
    await flushPromises()
    let dialog = wrapper.getComponent(PluginSecretsDialog)
    await dialog.get('[data-secret-mode="token"]').setValue('clear')
    await dialog.get('form').trigger('submit')
    await flushPromises()
    expect(saveSecrets).toHaveBeenCalledTimes(1)
    await wrapper.get('iframe').trigger('load')
    send('plugin.secrets.edit')
    await flushPromises()
    complete({ endpoint: 'PRIVATE', _host_secrets: { endpoint: false, token: true } })
    await flushPromises()
    expect(post).not.toHaveBeenCalled()
    dialog = wrapper.getComponent(PluginSecretsDialog)
    expect(dialog.props('configured')).toEqual({ endpoint: true, token: false })
    expect(dialog.props('saving')).toBe(false)
    expect(wrapper.html()).not.toContain('PRIVATE')
  })
})
