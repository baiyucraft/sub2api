<template>
  <section v-if="item.show_group_rate" class="mt-4 rounded-xl border border-sky-100/80 bg-sky-50/50 px-3 py-2.5 dark:border-sky-500/20 dark:bg-sky-500/5" @click.stop>
    <div class="mb-1.5 flex items-center justify-between gap-2">
      <span class="text-[11px] font-medium text-gray-500 dark:text-gray-400">{{ t('channelStatus.rateTrend.title') }}</span>
      <span class="font-mono text-xs font-semibold text-sky-700 dark:text-sky-300">{{ formatRate(item.current_public_rate) }}</span>
    </div>
    <TrendChart
      :timestamps="timestamps"
      :series="series"
      :height="72"
      :show-legend="false"
      :max-ticks="4"
      :empty-text="t('channelStatus.rateTrend.empty')"
      :chart-label="t('channelStatus.rateTrend.chartLabel')"
      :time-column-label="t('channelStatus.rateTrend.timeColumn')"
      :value-formatter="formatRate"
    />
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { UserMonitorView } from '@/api/channelMonitor'
import TrendChart from '@/components/charts/TrendChart.vue'

const props = defineProps<{ item: UserMonitorView }>()
const { t } = useI18n()
const timestamps = computed(() => (props.item.rate_trend || []).map(point => point.observed_at))
const series = computed(() => [{
  label: t('channelStatus.rateTrend.series'),
  data: (props.item.rate_trend || []).map(point => point.rate),
  tone: 'primary' as const,
  stepped: true as const,
  pointStyle: 'circle' as const,
}])

function formatRate(value: number | null | undefined): string {
  if (value == null || !Number.isFinite(value)) return '-'
  return `${value.toFixed(2)}x`
}
</script>
