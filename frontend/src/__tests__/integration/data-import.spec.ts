import { defineComponent } from 'vue'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import ImportDataModal from '@/components/admin/account/ImportDataModal.vue'

const showError = vi.fn()
const showSuccess = vi.fn()
const showWarning = vi.fn()

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError,
    showSuccess,
    showWarning
  })
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      importData: vi.fn()
    },
    proxyIpGroups: {
      list: vi.fn()
    },
    groups: {
      getAll: vi.fn()
    }
  }
}))

vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key
    })
  }
})

const GroupSelectorStub = defineComponent({
  name: 'GroupSelector',
  props: {
    modelValue: { type: Array, default: () => [] },
    preferredGroupIds: { type: Array, default: () => [] },
    groups: { type: Array, default: () => [] },
    platform: { type: String, default: '' }
  },
  emits: ['update:modelValue', 'update:preferredGroupIds'],
  template: `
    <div data-testid="group-selector-stub" :data-platform="platform">
      <button type="button" data-testid="select-import-groups" @click="$emit('update:modelValue', [101, 102])">groups</button>
      <button type="button" data-testid="select-import-preferred" @click="$emit('update:preferredGroupIds', [102])">preferred</button>
    </div>
  `
})

const mountModal = () =>
  mount(ImportDataModal, {
    props: { show: true },
    global: {
      stubs: {
        BaseDialog: { template: '<div><slot /><slot name="footer" /></div>' },
        GroupSelector: GroupSelectorStub
      }
    }
  })

const makeJsonFile = (name: string, content: string, type = 'application/json') => {
  const file = new File([content], name, { type })
  Object.defineProperty(file, 'text', {
    value: () => Promise.resolve(content)
  })
  return file
}

const setInputFiles = (element: Element, files: File[]) => {
  Object.defineProperty(element, 'files', {
    value: files,
    configurable: true
  })
}

