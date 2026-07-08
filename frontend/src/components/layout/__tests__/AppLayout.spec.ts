import { beforeEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'

import AppLayout from '../AppLayout.vue'
import { useAuthStore } from '@/stores/auth'
import { useOnboardingStore } from '@/stores/onboarding'
import type { User } from '@/types'

const { driverMock, driveMock } = vi.hoisted(() => ({
  driverMock: vi.fn(),
  driveMock: vi.fn()
}))

vi.mock('driver.js', () => ({
  driver: driverMock
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key
    })
  }
})

vi.mock('@/components/Guide/steps', () => ({
  getAdminSteps: vi.fn(() => []),
  getUserSteps: vi.fn(() => [])
}))

const adminUser: User = {
  id: 1,
  username: 'admin',
  email: 'admin@example.com',
  role: 'admin',
  balance: 0,
  concurrency: 10,
  status: 'active',
  allowed_groups: null,
  balance_notify_enabled: false,
  balance_notify_threshold: null,
  balance_notify_extra_emails: [],
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-01T00:00:00Z'
}

describe('AppLayout onboarding', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    setActivePinia(createPinia())
    localStorage.clear()
    driverMock.mockReset()
    driveMock.mockReset()
    driverMock.mockReturnValue({
      drive: driveMock,
      destroy: vi.fn(),
      isActive: vi.fn(() => false)
    })

    const authStore = useAuthStore()
    authStore.user = adminUser
  })

  it('does not auto-start onboarding and leaves replay disabled', async () => {
    mount(AppLayout, {
      global: {
        stubs: {
          AppSidebar: true,
          AppHeader: true
        }
      }
    })

    const onboardingStore = useOnboardingStore()
    onboardingStore.replay()
    await vi.advanceTimersByTimeAsync(1500)

    expect(driverMock).not.toHaveBeenCalled()
    expect(driveMock).not.toHaveBeenCalled()
    expect(onboardingStore.getDriverInstance()).toBeNull()
  })
})
