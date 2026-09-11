<template>
  <section v-if="item.show_group_rate" class="mt-4 rounded-xl border border-sky-100/80 bg-sky-50/50 px-3 py-3 dark:border-sky-500/20 dark:bg-sky-500/5" @click.stop>
    <button
      type="button"
      class="flex w-full flex-wrap items-start justify-between gap-x-3 gap-y-2 text-left"
      :aria-expanded="expanded"
      :aria-controls="`rate-trend-${item.id}`"
      :title="t(expanded ? 'channelStatus.rateTrend.collapse' : 'channelStatus.rateTrend.expand')"
      @click.stop="expanded = !expanded"
    >
      <div class="min-w-0">
        <span class="text-[11px] font-medium text-gray-500 dark:text-gray-400">{{ t('channelStatus.rateTrend.title') }}</span>
        <p
          v-if="expanded && historyPartial"
          class="mt-0.5 text-[10px] leading-4 text-amber-700 dark:text-amber-300"
          data-test="rate-history-partial"
        >
          {{ t('channelStatus.rateTrend.historyIncomplete') }}
        </p>
      </div>
      <div class="flex items-center gap-2">
        <dl class="text-right tabular-nums">
          <dt class="text-[9px] font-medium uppercase tracking-wide text-gray-400 dark:text-gray-500">
            {{ t('channelStatus.rateTrend.current') }}
          </dt>
          <dd class="font-mono text-xs font-semibold text-sky-700 dark:text-sky-300" data-test="current-public-rate">
            {{ formatPublicRate(item.current_public_rate) }}
          </dd>
        </dl>
        <Icon
          name="chevronDown"
          size="sm"
          class="mt-2 shrink-0 text-sky-500 transition-transform dark:text-sky-300"
          :class="expanded ? 'rotate-180' : ''"
          aria-hidden="true"
        />
      </div>
    </button>
    <div v-if="expanded" :id="`rate-trend-${item.id}`" class="mt-2.5">
      <div class="mb-2.5 flex justify-end">
        <dl class="text-right tabular-nums">
          <dt class="text-[9px] font-medium uppercase tracking-wide text-gray-400 dark:text-gray-500">
            {{ t(historyPartial ? 'channelStatus.rateTrend.observedAverage' : 'channelStatus.rateTrend.average') }}
          </dt>
          <dd class="font-mono text-xs font-semibold text-gray-700 dark:text-gray-200" data-test="average-public-rate">
            {{ formatPublicRate(item.average_public_rate) }}
          </dd>
        </dl>
      </div>
      <TrendChart
        :timestamps="chartData.timestamps"
        :series="series"
        :height="152"
        :show-legend="false"
        :max-ticks="4"
        proportional-time
        :x-min="item.rate_range_start"
        :x-max="item.rate_range_end"
        :y-min="axisBounds.min"
        :y-max="axisBounds.max"
        y-grace="10%"
        :empty-text="t('channelStatus.rateTrend.empty')"
        :chart-label="t('channelStatus.rateTrend.chartLabel')"
        :time-column-label="t('channelStatus.rateTrend.timeColumn')"
        :value-formatter="formatPublicRate"
      />
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import type { UserMonitorView } from '@/api/channelMonitor'
import TrendChart from '@/components/charts/TrendChart.vue'
import {
  buildRateTrendChartData,
  formatPublicRate,
  rateAxisBounds,
  rateHistoryIsPartial,
} from '@/utils/channelMonitorRateTrend'

const props = defineProps<{ item: UserMonitorView }>()
const { t } = useI18n()
const expanded = ref(false)
const chartData = computed(() => buildRateTrendChartData(
  props.item.rate_trend,
  props.item.rate_range_end,
  props.item.current_public_rate,
))
const axisBounds = computed(() => rateAxisBounds(chartData.value.values))
const historyPartial = computed(() => rateHistoryIsPartial(
  props.item.rate_observed_since,
  props.item.rate_range_start,
))
const series = computed(() => [{
  label: t('channelStatus.rateTrend.series'),
  data: chartData.value.values,
  tone: 'primary' as const,
  cubicInterpolationMode: 'monotone' as const,
  pointStyle: 'circle' as const,
  pointRadius: 0,
  pointHoverRadius: 4,
}])
</script>
