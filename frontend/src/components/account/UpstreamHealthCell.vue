<template>
  <div class="flex min-w-[150px] max-w-[250px] flex-col items-start gap-1.5">
    <div class="flex max-w-full flex-wrap items-center gap-1.5" data-test="health-confidence-row">
      <HelpTooltip
        v-if="health"
        width-class="w-72 max-w-[calc(100vw-2rem)]"
        trigger-class="!ml-0 max-w-full"
      >
      <template #trigger>
        <span class="inline-flex items-center gap-1.5">
          <span
            :data-upstream-health-state="health.status"
            :class="[
              'inline-flex min-h-6 max-w-full items-center gap-1.5 whitespace-nowrap rounded-md px-2 py-1 text-xs font-medium',
              healthStateClass
            ]"
          >
            <span class="h-1.5 w-1.5 shrink-0 rounded-full bg-current" />
            <span class="truncate">{{ healthStateLabel }}</span>
          </span>
        </span>
      </template>

      <div class="mb-1 font-medium" :class="healthTooltipTitleClass">
        {{ t('admin.upstreamManagement.health.keyHealth') }} · {{ healthStateLabel }}
      </div>
      <div>{{ t('admin.upstreamManagement.health.reason') }}: {{ healthReasonLabel }}</div>
      <div>
        {{ t('admin.upstreamManagement.health.lastProbe') }}:
        {{ health.last_probe_at ? formatDateTime(health.last_probe_at) : '-' }}
      </div>
      <div>{{ t('admin.upstreamManagement.health.probeStatus') }}: {{ health.last_probe_status || '-' }}</div>
      <div>
        {{ t('admin.upstreamManagement.health.lastTraffic') }}:
        {{ health.last_traffic_status || '-' }} ·
        {{ health.last_evidence_at ? formatDateTime(health.last_evidence_at) : '-' }}
      </div>
      <div>{{ t('admin.upstreamManagement.health.schedulable') }}: {{ schedulable ? t('common.yes') : t('common.no') }}</div>
      <div>{{ t('admin.upstreamManagement.health.failures') }}: {{ health.consecutive_failures }}</div>
      <div>
        {{ t('admin.upstreamManagement.health.recovery') }}:
        {{ health.recovery_samples || 0 }} / {{ health.recovery_samples_required || 3 }}
      </div>
      <div v-if="health.observation_enabled !== true" class="mt-1 text-gray-300">
        {{ t('admin.upstreamManagement.health.observationDisabled') }}
      </div>
      <div v-if="health.status === 'suspended'" class="mt-1 font-medium text-red-200">
        {{ t('admin.upstreamManagement.health.temporarilyExcluded') }}
      </div>
      <div v-else-if="health.status === 'recovering'" class="mt-1 font-medium text-cyan-200">
        {{ t('admin.upstreamManagement.health.recoveryInProgress') }}
      </div>
      </HelpTooltip>

      <HelpTooltip v-if="unifiedConfidence" trigger="click" width-class="w-[30rem] max-w-[calc(100vw-2rem)]" trigger-class="!ml-0 max-w-full">
        <template #trigger>
          <button
            type="button"
            data-test="unified-confidence-badge"
            :data-confidence-status="unifiedConfidence.status"
            :aria-label="`${t('admin.upstreamManagement.health.unified.title')} · ${unifiedConfidenceLabel}`"
            :class="['inline-flex min-h-6 max-w-full items-center gap-1.5 rounded-md px-2 py-1 text-xs font-medium outline-none transition-colors focus-visible:ring-2 focus-visible:ring-current focus-visible:ring-offset-2', unifiedConfidenceClass]"
          >
            <span class="h-1.5 w-1.5 shrink-0 rounded-full bg-current" />
            <span class="truncate">{{ unifiedConfidenceLabel }}</span>
          </button>
        </template>
        <div data-test="unified-confidence-details" class="max-h-[65vh] space-y-3 overflow-y-auto pr-4">
          <div class="border-b border-white/10 pb-2">
            <div class="font-medium text-gray-100">{{ t('admin.upstreamManagement.health.unified.title') }}</div>
            <div class="mt-1 text-base font-semibold text-white">{{ unifiedConfidenceLabel }}</div>
            <div class="mt-1 break-all text-gray-300">{{ t('admin.upstreamManagement.health.unified.targetModel') }}: {{ unifiedConfidence.target_model || '-' }}</div>
            <p v-if="unifiedConfidence.reason" data-test="unified-confidence-reason" class="mt-1 text-gray-300">{{ safeConfidenceReason(unifiedConfidence.reason) }}</p>
            <span v-if="unifiedConfidence.inference" data-test="unified-confidence-inference" class="mt-2 inline-flex rounded border border-white/15 px-1.5 py-0.5 text-gray-300">{{ t('admin.upstreamManagement.health.unified.inference') }}</span>
          </div>
          <div v-if="confidenceMethods.length" class="overflow-x-auto">
            <table class="w-full text-left text-xs" data-test="unified-confidence-methods">
              <thead class="text-gray-400">
                <tr>
                  <th class="pb-2 pr-3 font-normal">{{ t('admin.upstreamManagement.health.unified.method') }}</th>
                  <th class="pb-2 pr-3 font-normal">{{ t('admin.upstreamManagement.health.unified.attribution') }}</th>
                  <th class="pb-2 text-right font-normal">{{ t('admin.upstreamManagement.health.unified.contribution') }}</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="method in confidenceMethods" :key="method.id" class="border-t border-white/10 align-top" :data-test="`confidence-method-${method.id}`">
                  <td class="py-2 pr-3">
                    <div class="font-medium text-gray-100">{{ attributionMethodName(method.id) }}</div>
                    <div class="mt-1 whitespace-nowrap tabular-nums text-gray-300" :data-test="`confidence-progress-${method.id}`">
                      <template v-if="method.baseline_version && method.minimum_samples && method.id !== 'behavioral'">{{ method.sample_count }} / {{ method.minimum_samples }}</template>
                      <template v-else>{{ t('admin.upstreamManagement.health.attribution.collectedSamples', { count: method.sample_count }) }}</template>
                    </div>
                    <div v-if="method.id !== 'distribution' && method.current_sample_count !== undefined && method.current_sample_count !== method.sample_count" class="mt-1 whitespace-nowrap text-gray-400">{{ t('admin.upstreamManagement.health.unified.currentRound', { count: method.current_sample_count, total: method.minimum_samples || 3 }) }}</div>
                    <div v-if="method.confirmed_rounds !== undefined && method.id !== 'distribution'" class="mt-1 whitespace-nowrap text-gray-400">{{ t('admin.upstreamManagement.health.unified.confirmedRounds', { count: method.confirmed_rounds }) }}</div>
                  </td>
                  <td class="py-2 pr-3">
                    <div class="break-all text-gray-100" :data-test="`confidence-candidate-${method.id}`">{{ confidenceCandidate(method) }}</div>
                    <div class="mt-1 text-gray-400">{{ method.participating ? t('admin.upstreamManagement.health.unified.qualified') : attributionMethodLabel(method.status) }}</div>
                    <div v-if="method.reason" class="mt-1 text-gray-400">{{ safeConfidenceReason(method.reason) }}</div>
                  </td>
                  <td class="py-2 text-right">
                    <div class="whitespace-nowrap tabular-nums text-gray-200" :data-test="`confidence-weight-${method.id}`">{{ method.effective_weight || 0 }} / {{ method.weight || 0 }}</div>
                    <div class="mt-1 whitespace-nowrap text-gray-400">{{ t(`admin.upstreamManagement.health.unified.${method.participating ? 'participating' : 'notParticipating'}`) }}</div>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
          <p class="text-gray-400">{{ t('admin.upstreamManagement.health.unified.weightsHint') }}</p>
          <details class="border-t border-white/10 pt-2" data-test="unified-confidence-technical">
            <summary class="cursor-pointer select-none text-gray-300">{{ t('admin.upstreamManagement.health.unified.technicalDetails') }}</summary>
            <dl class="mt-2 grid grid-cols-[minmax(0,1fr)_minmax(0,2fr)] gap-x-3 gap-y-1 text-gray-300">
              <dt>{{ t('admin.upstreamManagement.health.unified.effectiveModel') }}</dt>
              <dd data-test="confidence-effective-model" class="break-all text-right">{{ unifiedConfidence.effective_model || '-' }}</dd>
              <dt>{{ t('admin.upstreamManagement.health.unified.fusionVersion') }}</dt>
              <dd class="break-all text-right">{{ unifiedConfidence.fusion_version ?? '-' }}</dd>
              <dt>{{ t('admin.upstreamManagement.health.attribution.protocol') }}</dt>
              <dd class="break-all text-right">{{ unifiedConfidence.protocol || '-' }}</dd>
              <template v-if="unifiedConfidence.observed_protocol">
                <dt>{{ t('admin.upstreamManagement.health.attribution.observedProtocol') }}</dt>
                <dd data-test="attribution-observed-protocol" class="break-all text-right">{{ unifiedConfidence.observed_protocol }}</dd>
              </template>
              <template v-if="unifiedConfidence.protocol_status">
                <dt>{{ t('admin.upstreamManagement.health.attribution.protocolStatus') }}</dt>
                <dd data-test="attribution-protocol-status" class="break-all text-right">{{ attributionProtocolLabel }}</dd>
              </template>
              <template v-for="method in confidenceMethods.filter(value => value.baseline_version)" :key="method.id">
                <dt>{{ attributionMethodName(method.id) }}</dt>
                <dd class="break-all text-right">{{ method.baseline_version }}</dd>
              </template>
            </dl>
            <p v-if="unifiedConfidence.protocol_reason" data-test="attribution-protocol-reason" class="mt-2 text-amber-200">{{ safeConfidenceReason(unifiedConfidence.protocol_reason) }}</p>
          </details>
          <p class="border-t border-white/10 pt-2 text-gray-400">{{ t('admin.upstreamManagement.health.unified.disclaimer') }}</p>
        </div>
      </HelpTooltip>

    </div>

    <span
      v-if="!health"
      data-upstream-health-state="unobserved"
      class="inline-flex min-h-6 items-center gap-1.5 rounded-md bg-gray-100 px-2 py-1 text-xs text-gray-500 dark:bg-dark-700 dark:text-gray-400"
    >
      <span class="h-1.5 w-1.5 rounded-full bg-current opacity-60" />
      {{ t('admin.upstreamManagement.health.noData') }}
    </span>

    <UpstreamHealthHistory :observations="health?.history" :limit="24" @show-history="emit('showHistory')" />
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import HelpTooltip from '@/components/common/HelpTooltip.vue'
import type { Account, UpstreamConfidenceAttributionMethod } from '@/types'
import { formatDateTime } from '@/utils/format'
import UpstreamHealthHistory from './UpstreamHealthHistory.vue'

