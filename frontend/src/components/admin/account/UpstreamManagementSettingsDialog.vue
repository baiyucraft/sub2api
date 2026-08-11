<template>
  <BaseDialog :show="show" :title="t('admin.upstreamManagement.settings.title')" width="wide" @close="closeDialog">
    <div v-if="loading" class="flex min-h-64 items-center justify-center text-sm text-gray-500 dark:text-gray-400">
      <Icon name="refresh" size="md" class="mr-2 animate-spin" />
      {{ t('common.loading') }}
    </div>

    <div v-else class="space-y-6">
      <section class="rounded-2xl border border-gray-200 bg-gray-50/70 p-5 dark:border-dark-700 dark:bg-dark-800/60">
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

      <section class="rounded-2xl border border-gray-200 bg-white p-5 dark:border-dark-700 dark:bg-dark-900/60">
        <div>
          <h4 class="text-base font-semibold text-gray-900 dark:text-white">{{ t('admin.upstreamManagement.probeModels.title') }}</h4>
          <p class="mt-1 text-sm leading-6 text-gray-500 dark:text-gray-400">{{ t('admin.upstreamManagement.probeModels.description') }}</p>
        </div>
        <div class="mt-5 grid gap-4 lg:grid-cols-3">
          <label v-for="platform in platforms" :key="platform" class="space-y-1.5">
            <span class="text-sm font-medium text-gray-700 dark:text-gray-200">{{ platformLabels[platform] }}</span>
            <Select
              v-model="draft.probe_models[platform]"
              :options="candidateOptions[platform]"
              searchable
              creatable
              :creatable-prefix="t('admin.upstreamManagement.probeModels.useCustom')"
              :search-placeholder="t('admin.upstreamManagement.probeModels.search')"
              :placeholder="t('admin.upstreamManagement.probeModels.placeholder')"
            />
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
import upstreamManagementAPI, { type UpstreamManagementSettings } from '@/api/admin/upstreamManagement'
import { useAppStore } from '@/stores/app'
import { extractApiErrorMessage } from '@/utils/apiError'
import BaseDialog from '@/components/common/BaseDialog.vue'
import HelpTooltip from '@/components/common/HelpTooltip.vue'
import Toggle from '@/components/common/Toggle.vue'
import Select, { type SelectOption } from '@/components/common/Select.vue'
import Icon from '@/components/icons/Icon.vue'

const props = defineProps<{ show: boolean }>()
const emit = defineEmits<{ (event: 'close'): void; (event: 'saved', value: UpstreamManagementSettings): void }>()
const { t } = useI18n()
const appStore = useAppStore()

const platforms = ['openai', 'anthropic', 'gemini'] as const
type ProbePlatform = (typeof platforms)[number]
const platformLabels: Record<ProbePlatform, string> = { openai: 'OpenAI', anthropic: 'Anthropic', gemini: 'Gemini' }
const defaults: UpstreamManagementSettings = {
  ttft_guard: { enabled: false, degradation_ttft_seconds: 20, min_samples: 5 },
  probe_models: { openai: 'gpt-4o-mini', anthropic: 'claude-3-5-haiku-latest', gemini: 'gemini-2.0-flash' },
  probe_interval_seconds: 300
}
const draft = reactive<UpstreamManagementSettings>(structuredClone(defaults))
const probeIntervalMinutes = ref(5)
const candidates = reactive<Record<ProbePlatform, string[]>>({ openai: [], anthropic: [], gemini: [] })
const loading = ref(false)
const saving = ref(false)

const candidateOptions = computed<Record<ProbePlatform, SelectOption[]>>(() => ({
  openai: candidates.openai.map(value => ({ value, label: value })),
  anthropic: candidates.anthropic.map(value => ({ value, label: value })),
  gemini: candidates.gemini.map(value => ({ value, label: value }))
}))

const valid = computed(() => {
  const threshold = Number(draft.ttft_guard.degradation_ttft_seconds)
  const samples = Number(draft.ttft_guard.min_samples)
  const intervalMinutes = Number(probeIntervalMinutes.value)
  return Number.isFinite(threshold) && threshold >= 5 && threshold <= 300 &&
    Number.isInteger(samples) && samples >= 2 && samples <= 20 &&
    Number.isInteger(intervalMinutes) && intervalMinutes >= 1 && intervalMinutes <= 60 &&
    platforms.every(platform => {
      const value = draft.probe_models[platform]?.trim() || ''
      return value.length > 0 && value.length <= 120
    })
})

async function load() {
  loading.value = true
  try {
    const [settings, options] = await Promise.all([
      upstreamManagementAPI.getSettings(),
      upstreamManagementAPI.getProbeModelCandidates()
    ])
    Object.assign(draft.ttft_guard, settings.ttft_guard)
    Object.assign(draft.probe_models, settings.probe_models)
    draft.probe_interval_seconds = settings.probe_interval_seconds ?? defaults.probe_interval_seconds
    probeIntervalMinutes.value = Math.max(1, Math.min(60, Math.round(draft.probe_interval_seconds / 60)))
    for (const platform of platforms) candidates[platform] = options.candidates[platform] || []
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
    const payload: UpstreamManagementSettings = {
      ttft_guard: { ...draft.ttft_guard },
      probe_models: {
        openai: draft.probe_models.openai.trim(),
        anthropic: draft.probe_models.anthropic.trim(),
        gemini: draft.probe_models.gemini.trim()
      },
      probe_interval_seconds: probeIntervalMinutes.value * 60
    }
    const saved = await upstreamManagementAPI.updateSettings(payload)
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
