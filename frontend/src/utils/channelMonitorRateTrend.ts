import type { MonitorRateTrendPoint } from '@/api/channelMonitor'

export interface RateTrendChartData {
  timestamps: string[]
  values: number[]
}

export interface RateAxisBounds {
  min?: number
  max?: number
}

export function buildRateTrendChartData(
  points: MonitorRateTrendPoint[] | null | undefined,
  rangeEnd: string | null | undefined,
  currentRate: number | null | undefined,
): RateTrendChartData {
  const sorted = (points || [])
    .filter((point) => Number.isFinite(Date.parse(point.observed_at)) && Number.isFinite(point.rate))
    .slice()
    .sort((a, b) => Date.parse(a.observed_at) - Date.parse(b.observed_at))
  const normalized = sorted.reduce<MonitorRateTrendPoint[]>((result, point) => {
    const previous = result[result.length - 1]
    if (previous && Date.parse(previous.observed_at) === Date.parse(point.observed_at)) {
      result[result.length - 1] = point
    } else {
      result.push(point)
    }
    return result
  }, [])

  const timestamps = normalized.map((point) => point.observed_at)
  const values = normalized.map((point) => point.rate)
  const endTimestamp = rangeEnd ? Date.parse(rangeEnd) : Number.NaN
  const lastTimestamp = timestamps.length > 0 ? Date.parse(timestamps[timestamps.length - 1]) : Number.NaN

  if (Number.isFinite(endTimestamp) && Number.isFinite(lastTimestamp) && endTimestamp > lastTimestamp) {
    const terminalRate = Number.isFinite(currentRate) ? Number(currentRate) : values[values.length - 1]
    timestamps.push(rangeEnd as string)
    values.push(terminalRate)
  }

  return { timestamps, values }
}

export function rateAxisBounds(values: number[]): RateAxisBounds {
  const finite = values.filter(Number.isFinite)
  if (finite.length === 0) return {}
  const min = Math.min(...finite)
  const max = Math.max(...finite)
  const spread = max - min
  const reference = Math.max(Math.abs(min), Math.abs(max), 0.001)
  const padding = spread > 0
    ? Math.max(spread * 0.16, reference * 0.025, 0.0005)
    : Math.max(reference * 0.08, 0.001)

  return {
    min: Math.max(0, min - padding),
    max: max + padding,
  }
}

export function formatPublicRate(value: number | null | undefined): string {
  if (value == null || !Number.isFinite(value)) return '-'
  return `${value.toFixed(3)}x`
}

export function rateHistoryIsPartial(
  observedSince: string | null | undefined,
  rangeStart: string | null | undefined,
): boolean {
  if (!observedSince || !rangeStart) return false
  const observed = Date.parse(observedSince)
  const start = Date.parse(rangeStart)
  return Number.isFinite(observed) && Number.isFinite(start) && observed > start
}
