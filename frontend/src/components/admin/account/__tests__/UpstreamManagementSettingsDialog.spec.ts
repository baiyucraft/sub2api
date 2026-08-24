import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import UpstreamManagementSettingsDialog from '../UpstreamManagementSettingsDialog.vue'

const { getSettings, getProbeSettings, getCandidates, updateSettings, updateProbeSettings, showError, showSuccess } = vi.hoisted(() => ({
  getSettings: vi.fn(),
  getProbeSettings: vi.fn(),
  getCandidates: vi.fn(),
  updateSettings: vi.fn(),
  updateProbeSettings: vi.fn(),
  showError: vi.fn(),
  showSuccess: vi.fn()
}))

vi.mock('@/api/admin/upstreamManagement', () => ({
  default: {
    getSettings,
    getProbeSettings,
    getProbeModelCandidates: getCandidates,
    updateSettings,
    updateProbeSettings
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError, showSuccess })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key })
  }
})

const BaseDialogStub = defineComponent({
  props: ['show', 'title'],
  emits: ['close'],
  template: `
    <div v-if="show" data-test="dialog">
      <h1>{{ title }}</h1>
      <button data-test="close" @click="$emit('close')">close</button>
      <slot />
      <slot name="footer" />
    </div>
  `
})

const SelectStub = defineComponent({
  props: ['modelValue', 'options'],
  emits: ['update:modelValue'],
  template: '<button data-test="select" @click="$emit(\'update:modelValue\', \'custom-model\')">{{ modelValue }}</button>'
})

const ToggleStub = defineComponent({
  props: ['modelValue'],
  emits: ['update:modelValue'],
  template: '<button data-test="toggle" @click="$emit(\'update:modelValue\', !modelValue)">{{ modelValue }}</button>'
})

