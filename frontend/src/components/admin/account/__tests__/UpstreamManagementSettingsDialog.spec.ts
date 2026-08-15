import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import UpstreamManagementSettingsDialog from '../UpstreamManagementSettingsDialog.vue'

const { getSettings, getCandidates, updateSettings, showError, showSuccess } = vi.hoisted(() => ({
  getSettings: vi.fn(),
  getCandidates: vi.fn(),
  updateSettings: vi.fn(),
  showError: vi.fn(),
  showSuccess: vi.fn()
}))

vi.mock('@/api/admin/upstreamManagement', () => ({
  default: {
    getSettings,
    getProbeModelCandidates: getCandidates,
    updateSettings
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
    getCandidates.mockReset()
    updateSettings.mockReset()
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
      probe_interval_seconds: 420
    })
    getCandidates.mockResolvedValue({ candidates: {
      openai: ['gpt-live', 'gpt-fallback'],
      anthropic: ['claude-live'],
      gemini: ['gemini-live']
    } })
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
      probe_interval_seconds: 300
    })
  })

  function mountDialog(show = true) {
    return mount(UpstreamManagementSettingsDialog, {
      props: { show },
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

  it('does not persist a draft on close and saves all settings atomically', async () => {
    const wrapper = mountDialog(true)
    await flushPromises()
    const inputs = wrapper.findAll('input[type="number"]')
    await inputs[0].setValue(45)
    expect((wrapper.get('[data-test="probe-interval-minutes"]').element as HTMLInputElement).value).toBe('7')
    await wrapper.findAll('[data-test="select"]')[0].trigger('click')
    await wrapper.find('[data-test="close"]').trigger('click')
    expect(updateSettings).not.toHaveBeenCalled()

    await wrapper.setProps({ show: false })
    await wrapper.setProps({ show: true })
    await flushPromises()
    expect((wrapper.findAll('input[type="number"]')[0].element as HTMLInputElement).value).toBe('30')

    await wrapper.findAll('[data-test="select"]')[0].trigger('click')
    await wrapper.get('[data-test="probe-interval-minutes"]').setValue(12)
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
      probe_models: expect.objectContaining({ openai: 'custom-model' }),
      probe_interval_seconds: 720
    }))
  })

  it('rejects probe intervals outside one to sixty minutes', async () => {
    const wrapper = mountDialog(true)
    await flushPromises()
    await wrapper.get('[data-test="probe-interval-minutes"]').setValue(61)
    expect(wrapper.find('button.btn-primary').attributes('disabled')).toBeDefined()
  })
})
