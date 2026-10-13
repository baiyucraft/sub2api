<template>
  <div v-if="states.length" class="flex max-w-full flex-col items-start gap-1" data-test="cache-guard-states">
    <HelpTooltip v-for="item in states" :key="item.group_id" trigger="click" trigger-class="!ml-0 max-w-full" width-class="w-80 max-w-[calc(100vw-2rem)]">
      <template #trigger>
        <button type="button" :data-cache-guard-state="item.state" :class="['inline-flex max-w-full items-center gap-1.5 rounded-md px-2 py-1 text-xs font-medium focus-visible:ring-2 focus-visible:ring-violet-500', stateClass(item.state)]">
          <span class="truncate">{{ groupLabel(item) }} · {{ t(`admin.upstreamManagement.cacheGuard.states.${item.state}`) }} · {{ rateLabel(item.rate_percent) }}</span>
          <span v-if="item.linked_protection?.protected" class="shrink-0 rounded bg-white/25 px-1 dark:bg-black/15" data-test="linked-protection">{{ t(`admin.upstreamManagement.cacheGuard.${item.state === 'observing' ? 'linkedObservation' : 'linkedProtection'}`) }}</span>
        </button>
      </template>
      <div class="mb-1 font-medium">{{ t('admin.upstreamManagement.cacheGuard.title') }} · {{ groupLabel(item) }}</div>
      <div>{{ t('admin.upstreamManagement.cacheGuard.rate') }}: {{ rateLabel(item.rate_percent) }}</div>
      <div>{{ t('admin.upstreamManagement.cacheGuard.validRequests') }}: {{ item.valid_requests }}</div>
      <div>{{ t('admin.upstreamManagement.cacheGuard.reason') }}: {{ item.linked_protection?.protected && item.state === 'observing' ? t('admin.upstreamManagement.cacheGuard.linkedObserveReason') : reasonLabel(item.reason) }}</div>
      <div v-if="item.linked_protection?.protected && item.native_state">{{ t('admin.upstreamManagement.cacheGuard.nativeState') }}: {{ t(`admin.upstreamManagement.cacheGuard.states.${item.native_state}`) }} · {{ reasonLabel(item.native_reason || '') }}</div>
      <template v-if="item.state === 'recovering' || item.recovery_started_at">
        <div class="mt-2 border-t border-white/10 pt-2">{{ t('admin.upstreamManagement.cacheGuard.recoveryRate') }}: {{ rateLabel(item.recovery_rate_percent) }}</div>
        <div>{{ t('admin.upstreamManagement.cacheGuard.recoveryRequests') }}: {{ item.recovery_valid_requests }}</div>
        <div>{{ t('admin.upstreamManagement.cacheGuard.recoverySessions') }}: {{ item.recovery_sessions }}</div>
        <div>{{ t('admin.upstreamManagement.cacheGuard.recoveryStartedAt') }}: {{ dateLabel(item.recovery_started_at) }}</div>
      </template>
      <div v-if="item.cooldown_until">{{ t('admin.upstreamManagement.cacheGuard.cooldownUntil') }}: {{ dateLabel(item.cooldown_until) }}</div>
      <div>{{ t('admin.upstreamManagement.cacheGuard.updatedAt') }}: {{ dateLabel(item.updated_at) }}</div>
      <template v-if="item.linked_protection?.protected">
        <div v-for="source in item.linked_protection.sources" :key="source.group_id" class="mt-2 border-t border-white/10 pt-2" :data-cache-guard-source="source.group_id">
          <div class="font-medium">{{ t('admin.upstreamManagement.cacheGuard.linkedSource') }}: {{ groupLabel(source) }}</div>
          <div>{{ t('admin.upstreamManagement.cacheGuard.sourceThreshold') }}: {{ rateLabel(source.threshold_percent) }}</div>
          <div>{{ t('admin.upstreamManagement.cacheGuard.sourceState') }}: {{ t(`admin.upstreamManagement.cacheGuard.sourcePhases.${source.phase}`) }}</div>
          <div>{{ t('admin.upstreamManagement.cacheGuard.sourceRate') }}: {{ rateLabel(source.rate_percent) }}</div>
          <div>{{ t('admin.upstreamManagement.cacheGuard.sourceRequests') }}: {{ source.valid_requests }}</div>
          <template v-if="source.phase === 'trial' || source.recovery_started_at">
            <div>{{ t('admin.upstreamManagement.cacheGuard.sourceRecoveryRate') }}: {{ rateLabel(source.recovery_rate_percent) }}</div>
            <div>{{ t('admin.upstreamManagement.cacheGuard.sourceRecoveryRequests') }}: {{ source.recovery_valid_requests }}</div>
            <div>{{ t('admin.upstreamManagement.cacheGuard.sourceRecoverySessions') }}: {{ source.recovery_sessions }}</div>
            <div>{{ t('admin.upstreamManagement.cacheGuard.recoveryStartedAt') }}: {{ dateLabel(source.recovery_started_at) }}</div>
          </template>
          <div v-if="source.cooldown_until">{{ t('admin.upstreamManagement.cacheGuard.cooldownUntil') }}: {{ dateLabel(source.cooldown_until) }}</div>
          <p class="mt-1 text-gray-300">{{ t('admin.upstreamManagement.cacheGuard.sourceRecoveryRequirement', { count: source.min_valid_requests, rate: source.recovery_threshold_percent }) }}</p>
        </div>
        <p class="mt-2 text-gray-300">{{ t(`admin.upstreamManagement.cacheGuard.${item.state === 'observing' ? 'linkedObserveHint' : 'linkedHint'}`) }}</p>
      </template>
      <p class="mt-2 text-gray-300">{{ t('admin.upstreamManagement.cacheGuard.metricHint') }}</p>
    </HelpTooltip>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import HelpTooltip from '@/components/common/HelpTooltip.vue'
import type { Account, UpstreamCacheGuardState } from '@/types'
import { formatDateTime } from '@/utils/format'

const props = defineProps<{ account: Account }>()
const { t, te } = useI18n()
const states = computed(() => (props.account.upstream_cache_guard_states || []).filter(item => item.state !== 'disabled'))

function groupLabel(item: { group_id: number; group_name?: string }) {
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
    case 'normal': return 'bg-violet-100 text-violet-700 dark:bg-violet-900/30 dark:text-violet-300'
    default: return 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-gray-300'
  }
}
</script>
