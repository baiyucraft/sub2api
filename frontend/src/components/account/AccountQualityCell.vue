<template>
  <div class="w-[17rem] min-w-[17rem] max-w-[17rem] tabular-nums">
    <div v-if="loading && !stats" class="grid gap-1" aria-busy="true">
      <div class="h-5 w-full animate-pulse rounded bg-gray-200 dark:bg-gray-700"></div>
      <div class="h-5 w-full animate-pulse rounded bg-gray-200 dark:bg-gray-700"></div>
      <div class="h-5 w-full animate-pulse rounded bg-gray-200 dark:bg-gray-700"></div>
    </div>
    <div v-else-if="error && !stats" class="text-xs text-red-500">{{ error }}</div>
    <div v-else-if="stats" class="grid gap-1 text-[11px] leading-5">
      <div
        class="grid min-h-5 grid-cols-[minmax(0,1fr)_auto_2.5rem] items-center gap-x-1 whitespace-nowrap"
        :title="activityTitle"
      >
        <span
          :class="`min-w-0 truncate rounded px-1.5 text-center font-semibold ${activityClass(resolvedActivityState)}`"
          :data-quality-activity="resolvedActivityState"
        >
          {{ t(`admin.accounts.quality.activity.${resolvedActivityState}`) }}
        </span>
        <span class="font-mono text-gray-600 dark:text-gray-300">
          {{ compactActivityCounts }}
        </span>
        <span class="text-right font-mono text-gray-400 dark:text-gray-500">{{ lastSuccessLabel }}</span>
      </div>
      <QualityRow label="1H" :window="stats.recent_1h" />
      <QualityRow label="24H" :window="stats.recent_24h" :muted="muted" />
    </div>
    <span v-else class="text-xs text-gray-400">-</span>
  </div>
</template>

<script setup lang="ts">
import { computed, defineComponent, h, type PropType } from 'vue'
import { useI18n } from 'vue-i18n'
import type { AccountQualityStats, AccountQualityWindow } from '@/types'

type ActivityStateOverride = 'unassigned' | 'paused'

const props = withDefaults(defineProps<{
  stats?: AccountQualityStats | null
  activityStateOverride?: ActivityStateOverride | null
  muted?: boolean
  loading?: boolean
  error?: string | null
}>(), {
  stats: null,
  activityStateOverride: null,
  muted: false,
  loading: false,
  error: null
})

const { t } = useI18n()

const formatLatency = (value: number | null): string => {
  if (value == null || !Number.isFinite(value)) return '-'
  if (value < 1000) return `${Math.round(value)}ms`
  const seconds = value / 1000
  return `${seconds < 10 ? seconds.toFixed(1) : Math.round(seconds)}s`
}

const gradeClass = (grade: string | undefined, muted = false): string => {
  if (muted || !grade) return 'bg-gray-100 text-gray-500 dark:bg-gray-700 dark:text-gray-300'
  if (grade.startsWith('S')) return 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/35 dark:text-emerald-300'
  if (grade.startsWith('A')) return 'bg-blue-100 text-blue-700 dark:bg-blue-900/35 dark:text-blue-300'
  if (grade.startsWith('B')) return 'bg-amber-100 text-amber-700 dark:bg-amber-900/35 dark:text-amber-300'
  return 'bg-red-100 text-red-700 dark:bg-red-900/35 dark:text-red-300'
}

const activity = computed(() => props.stats?.activity ?? null)
const resolvedActivityState = computed(() => props.activityStateOverride || activity.value?.state || 'idle')

const activityClass = (state: string): string => {
  if (state === 'active') return 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/35 dark:text-emerald-300'
  if (state === 'degraded' || state === 'low_sample') {
    return 'bg-amber-100 text-amber-700 dark:bg-amber-900/35 dark:text-amber-300'
  }
  if (state === 'failing') return 'bg-red-100 text-red-700 dark:bg-red-900/35 dark:text-red-300'
  return 'bg-gray-100 text-gray-500 dark:bg-gray-700 dark:text-gray-300'
}

const lastSuccessLabel = computed(() => {
  const raw = activity.value?.last_success_at
  if (!raw) return t('admin.accounts.quality.activity.over24h')
  const timestamp = new Date(raw).getTime()
  if (!Number.isFinite(timestamp)) return t('admin.accounts.quality.activity.over24h')
  const elapsedMs = Math.max(0, Date.now() - timestamp)
  if (elapsedMs < 60_000) return t('admin.accounts.quality.activity.lastSuccessNow')
  if (elapsedMs < 60 * 60_000) {
    return t('admin.accounts.quality.activity.lastSuccessMinutes', {
      count: Math.max(1, Math.floor(elapsedMs / 60_000))
    })
  }
  if (elapsedMs < 24 * 60 * 60_000) {
    return t('admin.accounts.quality.activity.lastSuccessHours', {
      count: Math.max(1, Math.floor(elapsedMs / (60 * 60_000)))
    })
  }
  return t('admin.accounts.quality.activity.over24h')
})

