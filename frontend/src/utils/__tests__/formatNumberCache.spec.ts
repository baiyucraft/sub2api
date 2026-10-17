import { beforeEach, describe, expect, it, vi } from 'vitest'

const state = vi.hoisted(() => ({ locale: 'en-US' }))
vi.mock('@/i18n', () => ({ getLocale: () => state.locale, i18n: { global: { t: (key: string) => key } } }))

describe('number formatter reuse', () => {
  beforeEach(() => { vi.resetModules(); state.locale = 'en-US' })

  it('matches uncached formatting across languages, boundaries and nonfinite values', async () => {
    const { formatNumber, formatCurrency } = await import('../format')
    for (const locale of ['en-US', 'zh-CN']) {
      state.locale = locale
      for (const value of [-Infinity, -10001, -0, 0, 0.005, 0.01, 9999, 10000, Infinity, NaN]) {
        expect(formatNumber(value)).toBe(new Intl.NumberFormat(locale, { notation: Math.abs(value) >= 10000 ? 'compact' : 'standard', maximumFractionDigits: 1 }).format(value))
        for (const currency of ['USD', 'CNY', 'JPY']) {
          const digits = value > 0 && value < 0.01 ? 6 : 2
          expect(formatCurrency(value, currency)).toBe(new Intl.NumberFormat(locale, { style: 'currency', currency, minimumFractionDigits: digits, maximumFractionDigits: digits }).format(value))
        }
      }
    }
    expect(formatNumber(null)).toBe('0')
    expect(formatCurrency(undefined)).toBe('$0.00')
  })

  it('separates locale, notation, currency and precision while reusing matching formatters', async () => {
    const { formatNumber, formatCurrency } = await import('../format')
    const spy = vi.spyOn(Intl, 'NumberFormat')
    try {
      formatNumber(1); formatNumber(2)
      expect(spy).toHaveBeenCalledTimes(1)
      formatNumber(10000)
      state.locale = 'zh-CN'
      formatNumber(1)
      formatCurrency(1, 'USD'); formatCurrency(0.005, 'USD'); formatCurrency(1, 'CNY')
      expect(spy).toHaveBeenCalledTimes(6)
      formatCurrency(2, 'CNY')
      expect(spy).toHaveBeenCalledTimes(6)
      expect(() => formatCurrency(1, 'invalid')).toThrow(RangeError)
      expect(() => formatCurrency(1, 'invalid')).toThrow(RangeError)
      expect(spy).toHaveBeenCalledTimes(8)
    } finally { spy.mockRestore() }
  })

  it('bounds retained formatter identities', async () => {
    const { formatCurrency } = await import('../format')
    const spy = vi.spyOn(Intl, 'NumberFormat')
    try {
      formatCurrency(1, 'USD')
      for (let index = 0; index < 80; index++) formatCurrency(1, `X${String.fromCharCode(65 + Math.floor(index / 26))}${String.fromCharCode(65 + index % 26)}`)
      expect(spy).toHaveBeenCalledTimes(81)
      formatCurrency(1, 'USD')
      expect(spy).toHaveBeenCalledTimes(82)
    } finally { spy.mockRestore() }
  })
})
