<template>
  <div
    v-if="degradations.length > 0"
    :class="[
      degradations.length <= 4
        ? 'flex flex-col gap-1'
        : degradations.length <= 8
          ? 'columns-2 gap-x-2'
          : 'columns-3 gap-x-2'
    ]"
  >
    <div
      v-for="degradation in degradations"
      :key="`${degradation.model}-${degradation.degraded_at}`"
      class="min-w-0 max-w-[230px] break-inside-avoid"
    >
      <HelpTooltip
        width-class="w-72 max-w-[calc(100vw-2rem)]"
        trigger-class="max-w-full min-w-0"
      >
        <template #trigger>
          <span
            class="inline-flex min-h-6 max-w-full min-w-0 items-center gap-1 whitespace-nowrap rounded border border-amber-300/80 bg-amber-50 px-1.5 py-0.5 text-xs font-medium leading-5 text-amber-800 dark:border-amber-700/70 dark:bg-amber-900/25 dark:text-amber-300"
            :aria-label="`${degradation.model} · ${elapsedText(degradation)}`"
          >
            <Icon name="exclamationTriangle" size="xs" :stroke-width="2" />
            <span class="min-w-0 flex-1 truncate" :title="degradation.model">{{ degradation.model }}</span>
            <span class="shrink-0 text-[10px] opacity-80">·</span>
            <span class="shrink-0 text-[10px] tabular-nums opacity-90">{{ elapsedText(degradation) }}</span>
          </span>
        </template>
        <div data-test="ttft-guard-tooltip" class="mb-1 font-medium text-amber-200">{{ degradation.model }}</div>
        <div>{{ reasonText(degradation.reason) }}</div>
        <div>{{ t('admin.accounts.status.ttftGuard.lastTTFT', { value: formatMilliseconds(degradation.last_ttft_ms) }) }}</div>
        <div>{{ t('admin.accounts.status.ttftGuard.ewma', { value: formatMilliseconds(degradation.ewma_ms) }) }}</div>
        <div>{{ t('admin.accounts.status.ttftGuard.threshold', { value: formatMilliseconds(degradation.threshold_ms) }) }}</div>
        <div>{{ t('admin.accounts.status.ttftGuard.samples', { count: degradation.sample_count }) }}</div>
        <div>{{ t('admin.accounts.status.ttftGuard.degradedAt', { time: formatDateTime(degradation.degraded_at) }) }}</div>
        <div>{{ t('admin.accounts.status.ttftGuard.elapsed', { time: elapsedText(degradation) }) }}</div>
        <div>{{ t('admin.accounts.status.ttftGuard.lastSampleAt', { time: formatDateTime(degradation.last_sample_at) }) }}</div>
        <div>
          {{ t('admin.accounts.status.ttftGuard.recovery', {
            current: degradation.recovery_samples,
            required: degradation.recovery_samples_required
          }) }}
        </div>
        <div>{{ t('admin.accounts.status.ttftGuard.expiresAt', { time: formatDateTime(degradation.expires_at) }) }}</div>
        <div class="mt-1 text-gray-300">{{ t('admin.accounts.status.ttftGuard.probeHint') }}</div>
      </HelpTooltip>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import HelpTooltip from '@/components/common/HelpTooltip.vue'
import type { AccountTTFTGuardDegradation } from '@/types'
import { formatDateTime } from '@/utils/format'

const props = defineProps<{
  degradations?: AccountTTFTGuardDegradation[]
}>()

const { t } = useI18n()
const now = ref(Date.now())
let timer: ReturnType<typeof setInterval> | null = null

const degradations = computed(() => props.degradations ?? [])

const elapsedSeconds = (degradation: AccountTTFTGuardDegradation, referenceNow = now.value) => {
  const startedAt = Date.parse(degradation.degraded_at)
  if (!Number.isFinite(startedAt)) return 0
  return Math.max(0, Math.floor((referenceNow - startedAt) / 1000))
}

const formatDuration = (seconds: number) => {
  const days = Math.floor(seconds / 86400)
  const hours = Math.floor((seconds % 86400) / 3600)
  const minutes = Math.floor((seconds % 3600) / 60)
  const remainingSeconds = seconds % 60
  if (days > 0) return `${days}d${hours}h`
  if (hours > 0) return `${hours}h${minutes}m`
  if (minutes > 0) return `${minutes}m${remainingSeconds}s`
  return `${remainingSeconds}s`
}

const elapsedText = (degradation: AccountTTFTGuardDegradation) => {
  return formatDuration(elapsedSeconds(degradation))
}

const formatMilliseconds = (value: number) => {
  if (!Number.isFinite(value)) return '-'
  if (value >= 1000) return `${(value / 1000).toFixed(value % 1000 === 0 ? 0 : 1)}s`
  return `${Math.round(value)}ms`
}

const reasonText = (reason: string) => {
  const key = {
    critical_sample: 'criticalSample',
    consecutive_elevated: 'consecutiveElevated',
    ewma: 'ewmaReason'
  }[reason]
  return t(`admin.accounts.status.ttftGuard.${key ?? 'unknownReason'}`)
}

watch(
  () => degradations.value.length,
  (count) => {
    if (count > 0 && timer === null) {
      now.value = Date.now()
      timer = setInterval(() => {
        now.value = Date.now()
      }, 1000)
    } else if (count === 0 && timer !== null) {
      clearInterval(timer)
      timer = null
    }
  },
  { immediate: true }
)

onUnmounted(() => {
  if (timer !== null) {
    clearInterval(timer)
    timer = null
  }
})
</script>
