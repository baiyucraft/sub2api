import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import GroupTTFTGuardPolicyModal from '../GroupTTFTGuardPolicyModal.vue'
import type { AdminGroup } from '@/types'

const { getPolicy, updatePolicy, showError, showSuccess } = vi.hoisted(() => ({
  getPolicy: vi.fn(),
  updatePolicy: vi.fn(),
  showError: vi.fn(),
  showSuccess: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    groups: {
      getTTFTGuardPolicy: getPolicy,
      updateTTFTGuardPolicy: updatePolicy
    }
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError, showSuccess })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) =>
        params ? `${key} ${JSON.stringify(params)}` : key
    })
  }
})

const BaseDialogStub = defineComponent({
  props: { show: Boolean },
  emits: ['close'],
  template: '<div v-if="show"><slot /><slot name="footer" /></div>'
})

const group = {
  id: 7,
  name: 'Premium OpenAI',
  platform: 'openai'
} as AdminGroup

const policy = {
  group_id: 7,
  group_name: 'Premium OpenAI',
  group_platform: 'openai' as const,
  mode: 'inherit' as const,
  degradation_ttft_seconds: 12,
  min_samples: 4,
  enabled: true,
  effective_enabled: true,
  effective_degradation_ttft_seconds: 20,
  effective_min_samples: 5,
  global_enabled: true,
  global_degradation_ttft_seconds: 20,
  global_min_samples: 5,
  source: 'global' as const
}

const mountModal = () => mount(GroupTTFTGuardPolicyModal, {
  props: { show: true, group },
  global: {
    stubs: {
      BaseDialog: BaseDialogStub,
      PlatformIcon: true,
      Icon: true
    }
  }
})

describe('GroupTTFTGuardPolicyModal', () => {
  beforeEach(() => {
    getPolicy.mockReset().mockResolvedValue(policy)
    updatePolicy.mockReset().mockResolvedValue({
      ...policy,
      mode: 'enabled',
      degradation_ttft_seconds: 15,
      min_samples: 6,
      effective_degradation_ttft_seconds: 15,
      effective_min_samples: 6,
      source: 'group'
    })
    showError.mockReset()
    showSuccess.mockReset()
  })

  it('loads the group policy and displays global and effective values', async () => {
    const wrapper = mountModal()
    await flushPromises()

    expect(getPolicy).toHaveBeenCalledWith(7)
    expect(wrapper.get('[data-test="ttft-policy-mode-inherit"]').classes()).toContain('border-primary-500')
    expect(wrapper.get('[data-test="ttft-effective-values"]').text()).toContain('"threshold":20')
    expect(wrapper.get('[data-test="ttft-effective-values"]').text()).toContain('"samples":5')
  })

  it('validates custom thresholds and submits the enabled policy', async () => {
    const wrapper = mountModal()
    await flushPromises()

    await wrapper.get('[data-test="ttft-policy-mode-enabled"]').trigger('click')
    await wrapper.get('[data-test="ttft-threshold-input"]').setValue('4')
    await wrapper.get('[data-test="ttft-samples-input"]').setValue('1')

    expect(wrapper.get('[data-test="ttft-policy-save"]').attributes()).toHaveProperty('disabled')
    expect(wrapper.get('[data-test="ttft-validation-error"]').text()).toContain('thresholdInvalid')

    await wrapper.get('[data-test="ttft-threshold-input"]').setValue('15')
    await wrapper.get('[data-test="ttft-samples-input"]').setValue('6')
    await wrapper.get('[data-test="ttft-policy-save"]').trigger('click')
    await flushPromises()

    expect(updatePolicy).toHaveBeenCalledWith(7, {
      mode: 'enabled',
      degradation_ttft_seconds: 15,
      min_samples: 6
    })
    expect(wrapper.emitted('saved')).toHaveLength(1)
    expect(wrapper.emitted('close')).toHaveLength(1)
  })

  it('allows disabling without discarding the last custom values', async () => {
    const wrapper = mountModal()
    await flushPromises()

    await wrapper.get('[data-test="ttft-policy-mode-disabled"]').trigger('click')
    expect(wrapper.get('[data-test="ttft-effective-values"]').text()).toContain('admin.groups.ttftGuard.disabled')
    await wrapper.get('[data-test="ttft-policy-save"]').trigger('click')
    await flushPromises()

    expect(updatePolicy).toHaveBeenCalledWith(7, {
      mode: 'disabled',
      degradation_ttft_seconds: 12,
      min_samples: 4
    })
  })

  it('uses only the policy DTO global values', async () => {
    getPolicy.mockResolvedValueOnce({
      ...policy,
      degradation_ttft_seconds: null,
      min_samples: null,
      global_enabled: true,
      global_degradation_ttft_seconds: 37,
      global_min_samples: 9,
      effective_degradation_ttft_seconds: 37,
      effective_min_samples: 9
    })

    const wrapper = mountModal()
    await flushPromises()

    expect(wrapper.get('[data-test="ttft-effective-values"]').text()).toContain('"threshold":37')
    expect(wrapper.get('[data-test="ttft-effective-values"]').text()).toContain('"samples":9')
    expect((wrapper.get('[data-test="ttft-threshold-input"]').element as HTMLInputElement).value).toBe('37')
    expect((wrapper.get('[data-test="ttft-samples-input"]').element as HTMLInputElement).value).toBe('9')
  })

  it('keeps the source labeled as global when an inherited global policy is disabled', async () => {
    getPolicy.mockResolvedValueOnce({
      ...policy,
      enabled: false,
      effective_enabled: false,
      global_enabled: false
    })

    const wrapper = mountModal()
    await flushPromises()

    expect(wrapper.text()).toContain('admin.groups.ttftGuard.sources.global')
  })

  it('clears stale form state and disables save after a reload failure', async () => {
    const wrapper = mountModal()
    await flushPromises()
    expect(wrapper.find('[data-test="ttft-policy-modes"]').exists()).toBe(true)

    getPolicy.mockRejectedValueOnce(new Error('load failed'))
    await wrapper.setProps({ show: false })
    await wrapper.setProps({ show: true })
    await flushPromises()

    expect(showError).toHaveBeenCalledTimes(1)
    expect(wrapper.find('[data-test="ttft-policy-modes"]').exists()).toBe(false)
    expect(wrapper.get('[data-test="ttft-policy-save"]').attributes()).toHaveProperty('disabled')
    expect(updatePolicy).not.toHaveBeenCalled()
  })
})
