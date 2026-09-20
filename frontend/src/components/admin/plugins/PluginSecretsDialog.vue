<template>
  <BaseDialog :show="true" :title="t('admin.plugins.secretsTitle', { name })" :z-index="55"
    :close-on-escape="!saving" :show-close-button="!saving" @close="emit('cancel')">
    <form class="space-y-5" @submit.prevent="submit">
      <p v-if="error" role="alert" class="text-sm text-red-600 dark:text-red-400">{{ error }}</p>
      <p v-if="loading" class="text-sm text-gray-500">{{ t('common.loading') }}</p>
      <div v-for="row in rows" :key="row.field" class="space-y-2 border-b border-gray-200 pb-4 dark:border-dark-700">
        <div class="flex flex-wrap items-center justify-between gap-2">
          <span class="min-w-0 break-all font-mono text-sm text-gray-800 dark:text-gray-200">{{ row.field }}</span>
          <span class="text-xs text-gray-500">{{ t(configured[row.field] ? 'admin.plugins.secretConfigured' : 'admin.plugins.secretNotConfigured') }}</span>
        </div>
        <select :value="row.mode" :data-secret-mode="row.field" :aria-label="`${row.field} ${t('admin.plugins.secretOperation')}`"
          class="input w-full" :disabled="!ready || saving" @change="changeMode(row, ($event.target as HTMLSelectElement).value)">
          <option value="keep">{{ t('admin.plugins.secretKeep') }}</option>
          <option value="replace">{{ t('admin.plugins.secretReplace') }}</option>
          <option value="clear">{{ t('admin.plugins.secretClear') }}</option>
        </select>
        <input v-if="row.mode === 'replace'" v-model="row.value" :data-secret-value="row.field" :aria-label="row.field"
          type="password" autocomplete="new-password" spellcheck="false" data-1p-ignore data-lpignore="true"
          class="input w-full" :disabled="saving" maxlength="8192" required />
      </div>
      <div class="flex justify-end gap-3">
        <button type="button" class="btn btn-secondary" data-testid="secrets-cancel" :disabled="saving" @click="emit('cancel')">{{ t('common.cancel') }}</button>
        <button type="submit" class="btn btn-primary" data-testid="secrets-save" :disabled="!canSubmit">
          <Icon name="check" size="sm" />{{ t(saving ? 'common.saving' : 'common.save') }}
        </button>
      </div>
    </form>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, reactive } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Icon from '@/components/icons/Icon.vue'

const props = defineProps<{
  name: string
  fields: string[]
  configured: Record<string, boolean>
  loading: boolean
  ready: boolean
  saving: boolean
  error: string
}>()
const emit = defineEmits<{ save: [values: Record<string, string>]; cancel: [] }>()
const { t } = useI18n()
type Row = { field: string; mode: 'keep' | 'replace' | 'clear'; value: string }
// Mounted only for one host-owned session. Existing secret values are never loaded.
const rows = reactive<Row[]>(props.fields.map(field => ({ field, mode: 'keep', value: '' })))
const canSubmit = computed(() => props.ready && !props.saving && rows.every(row => row.mode !== 'replace' || row.value.length > 0))
function changeMode(row: Row, mode: string) {
  if (mode !== 'keep' && mode !== 'replace' && mode !== 'clear') return
  row.mode = mode
  row.value = ''
}
function submit() {
  if (!canSubmit.value) return
  emit('save', Object.fromEntries(rows.filter(row => row.mode !== 'keep').map(row => [row.field, row.mode === 'clear' ? '' : row.value])))
}
onBeforeUnmount(() => { for (const row of rows) row.value = '' })
</script>
