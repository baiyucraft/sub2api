<template>
  <BaseDialog
    :show="show"
    :title="t('admin.groups.ttftGuard.title')"
    width="normal"
    @close="handleClose"
  >
    <div v-if="group" class="space-y-5">
      <div class="flex items-center gap-3 rounded-lg bg-gray-50 px-4 py-3 dark:bg-dark-700">
        <PlatformIcon :platform="group.platform" size="sm" />
        <div class="min-w-0">
          <div class="truncate font-medium text-gray-900 dark:text-white">{{ group.name }}</div>
          <div class="text-xs text-gray-500 dark:text-gray-400">
            {{ t('admin.groups.platforms.' + group.platform) }}
          </div>
        </div>
      </div>

      <div v-if="loading" class="flex justify-center py-10">
        <Icon name="refresh" size="lg" class="animate-spin text-primary-500" />
      </div>

      <template v-else-if="policyLoaded">
        <div>
          <label class="input-label">{{ t('admin.groups.ttftGuard.modeLabel') }}</label>
          <div class="grid gap-2 sm:grid-cols-3" data-test="ttft-policy-modes">
            <button
              v-for="option in modeOptions"
              :key="option.value"
              type="button"
              :data-test="`ttft-policy-mode-${option.value}`"
              :class="[
                'rounded-lg border px-3 py-2.5 text-left transition-colors',
                mode === option.value
                  ? 'border-primary-500 bg-primary-50 text-primary-700 ring-1 ring-primary-500/20 dark:bg-primary-900/20 dark:text-primary-300'
                  : 'border-gray-200 bg-white text-gray-700 hover:border-gray-300 dark:border-dark-600 dark:bg-dark-800 dark:text-gray-300 dark:hover:border-dark-500'
              ]"
              @click="mode = option.value"
            >
              <span class="block text-sm font-medium">{{ option.label }}</span>
              <span class="mt-1 block text-xs opacity-75">{{ option.description }}</span>
            </button>
          </div>
        </div>

        <div class="grid gap-3 sm:grid-cols-2">
          <div class="rounded-lg border border-gray-200 bg-gray-50 p-3 dark:border-dark-600 dark:bg-dark-700/60">
            <div class="text-xs font-medium uppercase text-gray-500 dark:text-gray-400">
              {{ t('admin.groups.ttftGuard.globalValues') }}
            </div>
            <div class="mt-2 text-sm text-gray-700 dark:text-gray-300">
              <template v-if="globalSettings.enabled">
                {{ t('admin.groups.ttftGuard.valueSummary', {
                  threshold: globalSettings.degradation_ttft_seconds,
                  samples: globalSettings.min_samples
                }) }}
              </template>
              <template v-else>{{ t('admin.groups.ttftGuard.globalDisabled') }}</template>
            </div>
          </div>
          <div
            class="rounded-lg border p-3"
            :class="effectiveEnabled
              ? 'border-emerald-200 bg-emerald-50 dark:border-emerald-900/60 dark:bg-emerald-950/20'
              : 'border-gray-200 bg-gray-50 dark:border-dark-600 dark:bg-dark-700/60'"
          >
            <div class="text-xs font-medium uppercase text-gray-500 dark:text-gray-400">
              {{ t('admin.groups.ttftGuard.effectiveValues') }}
            </div>
            <div class="mt-2 text-sm font-medium text-gray-800 dark:text-gray-200" data-test="ttft-effective-values">
              <template v-if="effectiveEnabled">
                {{ t('admin.groups.ttftGuard.valueSummary', {
                  threshold: effectiveThreshold,
                  samples: effectiveSamples
                }) }}
              </template>
              <template v-else>{{ t('admin.groups.ttftGuard.disabled') }}</template>
            </div>
            <div class="mt-1 text-xs text-gray-500 dark:text-gray-400">
              {{ t('admin.groups.ttftGuard.sourceLabel', { source: sourceLabel }) }}
            </div>
          </div>
        </div>

        <div>
          <div class="mb-2 text-xs font-medium uppercase text-gray-500 dark:text-gray-400">
            {{ t('admin.groups.ttftGuard.customValues') }}
          </div>
          <div class="grid gap-4 sm:grid-cols-2">
            <label class="block">
            <span class="input-label">{{ t('admin.groups.ttftGuard.threshold') }}</span>
            <div class="relative">
              <input
                v-model.number="customThreshold"
                type="number"
                min="5"
                max="300"
                step="1"
                class="input pr-12"
                data-test="ttft-threshold-input"
                :disabled="mode !== 'enabled'"
              />
              <span class="pointer-events-none absolute right-3 top-1/2 -translate-y-1/2 text-xs text-gray-400">s</span>
            </div>
            <span class="mt-1 block text-xs text-gray-500 dark:text-gray-400">
              {{ t('admin.groups.ttftGuard.thresholdHint') }}
            </span>
            </label>
            <label class="block">
            <span class="input-label">{{ t('admin.groups.ttftGuard.minSamples') }}</span>
            <input
              v-model.number="customSamples"
              type="number"
              min="2"
              max="20"
              step="1"
              class="input"
              data-test="ttft-samples-input"
              :disabled="mode !== 'enabled'"
            />
            <span class="mt-1 block text-xs text-gray-500 dark:text-gray-400">
              {{ t('admin.groups.ttftGuard.minSamplesHint') }}
            </span>
            </label>
          </div>
        </div>

        <p v-if="mode === 'disabled'" class="rounded-lg bg-amber-50 px-3 py-2 text-xs text-amber-800 dark:bg-amber-900/20 dark:text-amber-300">
          {{ t('admin.groups.ttftGuard.disabledHint') }}
        </p>
        <p v-else-if="mode === 'inherit'" class="rounded-lg bg-blue-50 px-3 py-2 text-xs text-blue-700 dark:bg-blue-900/20 dark:text-blue-300">
          {{ t('admin.groups.ttftGuard.inheritHint') }}
        </p>
        <p v-if="validationError" class="text-sm text-red-600 dark:text-red-400" data-test="ttft-validation-error">
          {{ validationError }}
        </p>
      </template>
    </div>

    <template #footer>
      <div class="flex justify-end gap-3">
        <button type="button" class="btn btn-secondary" :disabled="saving" @click="handleClose">
          {{ t('common.cancel') }}
        </button>
        <button
          type="button"
          class="btn btn-primary"
          data-test="ttft-policy-save"
          :disabled="loading || saving || !policyLoaded || !formValid"
          @click="handleSave"
        >
          {{ saving ? t('admin.groups.saving') : t('common.save') }}
        </button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import type { AdminGroup } from '@/types'
