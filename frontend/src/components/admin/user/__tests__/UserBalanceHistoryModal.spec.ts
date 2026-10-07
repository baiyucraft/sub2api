import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import type { AdminUser } from '@/types'
import UserBalanceHistoryModal from '../UserBalanceHistoryModal.vue'

const mocks = vi.hoisted(() => ({ getUserBalanceHistory: vi.fn() }))
vi.mock('@/api/admin', () => ({ adminAPI: { users: mocks } }))
vi.mock('@/utils/format', () => ({ formatDateTime: () => 'date' }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => params ? `${key}:${Object.values(params).join(',')}` : key }) }))
enableAutoUnmount(afterEach)
beforeEach(() => { vi.clearAllMocks(); vi.spyOn(console, 'error').mockImplementation(() => {}) })
afterEach(() => vi.restoreAllMocks())
function result(id: number) {
  return { items: [{ id, type: 'admin_balance', value: id, notes: `History ${id}` }], total: 1, total_recharged: id }
}
function deferred() {
  let resolve!: (value: ReturnType<typeof result>) => void
  let reject!: (reason: unknown) => void
  const promise = new Promise<ReturnType<typeof result>>((res, rej) => { resolve = res; reject = rej })
  return { promise, resolve, reject }
}
async function openDialog(extraProps: Record<string, unknown> = {}) {
  const wrapper = mount(UserBalanceHistoryModal, {
    props: { show: false, user: { id: 1, email: 'one@example.com', balance: 1 } as AdminUser, ...extraProps },
    global: { stubs: { BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /></div>' }, Icon: true, Select: true } }
  })
  await wrapper.setProps({ show: true })
  return wrapper
}

describe('UserBalanceHistoryModal request ordering', () => {
  it('reloads the visible dialog for a new user and ignores the previous user response', async () => {
    const old = deferred()
    mocks.getUserBalanceHistory.mockReturnValueOnce(old.promise).mockResolvedValueOnce({ ...result(20), total_rewarded: 15 })
    const wrapper = await openDialog({ initialType: 'activity_reward' })
    await wrapper.setProps({ user: { id: 2, email: 'two@example.com', balance: 2 } as AdminUser })
    await flushPromises()
    expect(mocks.getUserBalanceHistory).toHaveBeenLastCalledWith(2, 1, 15, 'activity_reward')
    old.resolve(result(10))
    await flushPromises()
    expect(wrapper.text()).toContain('two@example.com')
    expect(wrapper.text()).toContain('History 20')
    expect(wrapper.text()).toContain('$15.00')
    expect(wrapper.text()).not.toContain('History 10')
  })

  it('resets paging and filtering when the visible dialog receives a new initial type', async () => {
    mocks.getUserBalanceHistory.mockResolvedValue({ items: [], total: 31, total_recharged: 100, total_rewarded: 15 })
    const wrapper = await openDialog({ initialType: 'activity_reward' })
    await flushPromises()
    await wrapper.findAll('button').find((button) => button.text() === 'pagination.next')!.trigger('click')
    await flushPromises()
    expect(mocks.getUserBalanceHistory).toHaveBeenLastCalledWith(1, 2, 15, 'activity_reward')
    await wrapper.setProps({ initialType: '' })
    await flushPromises()
    expect(mocks.getUserBalanceHistory).toHaveBeenLastCalledWith(1, 1, 15, undefined)
    expect(wrapper.findComponent({ name: 'Select' }).props('modelValue')).toBe('')
    expect(wrapper.text()).toContain('1 / 3')
  })

  it('opens the reward shortcut with the activity filter and preserves hidden actions', async () => {
    mocks.getUserBalanceHistory.mockResolvedValue({ items: [], total: 0, total_recharged: 100, total_rewarded: 0 })
    const wrapper = await openDialog({ initialType: 'activity_reward', hideActions: true })
    await flushPromises()
    expect(mocks.getUserBalanceHistory).toHaveBeenCalledWith(1, 1, 15, 'activity_reward')
    expect(wrapper.text()).not.toContain('admin.users.deposit')
    expect(wrapper.text()).not.toContain('admin.users.withdraw')
    expect(wrapper.text()).toContain('admin.users.totalRewarded')
    expect(wrapper.text()).toContain('$0.00')
    expect(wrapper.findComponent({ name: 'Select' }).props('options')).toEqual(expect.arrayContaining([
      expect.objectContaining({ value: 'activity_reward' })
    ]))
  })

  it('renders reward types, amounts and source IDs without treating them as redeem codes', async () => {
    const activityTypes = ['daily_gift', 'recharge_draw', 'spend_draw', 'invite_draw']
    mocks.getUserBalanceHistory.mockResolvedValue({
      items: activityTypes.map((activity_type, index) => ({
        id: index + 1, source_id: index + 1, record_source: 'activity_reward', type: 'activity_reward',
        activity_type, value: index === 0 ? 0 : 0.25, code: '', created_at: '2026-10-07T01:00:00Z',
        period_date: index === 0 ? '2026-10-06' : null
      })),
      total: 4, total_recharged: 100, total_rewarded: 156.28
    })
    const wrapper = await openDialog()
    await flushPromises()
    expect(wrapper.text()).toContain('$156.28')
    expect(wrapper.text()).toContain('$100.00')
    for (const activityType of activityTypes) expect(wrapper.text()).toContain(`admin.users.activityRewardTypes.${activityType}`)
    expect(wrapper.text()).toContain('admin.users.rewardRecordId:1')
    expect(wrapper.text()).toContain('admin.users.rewardPeriodDate:2026-10-06')
    expect(wrapper.text()).toContain('+$0.00')
    expect(wrapper.text()).toContain('+$0.25')
    expect(wrapper.text()).not.toContain('...')
  })

  it('uses independent source keys when redeem and reward IDs overlap across updates', async () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    const redeem = { id: 1, source_id: 1, record_source: 'redeem_code', type: 'balance', value: 10, code: 'redeem-code', created_at: '2026-10-07T01:00:00Z' }
    const reward = { id: 1, source_id: 1, record_source: 'activity_reward', type: 'activity_reward', activity_type: 'daily_gift', value: 0.25, code: '', created_at: '2026-10-07T01:00:00Z' }
    const items = [redeem, reward, { ...redeem, id: 2, source_id: 2 }, { ...reward, id: 2, source_id: 2 }, { ...redeem, id: 3, source_id: 3 }]
    mocks.getUserBalanceHistory.mockResolvedValueOnce({ items, total: 5, total_recharged: 30, total_rewarded: 0.5 })
      .mockResolvedValueOnce({ items: [...items].reverse(), total: 5, total_recharged: 30, total_rewarded: 0.5 })
    const wrapper = await openDialog()
    await flushPromises()
    wrapper.findComponent({ name: 'Select' }).vm.$emit('change', '')
    await flushPromises()
    expect(warn.mock.calls.flat().join(' ')).not.toContain('Duplicate keys')
    expect(wrapper.text()).toContain('+$10.00')
    expect(wrapper.text()).toContain('+$0.25')
  })

  it('keeps the lifetime reward total when paging or filtering the history', async () => {
    mocks.getUserBalanceHistory.mockResolvedValue({ items: [], total: 31, total_recharged: 100, total_rewarded: 156.28 })
    const wrapper = await openDialog({ initialType: 'activity_reward' })
    await flushPromises()
    await wrapper.findAll('button').find((button) => button.text() === 'pagination.next')!.trigger('click')
    await flushPromises()
    expect(mocks.getUserBalanceHistory).toHaveBeenLastCalledWith(1, 2, 15, 'activity_reward')
    expect(wrapper.text()).toContain('$156.28')
    const filter = wrapper.findComponent({ name: 'Select' })
    filter.vm.$emit('update:modelValue', 'balance')
    filter.vm.$emit('change', 'balance')
    await flushPromises()
    expect(mocks.getUserBalanceHistory).toHaveBeenLastCalledWith(1, 1, 15, 'balance')
    expect(wrapper.text()).toContain('$156.28')
  })

  it('keeps the new user history when an old response finishes later', async () => {
    const old = deferred()
    mocks.getUserBalanceHistory.mockReturnValueOnce(old.promise).mockResolvedValueOnce(result(20))
    const wrapper = await openDialog()
    await wrapper.setProps({ show: false })
    await wrapper.setProps({ show: true, user: { id: 2, email: 'two@example.com', balance: 2 } as AdminUser })
    await flushPromises()
    old.resolve(result(10))
    await flushPromises()
    expect(wrapper.text()).toContain('History 20')
    expect(wrapper.text()).not.toContain('History 10')
    expect(wrapper.text()).toContain('$20.00')
  })

  it('does not end the current filter loading state when an old request finishes', async () => {
    const old = deferred()
    const current = deferred()
    mocks.getUserBalanceHistory.mockReturnValueOnce(old.promise).mockReturnValueOnce(current.promise)
    const wrapper = await openDialog()
    wrapper.findComponent({ name: 'Select' }).vm.$emit('change', 'balance')
    await flushPromises()
    old.resolve(result(10))
    await flushPromises()
    expect(wrapper.find('svg.animate-spin').exists()).toBe(true)
    expect(wrapper.text()).not.toContain('History 10')
    current.resolve(result(20))
    await flushPromises()
    expect(wrapper.find('svg.animate-spin').exists()).toBe(false)
    expect(wrapper.text()).toContain('History 20')
  })

  it.each(['close', 'unmount'])('ignores failures after %s', async (action) => {
    const pending = deferred()
    mocks.getUserBalanceHistory.mockReturnValueOnce(pending.promise)
    const wrapper = await openDialog()
    if (action === 'close') await wrapper.setProps({ show: false })
    else wrapper.unmount()
    pending.reject(new Error('Stale error'))
    await flushPromises()
    expect(console.error).not.toHaveBeenCalled()
  })

  it('still reports current failures and ends loading', async () => {
    const error = new Error('Current error')
    mocks.getUserBalanceHistory.mockRejectedValueOnce(error)
    const wrapper = await openDialog()
    await flushPromises()
    expect(console.error).toHaveBeenCalledWith('Failed to load balance history:', error)
    expect(wrapper.find('svg.animate-spin').exists()).toBe(false)
  })
})
