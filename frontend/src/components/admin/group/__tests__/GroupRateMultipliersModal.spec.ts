import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import GroupRateMultipliersModal from '../GroupRateMultipliersModal.vue'

const { getGroupRateMultipliers, batchSetGroupRateMultipliers, showSuccess, showError } = vi.hoisted(() => ({
  getGroupRateMultipliers: vi.fn(),
  batchSetGroupRateMultipliers: vi.fn(),
  showSuccess: vi.fn(),
  showError: vi.fn(),
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    groups: { getGroupRateMultipliers, batchSetGroupRateMultipliers },
    users: { list: vi.fn() },
  },
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showSuccess,
    showError,
    cachedPublicSettings: null,
  }),
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key }),
}))

const baseGroup = {
  id: 3,
  name: 'OpenAI',
  platform: 'openai',
  rate_multiplier: 0.5,
  profit_control_enabled: false,
}

const serverEntry = {
  user_id: 7,
  user_name: 'user',
  user_email: 'user@example.test',
  user_notes: '',
  user_status: 'active',
  rate_percent: 50,
  rate_multiplier: 0.25,
  rpm_override: null,
}

const mountModal = async () => {
  const wrapper = mount(GroupRateMultipliersModal, {
    props: { show: false, group: baseGroup as never },
    global: {
      stubs: {
        BaseDialog: {
          props: ['show', 'title'],
          template: '<div v-if="show"><slot /></div>',
        },
        Pagination: true,
        Icon: true,
        PlatformIcon: true,
      },
    },
  })
  await wrapper.setProps({ show: true })
  await flushPromises()
  return wrapper
}

describe('GroupRateMultipliersModal percentage persistence', () => {
  beforeEach(() => {
    getGroupRateMultipliers.mockReset()
    batchSetGroupRateMultipliers.mockReset()
    showSuccess.mockReset()
    showError.mockReset()
    getGroupRateMultipliers.mockResolvedValue([serverEntry])
    batchSetGroupRateMultipliers.mockResolvedValue({ message: 'ok' })
    vi.spyOn(window, 'confirm').mockReturnValue(true)
  })

  it('keeps the percentage when the ordinary group rate changes', async () => {
    const wrapper = await mountModal()

    expect((wrapper.get('[data-test="entry-rate-percent-7"]').element as HTMLInputElement).value).toBe('50')
    expect((wrapper.get('[data-test="entry-effective-rate-7"]').element as HTMLInputElement).value).toBe('0.25')

    await wrapper.setProps({ group: { ...baseGroup, rate_multiplier: 0.8 } as never })

    expect((wrapper.get('[data-test="entry-rate-percent-7"]').element as HTMLInputElement).value).toBe('50')
    expect((wrapper.get('[data-test="entry-effective-rate-7"]').element as HTMLInputElement).value).toBe('0.4')
  })

  it('converts effective-rate edits back to percentage and saves only rate_percent', async () => {
    const wrapper = await mountModal()
    await wrapper.setProps({ group: { ...baseGroup, rate_multiplier: 0.8 } as never })

    await wrapper.get('[data-test="entry-effective-rate-7"]').setValue('0.2')
    expect((wrapper.get('[data-test="entry-rate-percent-7"]').element as HTMLInputElement).value).toBe('25')

    await wrapper.get('[data-test="save-rate-multipliers"]').trigger('click')
    await flushPromises()

    expect(batchSetGroupRateMultipliers).toHaveBeenCalledWith(3, [
      { user_id: 7, rate_percent: 25 },
    ])
  })

  it('applies batch factor and direct percentage to the canonical percentage', async () => {
    const wrapper = await mountModal()

    await wrapper.get('[data-test="batch-rate-factor-input"]').setValue('2')
    await wrapper.get('[data-test="apply-batch-factor"]').trigger('click')
    expect((wrapper.get('[data-test="entry-rate-percent-7"]').element as HTMLInputElement).value).toBe('100')
    expect((wrapper.get('[data-test="entry-effective-rate-7"]').element as HTMLInputElement).value).toBe('0.5')

    await wrapper.get('[data-test="batch-rate-percent-input"]').setValue('30')
    await wrapper.get('[data-test="apply-batch-percent"]').trigger('click')
    expect((wrapper.get('[data-test="entry-rate-percent-7"]').element as HTMLInputElement).value).toBe('30')
    expect((wrapper.get('[data-test="entry-effective-rate-7"]').element as HTMLInputElement).value).toBe('0.15')
  })

  it('preserves explicit zero and clearing semantics', async () => {
    const zeroWrapper = await mountModal()
    await zeroWrapper.get('[data-test="entry-rate-percent-7"]').setValue('0')
    await zeroWrapper.get('[data-test="save-rate-multipliers"]').trigger('click')
    await flushPromises()
    expect(batchSetGroupRateMultipliers).toHaveBeenLastCalledWith(3, [
      { user_id: 7, rate_percent: 0 },
    ])

    const clearWrapper = await mountModal()
    await clearWrapper.get('[data-test="remove-rate-entry-7"]').trigger('click')
    await clearWrapper.get('[data-test="save-rate-multipliers"]').trigger('click')
    await flushPromises()
    expect(batchSetGroupRateMultipliers).toHaveBeenLastCalledWith(3, [])
  })
})
