<template>
  <section class="channel-status-v1-hero sticky top-16 z-20 -mx-4 border-b border-slate-200/40 bg-[rgba(242,249,249,0.94)] px-4 pb-3 pt-2.5 backdrop-blur-md dark:border-dark-800/60 dark:bg-[rgba(10,18,30,0.94)] md:-mx-6 md:px-6 md:pb-3.5 md:pt-3">
    <div class="flex flex-wrap items-center justify-between gap-x-3 gap-y-2">
      <nav
        v-if="platforms.length"
        class="order-2 flex min-w-0 flex-1 items-center gap-1.5 overflow-x-auto pb-0.5 md:order-1 md:border-0 md:pb-0"
        :aria-label="t('channelStatus.platformNavigation')"
      >
        <span class="mr-0.5 shrink-0 text-[11px] font-medium text-gray-400 dark:text-gray-500">
          {{ t('channelStatus.platformNavigation') }}
        </span>
        <button
          v-for="platform in platforms"
          :key="platform.value"
          type="button"
          class="inline-flex shrink-0 items-center gap-1.5 rounded-lg border px-2.5 py-1.5 text-xs font-medium transition-colors"
          :class="activePlatform === platform.value
            ? 'text-white shadow-sm'
            : 'bg-white hover:bg-gray-50 dark:bg-dark-800 dark:hover:bg-dark-700'"
          :style="{
            color: activePlatform === platform.value ? '#fff' : platformAccentColor(platform.value),
            borderColor: platformAccentColor(platform.value),
            backgroundColor: activePlatform === platform.value ? platformAccentColor(platform.value) : undefined
          }"
          :aria-current="activePlatform === platform.value ? 'location' : undefined"
          @click="emit('navigate-platform', platform.value)"
        >
          <PlatformIcon :platform="platformIconValue(platform.value)" size="xs" />
          {{ platform.label }}
        </button>
      </nav>

      <div class="order-1 flex min-w-0 shrink-0 items-center gap-2 md:order-2">
        <div
          role="tablist"
          :aria-label="t('channelStatus.rangeLabel')"
          class="inline-flex shrink-0 rounded-lg border border-gray-200/80 bg-gray-100 p-0.5 text-xs dark:border-dark-700 dark:bg-dark-800"
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
          class="inline-flex items-center rounded-full px-2.5 py-1 text-xs font-semibold tracking-wider uppercase"
          :class="overallChipClass"
        >
          <span
            class="mr-1.5 h-1.5 w-1.5 rounded-full"
            :class="overallDotClass"
          ></span>
          {{ overallLabel }}
        </span>

        <button
          type="button"
          class="flex h-8 w-8 items-center justify-center rounded-lg text-gray-500 transition-colors hover:bg-gray-100 hover:text-gray-700 dark:text-gray-400 dark:hover:bg-dark-800 dark:hover:text-gray-200 disabled:opacity-50"
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
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import AutoRefreshButton from '@/components/common/AutoRefreshButton.vue'
import type { MonitorRange } from '@/api/channelMonitor'
import type { GroupPlatform } from '@/types'
import PlatformIcon from '@/components/common/PlatformIcon.vue'
import { platformAccentColor } from '@/utils/platformColors'
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

function platformIconValue(value: string): GroupPlatform | undefined {
  return value as GroupPlatform
}

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
