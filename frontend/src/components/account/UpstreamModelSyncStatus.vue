<template>
  <div
    class="mt-1 flex flex-wrap items-center justify-center gap-1 text-[10px]"
    :title="tooltip"
    data-test="upstream-model-sync-status"
  >
    <span
      class="inline-flex items-center gap-1 rounded-full px-1.5 py-0.5 font-semibold"
      :class="statusClass"
      :data-status="sync.status"
    >
      <span class="h-1.5 w-1.5 rounded-full" :class="dotClass" />
      {{ statusLabel }}
    </span>
    <span v-if="sync.model_count > 0" class="text-gray-400 dark:text-dark-400">
      {{ t('admin.accounts.upstreamModelSync.modelCount', { count: sync.model_count }) }}
    </span>
    <span
      v-if="sync.enforcement_expired"
      class="font-medium text-amber-600 dark:text-amber-400"
      data-test="upstream-model-sync-expired"
    >
      {{ t('admin.accounts.upstreamModelSync.fallback') }}
    </span>
    <span
      v-else-if="usesRetainedSnapshot"
      class="font-medium text-amber-600 dark:text-amber-400"
      data-test="upstream-model-sync-retained"
    >
      {{ t('admin.accounts.upstreamModelSync.retained') }}
    </span>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { UpstreamModelSync } from '@/types'
import { formatDateTime } from '@/utils/format'

const props = defineProps<{ sync: UpstreamModelSync }>()
const { t } = useI18n()

const statusLabel = computed(() => t(`admin.accounts.upstreamModelSync.status.${props.sync.status}`))
const usesRetainedSnapshot = computed(() =>
  props.sync.status !== 'available' && Boolean(props.sync.last_success_at)
)

const statusClass = computed(() => ({
  available: 'bg-emerald-50 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300',
  stale: 'bg-amber-50 text-amber-700 dark:bg-amber-900/30 dark:text-amber-300',
  error: 'bg-red-50 text-red-700 dark:bg-red-900/30 dark:text-red-300',
  unsupported: 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-dark-300'
}[props.sync.status]))

const dotClass = computed(() => ({
  available: 'bg-emerald-500',
  stale: 'bg-amber-500',
  error: 'bg-red-500',
  unsupported: 'bg-gray-400'
}[props.sync.status]))

const tooltip = computed(() => {
  const details = [
    t('admin.accounts.upstreamModelSync.tooltipStatus', { status: statusLabel.value }),
    t('admin.accounts.upstreamModelSync.tooltipMode', {
      mode: t(`admin.accounts.upstreamModelSync.mode.${props.sync.mode === 'sync_managed' ? 'managed' : 'manual'}`)
    })
  ]
  if (props.sync.last_success_at) {
    details.push(t('admin.accounts.upstreamModelSync.tooltipLastSuccess', {
      time: formatDateTime(props.sync.last_success_at)
    }))
  }
  if (props.sync.source) {
    details.push(t('admin.accounts.upstreamModelSync.tooltipSource', {
      source: t(`admin.accounts.upstreamModelSync.source.${props.sync.source}`)
    }))
  }
  if (props.sync.last_attempt_at && props.sync.last_attempt_at !== props.sync.last_success_at) {
    details.push(t('admin.accounts.upstreamModelSync.tooltipLastAttempt', {
      time: formatDateTime(props.sync.last_attempt_at)
    }))
  }
  if (props.sync.failure_kind) {
    details.push(t('admin.accounts.upstreamModelSync.tooltipFailure', { kind: props.sync.failure_kind }))
  }
  if (props.sync.enforcement_expired) {
    details.push(t('admin.accounts.upstreamModelSync.tooltipExpired'))
  } else if (usesRetainedSnapshot.value) {
    details.push(t('admin.accounts.upstreamModelSync.tooltipRetained'))
  }
  return details.join(' · ')
})
</script>