const props = withDefaults(defineProps<{
  /** Retained for existing consumers; the upstream table supplies narrow props. */
  account?: Account
  snapshot?: Account['upstream_health']
  platform?: string
  schedulable?: boolean
}>(), { schedulable: undefined })
const emit = defineEmits<{ (event: 'showHistory'): void }>()

const { t, te } = useI18n()

const health = computed(() => props.snapshot ?? props.account?.upstream_health)
const schedulable = computed(() => props.schedulable ?? props.account?.schedulable)
const unifiedConfidence = computed(() => health.value?.confidence_summary ?? undefined)
const attributionProtocolLabel = computed(() => t(`admin.upstreamManagement.health.attribution.protocolStatuses.${unifiedConfidence.value?.protocol_status || 'unknown'}`))
const unifiedConfidenceLabel = computed(() => {
  const summary = unifiedConfidence.value
  const label = t(`admin.upstreamManagement.health.unified.statuses.${summary?.status || 'insufficient'}`)
  return summary?.status === 'suspected' && summary.candidate_model
    ? `${label} · ${confidenceModelLabel(summary.candidate_model)}` : label
})
const unifiedConfidenceClass = computed(() => {
  switch (unifiedConfidence.value?.status) {
    case 'match': return 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300'
    case 'suspected': return 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-300'
    case 'insufficient': return 'bg-amber-100 text-amber-700 dark:bg-amber-900/30 dark:text-amber-300'
    default: return 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-gray-300'
  }
})
const confidenceMethods = computed(() => Object.entries(unifiedConfidence.value?.methods || {})
  .filter(([id]) => id !== 'behavioral')
  .sort(([left], [right]) => {
    const order = ['distribution', 'modeltrace_numeric', 'chanshui_behavioral', 'behavioral']
    const a = order.indexOf(left), b = order.indexOf(right)
    return (a === -1 ? 99 : a) - (b === -1 ? 99 : b) || left.localeCompare(right)
  })
  .map(([id, method]) => ({ id, ...method })))
