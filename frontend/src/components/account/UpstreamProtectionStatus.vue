<template>
  <div v-if="hasMetrics" class="flex max-w-full flex-col items-start gap-1">
    <button v-if="cache || groups.length" type="button" data-test="protection-cache-summary" :aria-label="`${t('admin.upstreamManagement.protection.viewCacheDetails')}：${cacheSummary}`" aria-haspopup="dialog" :aria-expanded="show" :class="['protection-summary', stateClass(cacheState)]" @click="show = true">
      <Icon name="shield" size="xs" />
      <span class="truncate">{{ cacheSummary }}</span>
      <Icon name="chevronRight" size="xs" />
    </button>
    <button type="button" data-test="protection-ttft-summary" :aria-label="`${t('admin.upstreamManagement.protection.viewTTFTDetails')}：${ttftSummary}`" aria-haspopup="dialog" :aria-expanded="show" :class="['protection-summary', stateClass(ttftState)]" @click="show = true">
      <Icon name="clock" size="xs" />
      <span class="truncate">{{ ttftSummary }}</span>
      <Icon name="chevronRight" size="xs" />
    </button>
    <BaseDialog :show="show" :title="t('admin.upstreamManagement.protection.details')" width="wide" @close="show = false">
      <div class="space-y-6" data-test="upstream-protection-details">
        <div class="border-b border-gray-200 pb-4 dark:border-dark-700">
          <p class="break-words font-semibold text-gray-900 dark:text-white">{{ account.name }}</p>
          <p class="mt-1 text-xs leading-5 text-gray-500 dark:text-gray-400">{{ t('admin.upstreamManagement.protection.sharedHint') }}</p>
        </div>
        <section v-if="cache" class="rounded-xl border border-gray-200 p-4 dark:border-dark-600" data-test="protection-cache-metrics">
          <h4 class="text-sm font-semibold text-gray-900 dark:text-white">{{ t('admin.upstreamManagement.cacheGuard.accountSummary') }} · {{ t('admin.upstreamManagement.protection.cache') }}</h4>
          <div class="mt-4 grid gap-4 sm:grid-cols-3">
            <div><p class="metric-label">{{ t('admin.upstreamManagement.cacheGuard.rate') }}</p><p class="metric-value">{{ rateLabel(cache.rate_percent) }}</p></div>
            <div><p class="metric-label">{{ t('admin.upstreamManagement.cacheGuard.validRequests') }}</p><p class="metric-value">{{ cache.valid_requests }}</p></div>
            <div class="text-xs leading-6 text-gray-600 dark:text-gray-300"><p>{{ t('admin.upstreamManagement.cacheGuard.windowMinutes', { count: cache.window_minutes }) }}</p><p>{{ t('admin.upstreamManagement.cacheGuard.updatedAt') }}: {{ dateLabel(cache.updated_at) }}</p></div>
          </div>
          <p class="mt-3 text-xs leading-5 text-gray-500 dark:text-gray-400">{{ t('admin.upstreamManagement.cacheGuard.metricHint') }}</p>
        </section>
        <section>
          <h4 class="mb-3 text-sm font-semibold text-gray-900 dark:text-white">{{ t('admin.upstreamManagement.protection.models') }}</h4>
          <p v-if="!models.length" class="rounded-lg bg-gray-50 p-3 text-xs text-gray-500 dark:bg-dark-800 dark:text-gray-400">{{ t(!protection && legacyTTFT.length ? 'admin.upstreamManagement.protection.legacyTTFTMetrics' : 'admin.upstreamManagement.protection.noTTFTSamples') }}</p>
          <div v-else class="space-y-2">
            <article v-for="item in models" :key="item.model" class="rounded-xl border border-gray-200 p-4 dark:border-dark-600" data-test="protection-ttft-detail">
              <div class="flex flex-wrap items-center justify-between gap-2"><span class="min-w-0 break-all text-sm font-semibold text-gray-900 dark:text-white">{{ item.model }}</span><span :class="['state-badge', stateClass(item.state)]">{{ stateLabel(item.state) }}</span></div>
              <dl class="mt-3 grid grid-cols-2 gap-3 text-xs sm:grid-cols-3"><div><dt class="metric-label">{{ t('admin.upstreamManagement.protection.ewma') }}</dt><dd class="mt-1 font-medium tabular-nums text-gray-900 dark:text-gray-100">{{ milliseconds(item.ewma_ms) }}</dd></div><div><dt class="metric-label">{{ t('admin.upstreamManagement.protection.lastTTFT') }}</dt><dd class="mt-1 font-medium tabular-nums text-gray-900 dark:text-gray-100">{{ milliseconds(item.last_ttft_ms) }}</dd></div><div><dt class="metric-label">{{ t('admin.upstreamManagement.protection.samples') }}</dt><dd class="mt-1 font-medium tabular-nums text-gray-900 dark:text-gray-100">{{ item.sample_count }}</dd></div></dl>
              <div class="mt-3 grid gap-1 text-xs leading-5 text-gray-500 dark:text-gray-400 sm:grid-cols-2"><p>{{ t('admin.upstreamManagement.protection.lastSample') }}: {{ dateLabel(item.last_sample_at) }}</p><p>{{ t('admin.upstreamManagement.protection.expiresAt') }}: {{ dateLabel(item.expires_at) }}</p></div>
            </article>
          </div>
        </section>
        <section>
          <h4 class="mb-3 text-sm font-semibold text-gray-900 dark:text-white">{{ t('admin.upstreamManagement.protection.groups') }}</h4>
          <p v-if="!groups.length" class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.upstreamManagement.protection.noGroups') }}</p>
          <div v-else class="space-y-3">
            <article v-for="item in groups" :key="item.group_id" class="rounded-xl border border-gray-200 p-4 dark:border-dark-600" :data-protection-group="item.group_id">
              <div class="flex flex-wrap items-start justify-between gap-2"><h5 class="min-w-0 break-all text-sm font-semibold text-gray-900 dark:text-white">{{ groupLabel(item) }}</h5><p class="text-xs text-gray-600 dark:text-gray-300">{{ item.priority_source === 'unavailable' ? t('admin.upstreamManagement.protection.priorityUnavailable') : `${t(`admin.upstreamManagement.protection.priorities.${item.priority}`)} · ${sourceLabel(item.priority_source)}` }}</p></div>
              <div class="mt-4 grid gap-4 sm:grid-cols-2">
                <section class="min-w-0 space-y-2 text-xs leading-5 text-gray-600 dark:text-gray-300" data-test="protection-group-cache">
                  <div class="flex flex-wrap items-center gap-2"><h6 class="font-semibold">{{ t('admin.upstreamManagement.protection.cache') }}</h6><span :class="['state-badge', stateClass(item.cache.state)]">{{ stateLabel(item.cache.state) }}</span><span class="text-gray-500 dark:text-gray-400">{{ sourceLabel(item.cache.source) }}</span></div>
                  <p v-if="item.cache.source !== 'unavailable'">{{ t('admin.upstreamManagement.protection.cacheValues', { threshold: item.cache.threshold_percent, recovery: item.cache.recovery_threshold_percent, samples: item.cache.min_valid_requests }) }}</p>
                  <p>{{ reasonLabel(item.cache.reason) }}</p>
                  <div v-if="item.cache.state === 'recovering' || item.cache.recovery_started_at" class="rounded-lg bg-gray-50 p-2 dark:bg-dark-800"><p>{{ t('admin.upstreamManagement.cacheGuard.recoveryRate') }}: {{ rateLabel(item.cache.recovery_rate_percent) }}</p><p>{{ t('admin.upstreamManagement.cacheGuard.recoveryRequests') }}: {{ item.cache.recovery_valid_requests }}</p><p>{{ t('admin.upstreamManagement.cacheGuard.recoverySessions') }}: {{ item.cache.recovery_sessions }}</p><p>{{ t('admin.upstreamManagement.cacheGuard.recoveryStartedAt') }}: {{ dateLabel(item.cache.recovery_started_at) }}</p></div>
                  <p v-if="item.cache.cooldown_until">{{ t('admin.upstreamManagement.cacheGuard.cooldownUntil') }}: {{ dateLabel(item.cache.cooldown_until) }}</p>
                </section>
                <section class="min-w-0 space-y-2 text-xs leading-5 text-gray-600 dark:text-gray-300" data-test="protection-group-ttft">
                  <div class="flex flex-wrap items-center gap-2"><h6 class="font-semibold">{{ t('admin.upstreamManagement.protection.ttft') }}</h6><span :class="['state-badge', stateClass(item.ttft.state)]">{{ stateLabel(item.ttft.state) }}</span><span class="text-gray-500 dark:text-gray-400">{{ sourceLabel(item.ttft.source) }}</span></div>
                  <p v-if="item.ttft.source !== 'unavailable'">{{ t('admin.upstreamManagement.protection.ttftValues', { threshold: milliseconds(item.ttft.threshold_ms), samples: item.ttft.min_samples }) }}</p>
                  <p v-if="!item.ttft.models.length && item.ttft.state !== 'disabled'">{{ t('admin.upstreamManagement.protection.noTTFTSamples') }}</p>
                  <div v-for="model in item.ttft.models" :key="model.model" class="rounded-lg bg-gray-50 p-2 dark:bg-dark-800"><div class="flex flex-wrap items-center justify-between gap-1"><span class="break-all font-medium">{{ model.model }}</span><span :class="stateTextClass(model.state)">{{ stateLabel(model.state) }}</span></div><p>{{ reasonLabel(model.reason) }}</p><p v-if="isProtected(model.state)">{{ t('admin.upstreamManagement.protection.recovery', { current: model.recovery_samples, required: model.recovery_samples_required }) }}</p></div>
                </section>
              </div>
            </article>
          </div>
        </section>
      </div>
      <template #footer><button type="button" class="btn btn-secondary" data-test="protection-close" @click="show = false">{{ t('common.close') }}</button></template>
    </BaseDialog>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import type { Account, UpstreamProtectionState } from '@/types'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Icon from '@/components/icons/Icon.vue'
