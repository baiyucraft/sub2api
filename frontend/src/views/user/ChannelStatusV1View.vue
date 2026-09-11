<template>
  <AppLayout>
    <MonitorHero
      :overall-status="overallStatus"
      :interval-seconds="DEFAULT_INTERVAL_SECONDS"
      :range="currentRange"
      :loading="loading"
      :platforms="platformNavItems"
      :active-platform="activePlatform"
      :auto-refresh="autoRefresh"
      @update:range="handleRangeChange"
      @refresh="manualReload"
      @navigate-platform="navigateToPlatform"
    />

    <MonitorCardGrid
      :groups="groupedItems"
      :range="currentRange"
      :countdown-seconds="countdown"
      :loading="loading"
      @card-click="openDetail"
      @platform-section="registerPlatformSection"
    />

    <MonitorDetailDialog
      :show="showDetail"
      :monitor-id="detailTarget?.id ?? null"
      :title="detailTitle"
      :range="currentRange"
      @close="closeDetail"
    />
  </AppLayout>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, onBeforeUnmount, watch, nextTick } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { extractApiErrorMessage } from '@/utils/apiError'
import {
  list as listChannelMonitorViews,
  type UserMonitorView,
  type MonitorRange,
} from '@/api/channelMonitor'
import AppLayout from '@/components/layout/AppLayout.vue'
import MonitorHero, { type OverallStatus } from '@/components/user/monitor/MonitorHero.vue'
import MonitorCardGrid from '@/components/user/monitor/MonitorCardGrid.vue'
import MonitorDetailDialog from '@/components/user/MonitorDetailDialog.vue'
import { DEFAULT_INTERVAL_SECONDS, STATUS_OPERATIONAL } from '@/constants/channelMonitor'
import { useAutoRefresh } from '@/composables/useAutoRefresh'
import { useChannelMonitorFormat } from '@/composables/useChannelMonitorFormat'
import { groupMonitorItems } from '@/utils/channelMonitorGrouping'

const { t } = useI18n()
const appStore = useAppStore()
const { providerLabel } = useChannelMonitorFormat()

// ── State ──
const items = ref<UserMonitorView[]>([])
const loading = ref(false)
const currentRange = ref<MonitorRange>('24h')
const showDetail = ref(false)
const detailTarget = ref<UserMonitorView | null>(null)
const activePlatform = ref('')
const platformSections = new Map<string, HTMLElement>()
const platformVisibility = new Map<string, number>()
let platformObserver: IntersectionObserver | null = null

let abortController: AbortController | null = null

const autoRefresh = useAutoRefresh({
  storageKey: 'channel-status-auto-refresh',
  intervals: [30, 60, 120] as const,
  defaultInterval: DEFAULT_INTERVAL_SECONDS,
  onRefresh: () => reload(true),
  shouldPause: () => document.hidden || loading.value,
})
const countdown = autoRefresh.countdown

// ── Computed ──
const overallStatus = computed<OverallStatus>(() => {
  if (items.value.length === 0) return 'operational'
  for (const it of items.value) {
    if (it.primary_status === 'failed' || it.primary_status === 'error') return 'degraded'
    if (it.primary_status !== STATUS_OPERATIONAL) return 'degraded'
  }
  return 'operational'
})

const detailTitle = computed(() => {
  return detailTarget.value?.name || t('channelStatus.detailTitle')
})

const groupedItems = computed(() => groupMonitorItems(items.value))
const platformNavItems = computed(() => groupedItems.value.map((group) => ({
  value: group.provider,
  label: group.provider === '__other__' ? t('channelStatus.otherPlatform') : providerLabel(group.provider),
})))

// ── Loaders ──
async function reload(silent = false) {
  if (abortController) abortController.abort()
  const ctrl = new AbortController()
  abortController = ctrl
  if (!silent) loading.value = true
  try {
    const res = await listChannelMonitorViews({ signal: ctrl.signal, range: currentRange.value })
    if (ctrl.signal.aborted || abortController !== ctrl) return
    items.value = res.items || []
  } catch (err: unknown) {
    const e = err as { name?: string; code?: string }
    if (e?.name === 'AbortError' || e?.code === 'ERR_CANCELED') return
    // 自动刷新失败属于页面数据暂不可用，不应把最近一次真实渠道状态
    // 误报成错误；保留 items 并静默等待下一轮刷新。手动刷新仍提示用户。
    if (!silent) {
      appStore.showError(extractApiErrorMessage(err, t('channelStatus.loadError')))
    }
  } finally {
    if (abortController === ctrl) {
      if (!silent) loading.value = false
      countdown.value = DEFAULT_INTERVAL_SECONDS
      abortController = null
    }
  }
}

async function manualReload() {
  await reload(false)
}

// ── Handlers ──
async function handleRangeChange(value: MonitorRange) {
  if (currentRange.value === value) return
  currentRange.value = value
  await reload(false)
}

function openDetail(row: UserMonitorView) {
  detailTarget.value = row
  showDetail.value = true
}

function closeDetail() {
  showDetail.value = false
  detailTarget.value = null
}

function registerPlatformSection(provider: string, element: HTMLElement | null) {
  const previous = platformSections.get(provider)
  if (previous && previous !== element) platformObserver?.unobserve(previous)
  if (!element) {
    platformSections.delete(provider)
    platformVisibility.delete(provider)
    return
  }
  platformSections.set(provider, element)
  platformObserver?.observe(element)
}

function navigateToPlatform(provider: string) {
  activePlatform.value = provider
  const target = platformSections.get(provider)
    || document.getElementById(`monitor-platform-${provider}`)?.closest('section') as HTMLElement | null
  target?.scrollIntoView?.({ behavior: 'smooth', block: 'start' })
}

watch(
  groupedItems,
  (groups) => {
    const providers = groups.map((group) => group.provider)
    if (!providers.includes(activePlatform.value)) activePlatform.value = providers[0] || ''
  },
  { immediate: true },
)

watch(
  () => appStore.cachedPublicSettings?.channel_monitor_enabled,
  (enabled) => {
    if (enabled === false) autoRefresh.stop()
    else if (autoRefresh.enabled.value) autoRefresh.start()
  },
)

onMounted(async () => {
  await nextTick()
  if (typeof IntersectionObserver !== 'undefined') {
    const heroHeight = Math.ceil(document.querySelector('.channel-status-v1-hero')?.getBoundingClientRect().height || 96)
    platformObserver = new IntersectionObserver((entries) => {
      for (const entry of entries) {
        const provider = entry.target instanceof HTMLElement ? entry.target.dataset.platform : undefined
        if (provider) platformVisibility.set(provider, entry.isIntersecting ? entry.intersectionRatio : 0)
      }
      const visible = [...platformVisibility.entries()]
        .filter(([, ratio]) => ratio > 0)
        .sort(([, leftRatio], [, rightRatio]) => rightRatio - leftRatio)[0]
      if (visible) activePlatform.value = visible[0]
    }, { rootMargin: `-${heroHeight}px 0px -55% 0px`, threshold: [0, 0.25, 0.5, 0.75, 1] })
    for (const element of platformSections.values()) platformObserver.observe(element)
  }
  void reload(false)
  if (appStore.cachedPublicSettings?.channel_monitor_enabled !== false) {
    autoRefresh.setEnabled(true)
  }
})

onBeforeUnmount(() => {
  if (abortController) abortController.abort()
  platformObserver?.disconnect()
  platformObserver = null
  platformVisibility.clear()
})
</script>
