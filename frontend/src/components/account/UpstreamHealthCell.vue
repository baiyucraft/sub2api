<template>
  <div class="flex min-w-[150px] max-w-[250px] flex-col items-start gap-1.5">
    <div class="flex max-w-full flex-nowrap items-center gap-1.5" data-test="health-confidence-row">
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
      <div>{{ t('admin.upstreamManagement.health.schedulable') }}: {{ account.schedulable ? t('common.yes') : t('common.no') }}</div>
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

      <HelpTooltip v-if="showConfidence" width-class="w-96 max-w-[calc(100vw-2rem)]" trigger-class="!ml-0">
      <template #trigger>
        <span data-test="confidence-badge" :class="['inline-flex min-h-6 items-center rounded-md px-2 py-1 text-xs font-medium', confidenceBadgeClass]">
          {{ t('admin.upstreamManagement.health.confidenceLabel') }} {{ formatScore(health?.confidence_score_24h) }} / {{ formatScore(health?.confidence_score_7d) }}
        </span>
      </template>
      <div class="mb-1 font-medium text-violet-200">{{ t('admin.upstreamManagement.health.confidenceTitle') }}</div>
      <div>{{ t('admin.upstreamManagement.health.confidence24h') }}: {{ ratioLine('24h') }}</div>
      <div>{{ t('admin.upstreamManagement.health.confidence7d') }}: {{ ratioLine('7d') }}</div>
      <div class="text-gray-300">{{ t('admin.upstreamManagement.health.confidenceFormula') }}</div>
      <div>{{ t('admin.upstreamManagement.health.networkErrors') }}: {{ health?.confidence_network_error_24h || 0 }}（{{ t('admin.upstreamManagement.health.excludedFromScore') }}）</div>
      <div>{{ t('admin.upstreamManagement.health.mixedCount') }}: {{ health?.confidence_mixed_24h || 0 }} · {{ t('admin.upstreamManagement.health.unsuccessfulCount') }}: {{ health?.confidence_unsuccessful_24h || 0 }}</div>
      <div class="mt-2 border-t border-white/10 pt-2 font-medium">{{ t('admin.upstreamManagement.health.latestEvidence') }}</div>
      <template v-if="evidence">
        <div>{{ t('admin.upstreamManagement.health.probeKind') }}: {{ evidenceKindLabel }}</div>
        <div v-if="evidence.claimed_model">{{ t('admin.upstreamManagement.health.claimedModel') }}: {{ evidence.claimed_model }}</div>
        <div v-if="evidence.requested_effort">{{ t('admin.upstreamManagement.health.requestedEffort') }}: {{ evidence.requested_effort }}</div>
        <div v-if="expectedLabel">{{ expectedLabel }}: {{ evidenceExpected || '-' }}</div>
        <div>{{ t('admin.upstreamManagement.health.observedValue') }}: {{ evidenceObserved || t('admin.upstreamManagement.health.noResponse') }}</div>
        <div>{{ t('admin.upstreamManagement.health.verdict') }}: {{ evidenceVerdict }}</div>
        <div v-if="mixedModels">{{ t('admin.upstreamManagement.health.matchedOtherModels') }}: {{ mixedModels }}</div>
      </template>
      <template v-else>
        <div>{{ t('admin.upstreamManagement.health.verdict') }}: {{ evidenceVerdict }}</div>
        <div class="text-gray-300">{{ t('admin.upstreamManagement.health.legacyEvidence') }}</div>
      </template>
      <div class="mt-2 border-t border-white/10 pt-2">{{ t('admin.upstreamManagement.health.reasoningTokens') }}: {{ health?.confidence_reasoning_tokens ?? '-' }}</div>
      <div class="text-gray-300">{{ t('admin.upstreamManagement.health.reasoningTokensHint') }}</div>
      <div>{{ t('admin.upstreamManagement.health.promptVersion') }}: {{ health?.confidence_prompt_version || '-' }}</div>
      <div>{{ t('admin.upstreamManagement.health.hardAnomaly24h') }}: {{ hardAnomalyLabel }}</div>
      <div class="mt-1 text-gray-300">{{ t('admin.upstreamManagement.health.confidenceDisclaimer') }}</div>
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
import type { Account } from '@/types'
import { formatDateTime } from '@/utils/format'
import UpstreamHealthHistory from './UpstreamHealthHistory.vue'