import { formatDateTime } from '@/utils/format'

const props = defineProps<{ account: Account }>()
const { t, te } = useI18n()
const show = ref(false)
const protection = computed(() => props.account.upstream_protection)
const cache = computed(() => protection.value?.cache ?? props.account.upstream_cache_guard)
const groups = computed(() => protection.value?.groups ?? [])
const models = computed(() => protection.value?.ttft_models ?? [])
const legacyTTFT = computed(() => props.account.ttft_guard_degradations ?? [])
const isProtected = (state: string) => ['degraded', 'draining', 'protected', 'recovering'].includes(state)
const cacheProtectedGroups = computed(() => new Set((protection.value ? groups.value.map(item => ({ group_id: item.group_id, state: item.cache.state })) : props.account.upstream_cache_guard_states ?? []).filter(item => isProtected(item.state)).map(item => item.group_id)).size)
const ttftProtectedGroups = computed(() => new Set(groups.value.filter(item => isProtected(item.ttft.state) || item.ttft.models.some(model => isProtected(model.state))).map(item => item.group_id)).size)
const protectedModels = computed(() => new Set(groups.value.flatMap(item => item.ttft.models.filter(model => isProtected(model.state)).map(model => model.model))).size)
const hasMetrics = computed(() => !!protection.value || !!cache.value || !!props.account.ttft_guard_degradations?.length)
function aggregateState(states: UpstreamProtectionState[]): UpstreamProtectionState {
  return (['unavailable', 'recovering', 'degraded', 'draining', 'protected', 'expired', 'insufficient', 'observing', 'normal', 'observed', 'disabled'].find(state => states.includes(state as UpstreamProtectionState)) as UpstreamProtectionState | undefined) ?? 'insufficient'
}
const cacheState = computed(() => aggregateState(groups.value.length ? groups.value.map(item => item.cache.state) : cache.value ? [cache.value.state] : []))
const ttftState = computed(() => !protection.value && legacyTTFT.value.length ? 'draining' : aggregateState(groups.value.length ? groups.value.map(item => item.ttft.state) : models.value.map(item => item.state)))
const cacheSummary = computed(() => `${t('admin.upstreamManagement.protection.cache')} ${rateLabel(cache.value?.rate_percent)} · ${cacheProtectedGroups.value ? t('admin.upstreamManagement.protection.protectedGroups', { count: cacheProtectedGroups.value }) : stateLabel(cacheState.value)}`)
const ttftSummary = computed(() => `${t('admin.upstreamManagement.protection.ttft')} · ${ttftProtectedGroups.value ? t('admin.upstreamManagement.protection.protectedModelsGroups', { models: protectedModels.value, groups: ttftProtectedGroups.value }) : protection.value ? stateLabel(ttftState.value) : legacyTTFT.value.length ? t('admin.upstreamManagement.protection.protectedModels', { count: new Set(legacyTTFT.value.map(item => item.model)).size }) : t('admin.upstreamManagement.protection.noTTFTDegradation')}`)
const stateLabel = (state: string) => t(`admin.upstreamManagement.protection.state.${state}`)
function stateClass(state: string) {
  if (['degraded', 'draining', 'protected'].includes(state)) return 'border-amber-200 bg-amber-50 text-amber-800 dark:border-amber-800 dark:bg-amber-950/30 dark:text-amber-300'
  if (state === 'recovering') return 'border-sky-200 bg-sky-50 text-sky-800 dark:border-sky-800 dark:bg-sky-950/30 dark:text-sky-300'
  if (state === 'normal') return 'border-emerald-200 bg-emerald-50 text-emerald-800 dark:border-emerald-800 dark:bg-emerald-950/30 dark:text-emerald-300'
  return 'border-gray-200 bg-gray-50 text-gray-600 dark:border-dark-600 dark:bg-dark-800 dark:text-gray-300'
}
const stateTextClass = (state: string) => isProtected(state) ? 'text-amber-700 dark:text-amber-300' : 'text-gray-500 dark:text-gray-400'
const rateLabel = (value?: number | null) => value == null || !Number.isFinite(value) ? t('admin.upstreamManagement.protection.unknown') : `${value.toFixed(1)}%`
const dateLabel = (value?: string | null) => value ? formatDateTime(value) : '—'
const milliseconds = (value: number | null) => value == null || !Number.isFinite(value) ? '—' : value >= 1000 ? `${(value / 1000).toFixed(1)}s` : `${Math.round(value)}ms`
const groupLabel = (item: { group_id: number; group_name?: string }) => item.group_name || props.account.groups?.find(group => group.id === item.group_id)?.name || `#${item.group_id}`
const sourceLabel = (source?: string) => source === 'unavailable' ? t('admin.upstreamManagement.protection.sourceUnavailable') : t(`admin.groups.ttftGuard.sources.${source === 'group' || source === 'disabled' ? source : 'global'}`)
function reasonLabel(reason: string) {
  const cacheKey = `admin.upstreamManagement.cacheGuard.reasons.${reason}`
  if (te(cacheKey)) return t(cacheKey)
  const key = ({ critical_sample: 'criticalSample', consecutive_elevated: 'consecutiveElevated', ewma: 'ewmaReason' } as Record<string, string>)[reason]
  return key ? t(`admin.accounts.status.ttftGuard.${key}`) : reason || '—'
}
</script>

<style scoped>
.protection-summary { @apply inline-flex max-w-full items-center gap-1.5 rounded-md border px-2 py-1 text-xs font-medium focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary-500; }
.state-badge { @apply rounded border px-2 py-0.5 text-xs; }
.metric-label { @apply text-xs text-gray-500 dark:text-gray-400; }
.metric-value { @apply mt-1 text-2xl font-semibold tabular-nums text-gray-900 dark:text-white; }
</style>
