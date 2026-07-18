<template>
  <TrendChart
    :timestamps="points.map((point) => point.bucket)"
    :series="series"
    :loading="loading"
    :loading-text="t('common.loading')"
    :empty-text="t('admin.upstreamConfigs.operations.emptyTrend')"
    :chart-label="t('admin.upstreamConfigs.operations.trendTitle')"
    :time-column-label="t('admin.upstreamConfigs.operations.time')"
    :value-formatter="formatMoney"
    :tooltip-footer="requestFooter"
    zero-baseline
  />
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import TrendChart from '@/components/charts/TrendChart.vue'
import type { TrendChartSeries } from '@/components/charts/trendChart'
import type { UpstreamUsageTrendPoint } from '@/api/admin/upstreamConfigs'

const props = defineProps<{
  points: UpstreamUsageTrendPoint[]
  loading?: boolean
}>()

const { t } = useI18n()
const series = computed<TrendChartSeries[]>(() => [
  {
    label: t('admin.upstreamConfigs.operations.trendSeries.billedCost'),
    data: props.points.map((point) => point.billed_cost),
    tone: 'primary',
    pointStyle: 'circle',
    order: 1
  },
  {
    label: t('admin.upstreamConfigs.operations.trendSeries.actualCost'),
    data: props.points.map((point) => point.upstream_cost),
    tone: 'secondary',
    borderDash: [7, 4],
    pointStyle: 'rect',
    order: 2
  },
  {
    label: t('admin.upstreamConfigs.operations.trendSeries.grossProfit'),
    data: props.points.map((point) => point.gross_profit),
    tone: 'profit',
    fill: true,
    pointStyle: 'triangle',
    order: 3
  }
])

function requestFooter(index: number): string {
  return t('admin.upstreamConfigs.operations.requestsTooltip', {
    count: props.points[index]?.requests ?? 0
  })
}

function formatMoney(value: number): string {
  return `¥${value.toLocaleString(undefined, {
    minimumFractionDigits: 2,
    maximumFractionDigits: 4
  })}`
}
</script>
