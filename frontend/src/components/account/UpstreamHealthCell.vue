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

      <HelpTooltip v-if="showConfidence" width-class="w-80 max-w-[calc(100vw-2rem)]" trigger-class="!ml-0">
      <template #trigger>
        <span data-test="confidence-badge" class="inline-flex min-h-6 items-center rounded-md bg-violet-100 px-2 py-1 text-xs font-medium text-violet-700 dark:bg-violet-900/30 dark:text-violet-300">
          {{ t('admin.upstreamManagement.health.confidenceLabel') }} {{ formatScore(health?.confidence_score_24h) }} / {{ formatScore(health?.confidence_score_7d) }}
        </span>
      </template>
      <div class="mb-1 font-medium text-violet-200">{{ t('admin.upstreamManagement.health.confidenceTitle') }}</div>
      <div>{{ t('admin.upstreamManagement.health.confidence24h') }}: {{ formatScore(health?.confidence_score_24h) }} ({{ health?.confidence_sample_count_24h || 0 }})</div>
      <div>{{ t('admin.upstreamManagement.health.confidence7d') }}: {{ formatScore(health?.confidence_score_7d) }} ({{ health?.confidence_sample_count_7d || 0 }})</div>
      <div>{{ t('admin.upstreamManagement.health.confidenceLast') }}: {{ formatScore(health?.confidence_last_score) }}</div>
      <div>{{ t('admin.upstreamManagement.health.requestedEffort') }}: {{ health?.confidence_requested_effort || '-' }}</div>
      <div>{{ t('admin.upstreamManagement.health.validSamples') }}: {{ health?.confidence_sample_count_7d ?? 0 }}</div>
      <div>{{ t('admin.upstreamManagement.health.juiceStatus') }}: {{ health?.confidence_status || '-' }}</div>
      <div>{{ t('admin.upstreamManagement.health.reasoningTokens') }}: {{ health?.confidence_reasoning_tokens ?? '-' }}</div>
      <div>{{ t('admin.upstreamManagement.health.promptVersion') }}: {{ health?.confidence_prompt_version || '-' }}</div>
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
  ((props.account.upstream_health?.confidence_sample_count_24h || 0) > 0 ||
    (props.account.upstream_health?.confidence_sample_count_7d || 0) > 0)
)
function formatScore(score: number | undefined) { return score === undefined || score === null ? '--' : Math.round(score).toString() }
</script>
