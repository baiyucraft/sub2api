<template>
  <div v-if="hasMetrics" class="flex max-w-full flex-col items-start gap-1">
    <button v-if="platformExcluded" type="button" data-test="protection-platform-summary" :aria-label="`${t('admin.upstreamManagement.protection.viewDetails')}：${stateLabel('platform_not_selected')}`" aria-haspopup="dialog" :aria-expanded="show" :class="['protection-summary', stateClass('platform_not_selected')]" @click="show = true">
      <Icon name="shield" size="xs" /><span class="truncate">{{ t('admin.upstreamManagement.protection.action') }} · {{ stateLabel('platform_not_selected') }}</span><Icon name="chevronRight" size="xs" />
    </button>
    <button v-else-if="cache || groups.length" type="button" data-test="protection-cache-summary" :aria-label="`${t('admin.upstreamManagement.protection.viewCacheDetails')}：${cacheSummary}`" aria-haspopup="dialog" :aria-expanded="show" :class="['protection-summary', stateClass(cacheProtectedGroups ? 'protected' : cacheDisplayState)]" @click="show = true">
      <Icon name="shield" size="xs" />
      <span class="truncate">{{ cacheSummary }}</span>
      <Icon name="chevronRight" size="xs" />
    </button>
    <button v-if="!platformExcluded" type="button" data-test="protection-ttft-summary" :aria-label="`${t('admin.upstreamManagement.protection.viewTTFTDetails')}：${ttftSummary}`" aria-haspopup="dialog" :aria-expanded="show" :class="['protection-summary', stateClass(ttftDisplayState)]" @click="show = true">
      <Icon name="clock" size="xs" />
      <span class="truncate">{{ ttftSummary }}</span>
      <Icon name="chevronRight" size="xs" />
    </button>
    <BaseDialog :show="show" :title="t('admin.upstreamManagement.protection.details')" width="wide" @close="show = false">
      <div class="space-y-6" data-test="upstream-protection-details">
        <div class="border-b border-gray-200 pb-4 dark:border-dark-700">
          <p class="break-words font-semibold text-gray-900 dark:text-white">{{ account.name }}</p>
          <p class="mt-1 text-xs leading-5 text-gray-500 dark:text-gray-400">{{ t('admin.upstreamManagement.protection.sharedHint') }}</p>
          <p v-if="platformExcluded" data-test="protection-platform-excluded" class="mt-3 rounded-lg bg-gray-50 p-3 text-sm text-gray-600 dark:bg-dark-800 dark:text-gray-300">{{ t('admin.upstreamManagement.protection.platformExcludedHint') }}</p>
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
          <p v-if="!platformExcluded && ttftModelTotal" class="mb-3 text-xs text-gray-500 dark:text-gray-400" data-test="protection-ttft-model-counts">{{ t('admin.upstreamManagement.protection.modelCounts', ttftPresentation.counts) }}</p>
          <p v-if="!models.length" class="rounded-lg bg-gray-50 p-3 text-xs text-gray-500 dark:bg-dark-800 dark:text-gray-400">{{ platformExcluded ? stateLabel('platform_not_selected') : t(!protection && legacyTTFT.length ? 'admin.upstreamManagement.protection.legacyTTFTMetrics' : 'admin.upstreamManagement.protection.noTTFTSamples') }}</p>
          <div v-else class="space-y-2">
            <article v-for="item in models" :key="item.model" class="rounded-xl border border-gray-200 p-4 dark:border-dark-600" data-test="protection-ttft-detail">
              <div class="flex flex-wrap items-center justify-between gap-2"><span class="min-w-0 break-all text-sm font-semibold text-gray-900 dark:text-white">{{ item.model }}</span><span :class="['state-badge', stateClass(platformExcluded ? 'observed' : item.state)]">{{ stateLabel(platformExcluded ? 'observed' : item.state) }}</span></div>
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
              <div class="flex flex-wrap items-start justify-between gap-2"><h5 class="min-w-0 break-all text-sm font-semibold text-gray-900 dark:text-white">{{ groupLabel(item) }}</h5><p class="text-xs text-gray-600 dark:text-gray-300">{{ groupExcluded(item) ? stateLabel('platform_not_selected') : item.priority_source === 'unavailable' ? t('admin.upstreamManagement.protection.priorityUnavailable') : `${t(`admin.upstreamManagement.protection.priorities.${item.priority}`)} · ${sourceLabel(item.priority_source)}` }}</p></div>
              <div class="mt-4 grid gap-4 sm:grid-cols-2">
                <section class="min-w-0 space-y-2 text-xs leading-5 text-gray-600 dark:text-gray-300" data-test="protection-group-cache">
                  <div class="flex flex-wrap items-center gap-2"><h6 class="font-semibold">{{ t('admin.upstreamManagement.protection.cache') }}</h6><span :class="['state-badge', stateClass(groupExcluded(item) ? 'platform_not_selected' : cacheDisplayStateFor(item))]">{{ stateLabel(groupExcluded(item) ? 'platform_not_selected' : cacheDisplayStateFor(item)) }}</span><span class="text-gray-500 dark:text-gray-400">{{ sourceLabel(item.cache.source) }}</span></div>
                  <p v-if="item.cache.source !== 'unavailable'">{{ t('admin.upstreamManagement.protection.cacheValues', { threshold: item.cache.threshold_percent, recovery: item.cache.recovery_threshold_percent, samples: item.cache.min_valid_requests }) }}</p>
                  <p>{{ groupExcluded(item) ? stateLabel('platform_not_selected') : reasonLabel(item.cache.reason) }}</p>
                  <div v-if="!groupExcluded(item) && item.cache.ordinary_recovery" class="rounded-lg bg-gray-50 p-2 dark:bg-dark-800" data-test="protection-cache-ordinary-recovery">
                    <p class="font-medium">{{ t('admin.upstreamManagement.protection.ordinaryCacheRecovery') }}</p>
                    <p>{{ t('admin.upstreamManagement.protection.cacheRecoveryRequests', { current: item.cache.ordinary_recovery.valid_requests, required: item.cache.ordinary_recovery.required_requests }) }}</p>
                    <p>{{ t('admin.upstreamManagement.protection.cacheRecoverySessions', { current: item.cache.ordinary_recovery.sessions, required: item.cache.ordinary_recovery.required_sessions }) }}</p>
                    <p>{{ t('admin.upstreamManagement.protection.cacheRecoveryRate', { current: rateLabel(item.cache.ordinary_recovery.rate_percent), required: rateLabel(item.cache.ordinary_recovery.required_rate_percent) }) }}</p>
                    <p>{{ cacheRecoveryReasonLabel(item.cache.ordinary_recovery.reason) }}</p>
                    <p v-if="validDate(item.cache.ordinary_recovery.started_at)">{{ t('admin.upstreamManagement.cacheGuard.recoveryStartedAt') }}: {{ dateLabel(item.cache.ordinary_recovery.started_at) }}</p>
                    <p v-if="validDate(item.cache.ordinary_recovery.ready_at)">{{ t('admin.upstreamManagement.protection.cacheRecoveryReadyAt') }}: {{ dateLabel(item.cache.ordinary_recovery.ready_at) }}</p>
                  </div>
                  <div v-if="!groupExcluded(item) && (item.cache.state === 'recovering' || item.cache.recovery_started_at)" class="rounded-lg bg-gray-50 p-2 dark:bg-dark-800" data-test="protection-cache-trial-recovery"><p class="font-medium">{{ t('admin.upstreamManagement.protection.trialCacheRecovery') }}</p><p>{{ t('admin.upstreamManagement.cacheGuard.recoveryRate') }}: {{ rateLabel(item.cache.recovery_rate_percent) }}</p><p>{{ t('admin.upstreamManagement.cacheGuard.recoveryRequests') }}: {{ item.cache.recovery_valid_requests }}</p><p>{{ t('admin.upstreamManagement.cacheGuard.recoverySessions') }}: {{ item.cache.recovery_sessions }}</p><p>{{ t('admin.upstreamManagement.cacheGuard.recoveryStartedAt') }}: {{ dateLabel(item.cache.recovery_started_at) }}</p></div>
                  <p v-if="!groupExcluded(item) && item.cache.cooldown_until">{{ t('admin.upstreamManagement.cacheGuard.cooldownUntil') }}: {{ dateLabel(item.cache.cooldown_until) }}</p>
                </section>
                <section class="min-w-0 space-y-2 text-xs leading-5 text-gray-600 dark:text-gray-300" data-test="protection-group-ttft">
                  <div class="flex flex-wrap items-center gap-2"><h6 class="font-semibold">{{ t('admin.upstreamManagement.protection.ttft') }}</h6><span :class="['state-badge', stateClass(groupExcluded(item) ? 'platform_not_selected' : ttftDisplayStateFor(item))]">{{ stateLabel(groupExcluded(item) ? 'platform_not_selected' : ttftDisplayStateFor(item)) }}</span><span class="text-gray-500 dark:text-gray-400">{{ sourceLabel(item.ttft.source) }}</span></div>
                  <p v-if="item.ttft.source !== 'unavailable'">{{ t('admin.upstreamManagement.protection.ttftValues', { threshold: milliseconds(item.ttft.threshold_ms), recovery: milliseconds(item.ttft.recovery_threshold_ms), samples: item.ttft.min_samples }) }}</p>
                  <p v-if="!item.ttft.models.length && !groupExcluded(item) && item.ttft.state !== 'disabled'">{{ t('admin.upstreamManagement.protection.noTTFTSamples') }}</p>
                  <p v-if="!groupExcluded(item) && ['disabled', 'unavailable'].includes(item.ttft.state)">{{ stateLabel(item.ttft.state) }}</p>
                  <div v-for="model in item.ttft.models" :key="model.model" class="rounded-lg bg-gray-50 p-3 dark:bg-dark-800" data-test="protection-model-policy">
                    <div class="flex flex-wrap items-center justify-between gap-1"><span class="break-all font-medium">{{ model.model }}</span><span :class="stateTextClass(groupExcluded(item) ? 'platform_not_selected' : ttftModelDisplay(model))">{{ stateLabel(groupExcluded(item) ? 'platform_not_selected' : ttftModelDisplay(model)) }}</span></div>
                    <template v-if="!groupExcluded(item)">
                      <p>{{ reasonLabel(model.reason) }}</p>
                      <p v-if="model.recovery_reason">{{ t('admin.upstreamManagement.protection.recoveryReason') }}: {{ recoveryReasonLabel(model.recovery_reason) }}</p>
                      <div v-if="model.last_recovery_block_reason" data-test="protection-recovery-block">
                        <p>{{ t('admin.upstreamManagement.protection.lastRecoveryBlockReason') }}: {{ recoveryBlockReasonLabel(model.last_recovery_block_reason) }}</p>
                        <p v-if="validDate(model.last_recovery_block_at)">{{ t('admin.upstreamManagement.protection.lastRecoveryBlockAt') }}: {{ dateLabel(model.last_recovery_block_at) }}</p>
                      </div>
                      <div class="mt-2 space-y-1 border-t border-gray-200 pt-2 dark:border-dark-700" data-test="protection-trigger-evidence">
                        <p class="font-medium">{{ t('admin.upstreamManagement.protection.trigger') }}</p>
                        <template v-if="hasTriggerSnapshot(model)"><p>{{ t('admin.upstreamManagement.protection.triggerValues', { ttft: milliseconds(model.trigger_ttft_ms), threshold: milliseconds(model.trigger_threshold_ms) }) }}</p><p>{{ t('admin.upstreamManagement.protection.triggeredAt') }}: {{ dateLabel(model.triggered_at) }}</p></template>
                        <p v-else>{{ t('admin.upstreamManagement.protection.noTriggerSnapshot') }}</p>
                        <p v-if="validDate(model.bad_evidence_expires_at)">{{ t('admin.upstreamManagement.protection.badEvidenceExpiresAt') }}: {{ dateLabel(model.bad_evidence_expires_at) }}</p>
                      </div>
                      <p class="mt-2">{{ t('admin.upstreamManagement.protection.currentQuality', { ewma: milliseconds(model.ewma_ms), latest: milliseconds(model.last_ttft_ms), samples: model.sample_count }) }}</p>
                      <div v-if="showRecoveryEvidence(model)" class="mt-2 space-y-1" data-test="protection-recovery-evidence">
                        <p>{{ t('admin.upstreamManagement.protection.recovery', { current: model.recovery_samples, required: model.recovery_samples_required }) }}</p>
                        <p>{{ t('admin.upstreamManagement.protection.recoverySessions', { current: countLabel(model.recovery_session_count), required: countLabel(model.recovery_sessions_required) }) }}</p>
                        <p>{{ t('admin.upstreamManagement.protection.recoveryContributions', { ordinary: countLabel(model.recovery_ordinary_samples), trial: countLabel(model.recovery_trial_samples) }) }}</p>
                      </div>
                      <details v-if="model.runtime_id || model.algorithm_version" class="mt-2 break-all text-gray-500 dark:text-gray-400" data-test="protection-runtime"><summary>{{ t('admin.upstreamManagement.protection.runtime') }}</summary><p>{{ t('admin.upstreamManagement.protection.runtimeID') }}: {{ model.runtime_id || '—' }}</p><p>{{ t('admin.upstreamManagement.protection.algorithm') }}: {{ model.algorithm_version || '—' }}</p><p>{{ t('admin.upstreamManagement.protection.policyGeneration') }}: {{ countLabel(model.policy_generation) }}</p><p>{{ t('admin.upstreamManagement.protection.revalidationEpoch') }}: {{ countLabel(model.revalidation_epoch) }}</p></details>
                      <div v-for="execution in model.executions ?? []" :key="`${execution.protocol}:${execution.runtime_id}`" class="mt-2 space-y-1 border-t border-gray-200 pt-2 dark:border-dark-700" data-test="protection-execution">
                        <p class="font-medium">{{ t('admin.upstreamManagement.protection.latestExecution') }} · {{ execution.protocol }}</p>
                        <p>{{ t('admin.upstreamManagement.protection.selectionPath') }}: {{ executionLabel('selectionPaths', execution.selection_path) }}</p>
                        <p v-if="execution.fallback_reason">{{ t('admin.upstreamManagement.protection.fallbackReason') }}: {{ executionLabel('fallbackReasons', execution.fallback_reason) }}</p>
                        <p>{{ t('admin.upstreamManagement.protection.executionResult', { sent: t(`admin.upstreamManagement.protection.boolean.${execution.send_attempted ? 'yes' : 'no'}`), success: executionOutcome(execution), trial: t(`admin.upstreamManagement.protection.boolean.${execution.trial ? 'yes' : 'no'}`) }) }}</p>
                        <p v-if="execution.recovery_counted !== undefined" data-test="protection-execution-recovery">{{ t('admin.upstreamManagement.protection.executionRecovery', { counted: t(`admin.upstreamManagement.protection.boolean.${execution.recovery_counted ? 'yes' : 'no'}`) }) }}<template v-if="execution.recovery_reason"> · {{ recoveryBlockReasonLabel(execution.recovery_reason) }}</template></p>
                        <p>{{ t('admin.upstreamManagement.protection.observedAt') }}: {{ dateLabel(execution.observed_at) }}</p>
                        <p>{{ t('admin.upstreamManagement.protection.executionWindow', { start: dateLabel(execution.window_started_at), sent: execution.sent_count, completed: execution.completed_count, success: execution.success_count, trial: execution.trial_count }) }}</p>
                        <p class="break-all">{{ t('admin.upstreamManagement.protection.runtimeID') }}: {{ execution.runtime_id || '—' }}</p>
                        <p class="text-gray-500 dark:text-gray-400">{{ t('admin.upstreamManagement.protection.sendAttemptedHint') }}</p>
                      </div>
                    </template>
                    <p v-else>{{ stateLabel('platform_not_selected') }}</p>
                  </div>
                  <p v-if="!groupExcluded(item) && item.ttft.models.some(model => displayState(model.state, model.display_state) === 'protected')" class="rounded-lg border border-amber-200 p-2 dark:border-amber-900" data-test="protection-routing-hint">{{ t('admin.upstreamManagement.protection.routingHint') }}</p>
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
import type { Account, UpstreamProtectionDisplayState, UpstreamProtectionGroupState, UpstreamTTFTExecution, UpstreamTTFTModelObservation } from '@/types'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Icon from '@/components/icons/Icon.vue'
import { formatDateTime } from '@/utils/format'
import { summarizeTTFTGroups, ttftGroupDisplay, ttftModelDisplay } from '@/utils/upstreamTTFTSummary'

