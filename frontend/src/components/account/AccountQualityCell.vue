<template>
  <div class="min-w-[18.75rem] tabular-nums">
    <div v-if="loading && !stats" class="space-y-1.5" aria-busy="true">
      <div class="h-4 w-40 animate-pulse rounded bg-gray-200 dark:bg-gray-700"></div>
      <div class="h-4 w-36 animate-pulse rounded bg-gray-200 dark:bg-gray-700"></div>
    </div>
    <div v-else-if="error && !stats" class="text-xs text-red-500">{{ error }}</div>
    <div v-else-if="stats" class="space-y-1 text-xs">
      <div v-if="showActivity" class="flex min-h-5 items-center gap-1 whitespace-nowrap">
        <span
          :class="`inline-flex rounded px-1.5 py-0.5 font-semibold ${activityClass(resolvedActivityState)}`"
          :data-quality-activity="resolvedActivityState"
        >
          {{ t(`admin.accounts.quality.activity.${resolvedActivityState}`) }}
        </span>
        <span v-if="activity" class="text-gray-500 dark:text-gray-400">
          {{
            t('admin.accounts.quality.activity.counts', {
              success: activity.successful_request_count,
              failed: activity.failed_request_count
            })
          }}
        </span>
        <span class="text-gray-400 dark:text-gray-500">{{ lastSuccessLabel }}</span>
      </div>
      <QualityRow :label="t('admin.accounts.quality.last10')" :window="stats.last_10" />
      <QualityRow :label="t('admin.accounts.quality.last100')" :window="stats.last_100" />
    </div>
    <span v-else class="text-xs text-gray-400">-</span>
  </div>
</template>

<script setup lang="ts">
import { computed, defineComponent, h, type PropType } from 'vue'
import { useI18n } from 'vue-i18n'
import type { AccountQualityActivity, AccountQualityPeriod, AccountQualityWindow } from '@/types'

type ActivityStateOverride = 'unassigned' | 'paused'

const props = withDefaults(defineProps<{
  stats?: AccountQualityPeriod | null
  activity?: AccountQualityActivity | null
  activityStateOverride?: ActivityStateOverride | null
  showActivity?: boolean
  muted?: boolean
  loading?: boolean
  error?: string | null
}>(), {
  stats: null,
  activity: null,
  activityStateOverride: null,
  showActivity: false,
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

const gradeClass = (grade: string | undefined): string => {
  if (props.muted || !grade) return 'bg-gray-100 text-gray-500 dark:bg-gray-700 dark:text-gray-300'
  if (grade.startsWith('S')) return 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/35 dark:text-emerald-300'
  if (grade.startsWith('A')) return 'bg-blue-100 text-blue-700 dark:bg-blue-900/35 dark:text-blue-300'
  if (grade.startsWith('B')) return 'bg-amber-100 text-amber-700 dark:bg-amber-900/35 dark:text-amber-300'
  return 'bg-red-100 text-red-700 dark:bg-red-900/35 dark:text-red-300'
}

const resolvedActivityState = computed(() => props.activityStateOverride || props.activity?.state || 'idle')

const activityClass = (state: string): string => {
  if (state === 'active') return 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/35 dark:text-emerald-300'
  if (state === 'degraded' || state === 'low_sample') {
    return 'bg-amber-100 text-amber-700 dark:bg-amber-900/35 dark:text-amber-300'
  }
  if (state === 'failing') return 'bg-red-100 text-red-700 dark:bg-red-900/35 dark:text-red-300'
  return 'bg-gray-100 text-gray-500 dark:bg-gray-700 dark:text-gray-300'
}

const lastSuccessLabel = computed(() => {
  const raw = props.activity?.last_success_at
  if (!raw) return t('admin.accounts.quality.activity.noSuccess24h')
  const timestamp = new Date(raw).getTime()
  if (!Number.isFinite(timestamp)) return t('admin.accounts.quality.activity.noSuccess24h')
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
  return t('admin.accounts.quality.activity.noSuccess24h')
})

const scoreLabel = (window: AccountQualityWindow): string => {
  if (window.quality_score == null) return '-'
  return `${window.quality_grade || ''} ${window.quality_score}`.trim()
}

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
  return base
}

const QualityRow = defineComponent({
  props: {
    label: { type: String, required: true },
    window: { type: Object as PropType<AccountQualityWindow>, required: true }
  },
  setup(rowProps) {
    return () => h('div', { class: 'grid min-h-5 grid-cols-[3.5rem_3.5rem_2.75rem_3.25rem_2.25rem_3.25rem] items-center gap-x-1 whitespace-nowrap' }, [
      h('span', { class: 'text-gray-500 dark:text-gray-400' }, rowProps.label),
      h('span', {
        class: `inline-flex justify-center rounded px-1 py-0.5 font-semibold ${gradeClass(rowProps.window.quality_grade)}`,
        'data-quality-grade': rowProps.window.quality_grade || undefined,
        title: scoreTitle(rowProps.window)
      }, scoreLabel(rowProps.window)),
      h('span', { class: 'text-gray-500 dark:text-gray-400' }, t('admin.accounts.quality.firstTokenShort')),
      h('span', { class: 'font-mono font-medium text-gray-700 dark:text-gray-200' }, formatLatency(rowProps.window.average_first_token_ms)),
      h('span', { class: 'text-gray-500 dark:text-gray-400' }, t('admin.accounts.quality.totalShort')),
      h('span', { class: 'font-mono font-medium text-gray-700 dark:text-gray-200' }, formatLatency(rowProps.window.average_duration_ms))
    ])
  }
})
</script>
