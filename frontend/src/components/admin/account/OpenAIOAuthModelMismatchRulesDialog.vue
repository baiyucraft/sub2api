<template>
  <BaseDialog :show="show" :title="t('admin.accounts.oauthModelMismatchRules.title')" width="wide" @close="close">
    <div v-if="loading" class="flex min-h-40 items-center justify-center text-sm text-gray-500">{{ t('admin.accounts.oauthModelMismatchRules.loading') }}</div>
    <div v-else class="space-y-4">
      <p class="text-sm leading-6 text-gray-500 dark:text-gray-400">
        {{ t('admin.accounts.oauthModelMismatchRules.description') }}
      </p>
      <div v-for="(rule, index) in draft" :key="index" class="grid gap-2 sm:grid-cols-[1fr_auto_1fr_auto] sm:items-center">
        <input v-model="rule.source" class="input" :placeholder="t('admin.accounts.oauthModelMismatchRules.requestedModel')" />
        <span class="text-center text-gray-400">→</span>
        <input v-model="rule.target" class="input" :placeholder="t('admin.accounts.oauthModelMismatchRules.responseModel')" />
        <button type="button" class="btn btn-secondary text-red-600" @click="draft.splice(index, 1)">{{ t('admin.accounts.oauthModelMismatchRules.remove') }}</button>
      </div>
      <button type="button" class="btn btn-secondary" @click="draft.push({ source: '', target: '' })">{{ t('admin.accounts.oauthModelMismatchRules.add') }}</button>
    </div>
    <template #footer>
      <div class="flex w-full justify-end gap-3">
        <button type="button" class="btn btn-secondary" :disabled="saving" @click="close">{{ t('admin.accounts.oauthModelMismatchRules.cancel') }}</button>
        <button type="button" class="btn btn-primary" :disabled="loading || saving" @click="save">{{ saving ? t('admin.accounts.oauthModelMismatchRules.saving') : t('admin.accounts.oauthModelMismatchRules.save') }}</button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import { settingsAPI, type OpenAIOAuthModelMismatchRule } from '@/api/admin/settings'
import { useAppStore } from '@/stores/app'
import { useI18n } from 'vue-i18n'

const props = defineProps<{ show: boolean }>()
const emit = defineEmits<{ (event: 'close'): void }>()
const appStore = useAppStore()
const { t } = useI18n()
const loading = ref(false)
const saving = ref(false)
const draft = ref<OpenAIOAuthModelMismatchRule[]>([])

async function load() {
  loading.value = true
  try {
    const settings = await settingsAPI.getOpenAIOAuthModelMismatchRules()
    draft.value = (settings.rules || []).map(rule => ({ ...rule }))
  } catch (error) {
    appStore.showError(error instanceof Error ? error.message : t('admin.accounts.oauthModelMismatchRules.loadFailed'))
    emit('close')
  } finally {
    loading.value = false
  }
}

async function save() {
  const rules = draft.value.map(rule => ({ source: rule.source.trim(), target: rule.target.trim() }))
  if (rules.some(rule => !rule.source || !rule.target)) {
    appStore.showError(t('admin.accounts.oauthModelMismatchRules.required'))
    return
  }
  saving.value = true
  try {
    await settingsAPI.updateOpenAIOAuthModelMismatchRules({ rules })
    appStore.showSuccess(t('admin.accounts.oauthModelMismatchRules.saved'))
    emit('close')
  } catch (error) {
    appStore.showError(error instanceof Error ? error.message : t('admin.accounts.oauthModelMismatchRules.saveFailed'))
  } finally {
    saving.value = false
  }
}

function close() {
  if (!saving.value) emit('close')
}

watch(() => props.show, visible => {
  if (visible) load()
}, { immediate: true })
</script>
