import { describe, expect, it } from 'vitest'
import {
  buildRateTrendChartData,
  formatPublicRate,
  rateAxisBounds,
  rateHistoryIsPartial,
} from '../channelMonitorRateTrend'

describe('channelMonitorRateTrend', () => {
  it('sorts valid points and extends the last observed rate to the selected range end', () => {
    const data = buildRateTrendChartData([
      { observed_at: '2026-08-29T10:00:00Z', rate: 0.025 },
      { observed_at: 'invalid', rate: 0.9 },
      { observed_at: '2026-08-29T01:00:00Z', rate: 0.035 },
    ], '2026-08-30T00:00:00Z', 0.025)

    expect(data).toEqual({
      timestamps: [
        '2026-08-29T01:00:00Z',
        '2026-08-29T10:00:00Z',
        '2026-08-30T00:00:00Z',
      ],
      values: [0.035, 0.025, 0.025],
    })
  })

  it('keeps irregular time spacing and collapses duplicate instants to the latest value', () => {
    const data = buildRateTrendChartData([
      { observed_at: '2026-08-29T10:00:00Z', rate: 0.12 },
      { observed_at: '2026-08-29T01:00:00Z', rate: 0.12 },
      { observed_at: '2026-08-29T10:00:00.000Z', rate: 0.15 },
      { observed_at: '2026-08-29T10:01:00Z', rate: 0.12 },
    ], '2026-08-30T00:00:00Z', 0.12)

    expect(data).toEqual({
      timestamps: [
        '2026-08-29T01:00:00Z',
        '2026-08-29T10:00:00.000Z',
        '2026-08-29T10:01:00Z',
        '2026-08-30T00:00:00Z',
      ],
      values: [0.12, 0.15, 0.12, 0.12],
    })
    expect(Date.parse(data.timestamps[1]) - Date.parse(data.timestamps[0])).toBe(9 * 60 * 60 * 1000)
    expect(Date.parse(data.timestamps[2]) - Date.parse(data.timestamps[1])).toBe(60 * 1000)
  })

  it('adds readable vertical padding for both flat and changing rate series', () => {
    const flat = rateAxisBounds([0.035, 0.035])
    expect(flat.min).toBeCloseTo(0.0322)
    expect(flat.max).toBeCloseTo(0.0378)

    const changing = rateAxisBounds([0.02, 0.04])
    expect(changing.min).toBeLessThan(0.02)
    expect(changing.max).toBeGreaterThan(0.04)
  })

  it('detects when observation starts after the selected range begins', () => {
    expect(rateHistoryIsPartial('2026-08-29T12:00:00Z', '2026-08-29T00:00:00Z')).toBe(true)
    expect(rateHistoryIsPartial('2026-08-28T12:00:00Z', '2026-08-29T00:00:00Z')).toBe(false)
    expect(rateHistoryIsPartial(null, '2026-08-29T00:00:00Z')).toBe(false)
  })

  it('formats finite rates to three decimals and rejects missing values', () => {
    expect(formatPublicRate(0.0354)).toBe('0.035x')
    expect(formatPublicRate(Number.NaN)).toBe('-')
    expect(formatPublicRate(null)).toBe('-')
  })
})