const props = defineProps<{ account: Account }>()
const { t, te } = useI18n()
const show = ref(false)
const protection = computed(() => props.account.upstream_protection)
const platformExcluded = computed(() => protection.value?.platform_selected === false)
const cache = computed(() => protection.value?.cache ?? props.account.upstream_cache_guard)
const groups = computed(() => protection.value?.groups ?? [])
const models = computed(() => protection.value?.ttft_models ?? [])
const legacyTTFT = computed(() => props.account.ttft_guard_degradations ?? [])
const isProtected = (state: string) => ['degraded', 'draining', 'protected', 'recovering'].includes(state)
const groupExcluded = (item: UpstreamProtectionGroupState) => platformExcluded.value || item.platform_selected === false || item.cache.state === 'platform_not_selected' || item.ttft.state === 'platform_not_selected'
const activeGroups = computed(() => groups.value.filter(item => !groupExcluded(item)))
const cacheProtectedGroups = computed(() => new Set((protection.value ? activeGroups.value.map(item => ({ group_id: item.group_id, state: cacheDisplayStateFor(item) })) : props.account.upstream_cache_guard_states ?? []).filter(item => isProtected(item.state)).map(item => item.group_id)).size)
const ttftProtectedGroups = computed(() => new Set(activeGroups.value.filter(item => ttftDisplayStateFor(item) === 'protected').map(item => item.group_id)).size)
const protectedModels = computed(() => new Set(activeGroups.value.filter(item => item.ttft.state !== 'disabled').flatMap(item => item.ttft.models.filter(model => ttftModelDisplay(model) === 'protected').map(model => model.model))).size)
const hasMetrics = computed(() => !!protection.value || !!cache.value || !!props.account.ttft_guard_degradations?.length)
function displayState(state: string, current?: UpstreamProtectionDisplayState): UpstreamProtectionDisplayState {
  if (current) return current
  if (isProtected(state)) return 'protected'
  return state === 'normal' || state === 'healthy' ? 'normal' : 'insufficient'
}
type CachePresentationState = UpstreamProtectionDisplayState | 'unavailable' | 'partial_unavailable'
const cacheDisplayStateFor = (item: UpstreamProtectionGroupState): CachePresentationState => item.cache.state === 'unavailable' ? 'unavailable' : displayState(item.cache.state, item.cache.display_state)
const ttftDisplayStateFor = ttftGroupDisplay
function aggregateState(states: CachePresentationState[]): CachePresentationState {
  if (states.length && states.every(state => state === 'unavailable')) return 'unavailable'
  if (states.includes('unavailable')) return 'partial_unavailable'
  if (states.includes('protected')) return 'protected'
  return states.length && states.every(state => state === 'normal') ? 'normal' : 'insufficient'
}
const cacheDisplayState = computed(() => aggregateState(groups.value.length ? activeGroups.value.map(cacheDisplayStateFor) : cache.value ? [cache.value.state === 'unavailable' ? 'unavailable' : displayState(cache.value.state)] : []))
const ttftPresentation = computed(() => summarizeTTFTGroups(groups.value))
const ttftModelTotal = computed(() => Object.values(ttftPresentation.value.counts).reduce((total, count) => total + count, 0))
const ttftDisplayState = computed(() => !protection.value && legacyTTFT.value.length ? 'protected' : ttftPresentation.value.state)
const cacheSummary = computed(() => {
  const state = cacheDisplayState.value
  const unavailable = state === 'unavailable' || state === 'partial_unavailable'
  const status = cacheProtectedGroups.value ? t('admin.upstreamManagement.protection.protectedGroups', { count: cacheProtectedGroups.value }) : stateLabel(state)
  const availability = cacheProtectedGroups.value && unavailable ? ` · ${stateLabel(state)}` : ''
  return `${t('admin.upstreamManagement.protection.cache')} ${rateLabel(state === 'unavailable' ? null : cache.value?.rate_percent)} · ${status}${availability}`
})
const ttftSummary = computed(() => `${t('admin.upstreamManagement.protection.ttft')} · ${ttftProtectedGroups.value ? t('admin.upstreamManagement.protection.protectedModelsGroups', { models: protectedModels.value, groups: ttftProtectedGroups.value }) : protection.value ? stateLabel(ttftDisplayState.value) : legacyTTFT.value.length ? t('admin.upstreamManagement.protection.protectedModels', { count: new Set(legacyTTFT.value.map(item => item.model)).size }) : stateLabel('insufficient')}`)
const stateLabel = (state: string) => t(`admin.upstreamManagement.protection.state.${state}`)
const hasTriggerSnapshot = (model: UpstreamTTFTModelObservation) => (model.trigger_ttft_ms ?? 0) > 0 && (model.trigger_threshold_ms ?? 0) > 0 && validDate(model.triggered_at)
const recoveryGapReasons = new Set(['bad_evidence_expired', 'recovery_samples_insufficient', 'recovery_sessions_insufficient', 'waiting_revalidation', 'waiting_recovery_samples', 'waiting_recovery_sessions'])
const showRecoveryEvidence = (model: UpstreamTTFTModelObservation) => {
  const state = displayState(model.state, model.display_state)
  if (state === 'normal') return false
  if (state === 'protected') return true
  if ((model.revalidation_epoch ?? 0) <= 0) return false
  return recoveryGapReasons.has(model.recovery_reason ?? '') || (model.recovery_samples ?? 0) > 0 || (model.recovery_session_count ?? 0) > 0
}
const validDate = (value?: string | null) => !!value && !value.startsWith('0001-') && Number.isFinite(Date.parse(value))
const countLabel = (value?: number) => value == null || !Number.isFinite(value) ? '—' : String(value)
function recoveryReasonLabel(reason: string) {
  const key = `admin.upstreamManagement.protection.recoveryReasons.${reason}`
  return te(key) ? t(key) : reason
}
function cacheRecoveryReasonLabel(reason: string) {
  const key = `admin.upstreamManagement.protection.cacheRecoveryReasons.${reason}`
  return te(key) ? t(key) : reason
}
function recoveryBlockReasonLabel(reason: string) {
  const key = `admin.upstreamManagement.protection.recoveryBlockReasons.${reason}`
  return te(key) ? t(key) : reason
}
function executionOutcome(execution: UpstreamTTFTExecution) {
  const state = execution.completed === false ? 'pending' : execution.completed === true ? (execution.succeeded ? 'succeeded' : 'failed') : 'unknown'
  return t(`admin.upstreamManagement.protection.executionOutcomes.${state}`)
}
function executionLabel(kind: 'selectionPaths' | 'fallbackReasons', value: string) {
  const key = `admin.upstreamManagement.protection.${kind}.${value}`
  return te(key) ? t(key) : value || '—'
}
function stateClass(state: string): string {
  if (['degraded', 'draining', 'protected'].includes(state)) return 'border-amber-200 bg-amber-50 text-amber-800 dark:border-amber-800 dark:bg-amber-950/30 dark:text-amber-300'
  if (state === 'recovering') return stateClass('protected')
  if (state === 'normal') return 'border-emerald-200 bg-emerald-50 text-emerald-800 dark:border-emerald-800 dark:bg-emerald-950/30 dark:text-emerald-300'
  return 'border-gray-200 bg-gray-50 text-gray-600 dark:border-dark-600 dark:bg-dark-800 dark:text-gray-300'
}
const stateTextClass = (state: string) => isProtected(state) ? 'text-amber-700 dark:text-amber-300' : 'text-gray-500 dark:text-gray-400'
const rateLabel = (value?: number | null) => value == null || !Number.isFinite(value) ? t('admin.upstreamManagement.protection.unknown') : `${value.toFixed(1)}%`
const dateLabel = (value?: string | null) => validDate(value) ? formatDateTime(value!) : '—'
const milliseconds = (value?: number | null) => value == null || !Number.isFinite(value) ? '—' : value >= 1000 ? `${(value / 1000).toFixed(1)}s` : `${Math.round(value)}ms`
const groupLabel = (item: { group_id: number; group_name?: string }) => item.group_name || props.account.groups?.find(group => group.id === item.group_id)?.name || `#${item.group_id}`
const sourceLabel = (source?: string) => source === 'unavailable' ? t('admin.upstreamManagement.protection.sourceUnavailable') : t(`admin.groups.ttftGuard.sources.${source === 'group' || source === 'disabled' ? source : 'global'}`)
function reasonLabel(reason: string) {
  if (reason === 'platform_not_selected') return stateLabel(reason)
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