function confidenceCandidate(method: UpstreamConfidenceAttributionMethod) {
  return method.candidate_model ? confidenceModelLabel(method.candidate_model)
    : method.candidates?.[0]?.display_name || method.candidates?.[0]?.model_id || '-'
}
function attributionMethodLabel(status: string) {
  const key = `admin.upstreamManagement.health.attribution.methodStatuses.${status}`
  return te(key) ? t(key) : t('admin.upstreamManagement.health.attribution.methodStatuses.unknown')
}
function attributionMethodName(id: string) {
  const key = `admin.upstreamManagement.health.attribution.methodNames.${id}`
  return te(key) ? t(key) : id
}
function safeConfidenceReason(reason: string) {
  for (const section of ['unified', 'attribution', 'distribution']) {
    const key = `admin.upstreamManagement.health.${section}.reasons.${reason}`
    if (te(key)) return t(key)
  }
  return t('admin.upstreamManagement.health.attribution.reasons.other')
}
function confidenceModelLabel(model: string) {
  const names: Record<string, string> = { 'gpt-6.1-sol': 'Sol', 'gpt-6-astra': 'Astra', 'gpt-5.6-terra': 'Terra', 'gpt-6-luna': 'Luna', other_known_external: t('admin.upstreamManagement.health.unified.otherModel'), other: t('admin.upstreamManagement.health.unified.otherModel') }
  return names[model] || model
}

