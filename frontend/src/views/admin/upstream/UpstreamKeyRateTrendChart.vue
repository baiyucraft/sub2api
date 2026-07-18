<template>
  <TrendChart
    :timestamps="points.map((point) => point.bucket)"
    :series="series"
    :loading="loading"
    :loading-text="t('common.loading')"
    :empty-text="t('admin.upstreamConfigs.operations.emptyRateTrend')"
    :chart-label="t('admin.upstreamConfigs.operations.rateTrendTitle')"
    :time-column-label="t('admin.upstreamConfigs.operations.time')"
    :value-formatter="formatRate"
  />
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import TrendChart from '@/components/charts/TrendChart.vue'
import type { TrendChartSeries } from '@/components/charts/trendChart'
import type { UpstreamKeyRateTrendPoint } from '@/api/admin/upstreamConfigs'

const props = defineProps<{
  points: UpstreamKeyRateTrendPoint[]
  loading?: boolean
}>()

const { t } = useI18n()
const series = computed<TrendChartSeries[]>(() => [{
  label: t('admin.upstreamConfigs.operations.rateSeries.rateMultiplier'),
  data: props.points.map((point) => point.rate_multiplier),
  tone: 'primary',
  stepped: 'before',
  pointStyle: 'circle'
}])

function formatRate(value: number): string {
  return `${value.toLocaleString(undefined, { minimumFractionDigits: 0, maximumFractionDigits: 6 })}x`
}
</script>
