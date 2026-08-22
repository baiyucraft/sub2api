<template>
  <div
    class="mx-auto mt-1 w-[15rem] max-w-full border-t border-gray-200/80 pt-1.5 text-center dark:border-dark-600/80"
    :title="summaryTitle"
    data-test="upstream-video-pricing-summary"
  >
    <div class="grid grid-cols-3 gap-1.5 tabular-nums">
      <div
        v-for="tier in tiers"
        :key="tier.key"
        class="flex min-w-0 flex-col items-center justify-center rounded-sm px-1 py-0.5 transition-colors hover:bg-gray-50 dark:hover:bg-dark-800"
        :title="costTooltip(tier)"
        :data-test="`video-cost-${tier.key}`"
      >
        <span class="flex items-center gap-1 text-[9px] font-semibold uppercase leading-3 tracking-wide text-gray-400 dark:text-dark-500">
          {{ tier.key }}
          <span v-if="hasWarning" class="h-1.5 w-1.5 shrink-0 rounded-full bg-amber-500" :aria-label="statusLabel" />
        </span>
        <span class="truncate text-[11px] font-semibold leading-4 text-gray-700 dark:text-dark-200">{{ formatCost(tier.value) }}</span>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { UpstreamVideoPricing } from '@/types'
import { formatDateTime } from '@/utils/format'
import { formatMultiplier } from '@/utils/formatters'

const props = defineProps<{ pricing: UpstreamVideoPricing }>()
const { t } = useI18n()
const formatCost = (value: number | null | undefined) => {
  if (value === null || value === undefined || !Number.isFinite(value)) return '—'
  const digits = value > 0 && value < 0.01 ? 4 : 3
  return `$${value.toFixed(digits)}`
}
const statusLabel = computed(() => {
  if (props.pricing.stale) return t('admin.accounts.upstreamVideoPricing.statusStale')
  if (props.pricing.status === 'partial') return t('admin.accounts.upstreamVideoPricing.statusPartial')
  if (props.pricing.status === 'available') return t('admin.accounts.upstreamVideoPricing.statusAvailable')
  return t('admin.accounts.upstreamVideoPricing.statusUnavailable')
})
const hasWarning = computed(() => props.pricing.stale || props.pricing.status === 'partial')
const rateMultiplierLabel = computed(() => formatMultiplier(props.pricing.effective_rate_multiplier ?? 0))
const tiers = computed(() => [
  { key: '480p', value: props.pricing.final_cost_480p },
  { key: '720p', value: props.pricing.final_cost_720p },
  { key: '1080p', value: props.pricing.final_cost_1080p }
])
const costTooltip = (tier: { key: string; value: number | null | undefined }) => t(
  props.pricing.rate_independent ? 'admin.accounts.upstreamVideoPricing.costTooltipIndependent' : 'admin.accounts.upstreamVideoPricing.costTooltipShared',
  { tier: tier.key, cost: formatCost(tier.value), multiplier: rateMultiplierLabel.value, status: statusLabel.value }
)
const summaryTitle = computed(() => t('admin.accounts.upstreamVideoPricing.costTitle', {
  mode: props.pricing.rate_independent ? t('admin.accounts.upstreamImagePricing.independent') : t('admin.accounts.upstreamImagePricing.shared'),
  status: statusLabel.value
}) + (props.pricing.observed_at ? ` · ${formatDateTime(props.pricing.observed_at)}` : ''))
</script>
