<template>
  <BaseDialog :show="show" :title="probeOnly ? t('admin.upstreamManagement.probeSettings.title') : t('admin.upstreamManagement.settings.title')" width="wide" @close="closeDialog">
    <div v-if="loading" class="flex min-h-64 items-center justify-center text-sm text-gray-500 dark:text-gray-400">
      <Icon name="refresh" size="md" class="mr-2 animate-spin" />
      {{ t('common.loading') }}
    </div>

    <div v-else class="space-y-6">
      <section v-if="!probeOnly" class="rounded-2xl border border-gray-200 bg-gray-50/70 p-5 dark:border-dark-700 dark:bg-dark-800/60">
        <div class="flex items-start justify-between gap-5">
          <div class="min-w-0">
            <div class="flex items-center gap-1.5">
              <h4 class="text-base font-semibold text-gray-900 dark:text-white">{{ t('admin.upstreamManagement.ttftGuard.title') }}</h4>
              <HelpTooltip trigger="click" width-class="w-96">
                <div class="space-y-2 pr-5">
                  <p>{{ t('admin.upstreamManagement.ttftGuard.tip.definition') }}</p>
                  <p>{{ t('admin.upstreamManagement.ttftGuard.tip.scope') }}</p>
                  <ul class="list-disc space-y-1 pl-4">
                    <li>{{ t('admin.upstreamManagement.ttftGuard.tip.normal') }}</li>
                    <li>{{ t('admin.upstreamManagement.ttftGuard.tip.fast') }}</li>
                    <li>{{ t('admin.upstreamManagement.ttftGuard.tip.immediate') }}</li>
                    <li>{{ t('admin.upstreamManagement.ttftGuard.tip.recovery') }}</li>
                  </ul>
                </div>
              </HelpTooltip>
            </div>
            <p class="mt-1 max-w-2xl text-sm leading-6 text-gray-500 dark:text-gray-400">{{ t('admin.upstreamManagement.ttftGuard.description') }}</p>
          </div>
          <Toggle v-model="draft.ttft_guard.enabled" :aria-label="t('admin.upstreamManagement.ttftGuard.enabled')" />
        </div>

        <div class="mt-5 grid gap-4 sm:grid-cols-2">
          <label class="space-y-1.5">
            <span class="text-sm font-medium text-gray-700 dark:text-gray-200">{{ t('admin.upstreamManagement.ttftGuard.threshold') }}</span>
            <input v-model.number="draft.ttft_guard.degradation_ttft_seconds" type="number" min="5" max="300" class="input" />
            <span class="block text-xs text-gray-400">5–300</span>
          </label>
          <label class="space-y-1.5">
            <span class="text-sm font-medium text-gray-700 dark:text-gray-200">{{ t('admin.upstreamManagement.ttftGuard.minSamples') }}</span>
            <input v-model.number="draft.ttft_guard.min_samples" type="number" min="2" max="20" class="input" />
            <span class="block text-xs text-gray-400">2–20</span>
          </label>
        </div>
      </section>

      <section v-if="probeOnly" class="rounded-2xl border border-gray-200 bg-white p-5 dark:border-dark-700 dark:bg-dark-900/60">
        <div>
          <h4 class="text-base font-semibold text-gray-900 dark:text-white">{{ t('admin.upstreamManagement.probeModels.title') }}</h4>
          <p class="mt-1 text-sm leading-6 text-gray-500 dark:text-gray-400">{{ t('admin.upstreamManagement.probeModels.description') }}</p>
        </div>
        <div class="mt-5 grid gap-4 lg:grid-cols-3">
          <label v-for="platform in platforms" :key="platform.id" class="space-y-1.5">
            <span class="flex items-center gap-2 text-sm font-medium text-gray-700 dark:text-gray-200">
              {{ platform.label }}
            </span>
            <Select
              v-model="draft.probe_models[platform.id]"
              :options="candidateOptions[platform.id] || []"
              searchable
              creatable
              :creatable-prefix="t('admin.upstreamManagement.probeModels.useCustom')"
              :search-placeholder="t('admin.upstreamManagement.probeModels.search')"
              :placeholder="t('admin.upstreamManagement.probeModels.placeholder')"
            />
            <span v-if="!platform.probe_supported" class="block text-xs text-gray-400 dark:text-dark-500">{{ platform.probe_reason }}</span>
          </label>
        </div>
        <label class="mt-5 block max-w-xs space-y-1.5">
          <span class="flex items-center gap-1.5 text-sm font-medium text-gray-700 dark:text-gray-200">
            {{ t('admin.upstreamManagement.probeModels.interval') }}
            <HelpTooltip width-class="w-72">
              <span>{{ t('admin.upstreamManagement.probeModels.intervalTip') }}</span>
            </HelpTooltip>
          </span>
          <div class="relative">
            <input
              v-model.number="probeIntervalMinutes"
              data-test="probe-interval-minutes"
              type="number"
              min="1"
              max="60"
              step="1"
              class="input pr-14"
            />
            <span class="pointer-events-none absolute inset-y-0 right-3 flex items-center text-xs text-gray-400 dark:text-dark-400">
              {{ t('admin.upstreamManagement.probeModels.minutes') }}
            </span>
          </div>
          <span class="block text-xs text-gray-400 dark:text-dark-500">{{ t('admin.upstreamManagement.probeModels.intervalRange') }}</span>
        </label>
        <div class="mt-6 border-t border-gray-200 pt-5 dark:border-dark-700">
          <div class="flex items-start justify-between gap-5">
            <div>
              <h5 class="text-sm font-semibold text-gray-900 dark:text-white">{{ t('admin.upstreamManagement.confidenceProbe.title') }}</h5>
              <p class="mt-1 text-xs leading-5 text-gray-500 dark:text-gray-400">{{ t('admin.upstreamManagement.confidenceProbe.description') }}</p>
            </div>
            <Toggle v-model="draft.confidence_probe.enabled" :aria-label="t('admin.upstreamManagement.confidenceProbe.enabled')" />
          </div>
          <div class="mt-4 grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
            <label class="space-y-1.5"><span class="text-sm font-medium text-gray-700 dark:text-gray-200">{{ t('admin.upstreamManagement.confidenceProbe.effort') }}</span><Select v-model="draft.confidence_probe.reasoning_effort" :options="[{ value: 'low', label: 'low' }, { value: 'medium', label: 'medium' }, { value: 'high', label: 'high' }]" /></label>
            <label class="space-y-1.5"><span class="text-sm font-medium text-gray-700 dark:text-gray-200">{{ t('admin.upstreamManagement.confidenceProbe.threshold') }}</span><input v-model.number="draft.confidence_probe.quality_degrade_threshold" type="number" min="0" max="100" class="input" /></label>
            <label class="flex items-center gap-2 pt-7 text-sm text-gray-700 dark:text-gray-200"><input v-model="draft.confidence_probe.long_context_enabled" type="checkbox" />{{ t('admin.upstreamManagement.confidenceProbe.longContext') }}</label>
            <label v-if="draft.confidence_probe.long_context_enabled" class="space-y-1.5"><span class="text-sm font-medium text-gray-700 dark:text-gray-200">{{ t('admin.upstreamManagement.confidenceProbe.maxTokens') }}</span><input v-model.number="draft.confidence_probe.long_context_max_tokens" type="number" min="256" max="16384" class="input" /></label>
          </div>
        </div>
        <div class="mt-6 border-t border-gray-200 pt-5 dark:border-dark-700">
          <div class="flex items-start justify-between gap-5">
            <div class="min-w-0">
              <h5 class="text-sm font-semibold text-gray-900 dark:text-white">{{ t('admin.upstreamManagement.probeGuard.title') }}</h5>
              <p class="mt-1 text-xs leading-5 text-gray-500 dark:text-gray-400">{{ t('admin.upstreamManagement.probeGuard.description') }}</p>
            </div>
            <Toggle v-model="draft.probe_guard.enabled" :aria-label="t('admin.upstreamManagement.probeGuard.enabled')" />
          </div>
          <div class="mt-4 grid gap-4 sm:grid-cols-2">
            <label class="space-y-1.5">
              <span class="text-sm font-medium text-gray-700 dark:text-gray-200">{{ t('admin.upstreamManagement.probeGuard.suspendAfterFailures') }}</span>
              <input v-model.number="draft.probe_guard.suspend_after_failures" type="number" min="1" max="20" class="input" />
              <span class="block text-xs text-gray-400">1–20</span>
            </label>
            <label class="space-y-1.5">
              <span class="text-sm font-medium text-gray-700 dark:text-gray-200">{{ t('admin.upstreamManagement.probeGuard.recoverySuccesses') }}</span>
              <input v-model.number="draft.probe_guard.recovery_successes" type="number" min="1" max="20" class="input" />
              <span class="block text-xs text-gray-400">1–20</span>
            </label>
          </div>
        </div>
        <div class="mt-6 border-t border-gray-200 pt-5 dark:border-dark-700">
          <CustomErrorCodeSelector
            v-model="draft.probe_guard.custom_error_codes"
            v-model:enabled="draft.probe_guard.custom_error_codes_enabled"
            :title="t('admin.upstreamManagement.probeGuard.customErrorCodesTitle')"
            :hint="t('admin.upstreamManagement.probeGuard.customErrorCodesHint')"
            :warning="t('admin.upstreamManagement.probeGuard.customErrorCodesWarning')"
            :input-placeholder="t('admin.upstreamManagement.probeGuard.enterErrorCode')"
            :none-selected-text="t('admin.upstreamManagement.probeGuard.noneSelected')"
            :add-label="t('common.add')"
            :remove-label="t('common.remove')"
            :confirm-429-message="t('admin.upstreamManagement.probeGuard.customErrorCodes429Warning')"
            :confirm-529-message="t('admin.upstreamManagement.probeGuard.customErrorCodes529Warning')"
            :invalid-error-message="t('admin.accounts.invalidErrorCode')"
            :duplicate-error-message="t('admin.accounts.errorCodeExists')"
            @error="appStore.showError"
            @info="appStore.showInfo"
          />
          <p class="mt-2 text-xs leading-5 text-gray-500 dark:text-gray-400">
            {{ t('admin.upstreamManagement.probeGuard.appendHint') }}
          </p>
        </div>
      </section>

      <section v-if="!probeOnly" class="rounded-2xl border border-gray-200 bg-white p-5 dark:border-dark-700 dark:bg-dark-900/60">
        <div class="flex items-start justify-between gap-5">
          <div class="min-w-0">
            <h4 class="text-base font-semibold text-gray-900 dark:text-white">{{ t('admin.upstreamManagement.modelAliases.title') }}</h4>
            <p class="mt-1 text-sm leading-6 text-gray-500 dark:text-gray-400">{{ t('admin.upstreamManagement.modelAliases.description') }}</p>
          </div>
        </div>
        <div class="mt-4 space-y-3" data-test="model-alias-rules">
          <div v-for="(mapping, index) in modelAliasRows" :key="mapping.id" class="flex items-center gap-2" data-test="model-alias-row">
            <input v-model="mapping.source" data-test="model-alias-source" type="text" class="input min-w-0 flex-1" :placeholder="t('admin.upstreamManagement.modelAliases.source')" />
            <span class="shrink-0 text-gray-400">→</span>
            <input v-model="mapping.target" data-test="model-alias-target" type="text" class="input min-w-0 flex-1" :placeholder="t('admin.upstreamManagement.modelAliases.target')" />
            <button type="button" class="shrink-0 text-red-500 hover:text-red-700" data-test="remove-model-alias" :aria-label="t('admin.upstreamManagement.modelAliases.remove')" @click="modelAliasRows.splice(index, 1)">
              <Icon name="trash" size="sm" />
            </button>
          </div>
          <button type="button" class="btn btn-secondary text-sm" data-test="add-model-alias" @click="addModelAliasRow()">
            + {{ t('admin.upstreamManagement.modelAliases.add') }}
          </button>
          <p v-if="modelAliasRows.length === 0" class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.upstreamManagement.modelAliases.empty') }}</p>
        </div>
        <p v-if="modelAliasError" class="mt-2 text-sm text-red-600 dark:text-red-400">{{ modelAliasError }}</p>
      </section>
    </div>

    <template #footer>
      <div class="flex w-full justify-end gap-3">
        <button type="button" class="btn btn-secondary" :disabled="saving" @click="closeDialog">{{ t('common.cancel') }}</button>
        <button type="button" class="btn btn-primary" :disabled="loading || saving || !valid" @click="save">
          <Icon v-if="saving" name="refresh" size="sm" class="mr-1.5 animate-spin" />
          {{ t('common.save') }}
        </button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import upstreamManagementAPI, { type ProbePlatformDescriptor, type UpstreamManagementSettings } from '@/api/admin/upstreamManagement'
