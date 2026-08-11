<template>
  <div v-if="loading" class="rounded-xl border border-dashed border-gray-300 py-12 text-center text-sm text-gray-500 dark:border-dark-600 dark:text-dark-300">
    {{ t('common.loading') }}
  </div>
  <div v-else-if="!trend" class="rounded-xl border border-dashed border-gray-300 py-12 text-center text-sm text-gray-500 dark:border-dark-600 dark:text-dark-300">
    {{ t('admin.upstreamConfigs.operations.emptyRateTrend') }}
  </div>
  <div v-else class="space-y-4">
    <div class="grid grid-cols-2 gap-3 sm:grid-cols-4">
      <div class="metric-block"><span>{{ t('admin.upstreamConfigs.operations.currentRate') }}</span><strong>{{ formatRate(trend.current_rate) }}</strong></div>
      <div class="metric-block"><span>{{ t('admin.upstreamConfigs.operations.previousRate') }}</span><strong>{{ formatRate(trend.previous_rate) }}</strong></div>
      <div class="metric-block"><span>{{ t('admin.upstreamConfigs.operations.lastChanged') }}</span><strong>{{ formatTime(trend.last_changed_at) }}</strong></div>
      <div class="metric-block"><span>{{ t('admin.upstreamConfigs.operations.observedSince') }}</span><strong>{{ formatTime(trend.first_observed_at) }}</strong></div>
    </div>
    <UpstreamKeyRateTrendChart :points="trend.points || []" />
    <section class="space-y-3">
      <h3 class="text-sm font-semibold text-gray-900 dark:text-gray-100">{{ t('admin.upstreamConfigs.operations.rateChanges') }}</h3>
      <div v-for="change in trend.changes || []" :key="`${change.type}-${change.occurred_at}`" class="operation-row">
        <div class="flex items-center justify-between gap-3">
          <span class="font-medium text-gray-900 dark:text-gray-100">{{ eventTypeLabel(change.type) }}</span>
          <span class="text-xs text-gray-500 dark:text-dark-400">{{ formatTime(change.occurred_at) }}</span>
        </div>
        <div class="mt-1 text-xs text-gray-600 dark:text-dark-300">
          {{ formatRate(change.old_rate) }} → {{ formatRate(change.new_rate) }}
        </div>
      </div>
      <div v-if="!(trend.changes || []).length" class="rounded-lg bg-gray-50 py-6 text-center text-sm text-gray-500 dark:bg-dark-800 dark:text-dark-300">
        {{ t('admin.upstreamConfigs.operations.emptyRateChanges') }}
      </div>
    </section>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { UpstreamKeyRateTrend } from '@/api/admin/upstreamConfigs'
import UpstreamKeyRateTrendChart from './UpstreamKeyRateTrendChart.vue'

defineProps<{
  trend: UpstreamKeyRateTrend | null
  loading?: boolean
}>()

const { t } = useI18n()

function formatRate(value: number | null | undefined): string {
  if (value == null || !Number.isFinite(value)) return '-'
  return `${value.toLocaleString(undefined, { maximumFractionDigits: 6 })}x`
}

function formatTime(value?: string | null): string {
  return value ? new Date(value).toLocaleString() : '-'
}

const knownEventTypes = new Set([
  'key_actual_rate_changed',
  'key_rate_changed',
  'key_effective_rate_changed',
  'key_platform_changed',
  'key_platform_conflict',
  'key_missing_detected',
  'key_marked_stale',
  'key_restored',
  'key_deleted',
  'group_added',
  'group_rate_changed',
  'group_removed',
  'sync_failed',
  'sync_recovered',
  'recharge_rate_changed',
  'balance_recalculated',
  'currency_conversion_changed'
])

function eventTypeLabel(type: string): string {
  const key = knownEventTypes.has(type) ? type : 'unknown'
  return t(`admin.upstreamConfigs.operations.eventTypes.${key}`)
}
</script>

<style scoped>
.metric-block {
  @apply rounded-xl border border-gray-200 bg-gray-50 px-3 py-3 dark:border-dark-700 dark:bg-dark-800;
}
.metric-block span {
  @apply block text-[11px] text-gray-500 dark:text-dark-400;
}
.metric-block strong {
  @apply mt-1 block text-sm font-semibold tabular-nums text-gray-900 dark:text-gray-100;
}
.operation-row {
  @apply rounded-xl border border-gray-200 bg-white px-4 py-3 dark:border-dark-700 dark:bg-dark-800;
}
</style>