import type {
  GroupTTFTGuardPolicy,
  GroupTTFTGuardPolicyMode
} from '@/api/admin/groups'
import { useAppStore } from '@/stores/app'
import { extractApiErrorMessage } from '@/utils/apiError'
import BaseDialog from '@/components/common/BaseDialog.vue'
import PlatformIcon from '@/components/common/PlatformIcon.vue'
import Icon from '@/components/icons/Icon.vue'

const props = defineProps<{
  show: boolean
  group: AdminGroup | null
}>()

const emit = defineEmits<{
  close: []
  saved: [policy: GroupTTFTGuardPolicy]
}>()

const { t } = useI18n()
const appStore = useAppStore()
const loading = ref(false)
const saving = ref(false)
const policyLoaded = ref(false)
const mode = ref<GroupTTFTGuardPolicyMode>('inherit')
const customThreshold = ref(20)
const customSamples = ref(5)
const globalSettings = reactive({
  enabled: false,
  degradation_ttft_seconds: 20,
  min_samples: 5
})

const modeOptions = computed(() => [
  {
    value: 'inherit' as const,
    label: t('admin.groups.ttftGuard.inherit'),
    description: t('admin.groups.ttftGuard.inheritDescription')
  },
  {
    value: 'enabled' as const,
    label: t('admin.groups.ttftGuard.enabled'),
    description: t('admin.groups.ttftGuard.enabledDescription')
  },
  {
    value: 'disabled' as const,
    label: t('admin.groups.ttftGuard.disabled'),
    description: t('admin.groups.ttftGuard.disabledDescription')
  }
])

