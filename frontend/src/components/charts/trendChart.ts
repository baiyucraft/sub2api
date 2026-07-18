export type TrendChartTone = 'primary' | 'secondary' | 'profit' | 'warning' | 'neutral'
export type TrendChartPointStyle = 'circle' | 'rect' | 'rectRot' | 'triangle'

export interface TrendChartSeries {
  label: string
  data: Array<number | null>
  tone?: TrendChartTone
  borderDash?: number[]
  fill?: boolean
  stepped?: boolean | 'before' | 'after' | 'middle'
  pointStyle?: TrendChartPointStyle
  order?: number
}
