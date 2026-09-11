/**
 * Convert between a user's effective/custom multiplier and the percentage of
 * the group's ordinary multiplier shown in the admin UI.
 *
 * The API and database continue to store the actual multiplier. Percentages
 * are only a presentation/input convenience: group rate 0.5 + 50% = 0.25.
 */

const MAX_RATE_DECIMALS = 6
const MAX_PERCENT_DECIMALS = 4

const round = (value: number, decimals: number) => {
  const factor = 10 ** decimals
  return Math.round((value + Number.EPSILON) * factor) / factor
}

export const parseNonNegativeNumber = (value: unknown): number | null => {
  if (value === '' || value === null || value === undefined) return null
  const parsed = typeof value === 'number' ? value : Number(value)
  return Number.isFinite(parsed) && parsed >= 0 ? parsed : null
}

export const exclusiveRateToPercent = (
  exclusiveRate: number | null | undefined,
  groupRate: number | null | undefined,
): number | null => {
  const rate = parseNonNegativeNumber(exclusiveRate)
  const base = parseNonNegativeNumber(groupRate)
  if (rate === null || base === null || base <= 0) return null
  return round((rate / base) * 100, MAX_PERCENT_DECIMALS)
}

export const percentToExclusiveRate = (
  percent: number | null | undefined,
  groupRate: number | null | undefined,
): number | null => {
  const ratio = parseNonNegativeNumber(percent)
  const base = parseNonNegativeNumber(groupRate)
  if (ratio === null || base === null || base <= 0) return null
  return round((base * ratio) / 100, MAX_RATE_DECIMALS)
}
