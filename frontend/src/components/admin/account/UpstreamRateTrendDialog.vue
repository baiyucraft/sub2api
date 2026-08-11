<template>
  <BaseDialog
    :show="show"
    :title="t('admin.upstreamConfigs.operations.rateTrendTitle')"
    width="extra-wide"
    @close="emit('close')"
  >
    <div class="space-y-4">
      <div class="flex flex-wrap items-center justify-between gap-3 rounded-xl border border-gray-200 bg-gray-50 px-4 py-3 dark:border-dark-700 dark:bg-dark-800">
        <div class="min-w-0">
          <div class="truncate text-sm font-semibold text-gray-900 dark:text-gray-100">{{ account?.name || '-' }}</div>
          <div class="mt-1 text-xs text-gray-500 dark:text-dark-400">{{ t('admin.upstreamManagement.rateTrend.keyId', { id: account?.upstream_key_id ?? '-' }) }}</div>
        </div>
        <div class="inline-flex rounded-lg border border-gray-200 bg-white p-1 dark:border-dark-600 dark:bg-dark-900">
          <button
            v-for="option in ranges"
            :key="option"
            type="button"
            class="min-w-12 rounded-md px-3 py-1.5 text-xs font-medium transition-colors"
            :class="range === option ? 'bg-primary-600 text-white shadow-sm' : 'text-gray-600 hover:bg-gray-100 dark:text-dark-300 dark:hover:bg-dark-700'"
            @click="range = option"
          >
            {{ option }}
          </button>
        </div>
      </div>
      <div v-if="error" class="rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700 dark:border-red-900/50 dark:bg-red-950/20 dark:text-red-300">
        {{ error }}
      </div>
      <UpstreamKeyRateTrendPanel :trend="trend" :loading="loading" />
    </div>
  </BaseDialog>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import { adminAPI } from '@/api/admin'
import type { Account } from '@/types'
import type { UpstreamKeyRateTrend, UpstreamTrendRange } from '@/api/admin/upstreamConfigs'
import UpstreamKeyRateTrendPanel from '@/views/admin/upstream/UpstreamKeyRateTrendPanel.vue'

const props = defineProps<{
  show: boolean
  account: Account | null
}>()
const emit = defineEmits<{ (event: 'close'): void }>()
const { t } = useI18n()
const ranges: UpstreamTrendRange[] = ['24h', '7d', '30d']
const range = ref<UpstreamTrendRange>('24h')
const trend = ref<UpstreamKeyRateTrend | null>(null)
const loading = ref(false)
const error = ref('')
let requestID = 0

async function loadTrend() {
  const account = props.account
  if (!props.show || account?.upstream_config_id == null || account.upstream_key_id == null) return
  const currentRequest = ++requestID
  loading.value = true
  error.value = ''
  try {
    const result = await adminAPI.upstreamConfigs.getKeyRateTrend(
      account.upstream_config_id,
      account.upstream_key_id,
      range.value
    )
    if (currentRequest === requestID) trend.value = result
  } catch (cause: any) {
    if (currentRequest === requestID) {
      trend.value = null
      error.value = cause?.response?.data?.message || cause?.message || t('common.error')
    }
  } finally {
    if (currentRequest === requestID) loading.value = false
  }
}

watch(() => [props.show, props.account?.id, range.value], () => {
  if (!props.show) {
    requestID += 1
    trend.value = null
    error.value = ''
    range.value = '24h'
    return
  }
  void loadTrend()
}, { immediate: true })
</script>