import { useAppStore } from '@/stores/app'
import { extractApiErrorMessage } from '@/utils/apiError'
import BaseDialog from '@/components/common/BaseDialog.vue'
import HelpTooltip from '@/components/common/HelpTooltip.vue'
import Toggle from '@/components/common/Toggle.vue'
import Select, { type SelectOption } from '@/components/common/Select.vue'
import Icon from '@/components/icons/Icon.vue'
import CustomErrorCodeSelector from '@/components/account/CustomErrorCodeSelector.vue'

const props = defineProps<{ show: boolean; probeOnly?: boolean }>()
const emit = defineEmits<{ (event: 'close'): void; (event: 'saved', value: UpstreamManagementSettings): void }>()
const { t } = useI18n()
const probeOnly = computed(() => props.probeOnly === true)
const appStore = useAppStore()

type ModelAliasRow = { id: number; source: string; target: string }
const platforms = ref<ProbePlatformDescriptor[]>([])
const legacyPlatforms: ProbePlatformDescriptor[] = [
  { id: 'openai', label: 'OpenAI', models: [], probe_supported: true },
  { id: 'anthropic', label: 'Anthropic', models: [], probe_supported: true },
  { id: 'gemini', label: 'Gemini', models: [], probe_supported: true }
]
const defaults: UpstreamManagementSettings = {
  ttft_guard: { enabled: false, degradation_ttft_seconds: 20, min_samples: 5 },
  probe_guard: {
    enabled: true,
    suspend_after_failures: 3,
    recovery_successes: 3,
    custom_error_codes_enabled: false,
    custom_error_codes: []
  },
  probe_models: { openai: 'gpt-4o-mini', anthropic: 'claude-3-5-haiku-latest', gemini: 'gemini-2.0-flash' },
  probe_interval_seconds: 300,
  model_alias_rules: {},
  confidence_probe: { enabled: true, reasoning_effort: 'high', long_context_enabled: false, long_context_max_tokens: 2048, quality_degrade_threshold: 70, prompt_version: 'openai-confidence-v1' }
}
const draft = reactive<UpstreamManagementSettings>(structuredClone(defaults))
const probeIntervalMinutes = ref(5)
const candidates = reactive<Record<string, string[]>>({})
const loading = ref(false)
const saving = ref(false)
const modelAliasRows = ref<ModelAliasRow[]>([])
const modelAliasError = ref('')
let nextModelAliasRowId = 1

