<template>
  <div
    class="w-full min-w-0 text-left sm:w-[18rem] sm:min-w-[18rem]"
    data-test="user-usage-stats-matrix"
  >
    <div
      class="grid grid-cols-[3.25rem_repeat(3,minmax(0,1fr))] items-center gap-x-2 border-b border-gray-100 pb-1 text-[10px] font-semibold uppercase tracking-[0.08em] text-gray-400 dark:border-dark-700 dark:text-dark-500"
    >
      <span aria-hidden="true"></span>
      <span class="text-right">{{ t('admin.users.usageStats.token') }}</span>
      <span class="text-right text-emerald-600/80 dark:text-emerald-400/80">
        {{ t('admin.users.usageStats.spend') }}
      </span>
      <span class="text-right text-amber-600/80 dark:text-amber-400/80">
        {{ t('admin.users.usageStats.cost') }}
      </span>
    </div>

    <div
      v-if="failed || stats?.aggregation_status === 'unavailable'"
      class="divide-y divide-gray-100 dark:divide-dark-700/70"
      data-test="user-usage-unavailable"
    >
      <div
        v-for="window in windows"
        :key="window.key"
        class="grid grid-cols-[3.25rem_repeat(3,minmax(0,1fr))] items-center gap-x-2 py-1 text-[11px] leading-4"
        :class="{ 'border-t border-gray-200 font-semibold dark:border-dark-600': window.key === 'lifetime' }"
      >
        <span class="text-gray-500 dark:text-dark-400">{{ window.label }}</span>
        <span v-for="index in 3" :key="index" class="text-right text-gray-400 dark:text-dark-500">—</span>
      </div>
    </div>

    <div v-else-if="loading || !stats" class="space-y-1.5 pt-1.5" aria-busy="true">
      <div v-for="window in windows" :key="window.key" class="grid grid-cols-[3.25rem_repeat(3,minmax(0,1fr))] gap-x-2">
        <span class="h-3.5 w-8 animate-pulse rounded bg-gray-100 dark:bg-dark-700"></span>
        <span v-for="index in 3" :key="index" class="h-3.5 animate-pulse rounded bg-gray-100 dark:bg-dark-700"></span>
      </div>
    </div>

    <div v-else class="divide-y divide-gray-100 dark:divide-dark-700/70">
      <div
        v-for="window in windows"
        :key="window.key"
        class="group/window relative"
        :class="{ 'border-t border-gray-200 dark:border-dark-600': window.key === 'lifetime' }"
      >
        <button
          type="button"
          class="grid w-full grid-cols-[3.25rem_repeat(3,minmax(0,1fr))] items-center gap-x-2 rounded-sm py-1 text-[11px] leading-4 outline-none transition-colors hover:bg-gray-50 focus-visible:bg-primary-50 focus-visible:ring-1 focus-visible:ring-primary-400 dark:hover:bg-dark-800 dark:focus-visible:bg-primary-900/20"
          :class="{ 'font-semibold': window.key === 'lifetime' }"
          :aria-expanded="activeWindow === window.key"
          :data-test="`user-usage-window-${window.key}`"
          @click="toggleDetails(window.key)"
          @keydown.esc="activeWindow = null"
          @blur="closeDetails"
        >
          <span class="flex items-center gap-1 text-left text-gray-500 dark:text-dark-400">
            {{ window.label }}
            <span
              v-if="window.key === 'lifetime' && isLifetimePartial"
              class="h-1.5 w-1.5 shrink-0 rounded-full bg-amber-500"
              :title="lifetimeHint"
              data-test="user-usage-lifetime-partial"
            ></span>
          </span>
          <span class="text-right font-mono tabular-nums text-gray-700 dark:text-gray-200">
            {{ formatCompactTokens(window.data.total_tokens) }}
          </span>
          <span class="text-right font-mono tabular-nums text-emerald-600 dark:text-emerald-400">
            {{ formatCompactMoney(window.data.user_spend) }}
          </span>
          <span class="text-right font-mono tabular-nums text-amber-600 dark:text-amber-400">
            {{ formatCompactMoney(window.data.account_cost) }}
          </span>
        </button>

        <div
          class="pointer-events-none absolute right-0 top-full z-50 mt-1 hidden w-[17rem] rounded-lg border border-gray-700/20 bg-gray-950 px-3 py-2.5 text-xs font-normal text-white shadow-xl group-hover/window:block group-focus-within/window:block dark:border-dark-500 dark:bg-dark-700"
          :class="{ '!block': activeWindow === window.key }"
          role="tooltip"
          :data-test="`user-usage-details-${window.key}`"
        >
          <div class="mb-2 flex items-center justify-between border-b border-white/10 pb-1.5">
            <span class="font-medium">{{ window.label }}</span>
            <span
              v-if="window.key === 'lifetime' && isLifetimePartial"
              class="text-[10px] text-amber-300"
            >
              {{ lifetimeHint }}
            </span>
          </div>
          <dl class="grid grid-cols-[1fr_auto] gap-x-4 gap-y-1 text-[11px]">
            <dt class="text-gray-400">{{ t('admin.users.usageStats.inputTokens') }}</dt>
            <dd class="font-mono tabular-nums">{{ formatExactTokens(window.data.input_tokens) }}</dd>
            <dt class="text-gray-400">{{ t('admin.users.usageStats.outputTokens') }}</dt>
            <dd class="font-mono tabular-nums">{{ formatExactTokens(window.data.output_tokens) }}</dd>
            <dt class="text-gray-400">{{ t('admin.users.usageStats.cacheCreationTokens') }}</dt>
            <dd class="font-mono tabular-nums">{{ formatExactTokens(window.data.cache_creation_tokens) }}</dd>
            <dt class="text-gray-400">{{ t('admin.users.usageStats.cacheReadTokens') }}</dt>
            <dd class="font-mono tabular-nums">{{ formatExactTokens(window.data.cache_read_tokens) }}</dd>
            <dt class="border-t border-white/10 pt-1 text-gray-400">{{ t('admin.users.usageStats.totalTokens') }}</dt>
            <dd class="border-t border-white/10 pt-1 font-mono tabular-nums">{{ formatExactTokens(window.data.total_tokens) }}</dd>
            <dt class="text-gray-400">{{ t('admin.users.usageStats.spend') }}</dt>
            <dd class="font-mono tabular-nums text-emerald-300">{{ formatExactMoney(window.data.user_spend) }}</dd>
            <dt class="text-gray-400">{{ t('admin.users.usageStats.cost') }}</dt>
            <dd class="font-mono tabular-nums text-amber-300">{{ formatExactMoney(window.data.account_cost) }}</dd>
            <dt class="text-gray-400">{{ t('admin.users.usageStats.margin') }}</dt>
            <dd class="font-mono tabular-nums">{{ formatExactMoney(window.data.user_spend - window.data.account_cost) }}</dd>
          </dl>
          <p class="mt-2 border-t border-white/10 pt-1.5 text-[10px] leading-4 text-gray-400">
            {{ t('admin.users.usageStats.costHint') }}
          </p>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import type { BatchUserUsageStats, UserUsageWindow } from '@/api/admin/dashboard'