describe('UpstreamManagementSettingsDialog', () => {
  beforeEach(() => {
    getSettings.mockReset()
    getProbeSettings.mockReset()
    getCandidates.mockReset()
    updateSettings.mockReset()
    updateProbeSettings.mockReset()
    showError.mockReset()
    showSuccess.mockReset()
    getSettings.mockResolvedValue({
      ttft_guard: { enabled: true, degradation_ttft_seconds: 30, min_samples: 7 },
      probe_guard: {
        enabled: true,
        suspend_after_failures: 4,
        recovery_successes: 2,
        custom_error_codes_enabled: true,
        custom_error_codes: [404]
      },
      probe_models: { openai: 'gpt-live', anthropic: 'claude-live', gemini: 'gemini-live' },
      probe_interval_seconds: 420,
      model_alias_rules: { 'gpt-5.6-luna': 'gpt-5.6-terra' }
    })
    getCandidates.mockResolvedValue({ candidates: {
      openai: ['gpt-live', 'gpt-fallback'],
      anthropic: ['claude-live'],
      gemini: ['gemini-live']
    } })
    getProbeSettings.mockImplementation(() => getSettings())
    updateProbeSettings.mockImplementation(updateSettings)
    updateSettings.mockResolvedValue({
      ttft_guard: { enabled: false, degradation_ttft_seconds: 20, min_samples: 5 },
      probe_guard: {
        enabled: true,
        suspend_after_failures: 4,
        recovery_successes: 2,
        custom_error_codes_enabled: true,
        custom_error_codes: [404]
      },
      probe_models: { openai: 'custom-model', anthropic: 'claude-live', gemini: 'gemini-live' },
      probe_interval_seconds: 300,
      model_alias_rules: { 'gpt-5.6-luna': 'gpt-5.6-terra' }
    })
  })

  function mountDialog(show = true, probeOnly = false) {
    return mount(UpstreamManagementSettingsDialog, {
      props: { show, probeOnly },
      global: {
        stubs: {
          BaseDialog: BaseDialogStub,
          Select: SelectStub,
          Toggle: ToggleStub,
          HelpTooltip: { template: '<span data-test="help"><slot /></span>' },
          Icon: true
        }
      }
    })
  }

  it('loads only when opened and keeps the TTFT help tip in the dialog', async () => {
    const wrapper = mountDialog(false)
    await flushPromises()
    expect(getSettings).not.toHaveBeenCalled()

    await wrapper.setProps({ show: true })
    await flushPromises()
    expect(getSettings).toHaveBeenCalledTimes(1)
    expect(getCandidates).toHaveBeenCalledTimes(1)
    expect(wrapper.find('[data-test="help"]').exists()).toBe(true)
    expect(wrapper.text()).toContain('admin.upstreamManagement.ttftGuard.tip.definition')
  })

  it('renders the upstream platform catalog and marks catalog-only providers', async () => {
    getProbeSettings.mockResolvedValueOnce({
      ttft_guard: { enabled: true, degradation_ttft_seconds: 30, min_samples: 7 },
      probe_guard: { enabled: true, suspend_after_failures: 3, recovery_successes: 2, custom_error_codes_enabled: false, custom_error_codes: [] },
      probe_models: { openai: 'gpt-live', anthropic: 'claude-live', gemini: 'gemini-live', grok: 'grok-4.5' },
      probe_interval_seconds: 300,
      model_alias_rules: {}
    })
    getCandidates.mockResolvedValueOnce({
      candidates: { openai: ['gpt-live'], anthropic: ['claude-live'], gemini: ['gemini-live'], grok: ['grok-4.5'] },
      platforms: [
        { id: 'openai', label: 'OpenAI', models: ['gpt-live'], probe_supported: true },
        { id: 'grok', label: 'Grok', models: ['grok-4.5'], probe_supported: false, probe_reason: '该平台暂无主动探针协议' }
      ]
    })
    const wrapper = mountDialog(true, true)
    await flushPromises()
    expect(wrapper.text()).toContain('OpenAI')
    expect(wrapper.text()).toContain('Grok')
    expect(wrapper.text()).not.toContain('仅模型目录')
    expect(wrapper.text()).not.toContain('Catalog only')
  })

  it('does not persist a draft on close and saves all settings atomically', async () => {
    const wrapper = mountDialog(true)
    await flushPromises()
    await wrapper.find('[data-test="close"]').trigger('click')
    expect(updateSettings).not.toHaveBeenCalled()

    await wrapper.setProps({ show: false })
    await wrapper.setProps({ show: true })
    await flushPromises()
    await wrapper.find('button.btn-primary').trigger('click')
    await flushPromises()
    expect(updateSettings).toHaveBeenCalledWith(expect.objectContaining({
      ttft_guard: { enabled: true, degradation_ttft_seconds: 30, min_samples: 7 },
      probe_guard: {
        enabled: true,
        suspend_after_failures: 4,
        recovery_successes: 2,
        custom_error_codes_enabled: true,
        custom_error_codes: [404]
      },
      probe_models: expect.objectContaining({ openai: 'gpt-live' }),
      probe_interval_seconds: 420,
      model_alias_rules: { 'gpt-5.6-luna': 'gpt-5.6-terra' }
    }))
  })

  it('loads legacy settings without aliases as an empty mapping list', async () => {
    getSettings.mockResolvedValueOnce({
      ttft_guard: { enabled: true, degradation_ttft_seconds: 30, min_samples: 7 },
      probe_guard: { enabled: true, suspend_after_failures: 4, recovery_successes: 2, custom_error_codes_enabled: false, custom_error_codes: [] },
      probe_models: { openai: 'gpt-live', anthropic: 'claude-live', gemini: 'gemini-live' },
      probe_interval_seconds: 300
    })
    const wrapper = mountDialog(true)
    await flushPromises()
    expect(wrapper.findAll('[data-test="model-alias-row"]')).toHaveLength(0)
  })

  it('validates alias rows and does not call sync APIs for incomplete rows', async () => {
    const wrapper = mountDialog(true)
    await flushPromises()
    await wrapper.get('[data-test="add-model-alias"]').trigger('click')
    const rows = wrapper.findAll('[data-test="model-alias-source"]')
    await rows[rows.length - 1].setValue('gpt-incomplete')
    expect(wrapper.find('button.btn-primary').attributes('disabled')).toBeDefined()
    expect(updateSettings).not.toHaveBeenCalled()
  })

  it('edits aliases as source-to-target rows and trims values before saving', async () => {
    const wrapper = mountDialog(true)
    await flushPromises()
    const source = wrapper.get('[data-test="model-alias-source"]')
    const target = wrapper.get('[data-test="model-alias-target"]')
    await source.setValue('  gpt-a  ')
    await target.setValue('  gpt-b  ')
    await wrapper.find('button.btn-primary').trigger('click')
    await flushPromises()
    expect(updateSettings).toHaveBeenCalledWith(expect.objectContaining({
      model_alias_rules: { 'gpt-a': 'gpt-b' }
    }))
  })

  it('rejects duplicate alias sources', async () => {
    const wrapper = mountDialog(true)
    await flushPromises()
    await wrapper.get('[data-test="add-model-alias"]').trigger('click')
    const sources = wrapper.findAll('[data-test="model-alias-source"]')
    const targets = wrapper.findAll('[data-test="model-alias-target"]')
    await sources[0].setValue('gpt-a')
    await targets[0].setValue('gpt-b')
    await sources[1].setValue('gpt-a')
    await targets[1].setValue('gpt-c')
    expect(wrapper.find('button.btn-primary').attributes('disabled')).toBeDefined()
  })

  it('rejects probe intervals outside one to sixty minutes', async () => {
    const wrapper = mountDialog(true, true)
    await flushPromises()
    await wrapper.get('[data-test="probe-interval-minutes"]').setValue(61)
    expect(wrapper.find('button.btn-primary').attributes('disabled')).toBeDefined()
  })
})
