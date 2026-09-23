import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import UserAllowedGroupsModal from '../UserAllowedGroupsModal.vue'

const { listGroups, updateUser, showSuccess } = vi.hoisted(() => ({
  listGroups: vi.fn(),
  updateUser: vi.fn(),
  showSuccess: vi.fn(),
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    groups: { list: listGroups },
    users: { update: updateUser },
  },
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showSuccess }),
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key }),
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
