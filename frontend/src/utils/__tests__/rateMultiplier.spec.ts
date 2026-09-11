import { describe, expect, it } from 'vitest'
import { exclusiveRateToPercent, percentToExclusiveRate, parseNonNegativeNumber } from '@/utils/rateMultiplier'

describe('rateMultiplier conversions', () => {
  it('converts an actual rate to a percentage of the group rate', () => {
    expect(exclusiveRateToPercent(0.25, 0.5)).toBe(50)
    expect(exclusiveRateToPercent(0.125, 0.5)).toBe(25)
  })

  it('converts a percentage back to the actual rate', () => {
    expect(percentToExclusiveRate(50, 0.5)).toBe(0.25)
    expect(percentToExclusiveRate(37.5, 0.8)).toBe(0.3)
  })

  it('rounds conversion results to storage/display precision', () => {
    expect(percentToExclusiveRate(33.333333, 0.7)).toBe(0.233333)
    expect(exclusiveRateToPercent(0.233333, 0.7)).toBe(33.3333)
  })

  it('returns null for empty, invalid, negative, or zero-base values', () => {
    expect(parseNonNegativeNumber('')).toBeNull()
    expect(parseNonNegativeNumber('nope')).toBeNull()
    expect(parseNonNegativeNumber(-1)).toBeNull()
    expect(exclusiveRateToPercent(0.25, 0)).toBeNull()
    expect(percentToExclusiveRate(50, 0)).toBeNull()
  })

  it('supports a zero custom rate when the group rate is positive', () => {
    expect(exclusiveRateToPercent(0, 0.5)).toBe(0)
    expect(percentToExclusiveRate(0, 0.5)).toBe(0)
  })
})
