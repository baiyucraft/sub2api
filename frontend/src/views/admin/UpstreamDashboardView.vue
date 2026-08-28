<template>
  <AppLayout>
    <div class="upstream-dashboard space-y-4 pb-12">
      <section class="control-deck" aria-label="Dashboard filters">
        <div v-if="!loading || items.length" class="summary-row" aria-label="Dashboard summary">
          <div class="summary-item"><span class="summary-icon summary-icon-teal"><Icon name="server" size="sm" /></span><span><small>{{ t('admin.upstreamDashboard.summary.configurations') }}</small><strong>{{ formatNumber(items.length) }}</strong></span></div>
          <div class="summary-item"><span class="summary-icon summary-icon-blue"><Icon name="chart" size="sm" /></span><span><small>{{ t('admin.upstreamDashboard.summary.withTraffic') }}</small><strong>{{ formatNumber(summary.trafficConfigs) }}</strong></span></div>
          <div class="summary-item"><span class="summary-icon summary-icon-amber"><Icon name="exclamationTriangle" size="sm" /></span><span><small>{{ t('admin.upstreamDashboard.summary.needAttention') }}</small><strong>{{ formatNumber(summary.attentionConfigs) }}</strong></span></div>
          <div class="summary-item"><span class="summary-icon summary-icon-slate"><Icon name="users" size="sm" /></span><span><small>{{ t('admin.upstreamDashboard.summary.schedulableAccounts') }}</small><strong>{{ formatNumber(summary.schedulableAccounts) }}</strong></span></div>
          <div class="summary-item"><span class="summary-icon summary-icon-violet"><Icon name="bell" size="sm" /></span><span><small>{{ t('admin.upstreamDashboard.summary.openIncidents') }}</small><strong>{{ formatNumber(summary.openIncidents) }}</strong></span></div>
          <div class="summary-item" :class="summary.balanceLowConfigs > 0 ? 'summary-item-alert' : ''"><span class="summary-icon summary-icon-rose"><Icon name="dollar" size="sm" /></span><span><small>{{ t('admin.upstreamDashboard.summary.balanceLow') }}</small><strong>{{ formatNumber(summary.balanceLowConfigs) }}</strong></span></div>
        </div>
        <div class="filter-row">
          <div class="filter-control filter-window-control"><span>{{ t('admin.upstreamDashboard.filters.window') }}</span><div class="window-tabs" role="tablist" :aria-label="t('admin.upstreamDashboard.filters.window')"><button v-for="item in windows" :key="item" type="button" class="window-tab" :class="{ 'window-tab-active': rangeWindow === item }" role="tab" :aria-selected="rangeWindow === item" @click="selectWindow(item)">{{ t(`admin.upstreamDashboard.windows.${item}`) }}</button></div></div>
          <label class="filter-control"><span>{{ t('admin.upstreamDashboard.filters.status') }}</span><Select v-model="status" :options="statusOptions" :aria-label="t('admin.upstreamDashboard.filters.status')" @change="load" /></label>
          <div class="filter-control sort-control"><span>{{ t('admin.upstreamDashboard.filters.sort') }}</span><button type="button" class="sort-toggle" :aria-label="sortDirection === 'asc' ? t('admin.upstreamDashboard.filters.sortDesc') : t('admin.upstreamDashboard.filters.sortAsc')" :title="sortDirection === 'asc' ? t('admin.upstreamDashboard.filters.sortDesc') : t('admin.upstreamDashboard.filters.sortAsc')" @click="toggleSortDirection"><Icon :name="sortDirection === 'asc' ? 'arrowUp' : 'arrowDown'" size="sm" /></button></div>
          <label class="filter-control filter-search"><span>{{ t('admin.upstreamDashboard.filters.searchLabel') }}</span><input v-model="search" class="input" :placeholder="t('common.search')" @keyup.enter="load" /></label>
          <div class="filter-actions"><small v-if="lastLoadedAt" class="last-updated">{{ t('admin.upstreamDashboard.lastUpdated', { time: formatClock(lastLoadedAt) }) }}</small><button class="refresh-button" :disabled="loading" :aria-label="t('common.refresh')" @click="load"><Icon name="refresh" size="sm" :class="loading ? 'animate-spin' : ''" /></button></div>
        </div>
      </section>

      <div v-if="error" class="rounded-xl border border-red-200 bg-red-50 p-4 text-sm text-red-700 dark:border-red-900/60 dark:bg-red-900/20 dark:text-red-300">{{ error }}</div>
      <div v-if="loading && !items.length" class="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
        <div v-for="n in 6" :key="n" class="dashboard-skeleton h-64 animate-pulse" />
      </div>
      <div v-else-if="!items.length" class="empty-state"><span class="empty-state-icon"><Icon name="inbox" size="lg" /></span><strong>{{ t('admin.upstreamDashboard.empty') }}</strong><span>{{ t('admin.upstreamDashboard.emptyHint') }}</span></div>
      <div v-else class="dashboard-grid">
        <article
          v-for="item in displayItems"
          :key="item.id"
          class="dashboard-card group cursor-pointer"
          :class="statusCardClass(item.overall_status)"
          tabindex="0"
          role="button"
          :aria-label="t('admin.upstreamDashboard.openDetail', { name: item.name })"
          @click="openDetail(item)"
          @keydown.enter="openDetail(item)"
          @keydown.space.prevent="openDetail(item)"
        >
          <div class="dashboard-card-head">
            <div class="min-w-0"><div class="card-kicker"><span class="provider-label"><span class="provider-mark" aria-hidden="true">{{ providerInitial(item.provider) }}</span>{{ item.provider }}</span><span class="provider-dot" /><span>{{ t('admin.upstreamDashboard.windowLabel', { window: t(`admin.upstreamDashboard.windows.${rangeWindow}`) }) }}</span></div><h2 :title="item.name">{{ item.name }}</h2><p :title="item.site_url">{{ item.site_url }}</p></div>
            <span class="status-badge" :class="statusClass(item.overall_status)"><span class="status-dot" />{{ t(`admin.upstreamDashboard.status.${item.overall_status}`) }}</span>
          </div>
          <div class="card-primary-metrics">
            <div class="success-metric"><span>{{ t('admin.upstreamDashboard.metrics.successRate') }}</span><strong>{{ formatRate(item.success_rate) }}</strong><small>{{ t('admin.upstreamDashboard.metrics.requests') }} {{ formatNumber(item.requests) }}</small></div>
            <div class="latency-metrics"><Metric :label="t('admin.upstreamDashboard.metrics.ttft')" :value="formatMs(item.p50_ttft_ms)" /><Metric :label="t('admin.upstreamDashboard.metrics.latency')" :value="formatMs(item.p50_latency_ms)" /></div>
          </div>
          <div class="card-error-row">
            <span><i class="error-dot error-dot-red" />429 <b>{{ item.error_429 }}</b></span>
            <span><i class="error-dot error-dot-orange" />5xx <b>{{ item.error_5xx }}</b></span>
            <span><i class="error-dot error-dot-slate" />{{ t('admin.upstreamDashboard.metrics.timeouts') }} <b>{{ item.timeouts }}</b></span>
            <span><i class="error-dot error-dot-red" />{{ t('admin.upstreamDashboard.metrics.authErrors') }} <b>{{ item.auth_config_errors }}</b></span>
          </div>
          <div class="card-signal-row">
            <span><span class="signal-marker" :class="item.probe?.latest_state ? 'signal-marker-live' : 'signal-marker-muted'" />{{ t('admin.upstreamDashboard.sections.probe') }}</span>
            <span class="signal-value"><strong>{{ item.probe?.samples ? `${formatNumber(item.probe.samples)} · ${stateLabel(item.probe.latest_state)}` : '-' }}</strong><small v-if="item.probe?.latest_observed_at">{{ t('admin.upstreamDashboard.lastProbe', { time: formatObservedAt(item.probe.latest_observed_at) }) }}</small></span>
          </div>
          <div class="card-ops-row">
            <span class="ops-value" :class="item.balance_low ? 'balance-low-value' : ''"><Icon name="dollar" size="xs" />{{ t('admin.upstreamDashboard.metrics.balance') }} <strong>{{ item.balance_available && item.balance_cny != null ? formatCny(item.balance_cny) : '-' }}</strong><small v-if="item.balance_observed_at">{{ formatObservedAt(item.balance_observed_at) }}</small></span>
            <span v-if="item.balance_low" class="balance-alert" role="status"><Icon name="exclamationTriangle" size="xs" />{{ t('admin.upstreamDashboard.metrics.balanceLow') }}</span>
            <span v-else-if="item.balance_available === false" class="balance-unavailable" :title="item.balance_unavailable_reason || ''">{{ t('admin.upstreamDashboard.metrics.balanceUnavailable') }}</span>
            <span v-if="item.open_incident_count > 0" class="incident-count"><Icon name="bell" size="xs" />{{ formatNumber(item.open_incident_count) }}</span>
            <span v-if="item.last_rate_change_at" class="rate-change" :title="t('admin.upstreamDashboard.metrics.lastRateChange')">{{ t('admin.upstreamDashboard.metrics.rateChangedAt', { time: formatObservedAt(item.last_rate_change_at) }) }}</span>
          </div>
          <div v-if="item.trend?.length" class="card-trend">
            <div class="trend-heading"><span>{{ t('admin.upstreamDashboard.sections.trend') }}</span><small>{{ formatNumber(item.trend[item.trend.length - 1]?.requests || 0) }} {{ t('admin.upstreamDashboard.metrics.requests') }}</small></div>
            <TrendBars :points="item.trend" />
          </div>
          <div class="card-footer">
            <div class="footer-metric"><span>{{ t('admin.upstreamDashboard.metrics.accounts') }}</span><strong>{{ item.schedulable_account_count }}/{{ item.account_count }}</strong></div>
            <div class="footer-metric"><span>{{ t('admin.upstreamDashboard.metrics.estimatedProfit') }}</span><strong :class="item.estimated_gross_profit == null ? 'muted-value' : 'profit-value'">{{ item.estimated_gross_profit == null ? '-' : item.estimated_gross_profit.toFixed(4) }}<small v-if="item.estimated_gross_profit != null && item.estimated_gross_profit_rate != null" class="profit-rate">({{ formatRate(item.estimated_gross_profit_rate) }})</small></strong></div>
            <span class="card-link" :aria-label="t('common.viewDetails')"><span aria-hidden="true">→</span></span>
          </div>
        </article>
      </div>
    </div>

    <transition name="slide">
      <div v-if="selected" class="fixed inset-0 z-50 flex justify-end" role="dialog" aria-modal="true" :aria-labelledby="detailTitleID" @click.self="closeDetail">
        <div class="absolute inset-0 bg-black/30" @click="closeDetail" />
        <aside ref="drawerRef" class="detail-drawer relative h-full w-full max-w-2xl overflow-y-auto bg-gray-50 shadow-2xl dark:bg-dark-950">
          <div class="detail-header"><div class="min-w-0"><p class="provider-label"><span class="provider-mark" aria-hidden="true">{{ providerInitial(selected.provider) }}</span>{{ selected.provider }}</p><h2 :id="detailTitleID" class="mt-2 truncate text-xl font-semibold tracking-tight text-gray-950 dark:text-white">{{ selected.name }}</h2><p class="mt-1 truncate text-xs text-gray-500 dark:text-dark-400" :title="selected.site_url">{{ selected.site_url }}</p></div><div class="detail-header-side"><span class="status-badge" :class="statusClass(selected.overall_status)"><span class="status-dot" />{{ t(`admin.upstreamDashboard.status.${selected.overall_status}`) }}</span><button ref="closeButtonRef" class="close-button" :aria-label="t('common.close')" @click="closeDetail">×</button></div></div>
          <div v-if="detailLoading" class="mt-6 text-sm text-gray-500">{{ t('common.loading') }}</div>
          <div v-else-if="detailError" class="mt-6 rounded-xl border border-red-200 bg-red-50 p-4 text-sm text-red-700 dark:border-red-900/60 dark:bg-red-900/20 dark:text-red-300">{{ detailError }}</div>
          <template v-else-if="detail">
            <div class="detail-body">
              <div class="detail-kpis"><Metric :label="t('admin.upstreamDashboard.metrics.requests')" :value="formatNumber(detail.requests)" /><Metric :label="t('admin.upstreamDashboard.metrics.failed')" :value="formatNumber(detail.failed_requests)" /><Metric :label="t('admin.upstreamDashboard.metrics.p95ttft')" :value="formatMs(detail.p95_ttft_ms)" /><Metric :label="t('admin.upstreamDashboard.metrics.p95latency')" :value="formatMs(detail.p95_latency_ms)" /></div>
              <section class="detail-section trend-section"><div class="section-heading"><span class="section-number">00</span><h3>{{ t('admin.upstreamDashboard.sections.trend') }}</h3></div><TrendBars v-if="detail.trend?.length" :points="detail.trend" compact /><p v-else class="detail-empty trend-empty">{{ t('admin.upstreamDashboard.noTrendData') }}</p><p class="detail-meta">{{ t('admin.upstreamDashboard.metrics.requests') }} {{ formatNumber(detail.requests) }} <span>·</span> {{ t('admin.upstreamDashboard.metrics.failed') }} {{ formatNumber(detail.failed_requests) }}</p></section>
              <section class="detail-section"><div class="section-heading"><span class="section-number">01</span><h3>{{ t('admin.upstreamDashboard.sections.traffic') }}</h3></div><div class="mt-4 grid grid-cols-3 gap-2"><Metric :label="'429'" :value="formatNumber(detail.error_429)" /><Metric :label="'5xx'" :value="formatNumber(detail.error_5xx)" /><Metric :label="t('admin.upstreamDashboard.metrics.timeouts')" :value="formatNumber(detail.timeouts)" /></div><ul v-if="detail.traffic?.models?.length" class="model-list"><li v-for="model in detail.traffic.models" :key="model.model"><span class="truncate">{{ model.model }}</span><span>{{ formatNumber(model.requests) }}</span></li></ul><p v-else class="detail-empty">{{ t('admin.upstreamDashboard.noTrafficData') }}</p></section>
              <section class="detail-section detail-section-probe"><div class="section-heading"><span class="section-number">02</span><h3>{{ t('admin.upstreamDashboard.sections.probe') }}</h3></div><p class="detail-callout">{{ stateLabel(detail.probe.latest_state) }}<span>·</span>{{ stateLabel(detail.probe.latest_reason) || t('admin.upstreamDashboard.noReason') }}</p><p class="detail-meta">{{ t('admin.upstreamDashboard.metrics.probeSamples') }} {{ detail.probe.samples }} <span>·</span> TTFT {{ formatMs(detail.probe.average_ttft_ms) }} <span>·</span> {{ formatMs(detail.probe.average_duration_ms) }}</p><p v-if="detail.probe.latest_observed_at" class="detail-meta">{{ t('admin.upstreamDashboard.lastProbe', { time: formatObservedAt(detail.probe.latest_observed_at) }) }}</p><p v-if="detail.probe.confidence_status" class="detail-meta">{{ t('admin.upstreamDashboard.metrics.confidence') }}: {{ stateLabel(detail.probe.confidence_status) }}</p></section>
              <section class="detail-section detail-section-ops"><div class="section-heading"><span class="section-number">03</span><h3>{{ t('admin.upstreamDashboard.sections.operations') }}</h3></div><div class="ops-detail-grid"><Metric :label="t('admin.upstreamDashboard.metrics.balance')" :value="detail.balance_available && detail.balance_cny != null ? formatCny(detail.balance_cny) : '-'" /><Metric :label="t('admin.upstreamDashboard.metrics.openIncidents')" :value="formatNumber(detail.open_incident_count)" /></div><p v-if="detail.balance_low" class="detail-callout balance-alert"><Icon name="exclamationTriangle" size="xs" />{{ t('admin.upstreamDashboard.metrics.balanceLow') }}<span v-if="detail.balance_threshold_cny != null">· {{ t('admin.upstreamDashboard.metrics.balanceThreshold', { amount: formatCny(detail.balance_threshold_cny) }) }}</span></p><p v-else-if="detail.balance_available === false" class="detail-meta">{{ detail.balance_unavailable_reason ? stateLabel(detail.balance_unavailable_reason) : t('admin.upstreamDashboard.metrics.balanceUnavailable') }}</p><p v-if="detail.balance_observed_at" class="detail-meta">{{ t('admin.upstreamDashboard.metrics.balanceUpdated', { time: formatObservedAt(detail.balance_observed_at) }) }}</p><p v-if="detail.last_rate_change_at" class="detail-meta">{{ t('admin.upstreamDashboard.metrics.lastRateChangeAt', { time: formatObservedAt(detail.last_rate_change_at) }) }}</p><ul v-if="detail.recent_incidents?.length" class="model-list"><li v-for="incident in detail.recent_incidents.slice(0, 3)" :key="incident.id"><span><b>{{ stateLabel(incident.type) }}</b><small>{{ incident.title }}</small></span><span>{{ stateLabel(incident.status) }}</span></li></ul><p v-else class="detail-empty">{{ t('admin.upstreamDashboard.noIncidentData') }}</p><ul v-if="detail.recent_rate_changes?.length" class="model-list"><li v-for="change in detail.recent_rate_changes.slice(0, 3)" :key="`${change.occurred_at}-${change.type}`"><span>{{ stateLabel(change.type) }}</span><span>{{ formatMultiplier(change.old_rate) }} → {{ formatMultiplier(change.new_rate) }}</span></li></ul><p v-else class="detail-empty">{{ t('admin.upstreamDashboard.noRateChangeData') }}</p></section>
              <div class="grid gap-4 sm:grid-cols-2"><section class="detail-section"><div class="section-heading"><span class="section-number">03</span><h3>{{ t('admin.upstreamDashboard.sections.accounts') }}</h3></div><p class="detail-big-value">{{ formatNumber(detail.schedulable_account_count) }}<small>/{{ formatNumber(detail.account_count) }}</small></p><p class="detail-meta">{{ t('admin.upstreamDashboard.metrics.tempUnschedulable') }} {{ formatNumber(detail.temp_unschedulable_count) }}</p></section><section class="detail-section"><div class="section-heading"><span class="section-number">04</span><h3>{{ t('admin.upstreamDashboard.sections.profit') }}</h3></div><p class="detail-big-value">{{ detail.profit_unavailable || detail.estimated_gross_profit == null ? '-' : detail.estimated_gross_profit.toFixed(4) }}</p><p class="detail-meta">{{ detail.profit_unavailable || detail.estimated_gross_profit == null ? t('admin.upstreamDashboard.estimatedUnavailable') : formatRate(detail.estimated_gross_profit_rate) }}</p></section></div>
              <section v-if="detail.recent_errors?.length" class="detail-section"><div class="section-heading"><span class="section-number">05</span><h3>{{ t('admin.upstreamDashboard.sections.errors') }}</h3></div><ul class="error-list"><li v-for="item in detail.recent_errors" :key="`${item.occurred_at}-${item.status_code}`"><span><b>{{ item.status_code }}</b><span class="ml-2">{{ item.model }}</span><small>{{ stateLabel(item.category) }}</small></span><time>{{ item.occurred_at }}</time></li></ul></section>
              <div class="detail-actions"><button class="btn btn-primary" @click="router.push({ path: '/admin/upstream/channels', query: { upstream_config_id: String(detail.id) } })">{{ t('admin.upstreamDashboard.actions.channels') }}</button><button class="btn btn-secondary" @click="router.push({ path: '/admin/upstream/accounts', query: { upstream_config_id: String(detail.id) } })">{{ t('admin.upstreamDashboard.actions.accounts') }}</button><button class="btn btn-secondary" @click="router.push({ path: '/admin/usage', query: { upstream_config_id: String(detail.id) } })">{{ t('admin.upstreamDashboard.actions.usage') }}</button></div>
            </div>
          </template>
        </aside>
      </div>
    </transition>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, defineComponent, h, nextTick, onMounted, onUnmounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import AppLayout from '@/components/layout/AppLayout.vue'
