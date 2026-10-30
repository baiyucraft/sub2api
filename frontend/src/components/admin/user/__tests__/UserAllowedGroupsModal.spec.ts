import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import UserAllowedGroupsModal from '../UserAllowedGroupsModal.vue'

const { listGroups, updateUser, showSuccess, showError } = vi.hoisted(() => ({
  listGroups: vi.fn(),
  updateUser: vi.fn(),
  showSuccess: vi.fn(),
  showError: vi.fn(),
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    groups: { list: listGroups },
    users: { update: updateUser },
  },
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showSuccess, showError }),
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => params ? `${key} ${JSON.stringify(params)}` : key }),
}))

afterEach(() => vi.restoreAllMocks())

const group = {
  id: 3,
  name: 'OpenAI',
  platform: 'openai',
  rate_multiplier: 0.5,
  is_exclusive: true,
  subscription_type: 'standard',
  status: 'active',
}

const user = {
  id: 7,
  email: 'user@example.test',
  allowed_groups: [3],
  restrict_public_groups: false,
  group_rate_percents: { 3: 50 },
  group_rates: { 3: 0.25 },
}

const mountModal = async (ordinaryRate = 0.5) => {
  listGroups.mockResolvedValue({ items: [{ ...group, rate_multiplier: ordinaryRate }] })
  const wrapper = mount(UserAllowedGroupsModal, {
    props: { show: false, user: user as never },
    global: {
      stubs: {
        BaseDialog: {
          props: ['show', 'title'],
          template: '<div v-if="show"><slot /><slot name="footer" /></div>',
        },
        PlatformIcon: true,
      },
    },
  })
  await wrapper.setProps({ show: true })
  await flushPromises()
  return wrapper
}

describe('UserAllowedGroupsModal percentage persistence', () => {
  beforeEach(() => {
    listGroups.mockReset()
    updateUser.mockReset()
    showSuccess.mockReset()
    updateUser.mockResolvedValue({})
    vi.spyOn(window, 'confirm').mockReturnValue(true)
  })

  it('keeps the stored percentage while deriving the effective rate from the current group rate', async () => {
    const first = await mountModal(0.5)
    expect((first.get('[data-test="group-rate-percent-3"]').element as HTMLInputElement).value).toBe('50')
    expect((first.get('[data-test="group-effective-rate-3"]').element as HTMLInputElement).value).toBe('0.25')
    first.unmount()

    const changed = await mountModal(0.8)
    expect((changed.get('[data-test="group-rate-percent-3"]').element as HTMLInputElement).value).toBe('50')
    expect((changed.get('[data-test="group-effective-rate-3"]').element as HTMLInputElement).value).toBe('0.4')
  })

  it('converts effective-rate edits and sends group_rate_percents without group_rates', async () => {
    const wrapper = await mountModal(0.8)

    await wrapper.get('[data-test="group-effective-rate-3"]').setValue('0.2')
    expect((wrapper.get('[data-test="group-rate-percent-3"]').element as HTMLInputElement).value).toBe('25')

    await wrapper.get('[data-test="save-group-config"]').trigger('click')
    await flushPromises()

    expect(updateUser).toHaveBeenCalledWith(7, {
      allowed_groups: [3],
      restrict_public_groups: false,
      group_rate_percents: { 3: 25 },
    })
    expect(updateUser.mock.calls[0][1]).not.toHaveProperty('group_rates')
  })

  it('preserves explicit zero and clear operations', async () => {
    const zeroWrapper = await mountModal()
    await zeroWrapper.get('[data-test="group-rate-percent-3"]').setValue('0')
    await zeroWrapper.get('[data-test="save-group-config"]').trigger('click')
    await flushPromises()
    expect(updateUser).toHaveBeenLastCalledWith(7, expect.objectContaining({
      group_rate_percents: { 3: 0 },
    }))

    const clearWrapper = await mountModal()
    await clearWrapper.get('[data-test="group-rate-percent-3"]').setValue('')
    await clearWrapper.get('[data-test="save-group-config"]').trigger('click')
    await flushPromises()
    expect(updateUser).toHaveBeenLastCalledWith(7, expect.objectContaining({
      group_rate_percents: { 3: null },
    }))
  })
})

