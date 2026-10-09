import { enableAutoUnmount, flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import ProfileBalanceNotifyCard from '@/components/user/profile/ProfileBalanceNotifyCard.vue'

const { updateProfileMock, sendNotifyEmailCode, verifyNotifyEmail, getProfile } = vi.hoisted(() => ({
  updateProfileMock: vi.fn(),
  sendNotifyEmailCode: vi.fn(),
  verifyNotifyEmail: vi.fn(),
  getProfile: vi.fn()
}))

vi.mock('@/api', () => ({
  userAPI: {
    updateProfile: updateProfileMock,
    toggleNotifyEmail: vi.fn(),
    sendNotifyEmailCode,
    verifyNotifyEmail,
    removeNotifyEmail: vi.fn(),
    getProfile
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

enableAutoUnmount(afterEach)

const deferred = () => {
  let resolve!: () => void
  const promise = new Promise<void>((done) => { resolve = done })
  return { promise, resolve }
}

const pendingRows = (wrapper: VueWrapper) => wrapper.findAll('.bg-yellow-50')
const pendingEmails = (wrapper: VueWrapper) => pendingRows(wrapper).map(row => row.get('span').text())

const button = (wrapper: VueWrapper, text: string) =>
  wrapper.findAll('button').find(item => item.text() === text)!

describe('ProfileBalanceNotifyCard async lifecycle', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.resetAllMocks()
    sendNotifyEmailCode.mockResolvedValue({})
    getProfile.mockResolvedValue({ balance_notify_extra_emails: [] })
  })

  afterEach(() => {
    vi.clearAllTimers()
    vi.useRealTimers()
  })

  it.each(['remove', 'unmount'])('does not start a pending email timer after %s', async (action) => {
    const request = deferred()
    sendNotifyEmailCode.mockReturnValueOnce(request.promise)
    const wrapper = mount(ProfileBalanceNotifyCard, {
      props: { enabled: true, threshold: null, extraEmails: [], systemDefaultThreshold: 5, userEmail: '' }
    })
    await wrapper.get('input[type="email"]').setValue('new@example.com')
    await button(wrapper, 'common.add').trigger('click')
    await button(wrapper, 'profile.balanceNotify.sendCode').trigger('click')
    if (action === 'remove') await button(wrapper, 'profile.balanceNotify.removeEmail').trigger('click')
    else wrapper.unmount()

    request.resolve()
    await flushPromises()
    expect(vi.getTimerCount()).toBe(0)
  })

  it('does not start a saved email timer after unmount', async () => {
    const request = deferred()
    sendNotifyEmailCode.mockReturnValueOnce(request.promise)
    const wrapper = mount(ProfileBalanceNotifyCard, {
      props: {
        enabled: true, threshold: null, systemDefaultThreshold: 5, userEmail: '',
        extraEmails: [{ email: 'saved@example.com', disabled: false, verified: false }]
      }
    })
    await button(wrapper, 'profile.balanceNotify.verify').trigger('click')
    wrapper.unmount()
    request.resolve()
    await flushPromises()
    expect(vi.getTimerCount()).toBe(0)
  })

  it.each([0, 1])('removes only verified emails when request %i finishes first', async (first) => {
    const emails = ['first@example.com', 'second@example.com', 'third@example.com']
    const requests = [deferred(), deferred()]
    verifyNotifyEmail.mockImplementation((email: string) => requests[emails.indexOf(email)]!.promise)
    const wrapper = mount(ProfileBalanceNotifyCard, {
      props: { enabled: true, threshold: null, extraEmails: [], systemDefaultThreshold: 5, userEmail: '' }
    })

    for (const email of emails) {
      await wrapper.get('input[type="email"]').setValue(email)
      await button(wrapper, 'common.add').trigger('click')
    }
    for (const row of pendingRows(wrapper)) {
      await row.findAll('button').find(item => item.text() === 'profile.balanceNotify.sendCode')!.trigger('click')
    }
    await flushPromises()
    for (const row of pendingRows(wrapper)) {
      await row.get('input').setValue('123456')
    }
    for (const row of pendingRows(wrapper).slice(0, 2)) {
      await row.findAll('button').find(item => item.text() === 'profile.balanceNotify.verify')!.trigger('click')
    }
    expect(verifyNotifyEmail.mock.calls).toEqual(emails.slice(0, 2).map(email => [email, '123456']))

    requests[first]!.resolve()
    await flushPromises()
    expect(pendingEmails(wrapper)).toEqual(emails.filter((_, index) => index !== first))

    requests[1 - first]!.resolve()
    await flushPromises()
    expect(pendingEmails(wrapper)).toEqual([emails[2]])
    expect((pendingRows(wrapper)[0]!.get('input').element as HTMLInputElement).value).toBe('123456')
    expect(getProfile).toHaveBeenCalledTimes(2)
  })
})