const compactActivityCounts = computed(() => {
  const successful = activity.value?.successful_request_count ?? 0
  const failed = activity.value?.failed_request_count ?? 0
  return `${successful}/${failed}`
})

const activityTitle = computed(() => t('admin.accounts.quality.activity.title', {
  state: t(`admin.accounts.quality.activity.${resolvedActivityState.value}`),
  success: activity.value?.successful_request_count ?? 0,
  failed: activity.value?.failed_request_count ?? 0,
  lastSuccess: lastSuccessLabel.value
}))

const scoreLabel = (window: AccountQualityWindow): string => {
  if (window.quality_score == null) return '-'
  return `${window.quality_grade || '-'} ${window.quality_score}`
}

const successRateLabel = (window: AccountQualityWindow): string => {
  if (window.success_rate == null || !Number.isFinite(window.success_rate)) return '-'
  return `${Math.round(window.success_rate)}%`
}

const cacheRateLabel = (window: AccountQualityWindow): string => {
  if (window.cache_rate == null || !Number.isFinite(window.cache_rate)) return '-'
  return `${Math.round(window.cache_rate)}%`
}

const cacheRateTitle = (window: AccountQualityWindow): string => t('admin.accounts.quality.cacheRateTitle', {
  rate: cacheRateLabel(window),
  numerator: window.cache_rate_numerator,
  denominator: window.cache_rate_denominator
})

const scoreTitle = (window: AccountQualityWindow): string => {
  if (window.quality_score == null) {
    return t('admin.accounts.quality.insufficientSamples', { count: window.sample_count })
  }
  const base = t('admin.accounts.quality.scoreTitle', {
    score: window.quality_score,
    grade: window.quality_grade || '-',
    count: window.sample_count,
    firstCount: window.first_token_sample_count
  })
  if (window.score_basis === 'duration_only') {
    return `${base} · ${t('admin.accounts.quality.durationOnly')}`
  }
  if (window.score_basis === 'ttft_only') {
    return `${base} · ${t('admin.accounts.quality.ttftOnly')}`
  }
  if (window.score_basis === 'ttft_duration_cache' || window.score_basis === 'ttft_cache' || window.score_basis === 'duration_cache') {
    return `${base} · ${t('admin.accounts.quality.cacheWeighted')}`
  }
  return base
}

const windowTitle = (label: string, window: AccountQualityWindow): string => {
  const latency = t('admin.accounts.quality.latencyTitle', {
    firstToken: formatLatency(window.average_first_token_ms),
    duration: formatLatency(window.average_duration_ms)
  })
  return `${label} · ${scoreTitle(window)} · ${latency} · ${cacheRateTitle(window)}`
}

const QualityRow = defineComponent({
  props: {
    label: { type: String, required: true },
    window: { type: Object as PropType<AccountQualityWindow>, required: true },
    muted: { type: Boolean, default: false }
  },
  setup(rowProps) {
    return () => h('div', {
      class: `grid min-h-5 grid-cols-[1.5rem_3.25rem_2.5rem_minmax(0,1fr)_2.75rem_2.25rem] items-center gap-x-1 whitespace-nowrap ${rowProps.muted ? 'opacity-60' : ''}`,
      title: windowTitle(rowProps.label, rowProps.window),
      'data-quality-window': rowProps.label
    }, [
      h('span', { class: 'font-semibold text-gray-500 dark:text-gray-400' }, rowProps.label),
      h('span', {
        class: `inline-flex justify-center rounded px-1 font-semibold ${gradeClass(rowProps.window.quality_grade, rowProps.muted)}`,
        'data-quality-grade': rowProps.window.quality_grade || undefined,
        title: scoreTitle(rowProps.window)
      }, scoreLabel(rowProps.window)),
      h('span', {
        class: 'text-center font-mono font-semibold text-emerald-600 dark:text-emerald-400',
        title: `${rowProps.window.successful_request_count}/${rowProps.window.failed_request_count}`
      }, successRateLabel(rowProps.window)),
      h('span', {
        class: 'min-w-0 truncate text-center font-mono font-medium text-gray-700 dark:text-gray-200'
      }, `${formatLatency(rowProps.window.average_first_token_ms)} / ${formatLatency(rowProps.window.average_duration_ms)}`),
      h('span', {
        class: 'text-center font-mono font-semibold text-cyan-600 dark:text-cyan-400',
        title: cacheRateTitle(rowProps.window),
        'data-quality-cache-rate': rowProps.window.cache_rate == null ? undefined : String(rowProps.window.cache_rate)
      }, cacheRateLabel(rowProps.window)),
      h('span', {
        class: 'text-right font-mono text-gray-500 dark:text-gray-400'
      }, `n${rowProps.window.sample_count}`)
    ])
  }
})
</script>
