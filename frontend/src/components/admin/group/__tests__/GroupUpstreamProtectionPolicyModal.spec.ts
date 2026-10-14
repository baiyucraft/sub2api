import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import GroupUpstreamProtectionPolicyModal from '../GroupUpstreamProtectionPolicyModal.vue'
import type { AdminGroup } from '@/types'
import type { GroupUpstreamProtectionPolicy } from '@/api/admin/groups'
import { SCHEDULING_PROTECTION_SETTINGS_UPDATED } from '@/utils/schedulingProtection'

const { getPolicy, updatePolicy, getSettings, showError } = vi.hoisted(() => ({ getPolicy: vi.fn(), updatePolicy: vi.fn(), getSettings: vi.fn(), showError: vi.fn() }))
vi.mock('@/api/admin', () => ({ adminAPI: { upstreamManagement: { getSettings }, groups: { getUpstreamProtectionPolicy: getPolicy, updateUpstreamProtectionPolicy: updatePolicy } } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError, showSuccess: vi.fn() }) }))
vi.mock('vue-i18n', async () => ({ ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'), useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => params ? `${key} ${JSON.stringify(params)}` : key }) }))
const dialog = defineComponent({ props: ['show'], template: '<div v-if="show"><slot/><slot name="footer"/></div>' })
const group = { id: 7, name: '分组 7', platform: 'openai' } as AdminGroup
const policy: GroupUpstreamProtectionPolicy = {
  group_id: 7, group_name: '分组 7', group_platform: 'openai', priority: 'inherit', effective_priority: 'cache_first', global_priority: 'cache_first', priority_source: 'global',
  ttft: { group_id: 7, group_name: '分组 7', group_platform: 'openai', mode: 'inherit', degradation_ttft_seconds: null, min_samples: null, enabled: false, effective_enabled: false, effective_degradation_ttft_seconds: 20, effective_min_samples: 5, global_enabled: false, global_degradation_ttft_seconds: 20, global_min_samples: 5, source: 'global' },
  cache: { mode: 'inherit', threshold_percent: null, recovery_threshold_percent: null, min_valid_requests: null, effective_mode: 'disabled', effective_threshold_percent: 75, effective_recovery_threshold_percent: 80, effective_min_valid_requests: 20, global_mode: 'disabled', global_threshold_percent: 75, global_recovery_threshold_percent: 80, global_min_valid_requests: 20, window_minutes: 60, cooldown_minutes: 10, trial_percent: 10, source: 'global' }
}
const wrappers: ReturnType<typeof mount>[] = []
const render = (selected = group) => {
  const wrapper = mount(GroupUpstreamProtectionPolicyModal, { props: { show: true, group: selected }, global: { stubs: { BaseDialog: dialog, Icon: true, PlatformIcon: true } } })
  wrappers.push(wrapper)
  return wrapper
}
beforeEach(() => { getSettings.mockReset().mockResolvedValue({ scheduling_protection_platforms: ['openai'] }); getPolicy.mockReset().mockResolvedValue(policy); updatePolicy.mockReset().mockResolvedValue(policy); showError.mockReset() })
afterEach(() => { wrappers.forEach(wrapper => wrapper.unmount()); wrappers.length = 0 })

describe('GroupUpstreamProtectionPolicyModal', () => {
  it('atomically saves both custom guards and the chosen priority, even with global protection off', async () => {
    const wrapper = render()
    await flushPromises()
    await wrapper.get('[data-test="protection-priority-ttft_first"]').setValue(true)
    await wrapper.get('[data-test="protection-ttft-mode"]').setValue('enabled')
    await wrapper.get('[data-test="protection-cache-mode"]').setValue('enabled')
    await wrapper.get('[data-test="protection-ttft-threshold"]').setValue('15')
    await wrapper.get('[data-test="protection-cache-threshold_percent"]').setValue('70')
    expect(wrapper.get('[data-test="protection-cache-effective"]').text()).toContain('modes.protect')
    expect(wrapper.get('[data-test="protection-cache-effective"]').text()).toContain('"window":60')
    expect(wrapper.findAll('[data-test^="protection-cache-"] input')).toHaveLength(0)
    await wrapper.get('[data-test="protection-save"]').trigger('click')
    await flushPromises()
    expect(updatePolicy).toHaveBeenCalledTimes(1)
    expect(updatePolicy).toHaveBeenCalledWith(7, { priority: 'ttft_first', ttft: { mode: 'enabled', degradation_ttft_seconds: 15, min_samples: 5 }, cache: { mode: 'enabled', threshold_percent: 70, recovery_threshold_percent: 80, min_valid_requests: 20 } })
    expect(wrapper.emitted('saved')).toHaveLength(1)
    expect(wrapper.emitted('close')).toHaveLength(1)
  })

  it('preserves inherited observe mode and limits cache customization to thresholds and sample minimum', async () => {
    getPolicy.mockResolvedValue({ ...policy, cache: { ...policy.cache, global_mode: 'observe' } })
    const wrapper = render()
    await flushPromises()
    expect(wrapper.get('[data-test="protection-cache-effective"]').text()).toContain('modes.observe')
    expect(wrapper.find('[data-test="protection-cache-window_minutes"]').exists()).toBe(false)
    expect(wrapper.find('[data-test="protection-cache-cooldown_minutes"]').exists()).toBe(false)
    expect(wrapper.find('[data-test="protection-cache-trial_percent"]').exists()).toBe(false)
    await wrapper.get('[data-test="protection-cache-mode"]').setValue('enabled')
    await wrapper.get('[data-test="protection-cache-recovery_threshold_percent"]').setValue(75)
    expect(wrapper.get('[data-test="protection-save"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-test="protection-save"]').trigger('click')
    expect(updatePolicy).not.toHaveBeenCalled()
  })

  it('rejects fractional integer fields while allowing decimal cache thresholds', async () => {
    const wrapper = render()
    await flushPromises()
    await wrapper.get('[data-test="protection-ttft-mode"]').setValue('enabled')
    await wrapper.get('[data-test="protection-ttft-threshold"]').setValue('15.5')
    expect(wrapper.get('[data-test="protection-save"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-test="protection-save"]').trigger('click')
    expect(updatePolicy).not.toHaveBeenCalled()
    await wrapper.get('[data-test="protection-ttft-threshold"]').setValue('15')
    await wrapper.get('[data-test="protection-ttft-samples"]').setValue('5.5')
    expect(wrapper.get('[data-test="protection-save"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-test="protection-ttft-samples"]').setValue('5')
    await wrapper.get('[data-test="protection-cache-mode"]').setValue('enabled')
    await wrapper.get('[data-test="protection-cache-min_valid_requests"]').setValue('20.5')
    expect(wrapper.get('[data-test="protection-save"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-test="protection-cache-min_valid_requests"]').setValue('20')
    await wrapper.get('[data-test="protection-cache-threshold_percent"]').setValue('70.5')
    await wrapper.get('[data-test="protection-cache-recovery_threshold_percent"]').setValue('80.5')
    expect(wrapper.get('[data-test="protection-save"]').attributes('disabled')).toBeUndefined()
    await wrapper.get('[data-test="protection-save"]').trigger('click')
    await flushPromises()
    expect(updatePolicy).toHaveBeenCalledTimes(1)
    expect(updatePolicy).toHaveBeenCalledWith(7, expect.objectContaining({
      ttft: { mode: 'enabled', degradation_ttft_seconds: 15, min_samples: 5 },
      cache: { mode: 'enabled', threshold_percent: 70.5, recovery_threshold_percent: 80.5, min_valid_requests: 20 }
    }))
  })

  it('allows cache customization on a platform that does not support TTFT', async () => {
    getSettings.mockResolvedValue({ scheduling_protection_platforms: ['anthropic'] })
    getPolicy.mockResolvedValue({ ...policy, group_platform: 'anthropic' })
    const wrapper = render({ ...group, platform: 'anthropic' })
    await flushPromises()
    expect(wrapper.get('[data-test="protection-ttft-mode"] option[value="enabled"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-test="protection-cache-mode"]').setValue('enabled')
    expect(wrapper.get('[data-test="protection-save"]').attributes('disabled')).toBeUndefined()
  })

  it.each(['anthropic', 'composite'])('closes direct access to an excluded group (%s) before reading or editing its policy', async platform => {
    const wrapper = render({ ...group, platform })
    await flushPromises()
    expect(getPolicy).not.toHaveBeenCalled()
    expect(updatePolicy).not.toHaveBeenCalled()
    expect(wrapper.find('[data-test="protection-cache-mode"]').exists()).toBe(false)
    expect(wrapper.emitted('close')).toHaveLength(1)
  })

  it('closes when the authoritative policy projection excludes the platform', async () => {
    getPolicy.mockResolvedValue({ ...policy, platform_selected: false })
    const wrapper = render()
    await flushPromises()
    expect(wrapper.find('[data-test="protection-save"]').exists()).toBe(false)
    expect(wrapper.emitted('close')).toHaveLength(1)
  })

  it('rechecks scope before saving and closes without updating a removed platform', async () => {
    const wrapper = render()
    await flushPromises()
    getSettings.mockResolvedValue({ scheduling_protection_platforms: [] })
    await wrapper.get('[data-test="protection-save"]').trigger('click')
    await flushPromises()
    expect(updatePolicy).not.toHaveBeenCalled()
    expect(wrapper.emitted('close')).toHaveLength(1)
    expect(wrapper.find('[data-test="protection-cache-mode"]').exists()).toBe(false)
  })

  it('closes an already-open dialog when a saved global range removes its platform', async () => {
    const wrapper = render()
    await flushPromises()
    getSettings.mockResolvedValue({ scheduling_protection_platforms: ['antigravity'] })
    window.dispatchEvent(new Event(SCHEDULING_PROTECTION_SETTINGS_UPDATED))
    await flushPromises()
    expect(wrapper.emitted('close')).toHaveLength(1)
    expect(wrapper.find('[data-test="protection-cache-mode"]').exists()).toBe(false)
    expect(updatePolicy).not.toHaveBeenCalled()
  })

  it.each([
    { code: 400, reason: 'GROUP_PROTECTION_PLATFORM_NOT_SELECTED' },
    { response: { data: { code: 400, reason: 'GROUP_PROTECTION_PLATFORM_NOT_SELECTED' } } }
  ])('honors the backend platform rejection if the range changes after the last check (%j)', async error => {
    updatePolicy.mockRejectedValue(error)
    const wrapper = render()
    await flushPromises()
    await wrapper.get('[data-test="protection-save"]').trigger('click')
    await flushPromises()
    expect(wrapper.emitted('close')).toHaveLength(1)
    expect(wrapper.emitted('saved')).toBeUndefined()
    expect(wrapper.find('[data-test="protection-cache-mode"]').exists()).toBe(false)
  })

  it('ignores an old focus-check failure after switching to another group', async () => {
    const wrapper = render()
    await flushPromises()
    let reject!: (reason: Error) => void
    getSettings.mockImplementationOnce(() => new Promise((_, fail) => { reject = fail }))
    window.dispatchEvent(new Event('focus'))
    await flushPromises()
    getPolicy.mockResolvedValue({ ...policy, group_id: 8, group_name: 'New group' })
    await wrapper.setProps({ group: { ...group, id: 8, name: 'New group' } })
    await flushPromises()
    reject(new Error('old scope failed'))
    await flushPromises()
    expect(wrapper.get('[data-test="protection-cache-mode"]').exists()).toBe(true)
    expect(wrapper.get('[data-test="protection-save"]').attributes('disabled')).toBeUndefined()
    expect(wrapper.emitted('close')).toBeUndefined()
    expect(showError).not.toHaveBeenCalled()
  })

  it('ignores an old save response after switching to another group', async () => {
    const wrapper = render()
    await flushPromises()
    let resolve!: (value: GroupUpstreamProtectionPolicy) => void
    updatePolicy.mockImplementationOnce(() => new Promise(done => { resolve = done }))
    await wrapper.get('[data-test="protection-save"]').trigger('click')
    await flushPromises()
    expect(updatePolicy).toHaveBeenCalledOnce()
    getPolicy.mockResolvedValue({ ...policy, group_id: 8, group_name: 'New group' })
    await wrapper.setProps({ group: { ...group, id: 8, name: 'New group' } })
    await flushPromises()
    resolve(policy)
    await flushPromises()
    expect(wrapper.get('[data-test="protection-cache-mode"]').exists()).toBe(true)
    expect(wrapper.get('[data-test="protection-save"]').attributes('disabled')).toBeUndefined()
    expect(wrapper.emitted('close')).toBeUndefined()
    expect(wrapper.emitted('saved')).toBeUndefined()
  })

  it('keeps failed saves open and blocks saves after stale loading fails', async () => {
    const wrapper = render()
    await flushPromises()
    updatePolicy.mockRejectedValueOnce(new Error('conflict'))
    await wrapper.get('[data-test="protection-save"]').trigger('click')
    await flushPromises()
    expect(showError).toHaveBeenCalledTimes(1)
    expect(wrapper.emitted('close')).toBeUndefined()
    getPolicy.mockRejectedValueOnce(new Error('load failed'))
    await wrapper.setProps({ show: false })
    await wrapper.setProps({ show: true })
    await flushPromises()
    expect(wrapper.get('[data-test="protection-save"]').attributes('disabled')).toBeDefined()
    expect(wrapper.find('[data-test="protection-cache-mode"]').exists()).toBe(false)
  })
})