const candidateOptions = computed<Record<string, SelectOption[]>>(() => Object.fromEntries(
  Object.entries(candidates).map(([platform, values]) => [platform, values.map(value => ({ value, label: value }))])
))

const valid = computed(() => {
  const threshold = Number(draft.ttft_guard.degradation_ttft_seconds)
  const samples = Number(draft.ttft_guard.min_samples)
  const intervalMinutes = Number(probeIntervalMinutes.value)
  const suspendAfterFailures = Number(draft.probe_guard.suspend_after_failures)
  const recoverySuccesses = Number(draft.probe_guard.recovery_successes)
  const customCodes = draft.probe_guard.custom_error_codes || []
  const confidence = draft.confidence_probe
  return Number.isFinite(threshold) && threshold >= 5 && threshold <= 300 &&
    Number.isInteger(samples) && samples >= 2 && samples <= 20 &&
    Number.isInteger(intervalMinutes) && intervalMinutes >= 1 && intervalMinutes <= 60 &&
    Number.isInteger(suspendAfterFailures) && suspendAfterFailures >= 1 && suspendAfterFailures <= 20 &&
    Number.isInteger(recoverySuccesses) && recoverySuccesses >= 1 && recoverySuccesses <= 20 &&
    customCodes.every(code => Number.isInteger(code) && code >= 100 && code <= 599) &&
    confidence && Number.isInteger(Number(confidence.quality_degrade_threshold)) && Number(confidence.quality_degrade_threshold) >= 0 && Number(confidence.quality_degrade_threshold) <= 100 &&
    platforms.value.filter(platform => platform.probe_supported).every(platform => {
      const value = draft.probe_models[platform.id]?.trim() || ''
      return value.length > 0 && value.length <= 120
    }) && parseModelAliasRules() !== null
})

