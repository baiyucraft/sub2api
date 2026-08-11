import { describe, expect, it } from 'vitest'

import en from '../locales/en'
import zh from '../locales/zh'

const keys = [
  'accountSchedulingThresholdOverride',
  'accountSchedulingThresholdOverrideHint',
  'accountSchedulingThresholdOverrideValue',
  'accountSchedulingThresholdOverrideDisabledHint'
] as const

describe('account scheduling threshold locale copy', () => {
  it.each([
    ['zh', zh],
    ['en', en]
  ] as const)('keeps every account-level label at the correct %s path', (_locale, messages) => {
    const accounts = messages.admin.accounts as Record<string, unknown>
    const status = accounts.status as Record<string, unknown>

    for (const key of keys) {
      expect(accounts[key]).toEqual(expect.any(String))
      expect((accounts[key] as string).trim()).not.toBe('')
      expect(status[key]).toBeUndefined()
    }
  })
})