const thresholdValid = computed(
  () => Number.isFinite(customThreshold.value) && customThreshold.value >= 5 && customThreshold.value <= 300
)
const samplesValid = computed(
  () => Number.isInteger(customSamples.value) && customSamples.value >= 2 && customSamples.value <= 20
)
const formValid = computed(() => mode.value !== 'enabled' || (thresholdValid.value && samplesValid.value))
const validationError = computed(() => {
  if (mode.value !== 'enabled') return ''
  if (!thresholdValid.value) return t('admin.groups.ttftGuard.thresholdInvalid')
  if (!samplesValid.value) return t('admin.groups.ttftGuard.minSamplesInvalid')
  return ''
})

const effectiveEnabled = computed(() => {
  if (mode.value === 'disabled') return false
  if (mode.value === 'enabled') return true
  return globalSettings.enabled
})
const effectiveThreshold = computed(() =>
  mode.value === 'enabled' ? customThreshold.value : globalSettings.degradation_ttft_seconds
)
const effectiveSamples = computed(() =>
  mode.value === 'enabled' ? customSamples.value : globalSettings.min_samples
)
const sourceLabel = computed(() => {
  const source = mode.value === 'enabled' ? 'group' : mode.value === 'disabled' ? 'disabled' : 'global'
  return t(`admin.groups.ttftGuard.sources.${source}`)
})

const resetPolicyState = () => {
  policyLoaded.value = false
  mode.value = 'inherit'
  customThreshold.value = 20
  customSamples.value = 5
  globalSettings.enabled = false
  globalSettings.degradation_ttft_seconds = 20
  globalSettings.min_samples = 5
}

const loadPolicy = async () => {
  resetPolicyState()
  if (!props.group) return
  loading.value = true
  try {
    const policy = await adminAPI.groups.getTTFTGuardPolicy(props.group.id)
    mode.value = policy.mode
    globalSettings.enabled = policy.global_enabled
    globalSettings.degradation_ttft_seconds = policy.global_degradation_ttft_seconds
    globalSettings.min_samples = policy.global_min_samples
    customThreshold.value = policy.degradation_ttft_seconds ?? policy.global_degradation_ttft_seconds
    customSamples.value = policy.min_samples ?? policy.global_min_samples
    policyLoaded.value = true
  } catch (error: unknown) {
    appStore.showError(extractApiErrorMessage(error, t('admin.groups.ttftGuard.loadFailed')))
  } finally {
    loading.value = false
  }
}

const handleSave = async () => {
  if (!props.group || !policyLoaded.value || !formValid.value) return
  saving.value = true
  try {
    const policy = await adminAPI.groups.updateTTFTGuardPolicy(props.group.id, {
      mode: mode.value,
      degradation_ttft_seconds: customThreshold.value,
      min_samples: customSamples.value
    })
    appStore.showSuccess(t('admin.groups.ttftGuard.saved'))
    emit('saved', policy)
    emit('close')
  } catch (error: unknown) {
    appStore.showError(extractApiErrorMessage(error, t('admin.groups.ttftGuard.saveFailed')))
  } finally {
    saving.value = false
  }
}

const handleClose = () => {
  if (!saving.value) emit('close')
}

watch(
  () => [props.show, props.group?.id] as const,
  ([show]) => {
    if (show) void loadPolicy()
    else resetPolicyState()
  },
  { immediate: true }
)
</script>
