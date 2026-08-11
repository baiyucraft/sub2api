<template>
  <div data-test="upstream-health-trend-chart">
    <div class="mb-3 flex flex-wrap items-center justify-between gap-2 text-xs text-gray-500 dark:text-dark-400">
      <span>{{ summary }}</span>
      <span v-if="trend" class="font-mono text-[11px]">{{ bucketLabel }}</span>
    </div>
    <TrendChart
      :timestamps="timestamps"
      :series="series"
      :loading="loading"
      :loading-text="t('admin.upstreamManagement.events.trendLoading')"
      :empty-text="t('admin.upstreamManagement.events.trendEmpty')"
      :chart-label="chartLabel"
      :time-column-label="t('admin.upstreamManagement.events.time')"
      :value-formatter="formatChartValue"
      :tooltip-footer="tooltipFooter"
      :zero-baseline="metric === 'ttft'"
      :y-min="metric === 'health' ? 0 : undefined"
      :y-max="metric === 'health' ? 5 : undefined"
      :show-legend="metric === 'ttft'"
      height="20rem"
    />
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import TrendChart from '@/components/charts/TrendChart.vue'
import type { TrendChartSeries } from '@/components/charts/trendChart'
import type { UpstreamHealthTrend, UpstreamHealthTrendPoint } from '@/api/admin/upstreamManagement'

type HealthMetric = 'health' | 'ttft'

const props = defineProps<{
  trend: UpstreamHealthTrend | null
  metric: HealthMetric
  loading?: boolean
}>()

const { t, te } = useI18n()
const points = computed(() => props.trend?.points || [])
const timestamps = computed(() => points.value.map(point => point.bucket))

const healthValues: Record<UpstreamHealthTrendPoint['state'], number> = {
  disabled: 0,
  suspended: 1,
  degraded: 2,
  recovering: 3,
  observing: 4,
  healthy: 5
}
const healthStates = Object.entries(healthValues).reduce<Record<number, UpstreamHealthTrendPoint['state']>>((result, [state, value]) => {
  result[value] = state as UpstreamHealthTrendPoint['state']
  return result
}, {})

const series = computed<TrendChartSeries[]>(() => {
  if (props.metric === 'health') {
    return [{
      label: t('admin.upstreamManagement.events.metrics.health'),
      data: points.value.map(point => healthValues[point.state] ?? null),
      tone: 'primary',
      stepped: 'after',
      pointStyle: 'rect',
      pointRadius: 3,
      fill: true
    }]
  }
  return [
    {
      label: t('admin.upstreamManagement.events.ttftP50'),
      data: points.value.map(point => point.ttft_p50_ms ?? null),
      tone: 'primary',
      pointStyle: 'circle',
      pointRadius: 2
    },
    {
      label: t('admin.upstreamManagement.events.failedOrMissing'),
      data: points.value.map(point => point.ttft_p50_ms == null && point.sample_count > 0 ? 0 : null),
      tone: 'warning',
      pointStyle: 'triangle',
      pointRadius: 5,
      pointHoverRadius: 6,
      showLine: false,
      order: -1
    }
  ]
})

const chartLabel = computed(() => props.metric === 'health'
  ? t('admin.upstreamManagement.events.healthChartLabel')
  : t('admin.upstreamManagement.events.ttftChartLabel'))
const bucketLabel = computed(() => {
  const seconds = props.trend?.bucket_seconds || 0
  if (!seconds) return ''
  const value = seconds >= 3600 ? `${seconds / 3600}h` : `${seconds / 60}m`
  return t('admin.upstreamManagement.events.bucket', { value })
})
const summary = computed(() => {
  if (!points.value.length) return t('admin.upstreamManagement.events.trendEmpty')
  const samples = points.value.reduce((total, point) => total + point.sample_count, 0)
  return t('admin.upstreamManagement.events.sampleSummary', { buckets: points.value.length, samples })
})

function stateLabel(state: string): string {
  const key = `admin.upstreamManagement.health.${state}`
  return te(key) ? t(key) : state
}

function sourceLabel(source?: string): string {
  if (!source) return '-'
  const key = `admin.upstreamManagement.health.sources.${source}`
  return te(key) ? t(key) : source
}

function reasonLabel(reason?: string): string {
  if (!reason) return '-'
  const key = `admin.upstreamManagement.health.reasons.${reason}`
  return te(key) ? t(key) : reason
}

function formatMilliseconds(value: number): string {
  if (value === 0 && props.metric === 'ttft') return t('admin.upstreamManagement.events.noValidTTFT')
  return value >= 1000
    ? `${(value / 1000).toLocaleString(undefined, { maximumFractionDigits: 2 })} s`
    : `${Math.round(value).toLocaleString()} ms`
}

function formatChartValue(value: number): string {
  if (props.metric === 'ttft') return formatMilliseconds(value)
  const state = healthStates[Math.round(value)]
  return state ? stateLabel(state) : '-'
}

function tooltipFooter(index: number): string[] {
  const point = points.value[index]
  if (!point) return []
  const lines = [
    t('admin.upstreamManagement.events.tooltipState', { state: stateLabel(point.state) }),
    t('admin.upstreamManagement.events.tooltipSamples', { count: point.sample_count }),
    t('admin.upstreamManagement.events.tooltipSource', { source: sourceLabel(point.primary_source) })
  ]
  if (props.metric === 'ttft' && point.ttft_p95_ms != null) {
    lines.push(t('admin.upstreamManagement.events.tooltipP95', { value: formatMilliseconds(point.ttft_p95_ms) }))
  }
  if (point.duration_avg_ms != null) {
    lines.push(t('admin.upstreamManagement.events.tooltipDuration', { value: formatMilliseconds(point.duration_avg_ms) }))
  }
  if (point.latest_reason) lines.push(t('admin.upstreamManagement.events.tooltipReason', { reason: reasonLabel(point.latest_reason) }))
  if (point.latest_result) lines.push(t('admin.upstreamManagement.events.tooltipResult', { result: point.latest_result }))
  return lines
}
</script>
