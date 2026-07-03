import { createI18n } from 'vue-i18n'
import { describe, expect, it } from 'vitest'

import en from '../locales/en'
import zh from '../locales/zh'

describe('sub2api account locale copy', () => {
  it('renders Sub2API login placeholders without linked-format errors', () => {
    const i18n = createI18n({
      legacy: false,
      locale: 'zh',
      fallbackLocale: 'en',
      messages: { zh, en }
    })

    const t = i18n.global.t

    expect(() => t('admin.accounts.sub2apiLogin.emailPlaceholder')).not.toThrow()
    expect(() => t('admin.accounts.sub2apiLogin.passwordEditPlaceholder')).not.toThrow()
    expect(() => t('admin.accounts.sub2apiLogin.refreshTokenPlaceholder')).not.toThrow()
    expect(() => t('admin.accounts.sub2apiLogin.refreshTokenEditPlaceholder')).not.toThrow()

    i18n.global.locale.value = 'en'
    expect(() => t('admin.accounts.sub2apiLogin.emailPlaceholder')).not.toThrow()
    expect(() => t('admin.accounts.sub2apiLogin.passwordEditPlaceholder')).not.toThrow()
    expect(() => t('admin.accounts.sub2apiLogin.refreshTokenPlaceholder')).not.toThrow()
    expect(() => t('admin.accounts.sub2apiLogin.refreshTokenEditPlaceholder')).not.toThrow()
  })
})
