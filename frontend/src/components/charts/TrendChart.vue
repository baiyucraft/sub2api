<template>
  <div
    class="relative w-full"
    :style="{ height: resolvedHeight }"
    role="img"
    :aria-label="chartLabel"
    data-test="trend-chart"
  >
    <Line v-if="hasData" :data="chartData" :options="chartOptions" />

    <div
      v-else-if="loading"
      class="flex h-full flex-col justify-end gap-5 px-3 pb-8 pt-12"
      aria-live="polite"
    >
      <Skeleton v-for="width in skeletonWidths" :key="width" height="1px" :width="width" />
      <span class="sr-only">{{ loadingText }}</span>
    </div>

    <div
      v-else
      class="flex h-full items-center justify-center px-4 text-center text-sm text-gray-500 dark:text-dark-400"
      data-test="trend-chart-empty"
    >
      {{ emptyText }}
    </div>

    <table v-if="hasData" class="sr-only">
      <caption>{{ chartLabel }}</caption>
      <thead>
        <tr>
          <th scope="col">{{ timeColumnLabel }}</th>
          <th v-for="seriesItem in series" :key="seriesItem.label" scope="col">
            {{ seriesItem.label }}
          </th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="(timestamp, index) in timestamps" :key="`${timestamp}-${index}`">
          <th scope="row">{{ formatFullTimestamp(timestamp) }}</th>
          <td v-for="seriesItem in series" :key="seriesItem.label">
            {{ seriesItem.data[index] == null ? '-' : formatValue(seriesItem.data[index] as number) }}
          </td>
        </tr>
      </tbody>
    </table>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
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
import type { ChartData, ChartDataset, ChartOptions, TooltipItem } from 'chart.js'
import { Line } from 'vue-chartjs'
import Skeleton from '@/components/common/Skeleton.vue'
import type { TrendChartPointStyle, TrendChartSeries } from './trendChart'

ChartJS.register(CategoryScale, Filler, Legend, LineElement, LinearScale, PointElement, Tooltip)

interface Props {
  timestamps: string[]
  series: TrendChartSeries[]
  loading?: boolean
  loadingText?: string
  emptyText: string
  chartLabel: string
  timeColumnLabel?: string
  height?: string | number
  showLegend?: boolean
  zeroBaseline?: boolean
  valueFormatter?: (value: number) => string
  tooltipFooter?: (index: number) => string | string[]
  maxTicks?: number
}

const props = withDefaults(defineProps<Props>(), {
  loading: false,
  loadingText: 'Loading',
  timeColumnLabel: 'Time',
  height: '18rem',
  showLegend: true,
  zeroBaseline: false,
  valueFormatter: (value: number) => value.toLocaleString(),
  maxTicks: 8
})

const isDark = ref(false)
let themeObserver: MutationObserver | null = null

const skeletonWidths = ['100%', '88%', '94%', '76%']
const resolvedHeight = computed(() => typeof props.height === 'number' ? `${props.height}px` : props.height)
const hasData = computed(() => props.timestamps.length > 0 && props.series.some((item) => item.data.length > 0))

const palette = computed(() => isDark.value
  ? {
      text: '#cbd5e1',
      grid: 'rgba(148, 163, 184, 0.16)',
      zero: 'rgba(226, 232, 240, 0.48)',
      tooltipSurface: '#111827',
      tooltipTitle: '#f8fafc',
      tooltipText: '#d1d5db',
      tooltipBorder: '#374151',
      primary: '#60a5fa',
      secondary: '#fbbf24',
      profit: '#2dd4bf',
      warning: '#fb7185',
      neutral: '#cbd5e1'
    }
  : {
      text: '#4b5563',
      grid: 'rgba(107, 114, 128, 0.14)',
      zero: 'rgba(31, 41, 55, 0.42)',
      tooltipSurface: '#ffffff',
      tooltipTitle: '#111827',
      tooltipText: '#374151',
      tooltipBorder: '#d1d5db',
      primary: '#2563eb',
      secondary: '#b45309',
      profit: '#0f766e',
      warning: '#be123c',
      neutral: '#64748b'
    })

const chartData = computed<ChartData<'line'>>(() => ({
  labels: props.timestamps.map(formatAxisTimestamp),
  datasets: props.series.map((item, index) => buildDataset(item, index))
}))

