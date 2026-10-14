<template>
  <BaseDialog :show="show && !blocked" :title="t('admin.upstreamManagement.protection.groupTitle')" width="wide" :close-on-escape="!saving" :show-close-button="!saving" @close="close">
    <div v-if="group" class="space-y-5">
      <div class="flex items-center gap-3 border-b border-gray-200 pb-4 dark:border-dark-700">
        <PlatformIcon :platform="group.platform" size="sm" />
        <div class="min-w-0">
          <p class="truncate font-semibold text-gray-900 dark:text-white">{{ group.name }}</p>
          <p class="mt-1 text-xs leading-5 text-gray-500 dark:text-gray-400">{{ t('admin.upstreamManagement.protection.sharedMetrics') }}</p>
        </div>
      </div>
      <div v-if="loading" class="flex justify-center gap-2 py-12 text-gray-500" role="status"><Icon name="refresh" class="animate-spin" />{{ t('common.loading') }}</div>
      <div v-else-if="!policy" role="alert" class="rounded-lg bg-red-50 p-4 text-sm text-red-700 dark:bg-red-950/20 dark:text-red-300">
        {{ t('admin.upstreamManagement.protection.loadFailed') }}
        <button type="button" class="ml-2 underline" @click="load">{{ t('common.retry') }}</button>
      </div>
      <template v-else>
        <fieldset class="space-y-3">
          <legend class="text-sm font-semibold text-gray-900 dark:text-white">{{ t('admin.upstreamManagement.protection.priority') }}</legend>
          <div class="grid gap-2 sm:grid-cols-3">
            <label v-for="value in priorities" :key="value" :class="['cursor-pointer rounded-xl border px-3 py-3 text-sm transition-colors focus-within:ring-2 focus-within:ring-primary-500', draft.priority === value ? 'border-primary-500 bg-primary-50 dark:bg-primary-950/20' : 'border-gray-200 dark:border-dark-600']">
              <span class="flex items-center gap-2"><input v-model="draft.priority" type="radio" name="protection-priority" :value="value" :data-test="`protection-priority-${value}`" /><span class="font-medium">{{ t(`admin.upstreamManagement.protection.priorities.${value}`) }}</span></span>
              <span class="mt-2 block text-xs leading-5 text-gray-500 dark:text-gray-400">{{ t(`admin.upstreamManagement.protection.priorityHints.${value}`) }}</span>
            </label>
          </div>
          <p class="text-xs text-gray-500 dark:text-gray-400" data-test="protection-effective-priority">{{ t('admin.upstreamManagement.protection.effective') }}: {{ t(`admin.upstreamManagement.protection.priorities.${effectivePriority}`) }} · {{ source(draft.priority === 'inherit' ? 'global' : 'group') }}</p>
        </fieldset>
        <div class="grid items-start gap-4 md:grid-cols-2">
          <section class="rounded-xl border border-gray-200 p-4 dark:border-dark-600">
            <h4 class="mb-4 flex items-center gap-2 font-semibold text-gray-900 dark:text-white"><Icon name="clock" size="sm" />{{ t('admin.upstreamManagement.ttftGuard.title') }}</h4>
            <label class="block"><span class="input-label">{{ t('admin.upstreamManagement.protection.mode') }}</span><select v-model="draft.ttft.mode" class="input" data-test="protection-ttft-mode"><option v-for="value in modes" :key="value" :value="value" :disabled="value === 'enabled' && !supportsTTFT">{{ t(`admin.upstreamManagement.protection.modes.${value}`) }}</option></select></label>
            <p v-if="!supportsTTFT" class="mt-2 text-xs text-gray-500 dark:text-gray-400">{{ t('admin.upstreamManagement.protection.ttftUnsupported') }}</p>
            <div class="mt-4 grid grid-cols-2 gap-3">
              <label><span class="input-label">{{ t('admin.upstreamManagement.ttftGuard.threshold') }}</span><input v-model.number="draft.ttft.degradation_ttft_seconds" data-test="protection-ttft-threshold" type="number" min="5" max="300" step="1" :disabled="draft.ttft.mode !== 'enabled'" class="input" /></label>
              <label><span class="input-label">{{ t('admin.upstreamManagement.ttftGuard.minSamples') }}</span><input v-model.number="draft.ttft.min_samples" data-test="protection-ttft-samples" type="number" min="2" max="20" step="1" :disabled="draft.ttft.mode !== 'enabled'" class="input" /></label>
            </div>
            <div class="mt-4 rounded-lg bg-gray-50 p-3 text-xs leading-6 text-gray-600 dark:bg-dark-800 dark:text-gray-300" data-test="protection-ttft-effective">
              <div class="font-medium">{{ t('admin.upstreamManagement.protection.effective') }} · {{ source(draft.ttft.mode === 'enabled' ? 'group' : draft.ttft.mode === 'disabled' ? 'disabled' : 'global') }}</div>
              <div v-if="ttftEnabled">{{ t('admin.groups.ttftGuard.valueSummary', { threshold: effectiveTTFTThreshold, samples: effectiveTTFTSamples }) }}</div><div v-else>{{ t('admin.upstreamManagement.protection.modes.disabled') }}</div>
            </div>
            <p class="mt-3 text-xs leading-5 text-gray-500 dark:text-gray-400">{{ t('admin.upstreamManagement.protection.ttftHint') }}</p>
          </section>
          <section class="rounded-xl border border-gray-200 p-4 dark:border-dark-600">
            <h4 class="mb-4 font-semibold text-gray-900 dark:text-white">{{ t('admin.upstreamManagement.cacheGuard.title') }}</h4>
            <label class="block"><span class="input-label">{{ t('admin.upstreamManagement.protection.mode') }}</span><select v-model="draft.cache.mode" class="input" data-test="protection-cache-mode"><option v-for="value in modes" :key="value" :value="value">{{ t(`admin.upstreamManagement.protection.modes.${value}`) }}</option></select></label>
            <div class="mt-4 grid grid-cols-2 gap-3">
              <label v-for="field in cacheFields" :key="field.key"><span class="input-label">{{ t(`admin.upstreamManagement.cacheGuard.${field.key}`) }}</span><input v-model.number="draft.cache[field.key]" :data-test="`protection-cache-${field.key}`" type="number" :min="field.min" :max="field.max" :step="field.step" :disabled="draft.cache.mode !== 'enabled'" class="input" /></label>
            </div>
            <div class="mt-4 rounded-lg bg-gray-50 p-3 text-xs leading-6 text-gray-600 dark:bg-dark-800 dark:text-gray-300" data-test="protection-cache-effective">
              <div class="font-medium">{{ t('admin.upstreamManagement.protection.effective') }} · {{ source(draft.cache.mode === 'enabled' ? 'group' : draft.cache.mode === 'disabled' ? 'disabled' : 'global') }}</div>
              <div>{{ t(`admin.upstreamManagement.cacheGuard.modes.${effectiveCacheMode}`) }}</div>
              <div v-if="effectiveCacheMode !== 'disabled'">{{ t('admin.upstreamManagement.protection.cacheValues', { threshold: effectiveCacheThreshold, recovery: effectiveCacheRecovery, samples: effectiveCacheSamples }) }}</div>
              <div class="mt-1 border-t border-gray-200 pt-1 dark:border-dark-600">{{ t('admin.upstreamManagement.protection.cacheInherited', { window: policy.cache.window_minutes, cooldown: policy.cache.cooldown_minutes, trial: policy.cache.trial_percent }) }}</div>
            </div>
          </section>
        </div>
        <p v-if="validationError" class="text-sm text-red-600 dark:text-red-400" data-test="protection-validation-error" role="alert">{{ validationError }}</p>
        <p class="text-xs leading-5 text-gray-500 dark:text-gray-400">{{ t('admin.upstreamManagement.protection.atomicHint') }}</p>
      </template>
    </div>
    <template #footer><div class="flex justify-end gap-3"><button type="button" class="btn btn-secondary" :disabled="saving" @click="close">{{ t('common.cancel') }}</button><button type="button" class="btn btn-primary" data-test="protection-save" :disabled="loading || saving || scopeChecking || !policy || !!validationError" @click="save">{{ saving ? t('admin.groups.saving') : t('common.save') }}</button></div></template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import type { AdminGroup } from '@/types'