const healthStateLabel = computed(() => {
  const state = health.value?.status
  return state ? t(`admin.upstreamManagement.health.${state}`) : t('admin.upstreamManagement.health.noData')
})

const healthStateClass = computed(() => {
  switch (health.value?.status) {
    case 'healthy':
      return 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-400'
    case 'degraded':
      return 'bg-amber-100 text-amber-700 dark:bg-amber-900/30 dark:text-amber-400'
    case 'suspended':
      return 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-400'
    case 'observing':
      return 'bg-blue-100 text-blue-700 dark:bg-blue-900/30 dark:text-blue-400'
    case 'recovering':
      return 'bg-cyan-100 text-cyan-700 dark:bg-cyan-900/30 dark:text-cyan-400'
    case 'disabled':
    default:
      return 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-gray-300'
  }
})

const healthTooltipTitleClass = computed(() => {
  switch (health.value?.status) {
    case 'healthy': return 'text-emerald-200'
    case 'degraded': return 'text-amber-200'
    case 'suspended': return 'text-red-200'
    case 'observing': return 'text-blue-200'
    case 'recovering': return 'text-cyan-200'
    default: return 'text-gray-200'
  }
})

const healthReasonLabel = computed(() => {
  const reason = health.value?.reason?.trim()
  if (!reason) return '-'
  const key = `admin.upstreamManagement.health.reasons.${reason}`
  return te(key) ? t(key) : reason
})

</script>
