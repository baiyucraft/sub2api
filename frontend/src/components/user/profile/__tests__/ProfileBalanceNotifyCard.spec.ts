import { mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import ProfileBalanceNotifyCard from '@/components/user/profile/ProfileBalanceNotifyCard.vue'

const { updateProfileMock } = vi.hoisted(() => ({
  updateProfileMock: vi.fn()
}))

vi.mock('@/api', () => ({
  userAPI: {
    updateProfile: updateProfileMock,
    toggleNotifyEmail: vi.fn(),
    sendNotifyEmailCode: vi.fn(),
    verifyNotifyEmail: vi.fn(),
    removeNotifyEmail: vi.fn(),
    getProfile: vi.fn()
  }
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ user: null })
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: vi.fn(),
    showSuccess: vi.fn()
  })
}))

vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key
    })
  }
})

function mountCard(extraEmails: Array<{ email: string; verified: boolean; disabled: boolean }> = []) {
  return mount(ProfileBalanceNotifyCard, {
    props: {
      enabled: true,
      threshold: null,
      extraEmails,
      systemDefaultThreshold: 10,
      userEmail: 'registered@example.com'
    }
  })
}

function mountCardWithEmail(userEmail: string) {
  return mount(ProfileBalanceNotifyCard, {
    props: {
      enabled: true,
      threshold: null,
      extraEmails: [],
      systemDefaultThreshold: 10,
      userEmail
    }
  })
}

describe('ProfileBalanceNotifyCard', () => {
  beforeEach(() => {
    updateProfileMock.mockReset()
  })

  it('shows the registration email as the active default recipient without pre-filling the custom input', () => {
    const wrapper = mountCard()

    const defaultEmail = wrapper.get('[data-testid="balance-notify-default-email"]')
    expect(defaultEmail.text()).toContain('registered@example.com')
    expect(defaultEmail.text()).toContain('profile.balanceNotify.defaultEmailActive')
    expect(wrapper.get('input[type="email"]').element.value).toBe('')
  })

  it('shows that active verified custom emails replace the registration email', () => {
    const wrapper = mountCard([
      { email: 'extra@example.com', verified: true, disabled: false }
    ])

    const defaultEmail = wrapper.get('[data-testid="balance-notify-default-email"]')
    expect(defaultEmail.text()).toContain('profile.balanceNotify.defaultEmailReplaced')
    expect(wrapper.text()).toContain('extra@example.com')
  })

  it('falls back to the registration email when custom emails are disabled or unverified', () => {
    const wrapper = mountCard([
      { email: 'disabled@example.com', verified: true, disabled: true },
      { email: 'unverified@example.com', verified: false, disabled: false }
    ])

    expect(wrapper.get('[data-testid="balance-notify-default-email"]').text()).toContain(
      'profile.balanceNotify.defaultEmailActive'
    )
  })

  it('does not let a malformed verified custom email replace the registration email', () => {
    const wrapper = mountCard([
      { email: 'not-an-email', verified: true, disabled: false }
    ])

    expect(wrapper.get('[data-testid="balance-notify-default-email"]').text()).toContain(
      'profile.balanceNotify.defaultEmailActive'
    )
  })

  it('does not present a synthetic oauth placeholder as a usable registration email', () => {
    const wrapper = mountCardWithEmail('legacy-user@oidc-connect.invalid')

    const defaultEmail = wrapper.get('[data-testid="balance-notify-default-email"]')
    expect(defaultEmail.text()).not.toContain('legacy-user@oidc-connect.invalid')
    expect(defaultEmail.text()).toContain('profile.balanceNotify.defaultEmailUnavailableStatus')
  })
})
