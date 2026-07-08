import { beforeEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'

import AppHeader from '../AppHeader.vue'
import { useAuthStore } from '@/stores/auth'
import type { User } from '@/types'

vi.mock('vue-router', () => ({
  useRoute: () => ({
    name: 'Dashboard',
    meta: {
      titleKey: 'admin.dashboard.title',
      descriptionKey: 'admin.dashboard.description'
    },
    params: {}
  }),
  useRouter: () => ({
    push: vi.fn()
  })
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

describe('AppHeader onboarding entry', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    const authStore = useAuthStore()
    authStore.user = adminUser
  })

  it('does not show restart onboarding entry for admins', async () => {
    const wrapper = mount(AppHeader, {
      global: {
        stubs: {
          AnnouncementBell: true,
          LocaleSwitcher: true,
          SubscriptionProgressMini: true,
          Icon: true,
          RouterLink: {
            props: ['to'],
            template: '<a><slot /></a>'
          }
        }
      }
    })

    await wrapper.get('button[aria-label="User Menu"]').trigger('click')

    expect(wrapper.text()).not.toContain('onboarding.restartTour')
    expect(wrapper.text()).not.toContain('重新查看新手引导')
  })
})
