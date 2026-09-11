<template>
  <section class="channel-status-v1-hero sticky top-0 z-30 -mx-4 bg-white/95 px-4 py-3 backdrop-blur-md supports-[backdrop-filter]:bg-white/80 dark:bg-dark-900/95 dark:supports-[backdrop-filter]:bg-dark-900/80 md:-mx-6 md:px-6 md:py-4">
    <div class="flex items-center justify-end gap-3 flex-wrap">
      <div
        role="tablist"
        :aria-label="t('channelStatus.rangeLabel')"
        class="inline-flex p-0.5 rounded-xl bg-gray-100 dark:bg-dark-800 border border-gray-200/60 dark:border-dark-700/60 text-xs"
      >
        <button
          v-for="opt in rangeOptions"
          :key="opt.value"
          type="button"
          role="tab"
          :aria-selected="range === opt.value"
          class="px-3 py-1 rounded-lg transition-colors"
          :class="range === opt.value
            ? 'bg-white dark:bg-dark-700 shadow-sm text-gray-900 dark:text-white font-semibold'
            : 'text-gray-500 hover:text-gray-700 dark:text-gray-400 dark:hover:text-gray-200'"
          @click="emit('update:range', opt.value)"
        >
          {{ opt.label }}
        </button>
      </div>

      <span
        class="inline-flex items-center px-2.5 py-1 rounded-full text-xs font-semibold tracking-wider uppercase"
        :class="overallChipClass"
      >
        <span
          class="w-1.5 h-1.5 rounded-full mr-1.5"
          :class="overallDotClass"
        ></span>
        {{ overallLabel }}
      </span>

      <button
        type="button"
        class="h-8 w-8 rounded-lg flex items-center justify-center text-gray-500 hover:text-gray-700 hover:bg-gray-100 dark:text-gray-400 dark:hover:text-gray-200 dark:hover:bg-dark-700 transition-colors disabled:opacity-50"
        :disabled="loading"
        :title="t('common.refresh')"
        @click="emit('refresh')"
      >
        <Icon name="refresh" size="md" :class="loading ? 'animate-spin' : ''" />
      </button>

      <AutoRefreshButton
        v-if="autoRefresh"
        :enabled="autoRefresh.enabled.value"
        :interval-seconds="autoRefresh.intervalSeconds.value"
        :countdown="autoRefresh.countdown.value"
        :intervals="autoRefresh.intervals"
        @update:enabled="autoRefresh.setEnabled"
        @update:interval="autoRefresh.setInterval"
      />
    </div>
    <nav
      v-if="platforms.length"
      class="mt-3 flex items-center gap-1.5 overflow-x-auto border-t border-gray-100 pt-2 dark:border-dark-700"
      :aria-label="t('channelStatus.platformNavigation')"
    >
      <span class="shrink-0 text-[11px] font-medium text-gray-400 dark:text-gray-500">
        {{ t('channelStatus.platformNavigation') }}
      </span>
      <button
        v-for="platform in platforms"
        :key="platform.value"
        type="button"
        class="shrink-0 rounded-lg border px-2.5 py-1 text-xs transition-colors"
        :class="activePlatform === platform.value
          ? 'border-primary-500 bg-primary-50 font-semibold text-primary-700 dark:border-primary-400 dark:bg-primary-500/15 dark:text-primary-300'
          : 'border-gray-200 bg-white text-gray-500 hover:border-primary-300 hover:text-primary-700 dark:border-dark-700 dark:bg-dark-800 dark:text-gray-400 dark:hover:border-primary-500/50 dark:hover:text-primary-300'"
        :aria-current="activePlatform === platform.value ? 'location' : undefined"
        @click="emit('navigate-platform', platform.value)"
      >
        {{ platform.label }}
      </button>
    </nav>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import AutoRefreshButton from '@/components/common/AutoRefreshButton.vue'
import type { MonitorRange } from '@/api/channelMonitor'
export type OverallStatus = 'operational' | 'degraded'

const props = defineProps<{
  overallStatus: OverallStatus
  intervalSeconds: number
  range: MonitorRange
  loading: boolean
  platforms?: readonly PlatformOption[]
  activePlatform?: string
  autoRefresh?: {
    enabled: { value: boolean }
    intervalSeconds: { value: number }
    countdown: { value: number }
    intervals: readonly number[]
    setEnabled: (v: boolean) => void
    setInterval: (v: number) => void
  }
}>()

const emit = defineEmits<{
  (e: 'update:range', value: MonitorRange): void
  (e: 'refresh'): void
  (e: 'navigate-platform', value: string): void
}>()

const { t } = useI18n()

interface PlatformOption {
  value: string
  label: string
}

const platforms = computed(() => props.platforms ?? [])
const activePlatform = computed(() => props.activePlatform ?? '')

const rangeOptions = computed<{ value: MonitorRange; label: string }[]>(() => [
  { value: '24h', label: t('channelStatus.range.24h') },
  { value: '7d', label: t('channelStatus.range.7d') },
  { value: '15d', label: t('channelStatus.range.15d') },
  { value: '30d', label: t('channelStatus.range.30d') },
])

const overallLabel = computed(() => t(`channelStatus.overall.${props.overallStatus}`))

const overallChipClass = computed(() => {
  switch (props.overallStatus) {
    case 'operational':
      return 'bg-emerald-100 text-emerald-700 dark:bg-emerald-500/15 dark:text-emerald-300'
    case 'degraded':
    default:
      return 'bg-amber-100 text-amber-700 dark:bg-amber-500/15 dark:text-amber-300'
  }
})

const overallDotClass = computed(() => {
  switch (props.overallStatus) {
    case 'operational':
      return 'bg-emerald-500 animate-pulse'
    case 'degraded':
    default:
      return 'bg-amber-500 animate-pulse'
  }
})

</script>