function parseModelAliasRules(): Record<string, string> | null {
  modelAliasError.value = ''
  const normalized: Record<string, string> = {}
  for (const row of modelAliasRows.value) {
    const source = row.source.trim()
    const target = row.target.trim()
    if (!source && !target) continue
    if (!source || !target) {
      modelAliasError.value = t('admin.upstreamManagement.modelAliases.invalidEntry')
      return null
    }
    if (normalized[source] !== undefined) {
      modelAliasError.value = t('admin.upstreamManagement.modelAliases.duplicateSource')
      return null
    }
    normalized[source] = target
  }
  return normalized
}

function addModelAliasRow(source = '', target = '') {
  modelAliasRows.value.push({ id: nextModelAliasRowId++, source, target })
}

async function load() {
  loading.value = true
  try {
    const [settings, options] = await Promise.all([
      (probeOnly.value ? upstreamManagementAPI.getProbeSettings() : upstreamManagementAPI.getSettings()),
      upstreamManagementAPI.getProbeModelCandidates()
    ])
    Object.assign(draft.ttft_guard, settings.ttft_guard)
    Object.assign(draft.probe_guard, settings.probe_guard || defaults.probe_guard)
    draft.probe_models = { ...defaults.probe_models, ...(settings.probe_models || {}) }
    draft.probe_interval_seconds = settings.probe_interval_seconds ?? defaults.probe_interval_seconds
    draft.confidence_probe = { ...defaults.confidence_probe, ...(settings.confidence_probe || {}) }
    modelAliasRows.value = Object.entries(settings.model_alias_rules || {}).map(([source, target]) => ({ id: nextModelAliasRowId++, source, target }))
    modelAliasError.value = ''
    probeIntervalMinutes.value = Math.max(1, Math.min(60, Math.round(draft.probe_interval_seconds / 60)))
    platforms.value = options.platforms?.length ? options.platforms : legacyPlatforms
    for (const platform of platforms.value) {
      candidates[platform.id] = options.candidates[platform.id] || platform.models || []
      if (draft.probe_models[platform.id] === undefined) draft.probe_models[platform.id] = candidates[platform.id][0] || ''
    }
  } catch (error) {
    appStore.showError(extractApiErrorMessage(error, t('admin.upstreamManagement.settings.loadFailed')))
    emit('close')
  } finally {
    loading.value = false
  }
}

