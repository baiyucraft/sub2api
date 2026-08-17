import { afterEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import AutoRefreshCountdownLabel from '../AutoRefreshCountdownLabel.vue'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string, params?: Record<string, unknown>) => params ? `${key}:${params.seconds}` : key
  })
}))

describe('AutoRefreshCountdownLabel', () => {
  afterEach(() => {
    vi.useRealTimers()
  })

  it('owns the per-second countdown update inside the small label component', async () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-08-17T00:00:00Z'))
    const wrapper = mount(AutoRefreshCountdownLabel, {
      props: { enabled: true, deadline: Date.now() + 5_000 }
    })

    expect(wrapper.text()).toBe('admin.accounts.autoRefreshCountdown:5')
    await vi.advanceTimersByTimeAsync(2_000)
    expect(wrapper.text()).toBe('admin.accounts.autoRefreshCountdown:3')
  })

  it('stops showing a countdown when disabled', () => {
    const wrapper = mount(AutoRefreshCountdownLabel, {
      props: { enabled: false, deadline: 0 }
    })
    expect(wrapper.text()).toBe('admin.accounts.autoRefresh')
  })
})