const props = defineProps<{
  stats?: BatchUserUsageStats
  loading?: boolean
  failed?: boolean
}>()

type WindowKey = 'today' | 'last_30d' | 'lifetime'

const { t, locale } = useI18n()
const activeWindow = ref<WindowKey | null>(null)
const numberLocale = () => (locale?.value === 'zh' ? 'zh-CN' : 'en-US')

const emptyWindow = (): UserUsageWindow => ({
  input_tokens: 0,
  output_tokens: 0,
  cache_creation_tokens: 0,
  cache_read_tokens: 0,
  total_tokens: 0,
  user_spend: 0,
  account_cost: 0
})

const windows = computed<Array<{ key: WindowKey; label: string; data: UserUsageWindow }>>(() => [
  {
    key: 'today',
    label: t('admin.users.today'),
    data: props.stats?.today ?? emptyWindow()
  },
  {
    key: 'last_30d',
    label: t('admin.users.total'),
    data: props.stats?.last_30d ?? emptyWindow()
  },
  {
    key: 'lifetime',
    label: t('admin.users.lifetime'),
    data: props.stats?.lifetime ?? emptyWindow()
  }
])

const isLifetimePartial = computed(
  () =>
    !!props.stats &&
    (!props.stats.lifetime_complete ||
      props.stats.aggregation_status === 'partial' ||
      props.stats.aggregation_status === 'building')
)

const lifetimeHint = computed(() => {
  if (!props.stats?.lifetime_since) {
    return t('admin.users.usageStats.lifetimePartial')
  }
  return t('admin.users.usageStats.lifetimeSince', {
    date: new Intl.DateTimeFormat(numberLocale(), {
      year: 'numeric',
      month: '2-digit',
      day: '2-digit'
    }).format(new Date(props.stats.lifetime_since))
  })
})

function formatCompactTokens(value: number): string {
  const amount = Number.isFinite(value) ? value : 0
  const absolute = Math.abs(amount)
  if (absolute >= 1_000_000_000) return `${trimFixed(amount / 1_000_000_000, 1)}B`
  if (absolute >= 1_000_000) return `${trimFixed(amount / 1_000_000, 1)}M`
  if (absolute >= 1_000) return `${trimFixed(amount / 1_000, 1)}K`
  return Math.round(amount).toString()
}

function formatCompactMoney(value: number): string {
  const amount = Number.isFinite(value) ? value : 0
  return `$${amount.toLocaleString(numberLocale(), {
    minimumFractionDigits: Math.abs(amount) < 1 ? 4 : 2,
    maximumFractionDigits: Math.abs(amount) < 1 ? 4 : 2
  })}`
}

function formatExactTokens(value: number): string {
  const amount = Number.isFinite(value) ? Math.round(value) : 0
  return amount.toLocaleString(numberLocale())
}

function formatExactMoney(value: number): string {
  const amount = Number.isFinite(value) ? value : 0
  return `$${amount.toLocaleString(numberLocale(), {
    minimumFractionDigits: 4,
    maximumFractionDigits: 6
  })}`
}

function trimFixed(value: number, digits: number): string {
  return value.toFixed(digits).replace(/\.0$/, '')
}

function toggleDetails(key: WindowKey) {
  activeWindow.value = activeWindow.value === key ? null : key
}

function closeDetails() {
  window.setTimeout(() => {
    activeWindow.value = null
  }, 0)
}
</script>
