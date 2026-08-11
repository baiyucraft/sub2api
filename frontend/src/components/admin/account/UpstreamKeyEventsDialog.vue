<template>
  <BaseDialog :show="show" :title="title" width="extra-wide" @close="$emit('close')">
    <div class="space-y-6">
      <section class="rounded-2xl border border-gray-200 bg-white p-4 shadow-sm dark:border-dark-700 dark:bg-dark-900/60 sm:p-5">
        <div class="mb-5 flex flex-col gap-3 lg:flex-row lg:items-center lg:justify-between">
          <div>
            <h3 class="text-sm font-semibold text-gray-900 dark:text-gray-100">{{ t('admin.upstreamManagement.events.trendTitle') }}</h3>
            <p class="mt-1 text-xs leading-5 text-gray-500 dark:text-dark-400">{{ t('admin.upstreamManagement.events.trendDescription') }}</p>
          </div>
          <div class="flex flex-wrap items-center gap-2">
            <div class="inline-flex rounded-lg bg-gray-100 p-1 dark:bg-dark-800" :aria-label="t('admin.upstreamManagement.events.range')">
              <button
                v-for="value in ranges"
                :key="value"
                type="button"
                :data-test="`health-range-${value}`"
                :class="segmentClass(range === value)"
                @click="range = value"
              >
                {{ value }}
              </button>
            </div>
            <div class="inline-flex rounded-lg bg-gray-100 p-1 dark:bg-dark-800" :aria-label="t('admin.upstreamManagement.events.metric')">
              <button
                v-for="value in metrics"
                :key="value"
                type="button"
                :data-test="`health-metric-${value}`"
                :class="segmentClass(metric === value)"
                @click="metric = value"
              >
                {{ t(`admin.upstreamManagement.events.metrics.${value}`) }}
              </button>
            </div>
          </div>
        </div>

        <div v-if="trendError" class="mb-4 flex items-center justify-between gap-3 rounded-lg bg-red-50 px-3 py-2 text-sm text-red-700 dark:bg-red-900/20 dark:text-red-300">
          <span>{{ trendError }}</span>
          <button type="button" class="font-medium underline underline-offset-2" @click="loadTrend">{{ t('common.retry') }}</button>
        </div>
        <UpstreamHealthTrendChart :trend="trend" :metric="metric" :loading="trendLoading" />
      </section>

      <section class="grid gap-5 xl:grid-cols-2">
        <div>
          <h3 class="mb-3 text-sm font-semibold text-gray-800 dark:text-gray-100">{{ t('admin.upstreamManagement.health.recentObservations') }}</h3>
          <div v-if="detailsLoading" class="rounded-lg border border-gray-200 py-8 text-center text-sm text-gray-500 dark:border-dark-700 dark:text-dark-400">
            {{ t('admin.upstreamManagement.events.loading') }}
          </div>
          <div v-else-if="detailsError" class="rounded-lg bg-red-50 px-3 py-2 text-sm text-red-700 dark:bg-red-900/20 dark:text-red-300">{{ detailsError }}</div>
          <div v-else-if="recentHistory.length" class="space-y-2">
            <article
              v-for="(item, index) in recentHistory"
              :key="`${item.observed_at}-${index}`"
              class="rounded-xl border border-gray-200 bg-gray-50/60 p-3 dark:border-dark-700 dark:bg-dark-800/50"
            >
              <div class="flex items-center justify-between gap-3">
                <span :class="['rounded px-2 py-0.5 text-xs font-medium', stateClass(item.state)]">{{ stateLabel(item.state) }}</span>
                <time class="text-xs text-gray-500 dark:text-dark-400">{{ formatDateTime(item.observed_at) }}</time>
              </div>
              <div class="mt-2 text-xs text-gray-600 dark:text-dark-300">
                {{ sourceLabel(item.source) }} · {{ item.model || '-' }} · {{ item.result || '-' }}
              </div>
              <div class="mt-1 flex flex-wrap gap-x-3 gap-y-1 text-xs text-gray-500 dark:text-dark-400">
                <span v-if="item.ttft_ms != null">TTFT {{ formatMilliseconds(item.ttft_ms) }}</span>
                <span v-if="item.duration_ms != null">{{ t('admin.upstreamManagement.health.duration') }} {{ formatMilliseconds(item.duration_ms) }}</span>
                <span>{{ reasonLabel(item.reason) }}</span>
              </div>
            </article>
          </div>
          <div v-else class="rounded-lg border border-dashed border-gray-200 py-6 text-center text-sm text-gray-500 dark:border-dark-700 dark:text-dark-400">
            {{ t('admin.upstreamManagement.health.noHistory') }}
          </div>
        </div>

        <div>
          <h3 class="mb-3 text-sm font-semibold text-gray-800 dark:text-gray-100">{{ t('admin.upstreamManagement.events.stateChanges') }}</h3>
          <div v-if="detailsLoading" class="rounded-lg border border-gray-200 py-8 text-center text-sm text-gray-500 dark:border-dark-700 dark:text-dark-400">
            {{ t('admin.upstreamManagement.events.loading') }}
          </div>
          <div v-else-if="events.length === 0" class="rounded-lg border border-dashed border-gray-200 py-6 text-center text-sm text-gray-500 dark:border-dark-700 dark:text-dark-400">{{ t('admin.upstreamManagement.events.empty') }}</div>
          <div v-else class="space-y-2">
            <article v-for="event in events" :key="event.id" class="rounded-xl border border-gray-200 p-3 dark:border-dark-700">
              <div class="flex items-center justify-between gap-3">
                <span :class="['rounded px-2 py-0.5 text-xs font-medium', severityClass(event.severity)]">{{ event.severity }}</span>
                <time class="text-xs text-gray-500 dark:text-dark-400">{{ formatDateTime(event.created_at) }}</time>
              </div>
              <div class="mt-2 text-sm font-medium text-gray-800 dark:text-gray-100">{{ event.message }}</div>
              <pre v-if="Object.keys(event.payload || {}).length" class="mt-2 overflow-x-auto whitespace-pre-wrap break-words rounded bg-gray-50 p-2 text-xs text-gray-600 dark:bg-dark-800 dark:text-dark-300">{{ JSON.stringify(event.payload, null, 2) }}</pre>
            </article>
          </div>
        </div>
      </section>
    </div>
    <template #footer>
      <div class="flex items-center justify-end">
        <button class="btn btn-secondary" @click="$emit('close')">{{ t('common.close') }}</button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import UpstreamHealthTrendChart from './UpstreamHealthTrendChart.vue'
