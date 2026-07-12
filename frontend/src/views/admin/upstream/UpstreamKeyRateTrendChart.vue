<template>
  <div class="h-72 min-h-72">
    <Line v-if="points.length" :data="chartData" :options="chartOptions" />
    <div v-else class="flex h-full items-center justify-center text-sm text-gray-500 dark:text-dark-400">
      {{ loading ? t('common.loading') : t('admin.upstreamConfigs.operations.emptyRateTrend') }}
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  Chart as ChartJS,
  CategoryScale,
  Filler,
  Legend,
  LineElement,
  LinearScale,
  PointElement,
  Tooltip
} from 'chart.js'
import { Line } from 'vue-chartjs'
import type { UpstreamKeyRateTrendPoint } from '@/api/admin/upstreamConfigs'

ChartJS.register(CategoryScale, Filler, Legend, LineElement, LinearScale, PointElement, Tooltip)

const props = defineProps<{
  points: UpstreamKeyRateTrendPoint[]
  loading?: boolean
}>()

const { t } = useI18n()
const dark = computed(() => document.documentElement.classList.contains('dark'))
const textColor = computed(() => dark.value ? '#9ca3af' : '#6b7280')
const gridColor = computed(() => dark.value ? '#374151' : '#e5e7eb')

const chartData = computed(() => ({
  labels: props.points.map((point) => formatBucket(point.bucket)),
  datasets: [
    dataset(t('admin.upstreamConfigs.operations.rateSeries.raw'), props.points.map((point) => point.raw_rate_multiplier), '#2563eb'),
    dataset(t('admin.upstreamConfigs.operations.rateSeries.effective'), props.points.map((point) => point.effective_cost_multiplier), '#ef4444')
  ]
}))

const chartOptions = computed(() => ({
  responsive: true,
  maintainAspectRatio: false,
  interaction: { intersect: false, mode: 'index' as const },
  plugins: {
    legend: {
      position: 'top' as const,
      align: 'end' as const,
      labels: { color: textColor.value, usePointStyle: true, boxWidth: 7, font: { size: 10 } }
    },
    tooltip: {
      callbacks: {
        label: (context: { dataset: { label?: string }; parsed: { y: number | null } }) =>
          `${context.dataset.label || ''}: ${formatRate(context.parsed.y || 0)}`
      }
    }
  },
  scales: {
    x: { grid: { display: false }, ticks: { color: textColor.value, maxTicksLimit: 8, font: { size: 10 } } },
    y: {
      grid: { color: gridColor.value },
      ticks: { color: textColor.value, callback: (value: string | number) => formatRate(Number(value)), font: { size: 10 } }
    }
  }
}))

function dataset(label: string, data: number[], color: string) {
  return {
    label,
    data,
    borderColor: color,
    backgroundColor: `${color}18`,
    fill: false,
    tension: 0.3,
    pointRadius: 0,
    pointHitRadius: 10
  }
}

function formatBucket(value: string): string {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString(undefined, { month: '2-digit', day: '2-digit', hour: '2-digit' })
}

function formatRate(value: number): string {
  return value.toLocaleString(undefined, { minimumFractionDigits: 0, maximumFractionDigits: 6 })
}
</script>
