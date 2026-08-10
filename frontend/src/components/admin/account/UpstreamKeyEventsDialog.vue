<template>
  <BaseDialog :show="show" :title="title" width="wide" @close="$emit('close')">
    <div v-if="loading" class="py-8 text-center text-sm text-gray-500 dark:text-dark-400">{{ t('admin.upstreamManagement.events.loading') }}</div>
    <div v-else-if="error" class="rounded-lg bg-red-50 px-3 py-2 text-sm text-red-700 dark:bg-red-900/20 dark:text-red-300">{{ error }}</div>
    <div v-else-if="events.length === 0" class="py-8 text-center text-sm text-gray-500 dark:text-dark-400">{{ t('admin.upstreamManagement.events.empty') }}</div>
    <div v-else class="max-h-[60vh] space-y-2 overflow-y-auto">
      <article v-for="event in events" :key="event.id" class="rounded-lg border border-gray-200 p-3 dark:border-dark-700">
        <div class="flex items-center justify-between gap-3">
          <span :class="['rounded px-2 py-0.5 text-xs font-medium', severityClass(event.severity)]">{{ event.severity }}</span>
          <time class="text-xs text-gray-500 dark:text-dark-400">{{ formatDateTime(event.created_at) }}</time>
        </div>
        <div class="mt-2 text-sm font-medium text-gray-800 dark:text-gray-100">{{ event.message }}</div>
        <pre v-if="Object.keys(event.payload || {}).length" class="mt-2 overflow-x-auto whitespace-pre-wrap break-words rounded bg-gray-50 p-2 text-xs text-gray-600 dark:bg-dark-800 dark:text-dark-300">{{ JSON.stringify(event.payload, null, 2) }}</pre>
      </article>
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
import { formatDateTime } from '@/utils/format'
import upstreamManagementAPI, { type UpstreamKeyEvent } from '@/api/admin/upstreamManagement'
import { extractApiErrorMessage } from '@/utils/apiError'

const props = defineProps<{ show: boolean; keyId: number | null; keyLabel?: string }>()
defineEmits<{ (event: 'close'): void }>()
const { t } = useI18n()
const events = ref<UpstreamKeyEvent[]>([])
const loading = ref(false)
const error = ref('')
const title = computed(() => props.keyLabel ? `${t('admin.upstreamManagement.events.title')} · ${props.keyLabel}` : t('admin.upstreamManagement.events.title'))

const load = async () => {
  if (!props.show || !props.keyId) return
  loading.value = true
  error.value = ''
  try {
    events.value = (await upstreamManagementAPI.getKeyEvents(props.keyId)).items
  } catch (loadError) {
    events.value = []
    error.value = extractApiErrorMessage(loadError, t('admin.upstreamManagement.events.loadFailed'))
  } finally {
    loading.value = false
  }
}
watch(() => [props.show, props.keyId], load, { immediate: true })

const severityClass = (severity: string) => {
  if (severity === 'error') return 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-300'
  if (severity === 'warning') return 'bg-amber-100 text-amber-700 dark:bg-amber-900/30 dark:text-amber-300'
  return 'bg-blue-100 text-blue-700 dark:bg-blue-900/30 dark:text-blue-300'
}
</script>
