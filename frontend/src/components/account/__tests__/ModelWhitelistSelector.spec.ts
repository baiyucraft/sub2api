import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

const { copyToClipboard, syncUpstreamModels } = vi.hoisted(() => ({
  copyToClipboard: vi.fn().mockResolvedValue(true),
  syncUpstreamModels: vi.fn()
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => (key === 'common.copy' ? '复制' : key)
    })
  }
})

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: vi.fn(),
    showSuccess: vi.fn(),
    showInfo: vi.fn()
  })
}))

vi.mock('@/composables/useClipboard', () => ({
  useClipboard: () => ({
    copyToClipboard
  })
}))

vi.mock('@/api/admin/accounts', () => ({
  accountsAPI: {
    syncUpstreamModels,
    syncUpstreamModelsPreview: vi.fn()
  }
}))

import ModelWhitelistSelector from '../ModelWhitelistSelector.vue'

function mountSelector() {
  return mount(ModelWhitelistSelector, {
    props: {
      modelValue: [],
      platform: 'openai'
    },
    global: {
      stubs: {
        ModelIcon: true
      }
    }
  })
}

function findModelRow(wrapper: ReturnType<typeof mountSelector>, modelId: string) {
  const row = wrapper
    .findAll('[data-testid="model-option"]')
    .find(candidate => candidate.text().includes(modelId))

  if (!row) {
    throw new Error(`Model row not found: ${modelId}`)
  }

  return row
}

describe('ModelWhitelistSelector', () => {
  beforeEach(() => {
    copyToClipboard.mockClear()
    syncUpstreamModels.mockReset()
  })

  it('copies a model ID without selecting the model', async () => {
    const wrapper = mountSelector()
    await wrapper.get('div.cursor-pointer').trigger('click')

    const row = findModelRow(wrapper, 'gpt-5.6-sol')

    const copyButton = row.get('[data-testid="copy-model-id"]')
    expect(copyButton.attributes('aria-label')).toBe('复制 gpt-5.6-sol')

    await copyButton.trigger('click')
    await flushPromises()

    expect(copyToClipboard).toHaveBeenCalledWith('gpt-5.6-sol')
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
  })

  it('keeps the existing model selection behavior', async () => {
    const wrapper = mountSelector()
    await wrapper.get('div.cursor-pointer').trigger('click')

    const row = findModelRow(wrapper, 'gpt-5.6-sol')
    await row.get('[data-testid="select-model"]').trigger('click')

    expect(wrapper.emitted('update:modelValue')).toEqual([[['gpt-5.6-sol']]])
    expect(copyToClipboard).not.toHaveBeenCalled()
  })

  it('refreshes managed upstream models without appending them to the editable whitelist', async () => {
    syncUpstreamModels.mockResolvedValue({ models: ['gpt-5.6-sol', 'gpt-5.6-terra'], persisted: true })
    const wrapper = mount(ModelWhitelistSelector, {
      props: {
        modelValue: ['existing-model'],
        platform: 'openai',
        accountId: 42,
        syncManaged: true
      },
      global: { stubs: { ModelIcon: true } }
    })

    const button = wrapper.findAll('button').find(candidate => candidate.text() === 'admin.accounts.refreshUpstreamModels')
    expect(button).toBeTruthy()
    await button!.trigger('click')
    await flushPromises()

    expect(syncUpstreamModels).toHaveBeenCalledWith(42)
    expect(wrapper.emitted('upstream-synced')).toEqual([[['gpt-5.6-sol', 'gpt-5.6-terra']]])
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
  })

  it('keeps manual account sync-and-append behavior', async () => {
    syncUpstreamModels.mockResolvedValue({ models: ['gpt-5.6-sol'] })
    const wrapper = mount(ModelWhitelistSelector, {
      props: {
        modelValue: ['existing-model'],
        platform: 'openai',
        accountId: 43
      },
      global: { stubs: { ModelIcon: true } }
    })

    const button = wrapper.findAll('button').find(candidate => candidate.text() === 'admin.accounts.syncUpstreamModels')
    expect(button).toBeTruthy()
    await button!.trigger('click')
    await flushPromises()

    expect(wrapper.emitted('update:modelValue')).toEqual([[['existing-model', 'gpt-5.6-sol']]])
    expect(wrapper.emitted('upstream-synced')).toBeUndefined()
  })

  it('trusts the backend persisted flag when the local managed state is stale', async () => {
    syncUpstreamModels.mockResolvedValue({ models: ['server-model'], persisted: false })
    const wrapper = mount(ModelWhitelistSelector, {
      props: {
        modelValue: ['existing-model'],
        platform: 'openai',
        accountId: 44,
        syncManaged: true
      },
      global: { stubs: { ModelIcon: true } }
    })

    const button = wrapper.findAll('button').find(candidate => candidate.text() === 'admin.accounts.refreshUpstreamModels')
    await button!.trigger('click')
    await flushPromises()

    expect(wrapper.emitted('update:modelValue')).toEqual([[['existing-model', 'server-model']]])
    expect(wrapper.emitted('upstream-synced')).toBeUndefined()
  })
})