const chartOptions = computed<ChartOptions<'line'>>(() => {
  const colors = palette.value
  const reduceMotion = typeof window !== 'undefined'
    && window.matchMedia?.('(prefers-reduced-motion: reduce)').matches

  return {
    responsive: true,
    maintainAspectRatio: false,
    animation: reduceMotion ? false : { duration: 180 },
    interaction: { intersect: false, mode: 'index' },
    plugins: {
      legend: {
        display: props.showLegend,
        position: 'top',
        align: 'end',
        labels: {
          color: colors.text,
          usePointStyle: true,
          boxWidth: 8,
          boxHeight: 8,
          padding: 16,
          font: { size: 11 }
        }
      },
      tooltip: {
        backgroundColor: colors.tooltipSurface,
        titleColor: colors.tooltipTitle,
        bodyColor: colors.tooltipText,
        footerColor: colors.tooltipText,
        borderColor: colors.tooltipBorder,
        borderWidth: 1,
        padding: 12,
        displayColors: true,
        callbacks: {
          title: (items: TooltipItem<'line'>[]) => {
            const index = items[0]?.dataIndex
            return index === undefined ? '' : formatFullTimestamp(props.timestamps[index] || '')
          },
          label: (context: TooltipItem<'line'>) => {
            const label = context.dataset.label || ''
            return `${label}：${context.parsed.y == null ? '-' : formatValue(context.parsed.y)}`
          },
          footer: (items: TooltipItem<'line'>[]) => {
            const index = items[0]?.dataIndex
            return index === undefined || !props.tooltipFooter ? '' : props.tooltipFooter(index)
          }
        }
      }
    },
    scales: {
      x: {
        grid: { display: false },
        ticks: {
          color: colors.text,
          maxTicksLimit: props.maxTicks,
          autoSkip: true,
          autoSkipPadding: 16,
          font: { size: 10 }
        },
        border: { color: colors.grid }
      },
      y: {
        beginAtZero: props.zeroBaseline,
        grid: {
          color: (context) => Number(context.tick.value) === 0 ? colors.zero : colors.grid,
          lineWidth: (context) => Number(context.tick.value) === 0 ? 1.5 : 1
        },
        border: { display: false },
        ticks: {
          color: colors.text,
          callback: (value) => formatValue(Number(value)),
          font: { size: 10 }
        }
      }
    }
  }
})

function buildDataset(item: TrendChartSeries, index: number): ChartDataset<'line'> {
  const tone = item.tone || 'primary'
  const color = palette.value[tone]
  const isPrimary = tone === 'primary'

  return {
    label: item.label,
    data: item.data,
    borderColor: color,
    backgroundColor: item.fill ? withAlpha(color, isDark.value ? 0.16 : 0.1) : 'transparent',
    borderWidth: isPrimary ? 2.5 : 2,
    borderDash: item.borderDash || [],
    fill: item.fill ? 'origin' : false,
    tension: item.stepped ? 0 : 0.22,
    stepped: item.stepped || false,
    pointStyle: item.pointStyle || defaultPointStyle(index),
    pointRadius: item.stepped ? 2.5 : 0,
    pointHoverRadius: 4,
    pointHitRadius: 12,
    order: item.order ?? index
  }
}

function defaultPointStyle(index: number): TrendChartPointStyle {
  return (['circle', 'rect', 'triangle', 'rectRot'] as TrendChartPointStyle[])[index % 4]
}

function formatValue(value: number): string {
  return props.valueFormatter(value)
}

function formatAxisTimestamp(value: string): string {
  const date = new Date(value)
  return Number.isNaN(date.getTime())
    ? value
    : date.toLocaleString(undefined, { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' })
}

function formatFullTimestamp(value: string): string {
  const date = new Date(value)
  return Number.isNaN(date.getTime())
    ? value
    : date.toLocaleString(undefined, {
        year: 'numeric',
        month: '2-digit',
        day: '2-digit',
        hour: '2-digit',
        minute: '2-digit',
        second: '2-digit'
      })
}

function withAlpha(hex: string, alpha: number): string {
  const normalized = hex.replace('#', '')
  if (normalized.length !== 6) return hex
  const red = Number.parseInt(normalized.slice(0, 2), 16)
  const green = Number.parseInt(normalized.slice(2, 4), 16)
  const blue = Number.parseInt(normalized.slice(4, 6), 16)
  return `rgba(${red}, ${green}, ${blue}, ${alpha})`
}

function updateTheme(): void {
  isDark.value = typeof document !== 'undefined' && document.documentElement.classList.contains('dark')
}

onMounted(() => {
  updateTheme()
  if (typeof MutationObserver === 'undefined') return
  themeObserver = new MutationObserver(updateTheme)
  themeObserver.observe(document.documentElement, { attributes: true, attributeFilter: ['class'] })
})

onBeforeUnmount(() => {
  themeObserver?.disconnect()
  themeObserver = null
})
</script>
