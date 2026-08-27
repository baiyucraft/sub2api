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
      <div v-if="selected" class="fixed inset-0 z-50 flex justify-end" role="dialog" aria-modal="true" @click.self="selected = null">
        <div class="absolute inset-0 bg-black/30" @click="selected = null" />
        <aside class="relative h-full w-full max-w-xl overflow-y-auto border-l border-gray-200 bg-white p-6 shadow-2xl dark:border-dark-700 dark:bg-dark-900">
          <div class="flex items-start justify-between"><div><p class="text-xs uppercase tracking-widest text-primary-600">{{ selected.provider }}</p><h2 class="mt-1 text-xl font-semibold text-gray-900 dark:text-white">{{ selected.name }}</h2></div><button class="btn btn-ghost" :aria-label="t('common.close')" @click="selected = null">×</button></div>
          <div class="mt-6 grid grid-cols-2 gap-3"><Metric :label="t('admin.upstreamDashboard.metrics.requests')" :value="formatNumber(selected.requests)" /><Metric :label="t('admin.upstreamDashboard.metrics.failed')" :value="formatNumber(selected.failed_requests)" /><Metric :label="t('admin.upstreamDashboard.metrics.p95ttft')" :value="formatMs(selected.p95_ttft_ms)" /><Metric :label="t('admin.upstreamDashboard.metrics.p95latency')" :value="formatMs(selected.p95_latency_ms)" /></div>
          <section class="mt-6 rounded-xl border border-gray-200 p-4 dark:border-dark-700"><h3 class="font-medium text-gray-900 dark:text-white">{{ t('admin.upstreamDashboard.sections.traffic') }}</h3><div class="mt-3 grid grid-cols-3 gap-3 text-sm"><Metric :label="'429'" :value="formatNumber(selected.error_429)" /><Metric :label="'5xx'" :value="formatNumber(selected.error_5xx)" /><Metric :label="t('admin.upstreamDashboard.metrics.timeouts')" :value="formatNumber(selected.timeouts)" /></div></section>
          <section class="mt-4 rounded-xl border border-gray-200 p-4 dark:border-dark-700"><h3 class="font-medium text-gray-900 dark:text-white">{{ t('admin.upstreamDashboard.sections.probe') }}</h3><p class="mt-2 text-sm text-gray-600 dark:text-dark-300">{{ selected.probe.latest_state || '-' }} · {{ selected.probe.latest_reason || t('admin.upstreamDashboard.noReason') }}</p><p class="mt-1 text-xs text-gray-500">{{ t('admin.upstreamDashboard.metrics.probeSamples') }} {{ selected.probe.samples }}</p></section>
          <section class="mt-4 rounded-xl border border-gray-200 p-4 dark:border-dark-700"><h3 class="font-medium text-gray-900 dark:text-white">{{ t('admin.upstreamDashboard.sections.profit') }}</h3><p class="mt-2 text-sm text-gray-600 dark:text-dark-300">{{ selected.estimated_gross_profit == null ? t('admin.upstreamDashboard.estimatedUnavailable') : `${selected.estimated_gross_profit.toFixed(4)} (${formatRate(selected.estimated_gross_profit_rate)})` }}</p></section>
          <div class="mt-6 flex flex-wrap gap-2"><button class="btn btn-primary" @click="router.push('/admin/upstream/channels')">{{ t('admin.upstreamDashboard.actions.channels') }}</button><button class="btn btn-secondary" @click="router.push('/admin/upstream/accounts')">{{ t('admin.upstreamDashboard.actions.accounts') }}</button><button class="btn btn-secondary" @click="router.push({ path: '/admin/usage', query: { upstream_config_id: String(selected.id) } })">{{ t('admin.upstreamDashboard.actions.usage') }}</button></div>
        </aside>
      </div>
    </transition>
  </AppLayout>
</template>

<script setup lang="ts">
import { defineComponent, h, onMounted, onUnmounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import AppLayout from '@/components/layout/AppLayout.vue'
import Icon from '@/components/icons/Icon.vue'
import { getDashboard, type UpstreamDashboardCard, type UpstreamDashboardWindow } from '@/api/admin/upstreamConfigs'

const Metric = defineComponent({ props: { label: { type: String, required: true }, value: { type: String, required: true } }, setup: (props) => () => h('div', { class: 'min-w-0' }, [h('div', { class: 'truncate text-[11px] uppercase tracking-wide text-gray-400 dark:text-dark-500' }, props.label), h('div', { class: 'mt-1 text-lg font-semibold text-gray-900 dark:text-white' }, props.value)]) })
const { t } = useI18n(); const router = useRouter(); const items = ref<UpstreamDashboardCard[]>([]); const selected = ref<UpstreamDashboardCard | null>(null); const loading = ref(false); const error = ref(''); const search = ref(''); const provider = ref(''); const status = ref(''); const rangeWindow = ref<UpstreamDashboardWindow>('24h'); const windows: UpstreamDashboardWindow[] = ['1h','24h','7d','15d','30d']; const statuses = ['operational','degraded','critical','disabled','data_insufficient']; const providers = ['sub2api','newapi','lcodex','other']; let timer: number | undefined
const statusClass = (status: string) => ({ operational: 'bg-emerald-50 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300', degraded: 'bg-amber-50 text-amber-700 dark:bg-amber-900/30 dark:text-amber-300', critical: 'bg-red-50 text-red-700 dark:bg-red-900/30 dark:text-red-300', disabled: 'bg-gray-100 text-gray-600 dark:bg-dark-800 dark:text-dark-300', data_insufficient: 'bg-slate-100 text-slate-600 dark:bg-dark-800 dark:text-dark-300' }[status] || 'bg-gray-100 text-gray-600')
const formatNumber = (value: number | null | undefined) => value == null ? '-' : new Intl.NumberFormat().format(value)
const formatMs = (value: number | null | undefined) => value == null || value <= 0 ? '-' : `${Math.round(value)} ms`
const formatRate = (value: number | null | undefined) => value == null || !Number.isFinite(value) ? '-' : `${(value * 100).toFixed(1)}%`
async function load() { loading.value = true; error.value = ''; try { const data = await getDashboard({ window: rangeWindow.value, provider: provider.value || undefined, status: status.value || undefined, search: search.value || undefined }); items.value = data.items || [] } catch (e: any) { error.value = e?.message || t('common.loadFailed') } finally { loading.value = false } }
function openDetail(item: UpstreamDashboardCard) { selected.value = item }
onMounted(() => { load(); timer = window.setInterval(load, 60000) }); onUnmounted(() => { if (timer) window.clearInterval(timer) })
</script>

<style scoped>
.slide-enter-active,.slide-leave-active{transition:opacity .2s ease}.slide-enter-active aside,.slide-leave-active aside{transition:transform .25s ease}.slide-enter-from,.slide-leave-to{opacity:0}.slide-enter-from aside,.slide-leave-to aside{transform:translateX(100%)}
</style>
