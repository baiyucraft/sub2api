import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import DailyActivitiesView from '../DailyActivitiesView.vue'

const { getActivitySummary, getActivityRewards, getAffiliateDetail, openDailyActivityGift } = vi.hoisted(() => ({
  getActivitySummary: vi.fn(),
  getActivityRewards: vi.fn(),
  getAffiliateDetail: vi.fn(),
  openDailyActivityGift: vi.fn(),
}))

vi.mock('@/api/user', () => ({ default: { getActivitySummary, getActivityRewards, getAffiliateDetail, openDailyActivityGift, drawDailyActivity: vi.fn() } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError: vi.fn(), showSuccess: vi.fn() }) }))
vi.mock('@/composables/useClipboard', () => ({ useClipboard: () => ({ copyToClipboard: vi.fn() }) }))
vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return { ...actual, useI18n: () => ({ t: (key: string, values?: Record<string, unknown>) => values ? `${key}:${JSON.stringify(values)}` : key }) }
})

describe('DailyActivitiesView', () => {
  const global = {
    stubs: {
      AppLayout: { template: '<main><slot /></main>' },
      Icon: true,
      RouterLink: { props: ['to'], template: '<a :href="to"><slot /></a>' },
    },
  }

  beforeEach(() => {
    vi.clearAllMocks()
    getActivitySummary.mockResolvedValue({
      enabled: true,
      activity_date: '2026-09-02',
      timezone: 'Asia/Shanghai',
      next_reset_at: '2026-09-03T00:00:00+08:00',
      daily_gift: { eligible: true, claimed: false, amount: 0, threshold: 10, reward_min: 0, reward_max: 0.5 },
      recharge: { amount: 10, threshold: 50, available_draws: 0, reward_min: 0.5, reward_max: 1 },
      consumption: { amount: 20, threshold: 50, available_draws: 0, reward_min: 0.5, reward_max: 1 },
      invite: { qualified_count: 3, required_count: 5, available_draws: 0, qualification_amount: 10, reward_min: 5, reward_max: 10 },
    })
    getActivityRewards.mockResolvedValue({ items: [], total: 0, page: 1, page_size: 20 })
    getAffiliateDetail.mockResolvedValue({ aff_code: 'ABC' })
  })

  it('renders server-owned progress and does not calculate a reward amount', async () => {
    const wrapper = mount(DailyActivitiesView, { global })
    await flushPromises()
    expect(wrapper.text()).toContain('activities.invite.qualified')
    expect(wrapper.text()).toContain('3 / 5')
    expect(wrapper.text()).toContain('activities.dailyGift.button')
    expect(wrapper.text()).toContain('activities.rewardRange')
    expect(wrapper.text()).toContain('activities.invite.description')
  })

  it('renders daily gift progress and remaining amount', async () => {
    getActivitySummary.mockResolvedValueOnce({
      enabled: true,
      activity_date: '2026-09-02',
      timezone: 'Asia/Shanghai',
      next_reset_at: '2026-09-03T00:00:00+08:00',
      daily_gift: { eligible: false, claimed: false, amount: 8, threshold: 10, reward_min: 0, reward_max: 0.5 },
      recharge: { amount: 10, threshold: 50, available_draws: 0, reward_min: 0.5, reward_max: 1 },
      consumption: { amount: 20, threshold: 50, available_draws: 0, reward_min: 0.5, reward_max: 1 },
      invite: { qualified_count: 3, required_count: 5, available_draws: 0, qualification_amount: 10, reward_min: 5, reward_max: 10 },
    })
    const progressWrapper = mount(DailyActivitiesView, { global })
    await flushPromises()
    expect(progressWrapper.text()).toContain('activities.dailyGift.progress')
    expect(progressWrapper.text()).toContain('activities.dailyGift.remaining')
    expect(progressWrapper.find('section.border-l-amber-400 .bg-amber-400').attributes('style')).toContain('width: 80%')
  })

  it('caps daily gift progress at 100% after reaching the threshold', async () => {
    getActivitySummary.mockResolvedValueOnce({
      enabled: true,
      activity_date: '2026-09-02',
      timezone: 'Asia/Shanghai',
      next_reset_at: '2026-09-03T00:00:00+08:00',
      daily_gift: { eligible: true, claimed: false, amount: 15, threshold: 10, reward_min: 0, reward_max: 0.5 },
      recharge: { amount: 10, threshold: 50, available_draws: 0, reward_min: 0.5, reward_max: 1 },
      consumption: { amount: 20, threshold: 50, available_draws: 0, reward_min: 0.5, reward_max: 1 },
      invite: { qualified_count: 3, required_count: 5, available_draws: 0, qualification_amount: 10, reward_min: 5, reward_max: 10 },
    })
    const cappedWrapper = mount(DailyActivitiesView, { global })
    await flushPromises()
    expect(cappedWrapper.text()).toContain('activities.dailyGift.eligible')
    expect(cappedWrapper.find('section.border-l-amber-400 .bg-amber-400').attributes('style')).toContain('width: 100%')
  })

  it('opens the daily gift through the user API', async () => {
    openDailyActivityGift.mockResolvedValue({ reward: { id: 1, type: 'daily_gift', amount: 0.25, created_at: '2026-09-02T01:00:00Z' } })
    const wrapper = mount(DailyActivitiesView, { global })
    await flushPromises()
    await wrapper.find('button.btn.btn-primary').trigger('click')
    await flushPromises()
    expect(openDailyActivityGift).toHaveBeenCalledOnce()
  })

  it('maps reward types and requests the selected server-side filter', async () => {
    getActivityRewards.mockResolvedValueOnce({ items: [{ id: 2, type: 'spend_draw', amount: 0.75, created_at: '2026-09-02T02:00:00Z' }], total: 1, page: 1, page_size: 20 })
    const wrapper = mount(DailyActivitiesView, { global })
    await flushPromises()
    expect(wrapper.text()).toContain('activities.rewards.consumption')

    getActivityRewards.mockResolvedValueOnce({ items: [], total: 0, page: 1, page_size: 20 })
    await wrapper.find('select').setValue('invite_draw')
    await flushPromises()
    expect(getActivityRewards).toHaveBeenLastCalledWith({ page: 1, page_size: 20, type: 'invite_draw' })
  })

  it('links both recharge activity cards to the recharge store', async () => {
    const wrapper = mount(DailyActivitiesView, { global })
    await flushPromises()

    const rechargeLinks = wrapper.findAll('a[href="/recharge-store"]')
    expect(rechargeLinks).toHaveLength(2)
    expect(rechargeLinks.every(link => link.text().includes('activities.goRecharge'))).toBe(true)
  })

  it('keeps the invite link action inside the invite card', async () => {
    const wrapper = mount(DailyActivitiesView, { global })
    await flushPromises()

    const inviteCard = wrapper.findAll('section.card').find(card => card.text().includes('activities.invite.title'))
    expect(inviteCard?.text()).toContain('activities.copyLink')
  })
})