const response = { items: [group] }
async function openDialog() {
  const wrapper = mount(UserAllowedGroupsModal, {
    props: { show: false, user: user as never },
    global: { stubs: { BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /><slot name="footer" /></div>' }, PlatformIcon: true } }
  })
  await wrapper.setProps({ show: true })
  return wrapper
}

describe('UserAllowedGroupsModal load readiness', () => {
  beforeEach(() => {
    listGroups.mockReset()
    updateUser.mockReset()
    vi.spyOn(console, 'error').mockImplementation(() => {})
    listGroups.mockResolvedValue(response)
    updateUser.mockResolvedValue(undefined)
  })

  it('cannot save an empty configuration while groups are loading', async () => {
    let resolve!: (value: typeof response) => void
    listGroups.mockReturnValueOnce(new Promise(res => { resolve = res }))
    const wrapper = await openDialog()
    const save = wrapper.get('button.btn-primary')
    expect(save.attributes('disabled')).toBeDefined()
    await save.trigger('click')
    expect(updateUser).not.toHaveBeenCalled()
    resolve(response)
    await flushPromises()
    expect(save.attributes('disabled')).toBeUndefined()
  })

  it('cannot save after loading fails', async () => {
    listGroups.mockRejectedValueOnce(new Error('Offline'))
    const wrapper = await openDialog()
    await flushPromises()
    const save = wrapper.get('button.btn-primary')
    expect(save.attributes('disabled')).toBeDefined()
    await save.trigger('click')
    expect(updateUser).not.toHaveBeenCalled()
  })

  it('does not reuse a previous successful load after reopening fails', async () => {
    const wrapper = await openDialog()
    await flushPromises()
    await wrapper.setProps({ show: false })
    listGroups.mockRejectedValueOnce(new Error('Offline'))
    await wrapper.setProps({ show: true })
    await flushPromises()
    expect(wrapper.get('button.btn-primary').attributes('disabled')).toBeDefined()
    expect(updateUser).not.toHaveBeenCalled()
  })

  it('preserves existing grants and rates on a successful save', async () => {
    const wrapper = await openDialog()
    await flushPromises()
    await wrapper.get('button.btn-primary').trigger('click')
    await flushPromises()
    expect(updateUser).toHaveBeenCalledWith(7, { allowed_groups: [3], restrict_public_groups: false, group_rate_percents: { 3: 50 } })
    expect(wrapper.emitted('success')).toHaveLength(1)
  })
})

async function mountConfiguredModal(userOverrides: Record<string, unknown> = {}, groupList = [group]) {
  listGroups.mockResolvedValue({ items: groupList })
  const wrapper = mount(UserAllowedGroupsModal, {
    props: { show: false, user: { ...user, ...userOverrides } as never },
    global: {
      stubs: {
        BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /><slot name="footer" /></div>' },
        PlatformIcon: true,
      },
    },
  })
  await wrapper.setProps({ show: true })
  await flushPromises()
  return wrapper
}

describe('UserAllowedGroupsModal default group percentage', () => {
  beforeEach(() => {
    listGroups.mockReset()
    updateUser.mockReset()
    showSuccess.mockReset()
    showError.mockReset()
    updateUser.mockResolvedValue({})
    vi.spyOn(window, 'confirm').mockReturnValue(true)
  })

  it('previews the inherited percentage without creating group-specific overrides', async () => {
    const wrapper = await mountConfiguredModal({ default_group_rate_percent: 95, group_rate_percents: {}, group_rates: {} })
    expect((wrapper.get('[data-test="default-group-rate-percent"]').element as HTMLInputElement).value).toBe('95')
    expect((wrapper.get('[data-test="group-rate-percent-3"]').element as HTMLInputElement).value).toBe('')
    expect(wrapper.get('[data-test="group-rate-percent-3"]').attributes('placeholder')).toBe('95')
    expect((wrapper.get('[data-test="group-effective-rate-3"]').element as HTMLInputElement).value).toBe('0.475')
    expect(wrapper.get('[data-test="group-rate-inheritance-3"]').text()).toContain('95')
    await wrapper.get('[data-test="save-group-config"]').trigger('click')
    await flushPromises()
    expect(updateUser).toHaveBeenCalledWith(7, { allowed_groups: [3], restrict_public_groups: false, group_rate_percents: undefined })
  })

  it('changes the default while preserving visible and hidden exceptions and grants', async () => {
    const wrapper = await mountConfiguredModal({ allowed_groups: [3, 99], group_rate_percents: { 3: 50, 99: 0 } })
    await wrapper.get('[data-test="default-group-rate-percent"]').setValue('95')
    expect((wrapper.get('[data-test="group-effective-rate-3"]').element as HTMLInputElement).value).toBe('0.25')
    expect(wrapper.get('[data-test="group-rate-override-count"]').text()).toContain('"count":2')
    await wrapper.get('[data-test="save-group-config"]').trigger('click')
    await flushPromises()
    const payload = updateUser.mock.calls[0][1]
    expect(payload).toEqual({ allowed_groups: [99, 3], restrict_public_groups: false, group_rate_percents: { 3: 50 }, default_group_rate_percent: 95 })
    expect(payload).not.toHaveProperty('reset_group_rate_percents')
    expect(payload).not.toHaveProperty('group_rates')
    expect(payload).not.toHaveProperty('rpm_override')
  })

  it('sends null when clearing the default and previews 100 percent', async () => {
    const wrapper = await mountConfiguredModal({ default_group_rate_percent: 95, group_rate_percents: {}, group_rates: {} })
    await wrapper.get('[data-test="default-group-rate-percent"]').setValue('')
    expect((wrapper.get('[data-test="group-effective-rate-3"]').element as HTMLInputElement).value).toBe('0.5')
    expect(wrapper.get('[data-test="group-rate-percent-3"]').attributes('placeholder')).toBe('100')
    await wrapper.get('[data-test="save-group-config"]').trigger('click')
    await flushPromises()
    expect(updateUser).toHaveBeenCalledWith(7, expect.objectContaining({ default_group_rate_percent: null }))
  })

  it('keeps explicit group zero ahead of a nonzero default and does not reconfirm it', async () => {
    const wrapper = await mountConfiguredModal({ default_group_rate_percent: 95, group_rate_percents: { 3: 0 }, group_rates: { 3: 0 } })
    expect((wrapper.get('[data-test="group-effective-rate-3"]').element as HTMLInputElement).value).toBe('0')
    expect(wrapper.find('[data-test="group-rate-inheritance-3"]').exists()).toBe(false)
    await wrapper.get('[data-test="save-group-config"]').trigger('click')
    await flushPromises()
    expect(window.confirm).not.toHaveBeenCalled()
    expect(updateUser).toHaveBeenCalledWith(7, expect.objectContaining({ group_rate_percents: { 3: 0 } }))
  })

  it('clears a single override to inherit the default rather than saving the inherited value', async () => {
    const wrapper = await mountConfiguredModal({ default_group_rate_percent: 95 })
    await wrapper.get('[data-test="group-rate-percent-3"]').setValue('')
    expect((wrapper.get('[data-test="group-effective-rate-3"]').element as HTMLInputElement).value).toBe('0.475')
    await wrapper.get('[data-test="save-group-config"]').trigger('click')
    await flushPromises()
    expect(updateUser).toHaveBeenCalledWith(7, expect.objectContaining({ group_rate_percents: { 3: null } }))
  })

  it('counts hidden and legacy overrides, resets locally and saves a new exception atomically', async () => {
    const wrapper = await mountConfiguredModal({ group_rate_percents: { 3: 0, 99: 50 }, group_rates: { 3: 0, 99: 0.1, 120: 0.2 } })
    await wrapper.get('[data-test="default-group-rate-percent"]').setValue('95')
    expect(wrapper.get('[data-test="group-rate-override-count"]').text()).toContain('"count":3')
    await wrapper.get('[data-test="reset-group-rate-percents"]').trigger('click')
    expect(window.confirm).toHaveBeenCalledWith(expect.stringContaining('"count":3'))
    expect(updateUser).not.toHaveBeenCalled()
    expect(wrapper.get('[data-test="group-rate-override-count"]').text()).toContain('"count":0')
    expect(wrapper.find('[data-test="group-rate-reset-pending"]').exists()).toBe(true)
    expect((wrapper.get('[data-test="group-effective-rate-3"]').element as HTMLInputElement).value).toBe('0.475')
    await wrapper.get('[data-test="group-rate-percent-3"]').setValue('90')
    await wrapper.get('[data-test="save-group-config"]').trigger('click')
    await flushPromises()
    expect(updateUser).toHaveBeenCalledOnce()
    expect(updateUser).toHaveBeenCalledWith(7, { allowed_groups: [3], restrict_public_groups: false, group_rate_percents: { 3: 90 }, default_group_rate_percent: 95, reset_group_rate_percents: true })
  })

  it('saves an explicit reset without sending the ambiguous empty percentage map', async () => {
    const wrapper = await mountConfiguredModal({ default_group_rate_percent: 95 })
    await wrapper.get('[data-test="reset-group-rate-percents"]').trigger('click')
    await wrapper.get('[data-test="save-group-config"]').trigger('click')
    await flushPromises()
    expect(updateUser).toHaveBeenCalledWith(7, { allowed_groups: [3], restrict_public_groups: false, group_rate_percents: undefined, reset_group_rate_percents: true })
  })

  it('does not reset when confirmation is declined and never writes on cancel', async () => {
    const wrapper = await mountConfiguredModal({ default_group_rate_percent: 95 })
    vi.mocked(window.confirm).mockReturnValueOnce(false)
    await wrapper.get('[data-test="reset-group-rate-percents"]').trigger('click')
    expect((wrapper.get('[data-test="group-rate-percent-3"]').element as HTMLInputElement).value).toBe('50')
    expect(wrapper.find('[data-test="group-rate-reset-pending"]').exists()).toBe(false)
    await wrapper.get('[data-test="reset-group-rate-percents"]').trigger('click')
    await wrapper.get('[data-test="cancel-group-config"]').trigger('click')
    expect(updateUser).not.toHaveBeenCalled()
    expect(wrapper.emitted('close')).toHaveLength(1)
    await wrapper.setProps({ show: false })
    await wrapper.setProps({ show: true })
    await flushPromises()
    expect((wrapper.get('[data-test="group-rate-percent-3"]').element as HTMLInputElement).value).toBe('50')
    expect(wrapper.find('[data-test="group-rate-reset-pending"]').exists()).toBe(false)
  })

  it('inherits the default for newly added public groups without granting exclusive access', async () => {
    const wrapper = await mountConfiguredModal({ default_group_rate_percent: 95, allowed_groups: [], group_rate_percents: {}, group_rates: {} }, [group, { ...group, id: 4, name: 'New public group', is_exclusive: false, rate_multiplier: 0.2 }])
    expect((wrapper.get('[data-test="group-effective-rate-4"]').element as HTMLInputElement).value).toBe('0.19')
    expect(wrapper.get('[data-test="group-rate-inheritance-4"]').text()).toContain('95')
    await wrapper.get('[data-test="save-group-config"]').trigger('click')
    await flushPromises()
    expect(updateUser).toHaveBeenCalledWith(7, expect.objectContaining({ allowed_groups: [] }))
  })

  it('requires confirmation for a newly configured zero default and preserves zero in the payload', async () => {
    const wrapper = await mountConfiguredModal({ group_rate_percents: {}, group_rates: {} })
    await wrapper.get('[data-test="default-group-rate-percent"]').setValue('0')
    expect((wrapper.get('[data-test="group-effective-rate-3"]').element as HTMLInputElement).value).toBe('0')
    vi.mocked(window.confirm).mockReturnValueOnce(false)
    await wrapper.get('[data-test="save-group-config"]').trigger('click')
    expect(window.confirm).toHaveBeenCalledWith('admin.users.defaultGroupZeroRateConfirm')
    expect(updateUser).not.toHaveBeenCalled()
    await wrapper.get('[data-test="save-group-config"]').trigger('click')
    await flushPromises()
    expect(updateUser).toHaveBeenCalledWith(7, expect.objectContaining({ default_group_rate_percent: 0 }))
  })

  it('does not repeatedly confirm an unchanged zero default', async () => {
    const wrapper = await mountConfiguredModal({ default_group_rate_percent: 0, group_rate_percents: {}, group_rates: {} })
    await wrapper.get('[data-test="save-group-config"]').trigger('click')
    await flushPromises()
    expect(window.confirm).not.toHaveBeenCalled()
  })

  it('blocks invalid negative defaults instead of treating them as clear operations', async () => {
    const wrapper = await mountConfiguredModal({ default_group_rate_percent: 95 })
    await wrapper.get('[data-test="default-group-rate-percent"]').setValue('-1')
    expect((wrapper.get('[data-test="default-group-rate-percent"]').element as HTMLInputElement).value).toBe('-1')
    expect(wrapper.get('[role="alert"]').text()).toBe('admin.users.defaultGroupRateInvalid')
    expect(wrapper.get('[data-test="save-group-config"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-test="save-group-config"]').trigger('click')
    expect(updateUser).not.toHaveBeenCalled()
  })

  it('keeps pending edits open and reports a failed atomic save', async () => {
    vi.spyOn(console, 'error').mockImplementation(() => {})
    updateUser.mockRejectedValueOnce(new Error('Storage failure'))
    const wrapper = await mountConfiguredModal({ default_group_rate_percent: 95 })
    await wrapper.get('[data-test="reset-group-rate-percents"]').trigger('click')
    await wrapper.get('[data-test="save-group-config"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-test="group-rate-reset-pending"]').exists()).toBe(true)
    expect(showError).toHaveBeenCalledWith('admin.users.failedToUpdateAllowedGroups')
    expect(showSuccess).not.toHaveBeenCalled()
    expect(wrapper.emitted('close')).toBeUndefined()
  })
})
