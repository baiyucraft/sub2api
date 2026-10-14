import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import UpstreamManagementSettingsDialog from '../UpstreamManagementSettingsDialog.vue'
import zh from '@/i18n/locales/zh/admin/upstreamManagement'
import { CONCRETE_PLATFORM_OPTIONS } from '@/constants/platforms'

const { getGroups, getSettings, getProbeSettings, getCandidates, updateSettings, updateProbeSettings, showError, showSuccess } = vi.hoisted(() => ({
  getGroups: vi.fn(),
  getSettings: vi.fn(),
  getProbeSettings: vi.fn(),
  getCandidates: vi.fn(),
  updateSettings: vi.fn(),
  updateProbeSettings: vi.fn(),
  showError: vi.fn(),
  showSuccess: vi.fn()
}))

vi.mock('@/api/admin/groups', () => ({ default: { getAllIncludingInactive: getGroups } }))

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
    useI18n: () => ({ t: (key: string) => {
      if (key === 'admin.upstreamManagement.confidenceProbe.window') return zh.upstreamManagement.confidenceProbe.window
      if (key === 'admin.upstreamManagement.confidenceProbe.samplePolicy') return zh.upstreamManagement.confidenceProbe.samplePolicy
      return key
    } })
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
    getGroups.mockReset().mockResolvedValue([{ id: 1, name: 'OpenAI', platform: 'openai' }, { id: 2, name: 'Claude', platform: 'anthropic' }])
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
      model_alias_rules: { 'gpt-5.6-luna': 'gpt-5.6-terra' },
      pool_mode_retry_status_codes: [401, 403, 429]
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
      model_alias_rules: { 'gpt-5.6-luna': 'gpt-5.6-terra' },
      pool_mode_retry_status_codes: [401, 403, 429]
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
          Icon: true,
          PlatformIcon: true
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

  it('defaults cache protection to off and saves the group scope without changing retries', async () => {
    const wrapper = mountDialog()
    await flushPromises()
    expect(wrapper.get('[data-test="cache-guard-mode"]').element).toHaveProperty('value', 'disabled')
    await wrapper.get('[data-test="cache-guard-mode"]').setValue('observe')
    await wrapper.get('button.btn-primary').trigger('click')
    await flushPromises()
    expect(updateSettings).toHaveBeenCalledWith(expect.objectContaining({
      scheduling_protection_platforms: ['openai'],
      cache_guard: { mode: 'observe', group_ids: [], threshold_percent: 75, window_minutes: 60, min_valid_requests: 20, recovery_threshold_percent: 80, cooldown_minutes: 10, trial_percent: 10 },
      pool_mode_retry_status_codes: [401, 403, 429]
    }))
  })

  it('uses the concrete catalog for accessible platform checkboxes and saves the selected platforms atomically', async () => {
    const wrapper = mountDialog()
    await flushPromises()
    expect(wrapper.findAll('[data-test="scheduling-protection-platforms"] input[type="checkbox"]')).toHaveLength(CONCRETE_PLATFORM_OPTIONS.length)
    expect(wrapper.get('[data-test="protection-platform-openai"]').element).toHaveProperty('checked', true)
    expect(wrapper.get('[data-test="protection-platform-antigravity"]').element).toHaveProperty('checked', false)
    expect(wrapper.find('[data-test="protection-platform-composite"]').exists()).toBe(false)
    expect(wrapper.get('[data-test="protection-platform-antigravity"]').element.closest('label')?.textContent).toContain('Antigravity')
    await wrapper.get('[data-test="protection-platform-antigravity"]').setValue(true)
    await wrapper.get('button.btn-primary').trigger('click')
    await flushPromises()
    expect(updateSettings).toHaveBeenCalledOnce()
    expect(updateSettings).toHaveBeenCalledWith(expect.objectContaining({ scheduling_protection_platforms: ['antigravity', 'openai'], ttft_guard: { enabled: true, degradation_ttft_seconds: 30, min_samples: 7 } }))
  })

  it('preserves an explicit empty platform scope and allows all platforms to stay off', async () => {
    const settings = await getSettings()
    getSettings.mockResolvedValue({ ...settings, scheduling_protection_platforms: [] })
    const wrapper = mountDialog()
    await flushPromises()
    expect(wrapper.get('[data-test="protection-platform-openai"]').element).toHaveProperty('checked', false)
    expect(wrapper.find('[data-test="protection-platforms-empty"]').exists()).toBe(true)
    expect(wrapper.get('button.btn-primary').attributes('disabled')).toBeUndefined()
    await wrapper.get('button.btn-primary').trigger('click')
    await flushPromises()
    expect(updateSettings).toHaveBeenCalledWith(expect.objectContaining({ scheduling_protection_platforms: [] }))
  })

  it('allows clearing the default scope and reloads the backend selection when reopened', async () => {
    const wrapper = mountDialog()
    await flushPromises()
    await wrapper.get('[data-test="protection-platform-openai"]').setValue(false)
    await wrapper.get('button.btn-primary').trigger('click')
    await flushPromises()
    expect(updateSettings).toHaveBeenCalledWith(expect.objectContaining({ scheduling_protection_platforms: [] }))
    const settings = await getSettings()
    getSettings.mockResolvedValue({ ...settings, scheduling_protection_platforms: ['gemini'] })
    await wrapper.setProps({ show: false })
    await wrapper.setProps({ show: true })
    await flushPromises()
    expect(wrapper.get('[data-test="protection-platform-gemini"]').element).toHaveProperty('checked', true)
    expect(wrapper.get('[data-test="protection-platform-openai"]').element).toHaveProperty('checked', false)
  })

  it('removes the duplicate group selector while preserving compatibility scope fields', async () => {
    const settings = await getSettings()
    getSettings.mockResolvedValue({ ...settings, cache_guard: { mode: 'protect', group_ids: [99, 2], threshold_percent: 75, window_minutes: 60, min_valid_requests: 20, recovery_threshold_percent: 80, cooldown_minutes: 10, trial_percent: 10 } })
    const wrapper = mountDialog()
    await flushPromises()
    expect(wrapper.find('[data-test="cache-guard-groups"]').exists()).toBe(false)
    await wrapper.get('button.btn-primary').trigger('click')
    await flushPromises()
    expect(updateSettings.mock.calls[0]?.[0].cache_guard.group_ids).toEqual([2, 99])
  })

  it('rejects recovery thresholds at or below protection and invalid windows', async () => {
    const wrapper = mountDialog()
    await flushPromises()
    await wrapper.get('[data-test="cache-guard-recovery_threshold_percent"]').setValue(75)
    expect(wrapper.get('button.btn-primary').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-test="cache-guard-recovery_threshold_percent"]').setValue(80)
    await wrapper.get('[data-test="cache-guard-window_minutes"]').setValue(4)
    expect(wrapper.get('button.btn-primary').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-test="cache-guard-window_minutes"]').setValue(5)
    expect(wrapper.get('button.btn-primary').attributes('disabled')).toBeUndefined()
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
      probe_interval_seconds: 420
    }))
    expect(updateSettings.mock.calls[0]?.[0]).not.toHaveProperty('model_alias_rules')
  })

  it('does not render the deprecated global model alias editor', async () => {
    getSettings.mockResolvedValueOnce({
      ttft_guard: { enabled: true, degradation_ttft_seconds: 30, min_samples: 7 },
      probe_guard: { enabled: true, suspend_after_failures: 4, recovery_successes: 2, custom_error_codes_enabled: false, custom_error_codes: [] },
      probe_models: { openai: 'gpt-live', anthropic: 'claude-live', gemini: 'gemini-live' },
      probe_interval_seconds: 300
    })
    const wrapper = mountDialog(true)
    await flushPromises()
    expect(wrapper.findAll('[data-test="model-alias-row"]')).toHaveLength(0)
    expect(wrapper.find('[data-test="add-model-alias"]').exists()).toBe(false)
  })

  it('normalizes retry status codes and confirms the upstream-wide overwrite', async () => {
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(true)
    const wrapper = mountDialog(true)
    await flushPromises()
    await wrapper.get('[data-test="pool-mode-retry-status-codes"]').setValue(' 503, 429, 503 ')
    await wrapper.find('button.btn-primary').trigger('click')
    await flushPromises()
    expect(confirm).toHaveBeenCalledTimes(1)
    expect(updateSettings).toHaveBeenCalledWith(expect.objectContaining({
      pool_mode_retry_status_codes: [429, 503]
    }))
    confirm.mockRestore()
  })

  it('does not submit when clearing retry status codes is cancelled', async () => {
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false)
    const wrapper = mountDialog(true)
    await flushPromises()
    await wrapper.get('[data-test="pool-mode-retry-status-codes"]').setValue('')
    await wrapper.find('button.btn-primary').trigger('click')
    await flushPromises()
    expect(confirm).toHaveBeenCalledTimes(1)
    expect(updateSettings).not.toHaveBeenCalled()
    confirm.mockRestore()
  })

  it('rejects retry status codes outside the HTTP range', async () => {
    const wrapper = mountDialog(true)
    await flushPromises()
    await wrapper.get('[data-test="pool-mode-retry-status-codes"]').setValue('99, 429')
    expect(wrapper.find('button.btn-primary').attributes('disabled')).toBeDefined()
  })

  it('rejects probe intervals outside one to sixty minutes', async () => {
    const wrapper = mountDialog(true, true)
    await flushPromises()
    await wrapper.get('[data-test="probe-interval-minutes"]').setValue(61)
    expect(wrapper.find('button.btn-primary').attributes('disabled')).toBeDefined()
  })

  it('keeps confidence disabled by default and writes the low-effort distribution contract', async () => {
    const wrapper = mountDialog(true, true)
    await flushPromises()
    expect(wrapper.find('[data-test="fixed-openai-probe-model"]').exists()).toBe(false)
    expect(wrapper.find('[data-test="cache-guard-settings"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('128 条有效样本滑动窗口')
    expect(wrapper.get('[data-test="confidence-sample-policy"]').text()).toContain('标点 64 条、国家 16 条、整数 48 条')
    expect(wrapper.get('[data-test="confidence-sample-policy"]').text()).toContain('超时、失败和空回答不占有效窗口')
    expect(wrapper.get('[data-test="confidence-sample-policy"]').text()).toContain('下一次正常探测继续补采')
    await wrapper.find('button.btn-primary').trigger('click')
    await flushPromises()
    expect(updateProbeSettings).toHaveBeenCalledWith(expect.objectContaining({
      confidence_probe: expect.objectContaining({ enabled: false, reasoning_effort: 'low', long_context_enabled: false, quality_degrade_threshold: 0, prompt_version: 'openai-sol-distribution-v1' }),
      probe_models: expect.objectContaining({ openai: 'gpt-live' })
    }))
    expect(updateProbeSettings.mock.calls[0]?.[0]).not.toHaveProperty('cache_guard')
    expect(getGroups).not.toHaveBeenCalled()
  })

  it('fixes the enabled OpenAI model without overwriting the ordinary model', async () => {
    getProbeSettings.mockResolvedValueOnce({
      ttft_guard: { enabled: true, degradation_ttft_seconds: 30, min_samples: 7 },
      probe_guard: { enabled: true, suspend_after_failures: 3, recovery_successes: 2, custom_error_codes_enabled: false, custom_error_codes: [] },
      probe_models: { openai: 'ordinary-original-model', anthropic: 'claude-live', gemini: 'gemini-live' }, probe_interval_seconds: 300,
      confidence_probe: { enabled: true, reasoning_effort: 'high', long_context_enabled: true, long_context_max_tokens: 2048, quality_degrade_threshold: 70, prompt_version: 'openai-juice-multiprobe-v2' }
    })
    const wrapper = mountDialog(true, true)
    await flushPromises()
    expect(wrapper.get('[data-test="fixed-openai-probe-model"]').text()).toBe('gpt-6.1-sol')
    expect(wrapper.findAll('[data-test="select"]')).toHaveLength(2)
    await wrapper.find('button.btn-primary').trigger('click')
    await flushPromises()
    expect(updateProbeSettings).toHaveBeenCalledWith(expect.objectContaining({
      confidence_probe: expect.objectContaining({ enabled: true, reasoning_effort: 'low', long_context_enabled: false, quality_degrade_threshold: 0, prompt_version: 'openai-sol-distribution-v1' }),
      probe_models: expect.objectContaining({ openai: 'ordinary-original-model' })
    }))
    await wrapper.get('[aria-label="admin.upstreamManagement.confidenceProbe.enabled"]').trigger('click')
    expect(wrapper.find('[data-test="fixed-openai-probe-model"]').exists()).toBe(false)
    expect(wrapper.findAll('[data-test="select"]')).toHaveLength(3)
    expect(wrapper.text()).toContain('ordinary-original-model')
  })
})
