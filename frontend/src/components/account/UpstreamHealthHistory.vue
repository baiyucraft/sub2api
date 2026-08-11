<template>
  <component
    :is="interactive ? 'button' : 'div'"
    v-if="observations.length"
    :type="interactive ? 'button' : undefined"
    data-upstream-health-history
    class="group block min-w-0 rounded-md text-left outline-none focus-visible:ring-2 focus-visible:ring-primary-500 focus-visible:ring-offset-2 dark:focus-visible:ring-offset-dark-800"
    :aria-label="interactive ? t('admin.upstreamManagement.health.openDetails') : undefined"
    @click="interactive && emit('showHistory')"
  >
    <div class="flex h-8 items-end gap-[3px]" aria-hidden="true">
      <HelpTooltip
        v-for="(item, index) in visibleObservations"
        :key="`${item.observed_at}-${index}`"
        width-class="w-64 max-w-[calc(100vw-2rem)]"
        trigger-class="!ml-0 flex h-8 items-end"
      >
        <template #trigger>
          <span class="flex h-8 w-2 items-end sm:w-2.5">
            <span
              :data-observation-state="item.state"
              :class="[
                'block w-full rounded-[2px] transition-[height,filter] duration-150 group-hover:brightness-105',
                barClass(item.state)
              ]"
              :style="{ height: barHeight(item.state) }"
            />
          </span>
        </template>
        <div class="font-medium" :class="textClass(item.state)">{{ stateLabel(item.state) }}</div>
        <div class="mt-1">{{ t('admin.upstreamManagement.health.observedAt') }}: {{ formatDateTime(item.observed_at) }}</div>
        <div>{{ t('admin.upstreamManagement.health.source') }}: {{ sourceLabel(item.source) }}</div>
        <div v-if="item.model">{{ t('admin.upstreamManagement.health.model') }}: {{ item.model }}</div>
        <div>{{ t('admin.upstreamManagement.health.result') }}: {{ item.result || '-' }}</div>
        <div v-if="item.ttft_ms != null">{{ t('admin.upstreamManagement.health.ttft') }}: {{ formatDuration(item.ttft_ms) }}</div>
        <div v-if="item.duration_ms != null">{{ t('admin.upstreamManagement.health.duration') }}: {{ formatDuration(item.duration_ms) }}</div>
        <div>{{ t('admin.upstreamManagement.health.reason') }}: {{ reasonLabel(item.reason) }}</div>
      </HelpTooltip>
    </div>
    <div class="mt-1 flex items-center justify-between gap-3 text-[10px] leading-3 text-gray-400 dark:text-dark-500">
      <span>{{ t('admin.upstreamManagement.health.past') }}</span>
      <span class="font-medium text-gray-500 transition-colors group-hover:text-primary-600 dark:text-dark-400 dark:group-hover:text-primary-400">
        {{ t('admin.upstreamManagement.health.historySummary', { healthy: healthyCount, total: visibleObservations.length }) }}
      </span>
      <span>{{ t('admin.upstreamManagement.health.now') }}</span>
    </div>
  </component>
  <div v-else data-upstream-health-history-empty class="text-[11px] text-gray-400 dark:text-dark-500">
    {{ t('admin.upstreamManagement.health.noHistory') }}
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import HelpTooltip from '@/components/common/HelpTooltip.vue'
import type { UpstreamHealthObservation } from '@/types'
import { formatDateTime } from '@/utils/format'

const props = withDefaults(defineProps<{
  observations?: UpstreamHealthObservation[]
  limit?: number
  interactive?: boolean
}>(), {
  observations: () => [],
  limit: 24,
  interactive: true
})
const emit = defineEmits<{ (event: 'showHistory'): void }>()
const { t, te } = useI18n()
const visibleObservations = computed(() => props.observations.slice(-Math.min(24, Math.max(1, props.limit))))
const healthyCount = computed(() => visibleObservations.value.filter(item => item.state === 'healthy').length)

const barHeight = (state: UpstreamHealthObservation['state']) => ({ healthy: '100%', recovering: '82%', degraded: '65%', observing: '52%', suspended: '35%', disabled: '18%' }[state] || '18%')
const barClass = (state: UpstreamHealthObservation['state']) => ({
  healthy: 'bg-emerald-500 dark:bg-emerald-400', degraded: 'bg-amber-500 dark:bg-amber-400', suspended: 'bg-red-500 dark:bg-red-400',
  observing: 'bg-blue-500 dark:bg-blue-400', recovering: 'bg-cyan-500 dark:bg-cyan-400', disabled: 'bg-zinc-400 dark:bg-zinc-500'
}[state] || 'bg-zinc-400 dark:bg-zinc-500')
const textClass = (state: UpstreamHealthObservation['state']) => ({
  healthy: 'text-emerald-200', degraded: 'text-amber-200', suspended: 'text-red-200', observing: 'text-blue-200', recovering: 'text-cyan-200', disabled: 'text-gray-200'
}[state] || 'text-gray-200')
const stateLabel = (state: string) => t(`admin.upstreamManagement.health.${state}`)
const sourceLabel = (source: string) => {
  const key = `admin.upstreamManagement.health.sources.${source}`
  return te(key) ? t(key) : source
}
const reasonLabel = (reason?: string) => {
  const value = reason?.trim()
  if (!value) return '-'
  const key = `admin.upstreamManagement.health.reasons.${value}`
  return te(key) ? t(key) : value
}
const formatDuration = (milliseconds: number) => milliseconds >= 1000
  ? `${(milliseconds / 1000).toLocaleString(undefined, { maximumFractionDigits: 2 })} s`
  : `${milliseconds.toLocaleString()} ms`
</script>
