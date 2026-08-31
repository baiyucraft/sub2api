export type TrendChartTone = 'primary' | 'secondary' | 'profit' | 'warning' | 'neutral'
export type TrendChartPointStyle = 'circle' | 'rect' | 'rectRot' | 'triangle'

export interface TrendChartSeries {
  label: string
  data: Array<number | null>
  tone?: TrendChartTone
  cubicInterpolationMode?: 'default' | 'monotone'
  borderDash?: number[]
  fill?: boolean
  stepped?: boolean | 'before' | 'after' | 'middle'
  pointStyle?: TrendChartPointStyle
  pointRadius?: number
  pointHoverRadius?: number
  showLine?: boolean
  order?: number
}
