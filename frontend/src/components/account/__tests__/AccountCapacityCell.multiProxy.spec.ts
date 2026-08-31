import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import AccountCapacityCell from '@/components/account/AccountCapacityCell.vue'
import type { Account } from '@/types'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key })
}))

const baseAccount = {
  id: 1,
  name: 'Multi proxy account',
  platform: 'openai',
  type: 'oauth',
  proxy_id: 11,
  concurrency: 5,
  priority: 1,
  status: 'active',
  error_message: null,
  last_used_at: null,
  expires_at: null,
  auto_pause_on_expired: false,
  created_at: '',
  updated_at: '',
  schedulable: true,
  rate_limited_at: null,
  rate_limit_reset_at: null,
  overload_until: null,
  temp_unschedulable_until: null,
  temp_unschedulable_reason: null
} as Account

describe('AccountCapacityCell multi proxy capacity', () => {
  it('renders one independent current/limit row per proxy', () => {
    const wrapper = mount(AccountCapacityCell, {
      props: {
        account: {
          ...baseAccount,
          proxy_capacities: [
            { proxy_id: 11, name: 'Hong Kong', current_concurrency: 2, waiting: 1, limit: 5, available: true },
            { proxy_id: 12, name: 'Tokyo', current_concurrency: 0, waiting: 0, limit: 5, available: true }
          ]
        }
      }
    })

    expect(wrapper.text()).toContain('Hong Kong')
    expect(wrapper.text()).toContain('2 / 5')
    expect(wrapper.text()).toContain('Tokyo')
    expect(wrapper.text()).toContain('0 / 5')
  })

  it('marks an unavailable proxy without displaying a fake zero load', () => {
    const wrapper = mount(AccountCapacityCell, {
      props: {
        account: {
          ...baseAccount,
          proxy_capacities: [
            { proxy_id: 11, name: 'Expired proxy', current_concurrency: 0, limit: 5, available: false }
          ]
        }
      }
    })

    expect(wrapper.text()).toContain('Expired proxy')
    expect(wrapper.text()).toContain('—')
    expect(wrapper.text()).not.toContain('0 / 5')
  })

  it('keeps one available proxy in the compact capacity badge', () => {
    const wrapper = mount(AccountCapacityCell, {
      props: {
        account: {
          ...baseAccount,
          proxy_capacities: [
            { proxy_id: 11, name: 'Hong Kong', current_concurrency: 2, waiting: 0, limit: 5, available: true }
          ]
        }
      }
    })

    expect(wrapper.text()).toContain('2/5')
    expect(wrapper.text()).not.toContain('Hong Kong')
  })

  it('shows an unavailable measurement when proxy load is missing', () => {
    const wrapper = mount(AccountCapacityCell, {
      props: {
        account: {
          ...baseAccount,
          proxy_capacities: [
            { proxy_id: 11, name: 'Hong Kong', limit: 5, available: true },
            { proxy_id: 12, name: 'Tokyo', current_concurrency: 1, limit: 5, available: true }
          ]
        }
      }
    })

    expect(wrapper.text()).toContain('Hong Kong— / 5')
    expect(wrapper.text()).not.toContain('Hong Kong0 / 5')
  })
})
