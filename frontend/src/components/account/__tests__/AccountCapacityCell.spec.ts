import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'

vi.mock('vue-i18n', async () => ({
  ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'),
  useI18n: () => ({ t: (key: string) => key }),
}))

import AccountCapacityCell from '../AccountCapacityCell.vue'

describe('AccountCapacityCell proxy concurrency', () => {
  it('renders daily, weekly and total quota badges for Vertex service accounts', () => {
    const wrapper = mount(AccountCapacityCell, {
      props: {
        account: {
          id: 1,
          platform: 'gemini',
          type: 'service_account',
          concurrency: 1,
          quota_limit: 200,
          quota_used: 35.5,
          quota_daily_limit: 20,
          quota_daily_used: 12,
          quota_weekly_limit: 70,
          quota_weekly_used: 30,
        } as never,
      },
      global: { stubs: { QuotaBadge: true } },
    })

    const quotas = wrapper.findAllComponents({ name: 'QuotaBadge' })
    expect(quotas).toHaveLength(3)
    expect(quotas.map(badge => badge.props('limit'))).toEqual([20, 70, 200])
    expect(quotas.map(badge => badge.props('used'))).toEqual([12, 30, 35.5])
    wrapper.unmount()
  })

  it('renders one status badge per proxy with current and limit values', () => {
    const wrapper = mount(AccountCapacityCell, {
      props: {
        account: {
          id: 1,
          platform: 'openai',
          type: 'oauth',
          concurrency: 6,
          current_concurrency: 3,
          proxy_concurrency: [
            { proxy_id: 1, proxy_name: 'idle', current_concurrency: 0, limit: 2, available: true },
            { proxy_id: 2, proxy_name: 'busy', current_concurrency: 1, limit: 2, available: true },
            { proxy_id: 3, proxy_name: 'full', current_concurrency: 2, limit: 2, available: false },
          ],
        } as never,
      },
    })

    const badges = wrapper.findAll('[data-testid="proxy-concurrency-badge"]')
    expect(badges.map(badge => badge.text())).toEqual(['idle0/2', 'busy1/2', 'full2/2'])
    expect(badges[0]!.classes()).toContain('bg-emerald-100')
    expect(badges[1]!.classes()).toContain('bg-amber-100')
    expect(badges[2]!.classes()).toContain('bg-red-100')
    expect(badges[0]!.classes()).toContain('text-xs')
    expect(badges[0]!.classes()).toContain('px-2')
    expect(badges[0]!.classes()).toContain('py-0.5')
    expect(badges[0]!.find('span').classes()).toContain('max-w-[120px]')
  })

  it('normalizes malformed concurrency rows and falls back to the proxy id', () => {
    const wrapper = mount(AccountCapacityCell, {
      props: {
        account: {
          id: 1,
          platform: 'openai',
          type: 'setup-token',
          concurrency: 4,
          current_concurrency: 0,
          proxy_concurrency: [
            { proxy_id: 9, proxy_name: '', current_concurrency: -3, limit: 0, available: true },
            { proxy_id: 0, proxy_name: 'invalid', current_concurrency: 1, limit: 2, available: true },
          ],
        } as never,
      },
    })

    const badges = wrapper.findAll('[data-testid="proxy-concurrency-badge"]')
    expect(badges).toHaveLength(1)
    expect(badges[0]!.text()).toBe('admin.accounts.capacity.proxyConcurrency.unnamed0/0')
    expect(badges[0]!.classes()).toContain('bg-red-100')
  })
})