describe('ImportDataModal', () => {
  beforeEach(async () => {
    showError.mockReset()
    showSuccess.mockReset()
    showWarning.mockReset()
    const { adminAPI } = await import('@/api/admin')
    vi.mocked(adminAPI.accounts.importData).mockReset()
    vi.mocked(adminAPI.proxyIpGroups.list).mockReset()
    vi.mocked(adminAPI.groups.getAll).mockReset()
    vi.mocked(adminAPI.proxyIpGroups.list).mockResolvedValue([
      {
        id: 12,
        name: 'Hong Kong pool',
        member_count: 3,
        per_ip_concurrency: 4
      } as never
    ])
    vi.mocked(adminAPI.groups.getAll).mockResolvedValue([])
  })

  it('打开弹窗时加载代理组', async () => {
    const { adminAPI } = await import('@/api/admin')
    mountModal()
    await flushPromises()
    expect(adminAPI.proxyIpGroups.list).toHaveBeenCalledTimes(1)
  })

  it('统一覆盖设置使用三列布局和新的默认值', async () => {
    const wrapper = mountModal()
    const input = wrapper.find('input[type="file"]')
    setInputFiles(input.element, [
      makeJsonFile('accounts.json', JSON.stringify({
        exported_at: '2026-09-20T00:00:00Z',
        proxies: [],
        accounts: [{ name: 'a', platform: 'openai' }]
      }))
    ])
    await input.trigger('change')
    await flushPromises()

    const overrideGrid = wrapper.get('[data-testid="data-import-numeric-overrides"]')
    expect(overrideGrid.classes()).toContain('sm:grid-cols-3')
    const numericInputs = overrideGrid.findAll('input[type="number"]')
    expect((numericInputs[0]!.element as HTMLInputElement).value).toBe('20')
    expect((numericInputs[1]!.element as HTMLInputElement).value).toBe('0')
    expect((numericInputs[2]!.element as HTMLInputElement).value).toBe('1')
  })

  it('未选择文件时提示错误', async () => {
    const wrapper = mountModal()

    await wrapper.find('form').trigger('submit')
    expect(showError).toHaveBeenCalledWith('admin.accounts.dataImportSelectFile')
  })

  it('无效 JSON 时按文件名提示解析失败', async () => {
    const { adminAPI } = await import('@/api/admin')
    const wrapper = mountModal()

    const input = wrapper.find('input[type="file"]')
    setInputFiles(input.element, [makeJsonFile('data.json', 'invalid json')])

    await input.trigger('change')
    await wrapper.find('form').trigger('submit')
    await flushPromises()

    expect(showError).toHaveBeenCalledWith('admin.accounts.dataImportParseFailedFile')
    expect(adminAPI.accounts.importData).not.toHaveBeenCalled()
  })

  it('不是导出数据的 JSON 按文件名拒绝', async () => {
    const { adminAPI } = await import('@/api/admin')
    const wrapper = mountModal()

    const input = wrapper.find('input[type="file"]')
    setInputFiles(input.element, [makeJsonFile('random.json', JSON.stringify({ name: 'test' }))])

    await input.trigger('change')
    await wrapper.find('form').trigger('submit')
    await flushPromises()

    expect(showError).toHaveBeenCalledWith('admin.accounts.dataImportInvalidFile')
    expect(adminAPI.accounts.importData).not.toHaveBeenCalled()
  })

  it('无有效 JSON 的选择不清空已有选择', async () => {
    const { adminAPI } = await import('@/api/admin')
    vi.mocked(adminAPI.accounts.importData).mockResolvedValue({
      proxy_created: 0,
      proxy_reused: 0,
      proxy_failed: 0,
      account_created: 1,
      account_failed: 0
    })

    const wrapper = mountModal()
    const input = wrapper.find('input[type="file"]')

    const valid = makeJsonFile(
      'valid.json',
      JSON.stringify({ exported_at: '2026-07-05T00:00:00Z', proxies: [], accounts: [{ name: 'a' }] })
    )
    setInputFiles(input.element, [valid])
    await input.trigger('change')

    setInputFiles(input.element, [new File(['hello'], 'notes.txt', { type: 'text/plain' })])
    await input.trigger('change')
    expect(showError).toHaveBeenCalledWith('admin.accounts.dataImportSelectFile')

    await wrapper.find('form').trigger('submit')
    await flushPromises()

    expect(adminAPI.accounts.importData).toHaveBeenCalledWith({
      data: expect.objectContaining({
        accounts: [{ name: 'a' }]
      }),
      skip_default_group_bind: true,
      override_concurrency: 20,
      override_rate_multiplier: 0,
      override_priority: 1,
      override_codex_fingerprint_mode: 'session'
    })
  })

  it('merges multiple selected JSON files before importing', async () => {
    const { adminAPI } = await import('@/api/admin')
    vi.mocked(adminAPI.accounts.importData).mockResolvedValue({
      proxy_created: 0,
      proxy_reused: 0,
      proxy_failed: 0,
      account_created: 2,
      account_failed: 0
    })

    const wrapper = mountModal()

    const input = wrapper.find('input[type="file"]')
    const first = makeJsonFile(
      'first.json',
      JSON.stringify({ exported_at: '2026-07-05T00:00:00Z', proxies: [], accounts: [{ name: 'a' }] })
    )
    const second = makeJsonFile(
      'second.json',
      JSON.stringify({
        exported_at: '2026-07-05T00:00:01Z',
        proxies: [{ proxy_key: 'p' }],
        accounts: [{ name: 'b' }]
      })
    )
    setInputFiles(input.element, [first, second])

    await input.trigger('change')
    await wrapper.find('form').trigger('submit')
    await flushPromises()

    expect(adminAPI.accounts.importData).toHaveBeenCalledWith({
      data: expect.objectContaining({
        proxies: [{ proxy_key: 'p' }],
        accounts: [{ name: 'a' }, { name: 'b' }]
      }),
      skip_default_group_bind: true,
      override_concurrency: 20,
      override_rate_multiplier: 0,
      override_priority: 1,
      override_codex_fingerprint_mode: 'session'
    })
    expect(showSuccess).toHaveBeenCalledWith('admin.accounts.dataImportSuccess')
  })

  it('为导入账号绑定所选代理组', async () => {
    const { adminAPI } = await import('@/api/admin')
    vi.mocked(adminAPI.accounts.importData).mockResolvedValue({
      proxy_created: 0,
      proxy_reused: 0,
      proxy_failed: 0,
      account_created: 2,
      account_failed: 0
    })
    const wrapper = mountModal()
    await flushPromises()
    const input = wrapper.find('input[type="file"]')
    setInputFiles(input.element, [
      makeJsonFile('accounts.json', JSON.stringify({
        exported_at: '2026-08-31T00:00:00Z',
        proxies: [],
        accounts: [
          { name: 'a', platform: 'openai', type: 'oauth' },
          { name: 'b', platform: 'openai', type: 'setup-token' }
        ]
      }))
    ])
    await input.trigger('change')
    await flushPromises()
    const groupSelect = wrapper.findAllComponents({ name: 'Select' })
      .find(select => select.attributes('data-testid') === 'data-import-proxy-ip-group')
    expect(groupSelect).toBeDefined()
    groupSelect!.vm.$emit('update:modelValue', 12)
    await flushPromises()
    await wrapper.find('form').trigger('submit')
    await flushPromises()

    expect(adminAPI.accounts.importData).toHaveBeenCalledWith(expect.objectContaining({
      proxy_ip_group_id: 12,
      data: expect.objectContaining({
        accounts: [
          { name: 'a', platform: 'openai', type: 'oauth' },
          { name: 'b', platform: 'openai', type: 'setup-token' }
        ]
      })
    }))
  })

  it('does not offer or submit a proxy group for non-OpenAI OAuth imports', async () => {
    const { adminAPI } = await import('@/api/admin')
    vi.mocked(adminAPI.accounts.importData).mockResolvedValue({
      proxy_created: 0,
      proxy_reused: 0,
      proxy_failed: 0,
      account_created: 1,
      account_failed: 0
    })
    const wrapper = mountModal()
    await flushPromises()
    const input = wrapper.find('input[type="file"]')
    setInputFiles(input.element, [
      makeJsonFile('anthropic.json', JSON.stringify({
        exported_at: '2026-09-20T00:00:00Z',
        proxies: [],
        accounts: [{ name: 'a', platform: 'anthropic', type: 'oauth' }]
      }))
    ])
    await input.trigger('change')
    await flushPromises()

    expect(wrapper.find('[data-testid="data-import-proxy-ip-group"]').exists()).toBe(false)
    await wrapper.find('form').trigger('submit')
    await flushPromises()
    expect(adminAPI.accounts.importData.mock.calls[0]?.[0]).not.toHaveProperty('proxy_ip_group_id')
  })

  it('单平台导入提交默认覆盖值、分组和优先分组', async () => {
    const { adminAPI } = await import('@/api/admin')
    vi.mocked(adminAPI.groups.getAll).mockResolvedValue([
      { id: 101, name: 'standard', platform: 'openai', status: 'active' } as never,
      { id: 102, name: 'preferred', platform: 'openai', status: 'active' } as never
    ])
    vi.mocked(adminAPI.accounts.importData).mockResolvedValue({
      proxy_created: 0,
      proxy_reused: 0,
      proxy_failed: 0,
      account_created: 1,
      account_failed: 0
    })

    const wrapper = mountModal()
    await flushPromises()
    const input = wrapper.find('input[type="file"]')
    setInputFiles(input.element, [
      makeJsonFile('openai.json', JSON.stringify({
        exported_at: '2026-08-31T00:00:00Z',
        proxies: [],
        accounts: [{ name: 'a', platform: 'openai' }]
      }))
    ])
    await input.trigger('change')
    await flushPromises()

    expect(wrapper.get('[data-testid="data-import-group-selector"]').attributes('data-platform')).toBe('openai')
    await wrapper.get('[data-testid="select-import-groups"]').trigger('click')
    await wrapper.get('[data-testid="select-import-preferred"]').trigger('click')
    await wrapper.find('form').trigger('submit')
    await flushPromises()

    expect(adminAPI.accounts.importData).toHaveBeenCalledWith(expect.objectContaining({
      override_concurrency: 20,
      override_rate_multiplier: 0,
      override_priority: 1,
      override_codex_fingerprint_mode: 'session',
      group_ids: [101, 102],
      preferred_group_ids: [102]
    }))
  })

  it('清空数字覆盖值后不提交对应字段', async () => {
    const { adminAPI } = await import('@/api/admin')
    vi.mocked(adminAPI.accounts.importData).mockResolvedValue({
      proxy_created: 0,
      proxy_reused: 0,
      proxy_failed: 0,
      account_created: 1,
      account_failed: 0
    })
    const wrapper = mountModal()
    const input = wrapper.find('input[type="file"]')
    setInputFiles(input.element, [
      makeJsonFile('openai.json', JSON.stringify({
        exported_at: '2026-08-31T00:00:00Z',
        proxies: [],
        accounts: [{ name: 'a', platform: 'openai' }]
      }))
    ])
    await input.trigger('change')
    await flushPromises()

    for (const numericInput of wrapper.findAll('input[type="number"]')) {
      await numericInput.setValue('')
    }
    await wrapper.find('form').trigger('submit')
    await flushPromises()

    const payload = vi.mocked(adminAPI.accounts.importData).mock.calls[0]![0]
    expect(payload).not.toHaveProperty('override_concurrency')
    expect(payload).not.toHaveProperty('override_rate_multiplier')
    expect(payload).not.toHaveProperty('override_priority')
    expect(payload.override_codex_fingerprint_mode).toBe('session')
  })

  it('混合平台导入禁用并清空分组设置', async () => {
    const { adminAPI } = await import('@/api/admin')
    vi.mocked(adminAPI.accounts.importData).mockResolvedValue({
      proxy_created: 0,
      proxy_reused: 0,
      proxy_failed: 0,
      account_created: 2,
      account_failed: 0
    })
    const wrapper = mountModal()
    const input = wrapper.find('input[type="file"]')
    setInputFiles(input.element, [
      makeJsonFile('openai.json', JSON.stringify({
        exported_at: '2026-08-31T00:00:00Z',
        proxies: [],
        accounts: [{ name: 'a', platform: 'openai' }]
      }))
    ])
    await input.trigger('change')
    await flushPromises()
    await wrapper.get('[data-testid="select-import-groups"]').trigger('click')
    await wrapper.get('[data-testid="select-import-preferred"]').trigger('click')

    setInputFiles(input.element, [
      makeJsonFile('mixed.json', JSON.stringify({
        exported_at: '2026-08-31T00:00:00Z',
        proxies: [],
        accounts: [
          { name: 'a', platform: 'openai' },
          { name: 'b', platform: 'anthropic' }
        ]
      }))
    ])
    await input.trigger('change')
    await flushPromises()

    expect(wrapper.find('[data-testid="data-import-group-selector"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="data-import-mixed-platform-warning"]').exists()).toBe(true)
    await wrapper.find('form').trigger('submit')
    await flushPromises()
    const payload = vi.mocked(adminAPI.accounts.importData).mock.calls[0]![0]
    expect(payload).not.toHaveProperty('group_ids')
    expect(payload).not.toHaveProperty('preferred_group_ids')
  })

  it('存在未知平台时不允许配置分组', async () => {
    const wrapper = mountModal()
    const input = wrapper.find('input[type="file"]')
    setInputFiles(input.element, [
      makeJsonFile('unknown.json', JSON.stringify({
        exported_at: '2026-08-31T00:00:00Z',
        proxies: [],
        accounts: [
          { name: 'a', platform: 'openai' },
          { name: 'b', platform: 'unknown-provider' }
        ]
      }))
    ])
    await input.trigger('change')
    await flushPromises()

    expect(wrapper.find('[data-testid="data-import-group-selector"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('admin.accounts.dataImportPlatformUnavailable')
  })

  it('accepts numeric v-model values and preserves zero overrides', async () => {
    const { adminAPI } = await import('@/api/admin')
    vi.mocked(adminAPI.accounts.importData).mockResolvedValue({
      proxy_created: 0,
      proxy_reused: 0,
      proxy_failed: 0,
      account_created: 1,
      account_failed: 0
    })

    const wrapper = mountModal()
    const input = wrapper.find('input[type="file"]')
    setInputFiles(input.element, [
      makeJsonFile('accounts.json', JSON.stringify({
        exported_at: '2026-08-31T00:00:00Z',
        proxies: [],
        accounts: [{ name: 'a' }]
      }))
    ])
    await input.trigger('change')
    await flushPromises()

    const numericInputs = wrapper.findAll('input[type="number"]')
    await numericInputs[0]!.setValue('0')
    await numericInputs[1]!.setValue('0')
    await wrapper.find('form').trigger('submit')
    await flushPromises()

    expect(adminAPI.accounts.importData).toHaveBeenCalledWith(expect.objectContaining({
      override_concurrency: 0,
      override_rate_multiplier: 0,
      override_priority: 1,
      override_codex_fingerprint_mode: 'session'
    }))
    expect(showError).not.toHaveBeenCalledWith('admin.accounts.dataImportFailed')
  })

  it('部分成功时关闭弹窗仍通知父组件刷新', async () => {
    const { adminAPI } = await import('@/api/admin')
    vi.mocked(adminAPI.accounts.importData).mockResolvedValue({
      proxy_created: 0,
      proxy_reused: 0,
      proxy_failed: 0,
      account_created: 1,
      account_failed: 1
    })

    const wrapper = mountModal()
    const input = wrapper.find('input[type="file"]')
    setInputFiles(input.element, [
      makeJsonFile(
        'mixed.json',
        JSON.stringify({
          exported_at: '2026-07-05T00:00:00Z',
          proxies: [],
          accounts: [{ name: 'a' }, { name: 'b' }]
        })
      )
    ])

    await input.trigger('change')
    await wrapper.find('form').trigger('submit')
    await flushPromises()

    expect(showError).toHaveBeenCalledWith('admin.accounts.dataImportCompletedWithErrors')
    expect(wrapper.emitted('imported')).toBeUndefined()

    // 第二个 btn-secondary 是 footer 的取消按钮(第一个是选择文件)
    await wrapper.findAll('button.btn-secondary')[1]!.trigger('click')

    expect(wrapper.emitted('imported')).toHaveLength(1)
    expect(wrapper.emitted('close')).toHaveLength(1)
  })
})
