<template>
  <BaseDialog
    :show="show"
    :title="title"
    width="wide"
    @close="$emit('close')"
  >
    <div v-if="loading" class="py-8 text-center text-sm text-gray-500">
      {{ t('common.loading') }}
    </div>
    <div v-else-if="!detail" class="py-8 text-center text-sm text-gray-500">
      {{ t('channelStatus.detailLoadError') }}
    </div>
    <div v-else>
      <section v-if="detail.show_group_rate" class="mb-5 rounded-xl border border-sky-100 bg-sky-50/50 p-4 dark:border-sky-500/20 dark:bg-sky-500/5">
        <div class="mb-3 flex flex-wrap items-start justify-between gap-3">
          <div>
            <h3 class="text-sm font-semibold text-gray-800 dark:text-gray-100">{{ t('channelStatus.rateTrend.title') }}</h3>
            <p
              v-if="historyPartial"
              class="mt-1 text-xs text-amber-700 dark:text-amber-300"
              data-test="detail-rate-history-partial"
            >
              {{ t('channelStatus.rateTrend.historyIncomplete') }}
            </p>
          </div>
          <dl class="grid grid-cols-2 gap-x-5 text-right tabular-nums">
            <div>
              <dt class="text-[10px] font-medium uppercase tracking-wide text-gray-400 dark:text-gray-500">
                {{ t('channelStatus.rateTrend.current') }}
              </dt>
              <dd class="font-mono text-sm font-semibold text-sky-700 dark:text-sky-300">
                {{ formatPublicRate(detail.current_public_rate) }}
              </dd>
            </div>
            <div>
              <dt class="text-[10px] font-medium uppercase tracking-wide text-gray-400 dark:text-gray-500">
                {{ t(historyPartial ? 'channelStatus.rateTrend.observedAverage' : 'channelStatus.rateTrend.average') }}
              </dt>
              <dd class="font-mono text-sm font-semibold text-gray-700 dark:text-gray-200">
                {{ formatPublicRate(detail.average_public_rate) }}
              </dd>
            </div>
          </dl>
        </div>
        <TrendChart
          :timestamps="rateChartData.timestamps"
          :series="rateSeries"
          :height="240"
          proportional-time
          :x-min="detail.rate_range_start"
          :x-max="detail.rate_range_end"
          :y-min="rateAxis.min"
          :y-max="rateAxis.max"
          y-grace="10%"
          :empty-text="t('channelStatus.rateTrend.empty')"
          :chart-label="t('channelStatus.rateTrend.chartLabel')"
          :time-column-label="t('channelStatus.rateTrend.timeColumn')"
          :value-formatter="formatPublicRate"
        />
      </section>
      <div class="hidden overflow-x-auto sm:block">
        <table class="w-full text-left text-sm" data-test="desktop-model-table">
        <thead class="border-b border-gray-200 dark:border-dark-700">
          <tr class="text-xs uppercase tracking-wider text-gray-500 dark:text-gray-400">
            <th class="py-2 pr-3">{{ t('channelStatus.detailColumns.model') }}</th>
            <th class="py-2 pr-3">{{ t('channelStatus.detailColumns.latestStatus') }}</th>
            <th class="py-2 pr-3">{{ t('channelStatus.detailColumns.latestLatency') }}</th>
            <th class="py-2 pr-3">{{ t('channelStatus.detailColumns.availability24h') }}</th>
            <th class="py-2 pr-3">{{ t('channelStatus.detailColumns.availability7d') }}</th>
            <th class="py-2 pr-3">{{ t('channelStatus.detailColumns.availability15d') }}</th>
            <th class="py-2 pr-3">{{ t('channelStatus.detailColumns.availability30d') }}</th>
            <th class="py-2 pr-3">{{ t('channelStatus.detailColumns.avgLatency7d') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="m in detail.models"
            :key="m.model"
            class="border-b border-gray-100 dark:border-dark-800"
          >
            <td class="py-2 pr-3 font-medium text-gray-900 dark:text-gray-100">{{ formatMonitorModel(m.model) }}</td>
            <td class="py-2 pr-3">
              <span
                class="inline-flex items-center rounded-full px-2 py-0.5 text-[11px]"
                :class="statusBadgeClass(m.latest_status)"
              >
                {{ statusLabel(m.latest_status) }}
              </span>
            </td>
            <td class="py-2 pr-3 text-gray-700 dark:text-gray-300">{{ formatLatency(m.latest_latency_ms) }}</td>
            <td class="py-2 pr-3 text-gray-700 dark:text-gray-300">{{ formatPercent(m.availability_24h) }}</td>
            <td class="py-2 pr-3 text-gray-700 dark:text-gray-300">{{ formatPercent(m.availability_7d) }}</td>
            <td class="py-2 pr-3 text-gray-700 dark:text-gray-300">{{ formatPercent(m.availability_15d) }}</td>
            <td class="py-2 pr-3 text-gray-700 dark:text-gray-300">{{ formatPercent(m.availability_30d) }}</td>
            <td class="py-2 pr-3 text-gray-700 dark:text-gray-300">{{ formatLatency(m.avg_latency_7d_ms) }}</td>
          </tr>
        </tbody>
        </table>
      </div>

      <div class="divide-y divide-gray-100 dark:divide-dark-800 sm:hidden" data-test="mobile-model-metrics">
        <section v-for="m in detail.models" :key="`mobile-${m.model}`" class="py-3 first:pt-0 last:pb-0">
          <div class="flex items-start justify-between gap-3">
            <h3 class="min-w-0 break-words text-sm font-semibold text-gray-900 dark:text-gray-100">{{ m.model }}</h3>
            <span
              class="inline-flex shrink-0 items-center rounded-full px-2 py-0.5 text-[11px]"
              :class="statusBadgeClass(m.latest_status)"
            >
              {{ statusLabel(m.latest_status) }}
            </span>
          </div>
          <dl class="mt-3 grid grid-cols-2 gap-x-4 gap-y-2 text-xs">
            <div>
              <dt class="text-gray-500 dark:text-gray-400">{{ t('channelStatus.detailColumns.latestLatency') }}</dt>
              <dd class="mt-0.5 font-medium text-gray-800 dark:text-gray-200">{{ formatLatency(m.latest_latency_ms) }}</dd>
            </div>
            <div>
              <dt class="text-gray-500 dark:text-gray-400">{{ t('channelStatus.detailColumns.avgLatency7d') }}</dt>
              <dd class="mt-0.5 font-medium text-gray-800 dark:text-gray-200">{{ formatLatency(m.avg_latency_7d_ms) }}</dd>
            </div>
            <div>
              <dt class="text-gray-500 dark:text-gray-400">{{ t('channelStatus.detailColumns.availability24h') }}</dt>
              <dd class="mt-0.5 font-medium text-gray-800 dark:text-gray-200">{{ formatPercent(m.availability_24h) }}</dd>
            </div>
            <div>
              <dt class="text-gray-500 dark:text-gray-400">{{ t('channelStatus.detailColumns.availability7d') }}</dt>
              <dd class="mt-0.5 font-medium text-gray-800 dark:text-gray-200">{{ formatPercent(m.availability_7d) }}</dd>
            </div>
            <div>
              <dt class="text-gray-500 dark:text-gray-400">{{ t('channelStatus.detailColumns.availability15d') }}</dt>
              <dd class="mt-0.5 font-medium text-gray-800 dark:text-gray-200">{{ formatPercent(m.availability_15d) }}</dd>
            </div>
            <div>
              <dt class="text-gray-500 dark:text-gray-400">{{ t('channelStatus.detailColumns.availability30d') }}</dt>
              <dd class="mt-0.5 font-medium text-gray-800 dark:text-gray-200">{{ formatPercent(m.availability_30d) }}</dd>
            </div>
          </dl>
        </section>
      </div>
    </div>

    <template #footer>
      <div class="flex justify-end">
        <button @click="$emit('close')" class="btn btn-secondary">
          {{ t('channelStatus.closeDetail') }}
        </button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { extractApiErrorMessage } from '@/utils/apiError'
import {
  status as fetchChannelMonitorDetail,
  type MonitorRange,
  type UserMonitorDetail,
} from '@/api/channelMonitor'
import BaseDialog from '@/components/common/BaseDialog.vue'
import TrendChart from '@/components/charts/TrendChart.vue'
import { useChannelMonitorFormat } from '@/composables/useChannelMonitorFormat'
import {
  buildRateTrendChartData,
  formatPublicRate,
  rateAxisBounds,
  rateHistoryIsPartial,
} from '@/utils/channelMonitorRateTrend'

const props = defineProps<{
  show: boolean
  monitorId: number | null
  title: string
  range: MonitorRange
}>()

defineEmits<{
  (e: 'close'): void
}>()

const { t } = useI18n()
const appStore = useAppStore()
const { statusLabel, statusBadgeClass, formatLatency, formatPercent, formatMonitorModel } = useChannelMonitorFormat()

const detail = ref<UserMonitorDetail | null>(null)
const loading = ref(false)
const rateChartData = computed(() => buildRateTrendChartData(
  detail.value?.rate_trend,
  detail.value?.rate_range_end,
  detail.value?.current_public_rate,
))
const rateAxis = computed(() => rateAxisBounds(rateChartData.value.values))
const historyPartial = computed(() => rateHistoryIsPartial(
  detail.value?.rate_observed_since,
  detail.value?.rate_range_start,
))
const rateSeries = computed(() => [{
  label: t('channelStatus.rateTrend.series'),
  data: rateChartData.value.values,
  tone: 'primary' as const,
  pointStyle: 'circle' as const,
  pointRadius: 0,
  pointHoverRadius: 4,
}])

async function load(id: number) {
  detail.value = null
  loading.value = true
  try {
    detail.value = await fetchChannelMonitorDetail(id, props.range)
  } catch (err: unknown) {
    appStore.showError(extractApiErrorMessage(err, t('channelStatus.detailLoadError')))
  } finally {
    loading.value = false
  }
}

watch(
  () => [props.show, props.monitorId, props.range] as const,
  ([show, id]) => {
    if (!show) {
      detail.value = null
      return
    }
    if (id != null) void load(id)
  },
  { immediate: true },
)
</script>