import Icon from '@/components/icons/Icon.vue'
import Select from '@/components/common/Select.vue'
import { getDashboard, getDashboardDetail, type UpstreamDashboardCard, type UpstreamDashboardDetail, type UpstreamDashboardTrendPoint, type UpstreamDashboardWindow } from '@/api/admin/upstreamConfigs'

const Metric = defineComponent({ props: { label: { type: String, required: true }, value: { type: String, required: true } }, setup: (props) => () => h('div', { class: 'min-w-0' }, [h('div', { class: 'truncate text-[11px] uppercase tracking-wide text-gray-400 dark:text-dark-500' }, props.label), h('div', { class: 'mt-1 text-lg font-semibold text-gray-900 dark:text-white' }, props.value)]) })
const TrendBars = defineComponent({
  props: { points: { type: Array, required: true }, compact: { type: Boolean, default: false } },
  setup: (props) => () => {
    const points = (props.points as UpstreamDashboardTrendPoint[]).slice(-12)
    const maxRequests = Math.max(1, ...points.map(point => Number.isFinite(point.requests) ? point.requests : 0))
    return h('div', { class: props.compact ? 'trend-bars trend-bars-compact' : 'trend-bars', role: 'img', 'aria-label': 'Traffic trend' }, points.map(point => {
      const requests = Number.isFinite(point.requests) ? point.requests : 0
      const height = requests > 0 ? Math.max(14, Math.round((requests / maxRequests) * 100)) : 8
      return h('span', { class: ['trend-bar', point.errors > 0 ? 'trend-bar-error' : ''], style: { height: `${height}%` }, title: `${point.bucket}: ${requests}` })
    }))
  }
})
const { t } = useI18n()
const router = useRouter()
const items = ref<UpstreamDashboardCard[]>([])
const selected = ref<UpstreamDashboardCard | null>(null)
const detail = ref<UpstreamDashboardDetail | null>(null)
const detailLoading = ref(false)
const detailError = ref('')
const loading = ref(false)
const error = ref('')
const search = ref('')
const status = ref('')
const rangeWindow = ref<UpstreamDashboardWindow>('24h')
const windows: UpstreamDashboardWindow[] = ['1h','24h','7d','15d','30d']
const statuses = ['operational', 'degraded', 'critical', 'disabled', 'data_insufficient']
const statusOptions = computed(() => [
  { value: '', label: t('admin.upstreamDashboard.filters.allStatuses') },
  ...statuses.map(value => ({ value, label: t(`admin.upstreamDashboard.status.${value}`) }))
])
type SortDirection = 'asc' | 'desc'
const sortDirection = ref<SortDirection>('asc')
const statusPriority: Record<string, number> = { critical: 0, degraded: 1, data_insufficient: 2, operational: 3, disabled: 4 }
const displayItems = computed(() => [...items.value].sort((a, b) => {
  const aBasePriority = statusPriority[a.overall_status]
  const bBasePriority = statusPriority[b.overall_status]
  const aPinned = aBasePriority == null || a.overall_status === 'disabled'
  const bPinned = bBasePriority == null || b.overall_status === 'disabled'
  if (aPinned !== bPinned) return aPinned ? 1 : -1
  if (aPinned && bPinned) return a.name.localeCompare(b.name)

  const priorityDelta = (sortDirection.value === 'asc' ? aBasePriority : 3 - aBasePriority) - (sortDirection.value === 'asc' ? bBasePriority : 3 - bBasePriority)
  if (priorityDelta) return priorityDelta
  const aRate = typeof a.success_rate === 'number' && Number.isFinite(a.success_rate) ? a.success_rate : 0
  const bRate = typeof b.success_rate === 'number' && Number.isFinite(b.success_rate) ? b.success_rate : 0
  if (aRate !== bRate) return sortDirection.value === 'asc' ? aRate - bRate : bRate - aRate
  return a.name.localeCompare(b.name)
}))
const summary = computed(() => ({
  trafficConfigs: items.value.filter(item => item.requests > 0).length,
  attentionConfigs: items.value.filter(item => ['degraded', 'critical'].includes(item.overall_status)).length,
  schedulableAccounts: items.value.reduce((sum, item) => sum + (Number.isFinite(item.schedulable_account_count) ? item.schedulable_account_count : 0), 0),
  openIncidents: items.value.reduce((sum, item) => sum + (Number.isFinite(item.open_incident_count) ? item.open_incident_count : 0), 0),
  balanceLowConfigs: items.value.reduce((sum, item) => sum + (item.balance_low === true ? 1 : 0), 0)
}))
const lastLoadedAt = ref<Date | null>(null)
const drawerRef = ref<HTMLElement | null>(null)
const closeButtonRef = ref<HTMLButtonElement | null>(null)
const detailTitleID = 'upstream-dashboard-detail-title'
let lastFocusedElement: HTMLElement | null = null
let timer: number | undefined
let loadSeq = 0
let detailSeq = 0
const statusClass = (status: string) => ({ operational: 'bg-emerald-50 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300', degraded: 'bg-amber-50 text-amber-700 dark:bg-amber-900/30 dark:text-amber-300', critical: 'bg-red-50 text-red-700 dark:bg-red-900/30 dark:text-red-300', disabled: 'bg-gray-100 text-gray-600 dark:bg-dark-800 dark:text-dark-300', data_insufficient: 'bg-slate-100 text-slate-600 dark:bg-dark-800 dark:text-dark-300' }[status] || 'bg-gray-100 text-gray-600')
const statusCardClass = (status: string) => `status-${status || 'unknown'}`
const providerInitial = (value: string | null | undefined) => (value || '?').trim().charAt(0).toUpperCase()
const formatNumber = (value: number | null | undefined) => value == null || !Number.isFinite(value) ? '-' : new Intl.NumberFormat().format(value)
const formatMs = (value: number | null | undefined) => value == null || value <= 0 ? '-' : `${Math.round(value)} ms`
const formatRate = (value: number | null | undefined) => value == null || !Number.isFinite(value) ? '-' : `${(value * 100).toFixed(1)}%`
const formatCny = (value: number | null | undefined) => value == null || !Number.isFinite(value) || value < 0 ? '-' : `¥${value.toFixed(2)}`
const formatMultiplier = (value: number | null | undefined) => value == null || !Number.isFinite(value) || value < 0 ? '-' : value.toFixed(4)
const formatClock = (value: Date) => new Intl.DateTimeFormat(undefined, { hour: '2-digit', minute: '2-digit' }).format(value)
const formatObservedAt = (value?: string | null) => {
  if (!value) return '-'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return '-'
  return new Intl.DateTimeFormat(undefined, { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' }).format(date)
}
function stateLabel(value?: string | null) {
  if (!value) return '-'
  const statusKey = `admin.upstreamDashboard.status.${value}`
  if (t(statusKey) !== statusKey) return t(statusKey)
  const reasonKey = `admin.upstreamDashboard.reasons.${value}`
  if (t(reasonKey) !== reasonKey) return t(reasonKey)
  const confidenceKey = `admin.upstreamDashboard.confidence.${value}`
  if (t(confidenceKey) !== confidenceKey) return t(confidenceKey)
  const eventKey = `admin.upstreamDashboard.events.${value}`
  if (t(eventKey) !== eventKey) return t(eventKey)
  return value.split('_').join(' ')
}
async function load() { const seq = ++loadSeq; loading.value = true; error.value = ''; try { const data = await getDashboard({ window: rangeWindow.value, status: status.value || undefined, search: search.value || undefined }); if (seq !== loadSeq) return; items.value = data.items || []; lastLoadedAt.value = new Date(); if (selected.value && !items.value.some(item => item.id === selected.value?.id)) closeDetail() } catch (e: any) { if (seq === loadSeq) error.value = e?.message || t('common.loadFailed') } finally { if (seq === loadSeq) loading.value = false } }
function selectWindow(value: UpstreamDashboardWindow) {
  if (rangeWindow.value === value) return
  rangeWindow.value = value
  load()
}
function toggleSortDirection() {
  sortDirection.value = sortDirection.value === 'asc' ? 'desc' : 'asc'
}
async function openDetail(item: UpstreamDashboardCard) { lastFocusedElement = document.activeElement instanceof HTMLElement ? document.activeElement : null; selected.value = item; detail.value = null; detailError.value = ''; const seq = ++detailSeq; detailLoading.value = true; await nextTick(); closeButtonRef.value?.focus(); try { const value = await getDashboardDetail(item.id, rangeWindow.value); if (seq === detailSeq) detail.value = value } catch (e: any) { if (seq === detailSeq) detailError.value = e?.message || t('common.loadFailed') } finally { if (seq === detailSeq) detailLoading.value = false } }
function closeDetail() { selected.value = null; detail.value = null; detailError.value = ''; detailSeq += 1; lastFocusedElement?.focus(); lastFocusedElement = null }
function onKeydown(event: KeyboardEvent) { if (event.key === 'Escape' && selected.value) closeDetail() }
onMounted(() => { load(); timer = window.setInterval(load, 60000) }); onUnmounted(() => { if (timer) window.clearInterval(timer) })
onMounted(() => window.addEventListener('keydown', onKeydown)); onUnmounted(() => window.removeEventListener('keydown', onKeydown))
</script>

<style scoped>
.upstream-dashboard{color:rgb(17 24 39)}
.dashboard-header{display:flex;align-items:center;justify-content:space-between;gap:1.5rem;border-bottom:1px solid rgb(229 231 235);padding-bottom:1rem}
.filter-bar{display:grid;width:100%;box-sizing:border-box;grid-template-columns:auto minmax(0,1fr);align-items:end;gap:1.5rem;border:1px solid rgb(226 232 240);border-radius:.85rem;background:rgb(248 250 252);padding:.85rem 1rem;box-shadow:0 1px 2px rgb(15 23 42 / .03)}
.filter-heading{display:flex;align-items:center;gap:.6rem;white-space:nowrap;font-size:.78rem;font-weight:700;letter-spacing:.08em;text-transform:uppercase;color:rgb(71 85 105)}
.filter-heading-mark{display:block;width:.45rem;height:1.25rem;border-radius:999px;background:rgb(20 184 166)}
.filter-controls{display:grid;min-width:0;grid-template-columns:repeat(3,minmax(0,1fr)) minmax(12rem,1.35fr);gap:.65rem}
.filter-control{display:flex;min-width:0;flex-direction:column;gap:.3rem;font-size:.66rem;font-weight:600;letter-spacing:.04em;text-transform:uppercase;color:rgb(100 116 139)}
.filter-control .input{width:100%;min-height:2.3rem;font-size:.8rem}.filter-search{min-width:0}
.summary-strip{display:grid;grid-template-columns:repeat(4,minmax(0,1fr));gap:1px;overflow:hidden;border:1px solid rgb(226 232 240);border-radius:1rem;background:rgb(226 232 240)}
.summary-item{display:flex;align-items:center;gap:.75rem;background:white;padding:.9rem 1rem}.summary-item strong{display:block;font-size:1.15rem;line-height:1.2;color:rgb(15 23 42)}.summary-item small{display:block;margin-top:.2rem;font-size:.7rem;color:rgb(100 116 139)}
.summary-icon{display:grid;height:2.25rem;width:2.25rem;place-items:center;border-radius:.7rem}.summary-icon-teal{background:rgb(204 251 241);color:rgb(13 148 136)}.summary-icon-blue{background:rgb(219 234 254);color:rgb(37 99 235)}.summary-icon-amber{background:rgb(254 243 199);color:rgb(217 119 6)}.summary-icon-violet{background:rgb(237 233 254);color:rgb(124 58 237)}
.dashboard-card{position:relative;overflow:hidden;border:1px solid rgb(226 232 240);border-radius:1rem;background:white;padding:1.2rem;box-shadow:0 1px 2px rgb(15 23 42 / .03);transition:transform .2s ease,box-shadow .2s ease,border-color .2s ease}.dashboard-card::before{position:absolute;inset:0 auto 0 0;width:3px;background:rgb(148 163 184);content:''}.dashboard-card:hover{transform:translateY(-2px);border-color:rgb(153 246 228);box-shadow:0 12px 28px rgb(15 23 42 / .08)}.dashboard-card:focus{outline:none;box-shadow:0 0 0 3px rgb(20 184 166 / .2),0 12px 28px rgb(15 23 42 / .08)}
.dashboard-card.status-operational::before{background:rgb(16 185 129)}.dashboard-card.status-degraded::before{background:rgb(245 158 11)}.dashboard-card.status-critical::before{background:rgb(239 68 68)}.dashboard-card.status-disabled::before,.dashboard-card.status-data_insufficient::before{background:rgb(148 163 184)}
.dashboard-card-head{display:flex;align-items:flex-start;justify-content:space-between;gap:.75rem}.provider-label{display:inline-flex;align-items:center;gap:.35rem;font-size:.68rem;font-weight:700;letter-spacing:.12em;text-transform:uppercase;color:rgb(13 148 136)}.provider-mark{display:inline-grid;height:1.05rem;width:1.05rem;place-items:center;border-radius:4px;background:rgb(204 251 241);font-size:.58rem;font-weight:800;letter-spacing:0;color:rgb(13 148 136)}.provider-dot{height:.25rem;width:.25rem;border-radius:999px;background:rgb(148 163 184)}.status-badge{display:inline-flex;align-items:center;gap:.4rem;border-radius:999px;padding:.35rem .6rem;font-size:.68rem;font-weight:700;white-space:nowrap}.status-dot{height:.4rem;width:.4rem;border-radius:999px;background:currentColor}
.error-chip{border-radius:.45rem;padding:.32rem .5rem;font-size:.68rem;font-weight:600}.error-chip b{margin-left:.25rem}.error-chip-red{background:rgb(254 226 226);color:rgb(185 28 28)}.error-chip-orange{background:rgb(255 237 213);color:rgb(194 65 12)}.error-chip-gray{background:rgb(241 245 249);color:rgb(71 85 105)}
.profit-row{display:flex;align-items:center;justify-content:space-between;border-top:1px solid rgb(241 245 249);margin-top:1rem;padding-top:.8rem;font-size:.7rem;color:rgb(100 116 139)}.profit-row strong{font-size:.88rem}.card-footer{display:flex;align-items:center;justify-content:space-between;border-top:1px solid rgb(241 245 249);margin-top:.85rem;padding-top:.8rem;font-size:.7rem;color:rgb(100 116 139)}.card-footer>span:first-child{display:flex;align-items:center;gap:.35rem}.card-link{font-weight:700;color:rgb(100 116 139);transition:color .15s ease}.dashboard-card:hover .card-link{color:rgb(13 148 136)}
.dashboard-skeleton{border:1px solid rgb(226 232 240);border-radius:1rem;background:linear-gradient(90deg,rgb(241 245 249),rgb(248 250 252),rgb(241 245 249));background-size:200% 100%;animation:shimmer 1.5s infinite}.empty-state{display:flex;min-height:16rem;flex-direction:column;align-items:center;justify-content:center;gap:.45rem;border:1px dashed rgb(203 213 225);border-radius:1rem;color:rgb(100 116 139)}.empty-state strong{font-size:.95rem;color:rgb(51 65 85)}.empty-state span:last-child{font-size:.75rem}.empty-state-icon{display:grid;height:3rem;width:3rem;place-items:center;border-radius:1rem;background:rgb(241 245 249);color:rgb(100 116 139)}
.detail-drawer{border-left:1px solid rgb(226 232 240)}.detail-header{position:sticky;top:0;z-index:2;display:flex;align-items:flex-start;justify-content:space-between;gap:1rem;border-bottom:1px solid rgb(226 232 240);background:rgb(248 250 252 / .94);padding:1.5rem;backdrop-filter:blur(10px)}.close-button{display:grid;height:2.25rem;width:2.25rem;place-items:center;border:1px solid rgb(203 213 225);border-radius:.65rem;background:white;color:rgb(71 85 105);font-size:1.2rem;line-height:1;transition:background .15s ease,color .15s ease}.close-button:hover{background:rgb(15 23 42);color:white}.detail-body{padding:1.5rem}.detail-kpis{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:.65rem;border-bottom:1px solid rgb(226 232 240);padding-bottom:1.25rem}.detail-kpis>div{border-radius:.75rem;background:white;padding:.8rem}.detail-section{border:1px solid rgb(226 232 240);border-radius:.85rem;background:white;padding:1rem}.section-heading{display:flex;align-items:center;gap:.6rem}.section-heading h3{font-size:.8rem;font-weight:700;color:rgb(30 41 59)}.section-number{font-family:ui-monospace,SFMono-Regular,Menlo,monospace;font-size:.65rem;color:rgb(13 148 136)}.detail-section-probe{margin-top:1rem;background:rgb(240 253 250)}.detail-callout{margin-top:.75rem;font-size:.8rem;font-weight:600;color:rgb(30 41 59)}.detail-callout span,.detail-meta span{margin:0 .35rem;color:rgb(148 163 184)}.detail-meta{margin-top:.4rem;font-size:.7rem;color:rgb(100 116 139)}.detail-big-value{margin-top:.8rem;font-size:1.35rem;font-weight:700;color:rgb(15 23 42)}.detail-big-value small{font-size:.8rem;color:rgb(100 116 139)}.model-list,.error-list{margin-top:1rem;border-top:1px solid rgb(241 245 249);font-size:.72rem;color:rgb(71 85 105)}.model-list li{display:flex;justify-content:space-between;border-bottom:1px solid rgb(241 245 249);padding:.55rem 0}.detail-empty{margin-top:.8rem;font-size:.72rem;color:rgb(148 163 184)}.error-list li{display:flex;justify-content:space-between;gap:.75rem;border-bottom:1px solid rgb(241 245 249);padding:.65rem 0}.error-list li:last-child,.model-list li:last-child{border-bottom:0}.error-list b{color:rgb(220 38 38)}.error-list small{display:block;margin-top:.2rem;color:rgb(100 116 139)}.error-list time{white-space:nowrap;color:rgb(148 163 184)}.detail-actions{display:flex;flex-wrap:wrap;gap:.6rem;padding-top:1.25rem}
.slide-enter-active,.slide-leave-active{transition:opacity .2s ease}.slide-enter-active aside,.slide-leave-active aside{transition:transform .25s ease}.slide-enter-from,.slide-leave-to{opacity:0}.slide-enter-from aside,.slide-leave-to aside{transform:translateX(100%)}
@keyframes shimmer{0%{background-position:200% 0}100%{background-position:-200% 0}}
@media (max-width:900px){.filter-bar{grid-template-columns:1fr}.filter-controls{grid-template-columns:repeat(2,minmax(0,1fr))}}
@media (max-width:768px){.dashboard-header{align-items:stretch;flex-direction:column}.filter-controls{grid-template-columns:1fr}.summary-strip{grid-template-columns:repeat(2,minmax(0,1fr))}.detail-body{padding:1rem}}
:global(.dark) .upstream-dashboard{color:rgb(226 232 240)}:global(.dark) .upstream-dashboard .dashboard-header{border-color:rgb(51 65 85)}:global(.dark) .upstream-dashboard .filter-bar{border-color:rgb(51 65 85);background:rgb(15 23 42)}:global(.dark) .upstream-dashboard .filter-heading{color:rgb(148 163 184)}:global(.dark) .upstream-dashboard .summary-strip{border-color:rgb(51 65 85);background:rgb(51 65 85)}:global(.dark) .upstream-dashboard .summary-item,:global(.dark) .upstream-dashboard .dashboard-card,:global(.dark) .upstream-dashboard .detail-kpis>div,:global(.dark) .upstream-dashboard .detail-section{background:rgb(17 24 39)}:global(.dark) .upstream-dashboard .summary-item strong,:global(.dark) .upstream-dashboard .dashboard-card h2,:global(.dark) .upstream-dashboard .detail-section h3,:global(.dark) .upstream-dashboard .detail-big-value{color:rgb(248 250 252)}:global(.dark) .upstream-dashboard .summary-item small,:global(.dark) .upstream-dashboard .profit-row,:global(.dark) .upstream-dashboard .card-footer,:global(.dark) .upstream-dashboard .detail-meta,:global(.dark) .upstream-dashboard .detail-empty{color:rgb(148 163 184)}:global(.dark) .upstream-dashboard .dashboard-card,:global(.dark) .upstream-dashboard .detail-section,:global(.dark) .upstream-dashboard .detail-drawer,:global(.dark) .upstream-dashboard .detail-header{border-color:rgb(51 65 85)}:global(.dark) .upstream-dashboard .profit-row,:global(.dark) .upstream-dashboard .card-footer,:global(.dark) .upstream-dashboard .model-list,:global(.dark) .upstream-dashboard .error-list,:global(.dark) .upstream-dashboard .model-list li,:global(.dark) .upstream-dashboard .error-list li{border-color:rgb(30 41 59)}:global(.dark) .upstream-dashboard .detail-drawer{background:rgb(2 6 23)}:global(.dark) .upstream-dashboard .detail-header{background:rgb(2 6 23 / .94)}:global(.dark) .upstream-dashboard .close-button{border-color:rgb(71 85 105);background:rgb(15 23 42);color:rgb(203 213 225)}:global(.dark) .upstream-dashboard .empty-state{border-color:rgb(71 85 105)}:global(.dark) .upstream-dashboard .empty-state strong{color:rgb(203 213 225)}:global(.dark) .upstream-dashboard .empty-state-icon{background:rgb(30 41 59)}

/* The dashboard is a quiet operations surface: one control deck, then a readable card grid. */
.upstream-dashboard{max-width:1600px;margin-inline:auto}
.control-deck{overflow:hidden;border:1px solid rgb(214 226 228);border-radius:10px;background:rgb(255 255 255 / .88);box-shadow:0 8px 24px rgb(15 23 42 / .045)}
.summary-row{display:grid;grid-template-columns:repeat(5,minmax(0,1fr));padding:1rem 1.25rem}
.summary-item{display:flex;align-items:center;gap:.75rem;min-width:0;border-right:1px solid rgb(226 232 240);background:transparent;padding:.15rem 1.25rem}.summary-item:first-child{padding-left:0}.summary-item:last-child{border-right:0;padding-right:0}.summary-item small{display:block;margin-bottom:.15rem;font-size:.68rem;line-height:1.2;color:rgb(100 116 139)}.summary-item strong{display:block;font-size:1.3rem;line-height:1.15;color:rgb(15 23 42)}
.summary-icon{display:grid;height:2.25rem;width:2.25rem;flex:none;place-items:center;border-radius:8px}.summary-icon-teal{background:rgb(204 251 241);color:rgb(13 148 136)}.summary-icon-blue{background:rgb(219 234 254);color:rgb(37 99 235)}.summary-icon-amber{background:rgb(254 243 199);color:rgb(217 119 6)}.summary-icon-slate{background:rgb(226 232 240);color:rgb(71 85 105)}
.filter-row{display:grid;grid-template-columns:minmax(300px,2fr) minmax(130px,1fr) minmax(130px,1fr) minmax(220px,1.6fr) auto;align-items:end;gap:.75rem;border-top:1px solid rgb(226 232 240);background:rgb(248 250 252 / .8);padding:.8rem 1.25rem}.filter-control{display:flex;min-width:0;flex-direction:column;gap:.3rem;font-size:.68rem;font-weight:600;color:rgb(100 116 139)}.filter-control .input{width:100%;min-height:2.35rem;border-color:rgb(203 213 225);background:white;font-size:.8rem}.filter-actions{display:flex;align-items:center;justify-content:flex-end;gap:.65rem;min-height:2.35rem}.last-updated{white-space:nowrap;font-size:.62rem;color:rgb(148 163 184)}.refresh-button{display:grid;height:2.35rem;width:2.35rem;place-items:center;border:1px solid rgb(203 213 225);border-radius:7px;background:white;color:rgb(13 148 136);transition:background .15s ease,border-color .15s ease}.refresh-button:hover:not(:disabled){border-color:rgb(20 184 166);background:rgb(240 253 250)}.refresh-button:focus-visible{outline:2px solid rgb(20 184 166);outline-offset:2px}.refresh-button:disabled{cursor:wait;opacity:.55}
.filter-control :deep(.select-trigger){width:100%;min-height:2.35rem;border-color:rgb(203 213 225);background:white;font-size:.8rem}
.sort-toggle{display:grid;height:2.35rem;width:2.35rem;place-items:center;border:1px solid rgb(203 213 225);border-radius:7px;background:white;color:rgb(13 148 136);transition:background .15s ease,border-color .15s ease}.sort-toggle:hover{border-color:rgb(20 184 166);background:rgb(240 253 250)}.sort-toggle:focus-visible{outline:2px solid rgb(20 184 166);outline-offset:2px}
:global(.dark) .upstream-dashboard .sort-toggle{border-color:rgb(71 85 105);background:rgb(15 23 42);color:rgb(203 213 225)}:global(.dark) .upstream-dashboard .sort-toggle:hover{border-color:rgb(20 184 166);background:rgb(19 78 74)}
.window-tabs{display:flex;min-width:0;overflow-x:auto;border:1px solid rgb(203 213 225);border-radius:7px;background:white;padding:2px;scrollbar-width:none}.window-tabs::-webkit-scrollbar{display:none}.window-tab{min-height:2rem;flex:1 0 auto;border:0;border-radius:5px;background:transparent;padding:0 .65rem;color:rgb(100 116 139);font-size:.68rem;font-weight:600;white-space:nowrap;transition:background .15s ease,color .15s ease}.window-tab:hover{background:rgb(240 253 250);color:rgb(13 148 136)}.window-tab:focus-visible{outline:2px solid rgb(20 184 166);outline-offset:-2px}.window-tab-active{background:rgb(13 148 136);color:white;box-shadow:0 1px 2px rgb(15 118 110 / .2)}
.dashboard-grid{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:1rem}.dashboard-card{min-width:0;border-radius:8px;padding:1.15rem}.dashboard-card-head{min-height:4.4rem}.dashboard-card-head h2{margin-top:.45rem;overflow:hidden;font-size:1.05rem;font-weight:700;line-height:1.35;text-overflow:ellipsis;white-space:nowrap;color:rgb(15 23 42)}.dashboard-card-head p{margin-top:.25rem;overflow:hidden;font-size:.72rem;color:rgb(100 116 139);text-overflow:ellipsis;white-space:nowrap}.card-kicker{display:flex;align-items:center;gap:.45rem;font-size:.68rem;color:rgb(148 163 184)}
.card-primary-metrics{display:grid;grid-template-columns:1.15fr 1fr;align-items:stretch;margin-top:1rem;border-top:1px solid rgb(241 245 249);border-bottom:1px solid rgb(241 245 249);padding:.9rem 0}.success-metric{display:flex;min-width:0;flex-direction:column;justify-content:center}.success-metric span,.footer-metric span{font-size:.68rem;color:rgb(100 116 139)}.success-metric strong{margin-top:.3rem;font-size:1.75rem;line-height:1;color:rgb(15 118 110)}.success-metric small{margin-top:.45rem;font-size:.7rem;color:rgb(148 163 184)}.latency-metrics{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:.75rem;border-left:1px solid rgb(226 232 240);padding-left:1rem}.latency-metrics>div{display:flex;min-width:0;flex-direction:column;justify-content:center}.latency-metrics>div>div:first-child{font-size:.65rem!important;letter-spacing:.02em!important}.latency-metrics>div>div:last-child{font-size:.93rem!important}
.card-error-row{display:flex;flex-wrap:wrap;gap:.9rem;padding:.8rem 0;font-size:.7rem;color:rgb(71 85 105)}.card-error-row>span{display:inline-flex;align-items:center;gap:.3rem}.card-error-row b{font-weight:700;color:rgb(30 41 59)}.error-dot{height:.4rem;width:.4rem;border-radius:999px}.error-dot-red{background:rgb(239 68 68)}.error-dot-orange{background:rgb(249 115 22)}.error-dot-slate{background:rgb(148 163 184)}
.card-signal-row{display:flex;align-items:center;justify-content:space-between;gap:.5rem;border-top:1px solid rgb(241 245 249);padding:.65rem 0 0;font-size:.68rem;color:rgb(100 116 139)}.card-signal-row>span{display:inline-flex;align-items:center;gap:.35rem}.card-signal-row strong{font-size:.68rem;font-weight:600;color:rgb(71 85 105)}.signal-marker{height:.42rem;width:.42rem;border-radius:999px}.signal-marker-live{background:rgb(20 184 166)}.signal-marker-muted{background:rgb(203 213 225)}
.card-ops-row{display:flex;align-items:center;gap:.65rem;min-height:1.8rem;border-top:1px solid rgb(241 245 249);font-size:.66rem;color:rgb(100 116 139)}.ops-value,.incident-count,.rate-change,.balance-alert{display:inline-flex;align-items:center;gap:.25rem;white-space:nowrap}.ops-value strong{color:rgb(51 65 85)}.incident-count{margin-left:auto;color:rgb(220 38 38);font-weight:700}.rate-change{color:rgb(13 148 136)}.balance-low-value strong,.balance-alert{color:rgb(220 38 38)}.balance-unavailable{color:rgb(217 119 6);font-size:.64rem}.summary-item-alert strong{color:rgb(220 38 38)}
.detail-section-ops{margin-top:1rem}.ops-detail-grid{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:1rem;margin-top:.85rem}
.ops-value small{font-size:.58rem;color:rgb(148 163 184)}
.card-footer{display:grid;grid-template-columns:1fr 1fr auto;align-items:end;gap:.75rem;margin-top:.2rem;border-top:1px solid rgb(241 245 249);padding-top:.8rem}.footer-metric{display:flex;min-width:0;flex-direction:column;gap:.25rem}.footer-metric strong{font-size:.85rem;color:rgb(30 41 59)}.profit-value{color:rgb(5 150 105)!important}.muted-value{color:rgb(148 163 184)!important}.card-link{display:grid;height:1.9rem;width:1.9rem;place-items:center;border:1px solid rgb(226 232 240);border-radius:6px;font-size:1.1rem;color:rgb(100 116 139);transition:color .15s ease,background .15s ease}.dashboard-card:hover .card-link{border-color:rgb(153 246 228);background:rgb(240 253 250);color:rgb(13 148 136)}
.dashboard-card.status-operational{border-color:rgb(187 247 208);background:rgb(253 255 254)}.dashboard-card.status-degraded{border-color:rgb(253 230 138);background:rgb(255 254 248)}.dashboard-card.status-critical{border-color:rgb(254 202 202);background:rgb(255 252 252)}.dashboard-card.status-disabled,.dashboard-card.status-data_insufficient{border-color:rgb(226 232 240);background:rgb(255 255 255)}
@media (max-width:1320px){.upstream-dashboard{max-width:none}.dashboard-grid{grid-template-columns:repeat(2,minmax(0,1fr))}.filter-control .input{font-size:.75rem}}
@media (max-width:1000px){.filter-window-control{grid-column:span 3}}
@media (max-width:760px){.summary-row{grid-template-columns:repeat(2,minmax(0,1fr));gap:.85rem}.summary-item{border-right:0;padding:.1rem 0}.filter-row{grid-template-columns:repeat(2,minmax(0,1fr))}.filter-window-control{grid-column:span 2}.filter-search{grid-column:span 2}.refresh-button{justify-self:end}.dashboard-grid{grid-template-columns:1fr}.dashboard-card{padding:1rem}.latency-metrics{padding-left:.75rem}}
@media (max-width:440px){.summary-row{grid-template-columns:1fr}.filter-row{grid-template-columns:1fr}.filter-window-control,.filter-search{grid-column:auto}.refresh-button{justify-self:start}}
:global(.dark) .upstream-dashboard .control-deck{border-color:rgb(51 65 85);background:rgb(15 23 42 / .9)}:global(.dark) .upstream-dashboard .summary-item{border-color:rgb(51 65 85);background:transparent}:global(.dark) .upstream-dashboard .summary-item strong,:global(.dark) .upstream-dashboard .dashboard-card-head h2,:global(.dark) .upstream-dashboard .footer-metric strong,:global(.dark) .upstream-dashboard .card-error-row b{color:rgb(248 250 252)}:global(.dark) .upstream-dashboard .summary-item small,:global(.dark) .upstream-dashboard .filter-control,:global(.dark) .upstream-dashboard .dashboard-card-head p,:global(.dark) .upstream-dashboard .success-metric small,:global(.dark) .upstream-dashboard .success-metric span,:global(.dark) .upstream-dashboard .footer-metric span,:global(.dark) .upstream-dashboard .card-error-row{color:rgb(148 163 184)}:global(.dark) .upstream-dashboard .filter-row{border-color:rgb(51 65 85);background:rgb(2 6 23 / .55)}:global(.dark) .upstream-dashboard .filter-control .input,:global(.dark) .upstream-dashboard .refresh-button{border-color:rgb(71 85 105);background:rgb(15 23 42);color:rgb(203 213 225)}:global(.dark) .upstream-dashboard .card-primary-metrics,:global(.dark) .upstream-dashboard .card-footer{border-color:rgb(51 65 85)}:global(.dark) .upstream-dashboard .latency-metrics{border-color:rgb(51 65 85)}:global(.dark) .upstream-dashboard .card-error-row b{color:rgb(226 232 240)}:global(.dark) .upstream-dashboard .card-link{border-color:rgb(71 85 105);color:rgb(148 163 184)}
.detail-header-side{display:flex;flex:none;align-items:center;gap:.75rem}.card-trend{border-top:1px solid rgb(241 245 249);padding:.7rem 0 .25rem}.trend-heading{display:flex;align-items:center;justify-content:space-between;margin-bottom:.45rem;font-size:.66rem;font-weight:600;color:rgb(100 116 139)}.trend-heading small{font-size:.62rem;font-weight:500;color:rgb(148 163 184)}.trend-bars{display:flex;height:2.2rem;align-items:flex-end;gap:3px}.trend-bars-compact{height:5rem;margin-top:1rem;padding:0 .2rem}.trend-bar{display:block;min-width:4px;flex:1;border-radius:3px 3px 1px 1px;background:rgb(45 212 191 / .72);transition:height .2s ease,background .2s ease}.trend-bar:hover{background:rgb(13 148 136)}.trend-bar-error{background:rgb(251 191 36 / .8)}.trend-bars-compact .trend-bar{min-width:7px;background:rgb(20 184 166 / .72)}.trend-bars-compact .trend-bar-error{background:rgb(245 158 11 / .82)}.trend-section{background:rgb(248 250 252)}
:global(.dark) .upstream-dashboard .filter-control :deep(.select-trigger),:global(.dark) .upstream-dashboard .window-tabs{border-color:rgb(71 85 105);background:rgb(15 23 42);color:rgb(203 213 225)}:global(.dark) .upstream-dashboard .window-tab{color:rgb(148 163 184)}:global(.dark) .upstream-dashboard .window-tab:hover{background:rgb(19 78 74);color:rgb(94 234 212)}:global(.dark) .upstream-dashboard .window-tab-active{background:rgb(13 148 136);color:white}
:global(.dark) .upstream-dashboard .card-trend,:global(.dark) .upstream-dashboard .trend-section{border-color:rgb(51 65 85)}:global(.dark) .upstream-dashboard .trend-section{background:rgb(15 23 42)}:global(.dark) .upstream-dashboard .trend-heading{color:rgb(203 213 225)}
@media (max-width:560px){.detail-header-side{gap:.45rem}.detail-header-side .status-badge{padding:.25rem .45rem;font-size:.62rem}.trend-bars-compact{height:4rem}}
:global(.dark) .upstream-dashboard .card-signal-row{border-color:rgb(51 65 85);color:rgb(148 163 184)}:global(.dark) .upstream-dashboard .card-signal-row strong{color:rgb(203 213 225)}:global(.dark) .upstream-dashboard .provider-mark{background:rgb(19 78 74);color:rgb(94 234 212)}
:global(.dark) .upstream-dashboard .last-updated{color:rgb(100 116 139)}
.profit-rate{margin-left:.35rem;font-size:.65rem;font-weight:600;color:rgb(100 116 139)}
:global(.dark) .upstream-dashboard .profit-rate{color:rgb(148 163 184)}

:global(.latency-metrics>div>div:last-child){white-space:nowrap}
:global(.signal-value){display:flex;min-width:0;flex-direction:column;align-items:flex-end;gap:.15rem;text-align:right}
:global(.signal-value small){font-size:.6rem;font-weight:500;color:rgb(148 163 184)}

/* TrendBars is a render-function child, so its nodes do not receive this SFC's scope attribute. */
:global(.trend-bars){display:flex;height:2.2rem;align-items:flex-end;gap:3px}
:global(.trend-bars-compact){height:5rem;margin-top:1rem;padding:0 .2rem}
:global(.trend-bar){display:block;min-width:4px;flex:1 1 0%;border-radius:3px 3px 1px 1px;background:rgb(45 212 191 / .72);transition:height .2s ease,background .2s ease}
:global(.trend-bar:hover){background:rgb(13 148 136)}
:global(.trend-bar-error){background:rgb(251 191 36 / .8)}
:global(.trend-bars-compact .trend-bar){min-width:7px;background:rgb(20 184 166 / .72)}
:global(.trend-bars-compact .trend-bar-error){background:rgb(245 158 11 / .82)}
:global(.dark) .trend-bar{background:rgb(45 212 191 / .78)}
:global(.dark) .trend-bar-error{background:rgb(245 158 11 / .84)}
:global(.dark) .upstream-dashboard .card-ops-row{border-color:rgb(51 65 85);color:rgb(148 163 184)}:global(.dark) .upstream-dashboard .ops-value strong{color:rgb(203 213 225)}:global(.dark) .upstream-dashboard .balance-low-value strong,:global(.dark) .upstream-dashboard .balance-alert{color:rgb(248 113 113)}:global(.dark) .upstream-dashboard .balance-unavailable{color:rgb(251 191 36)}:global(.dark) .upstream-dashboard .summary-item-alert strong{color:rgb(248 113 113)}
.filter-row{grid-template-columns:minmax(300px,2fr) minmax(130px,1fr) auto minmax(220px,1.6fr) auto}
@media (max-width:1000px){.filter-window-control{grid-column:span 2}}
</style>