import { formatDateTime } from '@/utils/format'
import upstreamManagementAPI, {
  type UpstreamHealthTrend,
  type UpstreamHealthTrendRange,
  type UpstreamKeyEvent
} from '@/api/admin/upstreamManagement'
import type { UpstreamHealthObservation } from '@/types'
import { extractApiErrorMessage } from '@/utils/apiError'

type HealthMetric = 'health' | 'ttft'

const props = defineProps<{ show: boolean; keyId: number | null; keyLabel?: string }>()
defineEmits<{ (event: 'close'): void }>()
const { t, te } = useI18n()
const events = ref<UpstreamKeyEvent[]>([])
const history = ref<UpstreamHealthObservation[]>([])
const trend = ref<UpstreamHealthTrend | null>(null)
const detailsLoading = ref(false)
const trendLoading = ref(false)
const detailsError = ref('')
const trendError = ref('')
const range = ref<UpstreamHealthTrendRange>('6h')
const metric = ref<HealthMetric>('health')
const ranges: UpstreamHealthTrendRange[] = ['6h', '24h', '7d', '30d']
const metrics: HealthMetric[] = ['health', 'ttft']
let trendRequestID = 0

const title = computed(() => props.keyLabel ? `${t('admin.upstreamManagement.events.title')} · ${props.keyLabel}` : t('admin.upstreamManagement.events.title'))
const recentHistory = computed(() => [...history.value].slice(-8).reverse())

async function loadDetails() {
  if (!props.show || !props.keyId) return
  detailsLoading.value = true
  detailsError.value = ''
  try {
    const result = await upstreamManagementAPI.getKeyEvents(props.keyId)
    events.value = result.items
    history.value = result.health_history || []
  } catch (loadError) {
    events.value = []
    history.value = []
    detailsError.value = extractApiErrorMessage(loadError, t('admin.upstreamManagement.events.loadFailed'))
  } finally {
    detailsLoading.value = false
  }
}

async function loadTrend() {
  if (!props.show || !props.keyId) return
  const requestID = ++trendRequestID
  trendLoading.value = true
  trendError.value = ''
  try {
    const result = await upstreamManagementAPI.getKeyHealthTrend(props.keyId, range.value)
    if (requestID === trendRequestID) trend.value = result
  } catch (loadError) {
    if (requestID !== trendRequestID) return
    trend.value = null
    trendError.value = extractApiErrorMessage(loadError, t('admin.upstreamManagement.events.trendLoadFailed'))
  } finally {
    if (requestID === trendRequestID) trendLoading.value = false
  }
}

watch(() => [props.show, props.keyId], ([visible]) => {
  if (!visible) return
  metric.value = 'health'
  if (range.value !== '6h') {
    range.value = '6h'
    void loadDetails()
    return
  }
  void Promise.all([loadDetails(), loadTrend()])
}, { immediate: true })

watch(range, () => {
  if (props.show) void loadTrend()
})

const segmentClass = (active: boolean) => [
  'rounded-md px-3 py-1.5 text-xs font-medium transition-colors focus:outline-none focus-visible:ring-2 focus-visible:ring-primary-500/40',
  active
    ? 'bg-white text-primary-700 shadow-sm dark:bg-dark-700 dark:text-primary-300'
    : 'text-gray-500 hover:text-gray-900 dark:text-dark-400 dark:hover:text-gray-100'
]
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
const formatMilliseconds = (value: number) => value >= 1000
  ? `${(value / 1000).toLocaleString(undefined, { maximumFractionDigits: 2 })} s`
  : `${value.toLocaleString()} ms`
const stateClass = (state: string) => {
  if (state === 'healthy') return 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300'
  if (state === 'degraded') return 'bg-amber-100 text-amber-700 dark:bg-amber-900/30 dark:text-amber-300'
  if (state === 'suspended') return 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-300'
  if (state === 'observing') return 'bg-blue-100 text-blue-700 dark:bg-blue-900/30 dark:text-blue-300'
  if (state === 'recovering') return 'bg-cyan-100 text-cyan-700 dark:bg-cyan-900/30 dark:text-cyan-300'
  return 'bg-gray-100 text-gray-700 dark:bg-dark-700 dark:text-dark-300'
}
const severityClass = (severity: string) => {
  if (severity === 'error') return 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-300'
  if (severity === 'warning') return 'bg-amber-100 text-amber-700 dark:bg-amber-900/30 dark:text-amber-300'
  return 'bg-blue-100 text-blue-700 dark:bg-blue-900/30 dark:text-blue-300'
}
</script>