const props = defineProps<{
  account: Account
}>()
const emit = defineEmits<{ (event: 'showHistory'): void }>()

const { t, te } = useI18n()

const health = computed(() => props.account.upstream_health)

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

const showConfidence = computed(() =>
  props.account.platform?.toLowerCase() === 'openai' &&
  props.account.upstream_health?.confidence_prompt_version === 'openai-juice-multiprobe-v2' &&
  ((props.account.upstream_health?.confidence_valid_completed_24h || props.account.upstream_health?.confidence_sample_count_24h || 0) > 0 ||
    (props.account.upstream_health?.confidence_valid_completed_7d || props.account.upstream_health?.confidence_sample_count_7d || 0) > 0 ||
    (props.account.upstream_health?.confidence_coverage_hard_anomaly_24h || 0) > 0 ||
    (props.account.upstream_health?.confidence_output_rewrite_24h || 0) > 0 ||
    Boolean(props.account.upstream_health?.confidence_evidence))
)
const evidence = computed(() => health.value?.confidence_evidence as Record<string, unknown> | undefined)
const hardAnomaly = computed(() => (health.value?.confidence_coverage_hard_anomaly_24h || 0) > 0 || (health.value?.confidence_output_rewrite_24h || 0) > 0)
const confidenceBadgeClass = computed(() => hardAnomaly.value
  ? 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-300'
  : 'bg-violet-100 text-violet-700 dark:bg-violet-900/30 dark:text-violet-300')
const evidenceKindLabel = computed(() => t(`admin.upstreamManagement.health.probeKinds.${String(evidence.value?.kind || 'juice')}`))
const evidenceExpected = computed(() => String(evidence.value?.expected_value ?? evidence.value?.synthetic_value ?? ''))
const evidenceObserved = computed(() => String(evidence.value?.observed_value ?? evidence.value?.normalized_value ?? ''))
const expectedLabel = computed(() => evidence.value?.kind === 'coverage'
  ? t('admin.upstreamManagement.health.syntheticValue')
  : evidence.value?.kind === 'output_integrity' ? t('admin.upstreamManagement.health.expectedOutput') : t('admin.upstreamManagement.health.expectedJuice'))
const evidenceVerdict = computed(() => t(`admin.upstreamManagement.health.confidenceStatuses.${String(evidence.value?.classification || health.value?.confidence_status || 'data_insufficient')}`))
const mixedModels = computed(() => Array.isArray(evidence.value?.mixed_models) ? (evidence.value?.mixed_models as string[]).join(', ') : '')
const hardAnomalyLabel = computed(() => {
  const parts: string[] = []
  if ((health.value?.confidence_coverage_hard_anomaly_24h || 0) > 0) parts.push(t('admin.upstreamManagement.health.coverageAnomaly'))
  if ((health.value?.confidence_output_rewrite_24h || 0) > 0) parts.push(t('admin.upstreamManagement.health.outputRewrite'))
  return parts.length ? parts.join('、') : t('admin.upstreamManagement.health.none')
})
function ratioLine(window: '24h' | '7d') {
  const success = window === '24h' ? health.value?.confidence_current_success_24h : health.value?.confidence_current_success_7d
  const valid = window === '24h' ? health.value?.confidence_valid_completed_24h : health.value?.confidence_valid_completed_7d
  const score = window === '24h' ? health.value?.confidence_score_24h : health.value?.confidence_score_7d
  return valid ? `${success || 0} / ${valid} = ${formatScore(score)}%` : t('admin.upstreamManagement.health.dataInsufficient')
}
function formatScore(score: number | undefined) { return score === undefined || score === null ? '--' : Math.round(score).toString() }
</script>
