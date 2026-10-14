<template>
  <HelpTooltip v-if="summary" trigger="adaptive" trigger-class="!ml-0 max-w-full" width-class="w-80 max-w-[calc(100vw-2rem)] max-h-[calc(100vh-2rem)] overflow-y-auto">
    <template #trigger>
      <button
        type="button"
        data-test="cache-guard-state"
        :data-cache-guard-state="summary.state"
        :class="['inline-flex max-w-full items-center rounded-md px-2 py-1 text-xs font-medium focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary-500', stateClass(summary.state)]"
      >
        <span class="truncate">{{ t(`admin.upstreamManagement.cacheGuard.states.${summary.state}`) }} · {{ rateLabel(summary.rate_percent) }}</span>
      </button>
    </template>
    <div class="mb-1 font-medium">{{ t('admin.upstreamManagement.cacheGuard.accountSummary') }}</div>
    <div>{{ t('admin.upstreamManagement.cacheGuard.rate') }}: {{ rateLabel(summary.rate_percent) }}</div>
    <div>{{ t('admin.upstreamManagement.cacheGuard.validRequests') }}: {{ summary.valid_requests }}</div>
    <div>{{ t('admin.upstreamManagement.cacheGuard.windowMinutes', { count: summary.window_minutes }) }}</div>
    <div>{{ t('admin.upstreamManagement.cacheGuard.reason') }}: {{ reasonLabel(summary.reason) }}</div>
    <template v-if="summary.state === 'recovering' || summary.recovery_started_at">
      <div class="mt-2 border-t border-white/10 pt-2">{{ t('admin.upstreamManagement.cacheGuard.recoveryRate') }}: {{ rateLabel(summary.recovery_rate_percent) }}</div>
      <div>{{ t('admin.upstreamManagement.cacheGuard.recoveryRequests') }}: {{ summary.recovery_valid_requests }}</div>
      <div>{{ t('admin.upstreamManagement.cacheGuard.recoverySessions') }}: {{ summary.recovery_sessions }}</div>
      <div>{{ t('admin.upstreamManagement.cacheGuard.recoveryStartedAt') }}: {{ dateLabel(summary.recovery_started_at) }}</div>
    </template>
    <div v-if="summary.cooldown_until">{{ t('admin.upstreamManagement.cacheGuard.cooldownUntil') }}: {{ dateLabel(summary.cooldown_until) }}</div>
    <div>{{ t('admin.upstreamManagement.cacheGuard.updatedAt') }}: {{ dateLabel(summary.updated_at) }}</div>
    <div v-if="states.length" class="mt-2 border-t border-white/10 pt-2 font-medium">{{ t('admin.upstreamManagement.cacheGuard.groupExecution') }}</div>
    <div v-for="item in states" :key="item.group_id" :data-cache-guard-group="item.group_id" class="mt-1 flex flex-wrap items-center gap-x-1">
      <span>{{ groupLabel(item) }}:</span>
      <span>{{ t(`admin.upstreamManagement.cacheGuard.states.${item.state}`) }}</span>
      <span v-if="item.trial_participant">· {{ t('admin.upstreamManagement.cacheGuard.trialParticipant') }}</span>
    </div>
    <p class="mt-2 text-gray-300">{{ t('admin.upstreamManagement.cacheGuard.metricHint') }}</p>
  </HelpTooltip>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import HelpTooltip from '@/components/common/HelpTooltip.vue'
import type { Account, UpstreamCacheGuardState } from '@/types'
import { formatDateTime } from '@/utils/format'

const props = defineProps<{ account: Account }>()
const { t, te } = useI18n()
const summary = computed(() => props.account.upstream_cache_guard)
const states = computed(() => props.account.upstream_cache_guard_states || [])

function groupLabel(item: UpstreamCacheGuardState) {
  return item.group_name || props.account.groups?.find(group => group.id === item.group_id)?.name || t('admin.upstreamManagement.cacheGuard.groupId', { id: item.group_id })
}
function rateLabel(rate: number | null) {
  return rate == null || !Number.isFinite(rate) ? t('admin.upstreamManagement.cacheGuard.unknownRate') : `${rate.toFixed(1)}%`
}
function reasonLabel(reason: string) {
  const key = `admin.upstreamManagement.cacheGuard.reasons.${reason}`
  return te(key) ? t(key) : reason || '-'
}
function dateLabel(value: string | null) { return value ? formatDateTime(value) : '-' }
function stateClass(state: UpstreamCacheGuardState['state']) {
  switch (state) {
    case 'protected': return 'bg-amber-100 text-amber-700 dark:bg-amber-900/30 dark:text-amber-300'
    case 'recovering': return 'bg-cyan-100 text-cyan-700 dark:bg-cyan-900/30 dark:text-cyan-300'
    case 'normal': return 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300'
    case 'unavailable': return 'bg-rose-100 text-rose-700 dark:bg-rose-900/30 dark:text-rose-300'
    default: return 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-gray-300'
  }
}
</script>