async function save() {
  if (!valid.value) {
    appStore.showError(t('admin.upstreamManagement.settings.invalid'))
    return
  }
  saving.value = true
  try {
    const modelAliasRules = parseModelAliasRules()
    if (!modelAliasRules) return
    const payload: UpstreamManagementSettings = {
      ttft_guard: { ...draft.ttft_guard },
      probe_guard: {
        enabled: Boolean(draft.probe_guard.enabled),
        suspend_after_failures: Number(draft.probe_guard.suspend_after_failures),
        recovery_successes: Number(draft.probe_guard.recovery_successes),
        custom_error_codes_enabled: Boolean(draft.probe_guard.custom_error_codes_enabled),
        custom_error_codes: Array.from(new Set(draft.probe_guard.custom_error_codes || [])).sort((a, b) => a - b)
      },
      probe_models: Object.fromEntries(Object.entries(draft.probe_models).map(([platform, model]) => [platform, model.trim()])),
      probe_interval_seconds: probeIntervalMinutes.value * 60,
      model_alias_rules: modelAliasRules
      , confidence_probe: { ...draft.confidence_probe }
    }
    const saved = probeOnly.value
      ? await upstreamManagementAPI.updateProbeSettings(payload)
      : await upstreamManagementAPI.updateSettings(payload)
    appStore.showSuccess(t('admin.upstreamManagement.saved'))
    emit('saved', saved)
    emit('close')
  } catch (error) {
    appStore.showError(extractApiErrorMessage(error, t('admin.upstreamManagement.saveFailed')))
  } finally {
    saving.value = false
  }
}

function closeDialog() {
  if (!saving.value) emit('close')
}

watch(() => props.show, visible => {
  if (visible) load()
}, { immediate: true })
</script>