import type { GroupUpstreamProtectionPolicy, UpdateGroupUpstreamProtectionPolicyRequest } from '@/api/admin/groups'
import { useAppStore } from '@/stores/app'
import { extractApiErrorMessage } from '@/utils/apiError'
import BaseDialog from '@/components/common/BaseDialog.vue'
import PlatformIcon from '@/components/common/PlatformIcon.vue'
import Icon from '@/components/icons/Icon.vue'
import { isSchedulingProtectionPlatformError, isSchedulingProtectionPlatformSelected, schedulingProtectionPlatforms, SCHEDULING_PROTECTION_SETTINGS_UPDATED } from '@/utils/schedulingProtection'

const props = defineProps<{ show: boolean; group: AdminGroup | null }>()
const emit = defineEmits<{ close: []; saved: [policy: GroupUpstreamProtectionPolicy] }>()
const { t } = useI18n()
const appStore = useAppStore()
const policy = ref<GroupUpstreamProtectionPolicy | null>(null)
const loading = ref(false)
const saving = ref(false)
const blocked = ref(false)
const scopeChecking = ref(false)
let generation = 0
const modes = ['inherit', 'enabled', 'disabled'] as const
const priorities = ['inherit', 'cache_first', 'ttft_first'] as const
const supportsTTFT = computed(() => props.group?.platform === 'openai')
const draft = reactive({ priority: 'inherit' as GroupUpstreamProtectionPolicy['priority'], ttft: { mode: 'inherit' as typeof modes[number], degradation_ttft_seconds: 20, min_samples: 5 }, cache: { mode: 'inherit' as typeof modes[number], threshold_percent: 75, recovery_threshold_percent: 80, min_valid_requests: 20 } })
const cacheFields = [{ key: 'threshold_percent', min: 0, max: 100, step: 0.1 }, { key: 'recovery_threshold_percent', min: 0, max: 100, step: 0.1 }, { key: 'min_valid_requests', min: 1, max: 100000, step: 1 }] as const
const effectivePriority = computed(() => draft.priority === 'inherit' ? policy.value?.global_priority ?? 'cache_first' : draft.priority)
const ttftEnabled = computed(() => supportsTTFT.value && (draft.ttft.mode === 'inherit' ? policy.value?.ttft.global_enabled : draft.ttft.mode === 'enabled'))
const effectiveTTFTThreshold = computed(() => draft.ttft.mode === 'enabled' ? draft.ttft.degradation_ttft_seconds : policy.value?.ttft.global_degradation_ttft_seconds)
const effectiveTTFTSamples = computed(() => draft.ttft.mode === 'enabled' ? draft.ttft.min_samples : policy.value?.ttft.global_min_samples)
const effectiveCacheMode = computed(() => draft.cache.mode === 'inherit' ? policy.value?.cache.global_mode ?? 'disabled' : draft.cache.mode === 'disabled' ? 'disabled' : 'protect')
const effectiveCacheThreshold = computed(() => draft.cache.mode === 'enabled' ? draft.cache.threshold_percent : policy.value?.cache.global_threshold_percent)
const effectiveCacheRecovery = computed(() => draft.cache.mode === 'enabled' ? draft.cache.recovery_threshold_percent : policy.value?.cache.global_recovery_threshold_percent)
const effectiveCacheSamples = computed(() => draft.cache.mode === 'enabled' ? draft.cache.min_valid_requests : policy.value?.cache.global_min_valid_requests)
const source = (value: string) => t(`admin.groups.ttftGuard.sources.${value}`)
const validationError = computed(() => {
  if (draft.ttft.mode === 'enabled') {
    if (!supportsTTFT.value) return t('admin.upstreamManagement.protection.ttftUnsupported')
    if (!Number.isInteger(draft.ttft.degradation_ttft_seconds) || draft.ttft.degradation_ttft_seconds < 5 || draft.ttft.degradation_ttft_seconds > 300) return t('admin.groups.ttftGuard.thresholdInvalid')
    if (!Number.isInteger(draft.ttft.min_samples) || draft.ttft.min_samples < 2 || draft.ttft.min_samples > 20) return t('admin.groups.ttftGuard.minSamplesInvalid')
  }
  if (draft.cache.mode === 'enabled' && (!cacheFields.every(field => Number.isFinite(draft.cache[field.key]) && draft.cache[field.key] >= field.min && draft.cache[field.key] <= field.max && (field.step !== 1 || Number.isInteger(draft.cache[field.key]))) || draft.cache.recovery_threshold_percent <= draft.cache.threshold_percent)) return t('admin.upstreamManagement.protection.cacheInvalid')
  return ''
})
async function load() {
  const request = ++generation
  const groupID = props.group?.id
  policy.value = null
  blocked.value = false
  saving.value = false
  scopeChecking.value = false
  if (!groupID) return
  loading.value = true
  try {
    if (!await checkLatestScope(request)) return
    if (request !== generation || !props.show || props.group?.id !== groupID) return
    const result = await adminAPI.groups.getUpstreamProtectionPolicy(groupID)
    if (request !== generation || !props.show || props.group?.id !== groupID) return
    if (result.platform_selected === false) { rejectPlatform(); return }
    policy.value = result
    draft.priority = result.priority
    Object.assign(draft.ttft, { mode: result.ttft.mode, degradation_ttft_seconds: result.ttft.degradation_ttft_seconds ?? result.ttft.global_degradation_ttft_seconds, min_samples: result.ttft.min_samples ?? result.ttft.global_min_samples })
    Object.assign(draft.cache, { mode: result.cache.mode, threshold_percent: result.cache.threshold_percent ?? result.cache.global_threshold_percent, recovery_threshold_percent: result.cache.recovery_threshold_percent ?? result.cache.global_recovery_threshold_percent, min_valid_requests: result.cache.min_valid_requests ?? result.cache.global_min_valid_requests })
  } catch (error) {
    if (request === generation) {
      if (isSchedulingProtectionPlatformError(error)) rejectPlatform()
      else appStore.showError(extractApiErrorMessage(error, t('admin.upstreamManagement.protection.loadFailed')))
    }
  } finally { if (request === generation) loading.value = false }
}
async function save() {
  if (!props.group || !policy.value || loading.value || scopeChecking.value || blocked.value || saving.value || validationError.value) return
  const groupID = props.group.id
  const request = generation
  saving.value = true
  try {
    if (!await checkLatestScope(request) || !props.show || props.group?.id !== groupID) return
    const body: UpdateGroupUpstreamProtectionPolicyRequest = { priority: draft.priority, ttft: { ...draft.ttft }, cache: { ...draft.cache } }
    const result = await adminAPI.groups.updateUpstreamProtectionPolicy(groupID, body)
    if (request !== generation || !props.show || props.group?.id !== groupID) return
    if (result.platform_selected === false) { rejectPlatform(); return }
    appStore.showSuccess(t('admin.upstreamManagement.protection.saved'))
    emit('saved', result)
    emit('close')
  } catch (error) {
    if (request !== generation || !props.show || props.group?.id !== groupID) return
    if (isSchedulingProtectionPlatformError(error)) rejectPlatform()
    else appStore.showError(extractApiErrorMessage(error, t('admin.upstreamManagement.protection.saveFailed')))
  }
  finally { if (request === generation) saving.value = false }
}
function close() { if (!saving.value) emit('close') }
function rejectPlatform() {
  blocked.value = true
  policy.value = null
  appStore.showError(t('admin.upstreamManagement.protection.platformNotSelected'))
  emit('close')
}
async function checkLatestScope(request = generation) {
  const group = props.group
  if (!group || group.platform === 'composite') { rejectPlatform(); return false }
  const settings = await adminAPI.upstreamManagement.getSettings()
  if (request !== generation || !props.show || props.group?.id !== group.id || props.group?.platform !== group.platform) return false
  if (!isSchedulingProtectionPlatformSelected(group.platform, schedulingProtectionPlatforms(settings))) { rejectPlatform(); return false }
  return true
}
async function refreshScope() {
  if (!props.show || !policy.value || scopeChecking.value || document.visibilityState === 'hidden') return
  scopeChecking.value = true
  const request = generation
  const groupID = props.group?.id
  try { await checkLatestScope(request) }
  catch (error) {
    if (request !== generation || !props.show || props.group?.id !== groupID) return
    policy.value = null
    appStore.showError(extractApiErrorMessage(error, t('admin.upstreamManagement.protection.loadFailed')))
  } finally { if (request === generation) scopeChecking.value = false }
}
watch(() => [props.show, props.group?.id, props.group?.platform] as const, ([show]) => { if (show) void load(); else { ++generation; policy.value = null; loading.value = false; saving.value = false; scopeChecking.value = false } }, { immediate: true })
onMounted(() => {
  window.addEventListener('focus', refreshScope)
  window.addEventListener(SCHEDULING_PROTECTION_SETTINGS_UPDATED, refreshScope)
  document.addEventListener('visibilitychange', refreshScope)
})
onUnmounted(() => {
  ++generation
  window.removeEventListener('focus', refreshScope)
  window.removeEventListener(SCHEDULING_PROTECTION_SETTINGS_UPDATED, refreshScope)
  document.removeEventListener('visibilitychange', refreshScope)
})
</script>
