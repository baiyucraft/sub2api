<template>
  <AppLayout>
    <div class="space-y-6 pb-12">
      <header class="flex flex-col gap-4 md:flex-row md:items-end md:justify-between">
        <div>
          <p class="text-xs font-semibold uppercase tracking-[0.18em] text-primary-600 dark:text-primary-400">{{ t('nav.upstreamDashboard') }}</p>
          <h1 class="mt-1 text-2xl font-semibold text-gray-900 dark:text-white">{{ t('admin.upstreamDashboard.title') }}</h1>
          <p class="mt-1 text-sm text-gray-500 dark:text-dark-400">{{ t('admin.upstreamDashboard.description') }}</p>
        </div>
        <div class="flex flex-wrap gap-2">
          <select v-model="rangeWindow" class="input min-w-[120px]" @change="load">
            <option v-for="item in windows" :key="item" :value="item">{{ t(`admin.upstreamDashboard.windows.${item}`) }}</option>
          </select>
          <select v-model="provider" class="input min-w-[130px]" @change="load">
            <option value="">{{ t('admin.upstreamDashboard.filters.allProviders') }}</option>
            <option v-for="item in providers" :key="item" :value="item">{{ item }}</option>
          </select>
          <select v-model="status" class="input min-w-[130px]" @change="load">
            <option value="">{{ t('admin.upstreamDashboard.filters.allStatuses') }}</option>
            <option v-for="item in statuses" :key="item" :value="item">{{ t(`admin.upstreamDashboard.status.${item}`) }}</option>
          </select>
          <input v-model="search" class="input min-w-[200px]" :placeholder="t('common.search')" @keyup.enter="load" />
          <button class="btn btn-secondary" :disabled="loading" @click="load"><Icon name="refresh" size="sm" :class="loading ? 'animate-spin' : ''" />{{ t('common.refresh') }}</button>
        </div>
      </header>

      <div v-if="error" class="rounded-xl border border-red-200 bg-red-50 p-4 text-sm text-red-700 dark:border-red-900/60 dark:bg-red-900/20 dark:text-red-300">{{ error }}</div>
      <div v-if="loading && !items.length" class="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
        <div v-for="n in 6" :key="n" class="h-64 animate-pulse rounded-2xl bg-gray-100 dark:bg-dark-800" />
      </div>
      <div v-else-if="!items.length" class="rounded-2xl border border-dashed border-gray-300 p-12 text-center text-sm text-gray-500 dark:border-dark-700 dark:text-dark-400">{{ t('admin.upstreamDashboard.empty') }}</div>
      <div v-else class="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
        <article
          v-for="item in items"
          :key="item.id"
          class="group cursor-pointer rounded-2xl border border-gray-200 bg-white p-5 shadow-sm transition hover:-translate-y-0.5 hover:border-primary-300 hover:shadow-lg focus:outline-none focus:ring-2 focus:ring-primary-500 dark:border-dark-700 dark:bg-dark-900"
          tabindex="0"
          role="button"
          :aria-label="t('admin.upstreamDashboard.openDetail', { name: item.name })"
          @click="openDetail(item)"
          @keydown.enter="openDetail(item)"
          @keydown.space.prevent="openDetail(item)"
        >
          <div class="flex items-start justify-between gap-3">
            <div class="min-w-0"><h2 class="truncate text-base font-semibold text-gray-900 dark:text-white">{{ item.name }}</h2><p class="mt-1 truncate text-xs text-gray-500 dark:text-dark-400">{{ item.provider }} · {{ item.site_url }}</p></div>
            <span class="inline-flex shrink-0 items-center gap-1.5 rounded-full px-2.5 py-1 text-xs font-medium" :class="statusClass(item.overall_status)"><span class="h-1.5 w-1.5 rounded-full bg-current" />{{ t(`admin.upstreamDashboard.status.${item.overall_status}`) }}</span>
          </div>
          <div class="mt-5 grid grid-cols-2 gap-3 text-sm">
            <Metric :label="t('admin.upstreamDashboard.metrics.requests')" :value="formatNumber(item.requests)" />
            <Metric :label="t('admin.upstreamDashboard.metrics.successRate')" :value="formatRate(item.success_rate)" />
            <Metric :label="t('admin.upstreamDashboard.metrics.ttft')" :value="formatMs(item.p50_ttft_ms)" />
            <Metric :label="t('admin.upstreamDashboard.metrics.latency')" :value="formatMs(item.p50_latency_ms)" />
          </div>
          <div class="mt-4 flex flex-wrap gap-2 text-xs">
            <span class="rounded-md bg-red-50 px-2 py-1 text-red-700 dark:bg-red-900/20 dark:text-red-300">429 · {{ item.error_429 }}</span>
            <span class="rounded-md bg-orange-50 px-2 py-1 text-orange-700 dark:bg-orange-900/20 dark:text-orange-300">5xx · {{ item.error_5xx }}</span>
            <span class="rounded-md bg-gray-100 px-2 py-1 text-gray-600 dark:bg-dark-800 dark:text-dark-300">{{ t('admin.upstreamDashboard.metrics.timeouts') }} · {{ item.timeouts }}</span>
          </div>
          <div class="mt-3 text-xs text-gray-500 dark:text-dark-400">
            {{ item.estimated_gross_profit == null ? t('admin.upstreamDashboard.estimatedUnavailable') : `${t('admin.upstreamDashboard.metrics.estimatedProfit')} ${item.estimated_gross_profit.toFixed(4)}` }}
          </div>
          <div class="mt-4 flex items-center justify-between border-t border-gray-100 pt-3 text-xs dark:border-dark-800"><span class="text-gray-500 dark:text-dark-400">{{ t('admin.upstreamDashboard.metrics.accounts') }} {{ item.schedulable_account_count }}/{{ item.account_count }}</span><span class="text-gray-400 group-hover:text-primary-600 dark:text-dark-500">{{ t('common.viewDetails') }} →</span></div>
        </article>
      </div>
    </div>

    <transition name="slide">
      <div v-if="selected" class="fixed inset-0 z-50 flex justify-end" role="dialog" aria-modal="true" :aria-labelledby="detailTitleID" @click.self="closeDetail">
        <div class="absolute inset-0 bg-black/30" @click="closeDetail" />
        <aside ref="drawerRef" class="relative h-full w-full max-w-xl overflow-y-auto border-l border-gray-200 bg-white p-6 shadow-2xl dark:border-dark-700 dark:bg-dark-900">
          <div class="flex items-start justify-between"><div><p class="text-xs uppercase tracking-widest text-primary-600">{{ selected.provider }}</p><h2 :id="detailTitleID" class="mt-1 text-xl font-semibold text-gray-900 dark:text-white">{{ selected.name }}</h2></div><button ref="closeButtonRef" class="btn btn-ghost" :aria-label="t('common.close')" @click="closeDetail">×</button></div>
          <div v-if="detailLoading" class="mt-6 text-sm text-gray-500">{{ t('common.loading') }}</div>
          <div v-else-if="detailError" class="mt-6 rounded-xl border border-red-200 bg-red-50 p-4 text-sm text-red-700 dark:border-red-900/60 dark:bg-red-900/20 dark:text-red-300">{{ detailError }}</div>
          <template v-else-if="detail">
            <div class="mt-6 grid grid-cols-2 gap-3"><Metric :label="t('admin.upstreamDashboard.metrics.requests')" :value="formatNumber(detail.requests)" /><Metric :label="t('admin.upstreamDashboard.metrics.failed')" :value="formatNumber(detail.failed_requests)" /><Metric :label="t('admin.upstreamDashboard.metrics.p95ttft')" :value="formatMs(detail.p95_ttft_ms)" /><Metric :label="t('admin.upstreamDashboard.metrics.p95latency')" :value="formatMs(detail.p95_latency_ms)" /></div>
            <section class="mt-6 rounded-xl border border-gray-200 p-4 dark:border-dark-700"><h3 class="font-medium text-gray-900 dark:text-white">{{ t('admin.upstreamDashboard.sections.traffic') }}</h3><div class="mt-3 grid grid-cols-3 gap-3 text-sm"><Metric :label="'429'" :value="formatNumber(detail.error_429)" /><Metric :label="'5xx'" :value="formatNumber(detail.error_5xx)" /><Metric :label="t('admin.upstreamDashboard.metrics.timeouts')" :value="formatNumber(detail.timeouts)" /></div><ul v-if="detail.traffic?.models?.length" class="mt-4 space-y-1 text-xs text-gray-600 dark:text-dark-300"><li v-for="model in detail.traffic.models" :key="model.model" class="flex justify-between"><span>{{ model.model }}</span><span>{{ formatNumber(model.requests) }}</span></li></ul></section>
            <section class="mt-4 rounded-xl border border-gray-200 p-4 dark:border-dark-700"><h3 class="font-medium text-gray-900 dark:text-white">{{ t('admin.upstreamDashboard.sections.probe') }}</h3><p class="mt-2 text-sm text-gray-600 dark:text-dark-300">{{ stateLabel(detail.probe.latest_state) }} · {{ detail.probe.latest_reason || t('admin.upstreamDashboard.noReason') }}</p><p class="mt-1 text-xs text-gray-500">{{ t('admin.upstreamDashboard.metrics.probeSamples') }} {{ detail.probe.samples }} · TTFT {{ formatMs(detail.probe.average_ttft_ms) }} · {{ formatMs(detail.probe.average_duration_ms) }}</p><p v-if="detail.probe.confidence_status" class="mt-1 text-xs text-gray-500">{{ t('admin.upstreamDashboard.metrics.confidence') }}: {{ stateLabel(detail.probe.confidence_status) }}</p></section>
            <section class="mt-4 rounded-xl border border-gray-200 p-4 dark:border-dark-700"><h3 class="font-medium text-gray-900 dark:text-white">{{ t('admin.upstreamDashboard.sections.accounts') }}</h3><p class="mt-2 text-sm text-gray-600 dark:text-dark-300">{{ formatNumber(detail.schedulable_account_count) }}/{{ formatNumber(detail.account_count) }} · {{ t('admin.upstreamDashboard.metrics.tempUnschedulable') }} {{ formatNumber(detail.temp_unschedulable_count) }}</p></section>
            <section class="mt-4 rounded-xl border border-gray-200 p-4 dark:border-dark-700"><h3 class="font-medium text-gray-900 dark:text-white">{{ t('admin.upstreamDashboard.sections.profit') }}</h3><p class="mt-2 text-sm text-gray-600 dark:text-dark-300">{{ detail.profit_unavailable || detail.estimated_gross_profit == null ? t('admin.upstreamDashboard.estimatedUnavailable') : `${detail.estimated_gross_profit.toFixed(4)} (${formatRate(detail.estimated_gross_profit_rate)})` }}</p></section>
            <section v-if="detail.recent_errors?.length" class="mt-4 rounded-xl border border-gray-200 p-4 dark:border-dark-700"><h3 class="font-medium text-gray-900 dark:text-white">{{ t('admin.upstreamDashboard.sections.errors') }}</h3><ul class="mt-2 space-y-2 text-xs text-gray-600 dark:text-dark-300"><li v-for="item in detail.recent_errors" :key="`${item.occurred_at}-${item.status_code}`" class="flex justify-between gap-3"><span>{{ item.model }} · {{ stateLabel(item.category) }}</span><span>{{ item.status_code }} · {{ item.occurred_at }}</span></li></ul></section>
            <div class="mt-6 flex flex-wrap gap-2"><button class="btn btn-primary" @click="router.push({ path: '/admin/upstream/channels', query: { upstream_config_id: String(detail.id) } })">{{ t('admin.upstreamDashboard.actions.channels') }}</button><button class="btn btn-secondary" @click="router.push({ path: '/admin/upstream/accounts', query: { upstream_config_id: String(detail.id) } })">{{ t('admin.upstreamDashboard.actions.accounts') }}</button><button class="btn btn-secondary" @click="router.push({ path: '/admin/usage', query: { upstream_config_id: String(detail.id) } })">{{ t('admin.upstreamDashboard.actions.usage') }}</button></div>
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
import { getDashboard, getDashboardDetail, type UpstreamDashboardCard, type UpstreamDashboardDetail, type UpstreamDashboardWindow } from '@/api/admin/upstreamConfigs'

