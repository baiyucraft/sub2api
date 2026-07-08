import { beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'

import { useAuthStore } from '@/stores/auth'
import { useOnboardingStore } from '@/stores/onboarding'
import { useOnboardingTour } from '../useOnboardingTour'
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

function mountHarness() {
  let api: ReturnType<typeof useOnboardingTour> | null = null
  const Harness = defineComponent({
    setup() {
      api = useOnboardingTour({
        storageKey: 'admin_guide',
        autoStart: true
      })
      return {}
    },
    template: '<div />'
  })

  mount(Harness)
  return api!
}

describe('useOnboardingTour disabled mode', () => {
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

  it('does not auto-start or instantiate driver for standard admin users', async () => {
    mountHarness()

    await vi.advanceTimersByTimeAsync(1500)

    expect(driverMock).not.toHaveBeenCalled()
    expect(driveMock).not.toHaveBeenCalled()
  })

  it('keeps start and replay as no-ops without changing seen storage', async () => {
    const api = mountHarness()
    const storageKey = 'admin_guide_1_admin_v4_interactive'
    localStorage.setItem(storageKey, 'true')

    await api.startTour()
    api.replayTour()

    expect(driverMock).not.toHaveBeenCalled()
    expect(localStorage.getItem(storageKey)).toBe('true')
  })

  it('destroys any existing driver instance when initialized', () => {
    const destroyMock = vi.fn()
    const onboardingStore = useOnboardingStore()
    onboardingStore.setDriverInstance({
      destroy: destroyMock,
      isActive: vi.fn(() => true)
    } as any)

    mountHarness()

    expect(destroyMock).toHaveBeenCalledTimes(1)
    expect(onboardingStore.getDriverInstance()).toBeNull()
    expect(onboardingStore.isDriverActive()).toBe(false)
  })
})
