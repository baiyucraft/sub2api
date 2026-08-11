<template>
  <BaseDialog :show="show" :title="title" width="wide" @close="$emit('close')">
    <div v-if="loading" class="py-8 text-center text-sm text-gray-500 dark:text-dark-400">{{ t('admin.upstreamManagement.events.loading') }}</div>
    <div v-else-if="error" class="rounded-lg bg-red-50 px-3 py-2 text-sm text-red-700 dark:bg-red-900/20 dark:text-red-300">{{ error }}</div>
    <div v-else class="max-h-[65vh] space-y-5 overflow-y-auto pr-1">
      <section>
        <div class="mb-3 flex items-center justify-between gap-3">
          <h3 class="text-sm font-semibold text-gray-800 dark:text-gray-100">{{ t('admin.upstreamManagement.health.recentObservations') }}</h3>
          <span class="text-xs text-gray-400 dark:text-dark-500">{{ t('admin.upstreamManagement.health.retentionHint') }}</span>
        </div>
        <div v-if="history.length" class="rounded-xl border border-gray-200 bg-gray-50/70 p-4 dark:border-dark-700 dark:bg-dark-800/60">
          <UpstreamHealthHistory :observations="history" :limit="30" :interactive="false" />
          <div class="mt-4 grid gap-2 sm:grid-cols-2">
            <article
              v-for="(item, index) in [...history].reverse()"
              :key="`${item.observed_at}-${index}`"
              class="rounded-lg border border-gray-200 bg-white p-3 dark:border-dark-700 dark:bg-dark-800"
            >
              <div class="flex items-center justify-between gap-3">
                <span :class="['rounded px-2 py-0.5 text-xs font-medium', stateClass(item.state)]">{{ stateLabel(item.state) }}</span>
                <time class="text-xs text-gray-500 dark:text-dark-400">{{ formatDateTime(item.observed_at) }}</time>
              </div>
              <div class="mt-2 text-xs text-gray-600 dark:text-dark-300">
                {{ sourceLabel(item.source) }} · {{ item.result || '-' }} · {{ reasonLabel(item.reason) }}
              </div>
            </article>
          </div>
        </div>
        <div v-else class="rounded-lg border border-dashed border-gray-200 py-6 text-center text-sm text-gray-500 dark:border-dark-700 dark:text-dark-400">
          {{ t('admin.upstreamManagement.health.noHistory') }}
        </div>
      </section>

      <section>
        <h3 class="mb-3 text-sm font-semibold text-gray-800 dark:text-gray-100">{{ t('admin.upstreamManagement.events.stateChanges') }}</h3>
        <div v-if="events.length === 0" class="rounded-lg border border-dashed border-gray-200 py-6 text-center text-sm text-gray-500 dark:border-dark-700 dark:text-dark-400">{{ t('admin.upstreamManagement.events.empty') }}</div>
        <div v-else class="space-y-2">
          <article v-for="event in events" :key="event.id" class="rounded-lg border border-gray-200 p-3 dark:border-dark-700">
            <div class="flex items-center justify-between gap-3">
              <span :class="['rounded px-2 py-0.5 text-xs font-medium', severityClass(event.severity)]">{{ event.severity }}</span>
              <time class="text-xs text-gray-500 dark:text-dark-400">{{ formatDateTime(event.created_at) }}</time>
            </div>
            <div class="mt-2 text-sm font-medium text-gray-800 dark:text-gray-100">{{ event.message }}</div>
            <pre v-if="Object.keys(event.payload || {}).length" class="mt-2 overflow-x-auto whitespace-pre-wrap break-words rounded bg-gray-50 p-2 text-xs text-gray-600 dark:bg-dark-800 dark:text-dark-300">{{ JSON.stringify(event.payload, null, 2) }}</pre>
          </article>
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
import UpstreamHealthHistory from '@/components/account/UpstreamHealthHistory.vue'
import { formatDateTime } from '@/utils/format'
import upstreamManagementAPI, { type UpstreamKeyEvent } from '@/api/admin/upstreamManagement'
import type { UpstreamHealthObservation } from '@/types'
import { extractApiErrorMessage } from '@/utils/apiError'

const props = defineProps<{ show: boolean; keyId: number | null; keyLabel?: string }>()
defineEmits<{ (event: 'close'): void }>()
const { t, te } = useI18n()
const events = ref<UpstreamKeyEvent[]>([])
const history = ref<UpstreamHealthObservation[]>([])
const loading = ref(false)
const error = ref('')
const title = computed(() => props.keyLabel ? `${t('admin.upstreamManagement.events.title')} · ${props.keyLabel}` : t('admin.upstreamManagement.events.title'))

const load = async () => {
  if (!props.show || !props.keyId) return
  loading.value = true
  error.value = ''
  try {
    const result = await upstreamManagementAPI.getKeyEvents(props.keyId)
    events.value = result.items
    history.value = result.health_history || []
  } catch (loadError) {
    events.value = []
    history.value = []
    error.value = extractApiErrorMessage(loadError, t('admin.upstreamManagement.events.loadFailed'))
  } finally {
    loading.value = false
  }
}
watch(() => [props.show, props.keyId], load, { immediate: true })

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