const Metric = defineComponent({ props: { label: { type: String, required: true }, value: { type: String, required: true } }, setup: (props) => () => h('div', { class: 'min-w-0' }, [h('div', { class: 'truncate text-[11px] uppercase tracking-wide text-gray-400 dark:text-dark-500' }, props.label), h('div', { class: 'mt-1 text-lg font-semibold text-gray-900 dark:text-white' }, props.value)]) })
const { t } = useI18n(); const router = useRouter(); const items = ref<UpstreamDashboardCard[]>([]); const selected = ref<UpstreamDashboardCard | null>(null); const detail = ref<UpstreamDashboardDetail | null>(null); const detailLoading = ref(false); const detailError = ref(''); const loading = ref(false); const error = ref(''); const search = ref(''); const provider = ref(''); const status = ref(''); const rangeWindow = ref<UpstreamDashboardWindow>('24h'); const windows: UpstreamDashboardWindow[] = ['1h','24h','7d','15d','30d']; const statuses = ['operational','degraded','critical','disabled','data_insufficient']; const providers = computed(() => Array.from(new Set(items.value.map(item => item.provider).filter(Boolean))).sort()); const drawerRef = ref<HTMLElement | null>(null); const closeButtonRef = ref<HTMLButtonElement | null>(null); const detailTitleID = 'upstream-dashboard-detail-title'; let lastFocusedElement: HTMLElement | null = null; let timer: number | undefined; let loadSeq = 0; let detailSeq = 0
const statusClass = (status: string) => ({ operational: 'bg-emerald-50 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300', degraded: 'bg-amber-50 text-amber-700 dark:bg-amber-900/30 dark:text-amber-300', critical: 'bg-red-50 text-red-700 dark:bg-red-900/30 dark:text-red-300', disabled: 'bg-gray-100 text-gray-600 dark:bg-dark-800 dark:text-dark-300', data_insufficient: 'bg-slate-100 text-slate-600 dark:bg-dark-800 dark:text-dark-300' }[status] || 'bg-gray-100 text-gray-600')
const formatNumber = (value: number | null | undefined) => value == null || !Number.isFinite(value) ? '-' : new Intl.NumberFormat().format(value)
const formatMs = (value: number | null | undefined) => value == null || value <= 0 ? '-' : `${Math.round(value)} ms`
const formatRate = (value: number | null | undefined) => value == null || !Number.isFinite(value) ? '-' : `${(value * 100).toFixed(1)}%`
function stateLabel(value?: string | null) { if (!value) return '-'; const key = `admin.upstreamDashboard.status.${value}`; return t(key) === key ? value : t(key) }
async function load() { const seq = ++loadSeq; loading.value = true; error.value = ''; try { const data = await getDashboard({ window: rangeWindow.value, provider: provider.value || undefined, status: status.value || undefined, search: search.value || undefined }); if (seq !== loadSeq) return; items.value = data.items || []; if (selected.value && !items.value.some(item => item.id === selected.value?.id)) closeDetail() } catch (e: any) { if (seq === loadSeq) error.value = e?.message || t('common.loadFailed') } finally { if (seq === loadSeq) loading.value = false } }
async function openDetail(item: UpstreamDashboardCard) { lastFocusedElement = document.activeElement instanceof HTMLElement ? document.activeElement : null; selected.value = item; detail.value = null; detailError.value = ''; const seq = ++detailSeq; detailLoading.value = true; await nextTick(); closeButtonRef.value?.focus(); try { const value = await getDashboardDetail(item.id, rangeWindow.value); if (seq === detailSeq) detail.value = value } catch (e: any) { if (seq === detailSeq) detailError.value = e?.message || t('common.loadFailed') } finally { if (seq === detailSeq) detailLoading.value = false } }
function closeDetail() { selected.value = null; detail.value = null; detailError.value = ''; detailSeq += 1; lastFocusedElement?.focus(); lastFocusedElement = null }
function onKeydown(event: KeyboardEvent) { if (event.key === 'Escape' && selected.value) closeDetail() }
onMounted(() => { load(); timer = window.setInterval(load, 60000) }); onUnmounted(() => { if (timer) window.clearInterval(timer) })
onMounted(() => window.addEventListener('keydown', onKeydown)); onUnmounted(() => window.removeEventListener('keydown', onKeydown))
</script>

<style scoped>
.slide-enter-active,.slide-leave-active{transition:opacity .2s ease}.slide-enter-active aside,.slide-leave-active aside{transition:transform .25s ease}.slide-enter-from,.slide-leave-to{opacity:0}.slide-enter-from aside,.slide-leave-to aside{transform:translateX(100%)}
</style>
