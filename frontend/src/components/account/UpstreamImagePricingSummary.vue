<template>
  <div
    class="mt-1 min-w-[15rem] max-w-[18rem]"
    :title="summaryTitle"
    data-test="upstream-image-pricing-summary"
  >
    <div class="grid grid-cols-[repeat(3,minmax(0,1fr))_auto] overflow-hidden rounded-md border border-gray-200/80 bg-gray-50/80 shadow-sm shadow-gray-200/30 dark:border-dark-600/80 dark:bg-dark-800/70 dark:shadow-none">
      <div
        v-for="tier in tiers"
        :key="tier.key"
        class="flex min-w-0 flex-col border-r border-gray-200/80 px-2 py-1 dark:border-dark-600/80"
        :data-test="`image-cost-${tier.key.toLowerCase()}`"
      >
        <span class="text-[9px] font-semibold uppercase leading-3 tracking-wide text-gray-400 dark:text-dark-500">{{ tier.key }}</span>
        <span class="truncate text-[11px] font-semibold leading-4 text-gray-700 tabular-nums dark:text-dark-200">{{ formatCost(tier.value) }}</span>
      </div>
      <div class="flex min-w-[3.75rem] items-center justify-center gap-1 px-2 py-1">
        <span
          v-if="hasWarning"
          class="h-1.5 w-1.5 shrink-0 rounded-full bg-amber-500"
          :aria-label="statusLabel"
          data-test="image-pricing-status"
        />
        <span
          class="whitespace-nowrap rounded-full px-1.5 py-0.5 text-[10px] font-semibold leading-4"
          :class="pricing.rate_independent
            ? 'bg-violet-100 text-violet-700 dark:bg-violet-900/40 dark:text-violet-300'
            : 'bg-gray-200/80 text-gray-600 dark:bg-dark-700 dark:text-dark-300'"
          data-test="image-pricing-rate-mode"
        >
          {{ rateModeLabel }}
        </span>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

import type { UpstreamImagePricing } from '@/types'
import { formatDateTime } from '@/utils/format'
import { formatMultiplier } from '@/utils/formatters'

const props = defineProps<{
  pricing: UpstreamImagePricing
}>()

const { t } = useI18n()

const formatCost = (value: number | null | undefined) => {
  if (value === null || value === undefined || !Number.isFinite(value)) return '—'
  const digits = value > 0 && value < 0.01 ? 4 : 3
  const amount = value.toFixed(digits)
  return props.pricing.currency === 'USD' ? `$${amount}` : `${props.pricing.currency} ${amount}`
}

const statusLabel = computed(() => {
  if (props.pricing.stale) return t('admin.accounts.upstreamImagePricing.statusStale')
  if (props.pricing.status === 'partial') return t('admin.accounts.upstreamImagePricing.statusPartial')
  if (props.pricing.status === 'available') return t('admin.accounts.upstreamImagePricing.statusAvailable')
  return t('admin.accounts.upstreamImagePricing.statusUnavailable')
})

const hasWarning = computed(() => props.pricing.stale || props.pricing.status === 'partial')

const rateModeLabel = computed(() => {
  const mode = props.pricing.rate_independent
    ? t('admin.accounts.upstreamImagePricing.independent')
    : t('admin.accounts.upstreamImagePricing.shared')
  const multiplier = props.pricing.effective_rate_multiplier
  if (!props.pricing.rate_independent || multiplier === null || multiplier === undefined) return mode
  return `${mode} ${formatMultiplier(multiplier)}×`
})

const tiers = computed(() => [
  { key: '1K', value: props.pricing.final_cost_1k },
  { key: '2K', value: props.pricing.final_cost_2k },
  { key: '4K', value: props.pricing.final_cost_4k }
])

const summaryTitle = computed(() => t('admin.accounts.upstreamImagePricing.costTitle', {
  mode: rateModeLabel.value,
  status: statusLabel.value
}) + (props.pricing.observed_at
  ? ` · ${formatDateTime(props.pricing.observed_at)}`
  : ''))
</script>
