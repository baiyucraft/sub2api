<template>
  <section class="space-y-4 rounded-lg border border-gray-200 p-4 dark:border-dark-600" data-testid="codex-account-ticket-settings">
    <div>
      <h3 class="text-sm font-semibold text-gray-900 dark:text-white">{{ t('admin.accounts.stateTicket.title') }}</h3>
      <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ t('admin.accounts.stateTicket.description') }}</p>
    </div>

    <p v-if="loading" class="text-xs text-gray-500">{{ t('common.loading') }}</p>

    <template v-if="status">
      <div class="space-y-2 text-xs text-gray-500 dark:text-gray-400">
        <p v-if="!status.global_enabled" class="rounded bg-amber-50 p-2 text-amber-800 dark:bg-amber-900/20 dark:text-amber-200" data-testid="codex-account-ticket-global-off">
          {{ t('admin.accounts.stateTicket.globalOff') }}
          <a href="/admin/settings?tab=gateway" target="_blank" rel="noopener noreferrer" class="font-medium underline">{{ t('admin.accounts.stateTicket.gatewaySettings') }}</a>
        </p>
        <div data-testid="codex-account-ticket-global-pool">
          <p v-if="status.proxy_configured">{{ t('admin.accounts.stateTicket.globalPoolConfigured', { address: status.proxy_display }) }}</p>
          <p v-else class="text-amber-700 dark:text-amber-300">{{ t('admin.accounts.stateTicket.globalPoolMissing') }}</p>
          <p>{{ t('admin.accounts.stateTicket.globalPoolHint') }}</p>
        </div>
        <p v-if="!status.fixed_proxy_configured" class="rounded bg-amber-50 p-2 text-amber-800 dark:bg-amber-900/20 dark:text-amber-200" data-testid="codex-account-ticket-fixed-proxy-missing">
          {{ t('admin.accounts.stateTicket.fixedProxyMissing') }}
        </p>
      </div>

      <div class="divide-y divide-gray-200 overflow-hidden rounded-lg border border-gray-200 dark:divide-dark-600 dark:border-dark-600">
        <article
          v-for="entry in modelEntries"
          :key="entry.model"
          class="space-y-3 bg-white p-3 dark:bg-dark-800"
          :data-testid="`codex-account-ticket-row-${entry.slug}`"
        >
          <div class="flex flex-wrap items-start justify-between gap-3">
            <div class="min-w-0">
              <div class="flex flex-wrap items-center gap-2">
                <h4 class="text-sm font-semibold text-gray-900 dark:text-white">{{ t(`admin.accounts.stateTicket.models.${entry.slug}`) }}</h4>
                <span class="font-mono text-[10px] text-gray-400">{{ entry.model }}</span>
              </div>
              <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
                {{ t('admin.accounts.stateTicket.targetLength', { length: modelStatus(entry.model).target_length }) }}
              </p>
            </div>
            <Toggle
              v-model="drafts[entry.model].enabled"
              :disabled="busy || (!drafts[entry.model].enabled && !canEnable)"
              :aria-label="t('admin.accounts.stateTicket.enableModel', { model: t(`admin.accounts.stateTicket.models.${entry.slug}`) })"
              :data-testid="`codex-account-ticket-enabled-${entry.slug}`"
            />
          </div>

          <div class="grid gap-3 sm:grid-cols-[minmax(0,180px)_minmax(0,1fr)]">
            <div>
              <label :for="`codex-account-ticket-plan-${entry.slug}-${accountId}`" class="input-label">{{ t('admin.accounts.stateTicket.plan') }}</label>
              <select
                :id="`codex-account-ticket-plan-${entry.slug}-${accountId}`"
                v-model="drafts[entry.model].ticket_plan"
                class="input w-full text-sm"
                :disabled="busy"
                :data-testid="`codex-account-ticket-plan-${entry.slug}`"
              >
                <option value="pro">{{ t('admin.accounts.stateTicket.planPro') }}</option>
                <option value="team">{{ t('admin.accounts.stateTicket.planTeam') }}</option>
              </select>
            </div>

            <div class="grid grid-cols-2 gap-2 text-xs sm:grid-cols-4" aria-live="polite">
              <div class="rounded bg-gray-50 p-2 dark:bg-dark-700" :data-testid="`codex-account-ticket-state-${entry.slug}`">
                <span class="block text-gray-400">{{ t('admin.accounts.stateTicket.status') }}</span>
                <span :class="stateClass(modelStatus(entry.model))">{{ stateLabel(modelStatus(entry.model)) }}</span>
              </div>
              <div class="rounded bg-gray-50 p-2 dark:bg-dark-700" :data-testid="`codex-account-ticket-active-${entry.slug}`">
                <span class="block text-gray-400">{{ t('admin.accounts.stateTicket.active') }}</span>
                <span :class="activeSlot(entry.model) ? 'text-emerald-600 dark:text-emerald-400' : 'text-gray-500'">
                  {{ slotLabel(activeSlot(entry.model)) }}
                </span>
              </div>
              <div class="rounded bg-gray-50 p-2 dark:bg-dark-700" :data-testid="`codex-account-ticket-ready-slot-${entry.slug}`">
                <span class="block text-gray-400">{{ t('admin.accounts.stateTicket.readySlot') }}</span>
                <span :class="modelStatus(entry.model).ready ? 'text-blue-600 dark:text-blue-400' : 'text-gray-500'">
                  {{ slotLabel(modelStatus(entry.model).ready) }}
                </span>
              </div>
              <div class="rounded bg-gray-50 p-2 dark:bg-dark-700" :data-testid="`codex-account-ticket-strikes-${entry.slug}`">
                <span class="block text-gray-400">{{ t('admin.accounts.stateTicket.strikes') }}</span>
                <span :class="modelStatus(entry.model).strikes > 0 ? 'text-amber-700 dark:text-amber-300' : 'text-gray-600 dark:text-gray-300'">
                  {{ modelStatus(entry.model).strikes }}
                </span>
              </div>
            </div>
          </div>

          <div class="space-y-1 text-xs">
            <p v-if="modelStatus(entry.model).refreshing" class="text-blue-600 dark:text-blue-400" :data-testid="`codex-account-ticket-refreshing-${entry.slug}`">
              {{ t('admin.accounts.stateTicket.refreshingModel') }}
            </p>
            <p v-if="modelStatus(entry.model).retry_after" class="text-gray-600 dark:text-gray-300" :data-testid="`codex-account-ticket-retry-after-${entry.slug}`">
              {{ t('admin.accounts.stateTicket.cooldownUntil', { time: formatLocalDate(modelStatus(entry.model).retry_after) }) }}
            </p>
            <p v-if="modelStatus(entry.model).attempts > 0" class="text-gray-500">
              {{ t('admin.accounts.stateTicket.attempts', { count: modelStatus(entry.model).attempts }) }}
            </p>
            <p v-if="modelStatus(entry.model).watchdog.trigger_count > 0" class="text-gray-600 dark:text-gray-300" :data-testid="`codex-account-ticket-watchdog-${entry.slug}`">
              {{ t('admin.accounts.stateTicket.watchdogTriggerCount', { count: modelStatus(entry.model).watchdog.trigger_count }) }}
              <span v-if="watchdogReason(modelStatus(entry.model))"> · {{ watchdogReason(modelStatus(entry.model)) }}</span>
            </p>
            <p v-if="modelStatus(entry.model).last_error" class="break-words text-amber-700 dark:text-amber-300" :data-testid="`codex-account-ticket-error-${entry.slug}`">
              {{ modelStatus(entry.model).last_error }}
            </p>
          </div>

          <div class="flex justify-end">
            <button
              type="button"
              class="btn btn-secondary btn-sm"
              :disabled="!canHarvest(entry.model)"
              :data-testid="`codex-account-ticket-harvest-${entry.slug}`"
              @click="harvest(entry.model)"
            >
              {{ activeSlot(entry.model) ? t('admin.accounts.stateTicket.reacquire') : t('admin.accounts.stateTicket.acquire') }}
            </button>
          </div>
        </article>
      </div>

      <p v-if="proxyChanged" class="text-xs text-amber-700 dark:text-amber-300">{{ t('admin.accounts.stateTicket.fixedProxyUnsaved') }}</p>
      <p v-else-if="dirty" class="text-xs text-gray-500">{{ t('admin.accounts.stateTicket.unsaved') }}</p>

      <div class="flex flex-wrap items-center gap-2">
        <button type="button" class="btn btn-primary btn-sm" :disabled="busy || !dirty || proxyChanged || invalidEnabledDraft" data-testid="codex-account-ticket-save" @click="save">
          {{ t('admin.accounts.stateTicket.save') }}
        </button>
        <span v-if="saving" class="text-xs text-gray-500">{{ t('common.saving') }}</span>
      </div>
      <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.accounts.stateTicket.failureHint') }}</p>
    </template>

    <p v-if="error" role="alert" class="text-xs text-red-600 dark:text-red-400">{{ error }}</p>
    <p v-if="saved" role="status" class="text-xs text-emerald-600 dark:text-emerald-400">{{ t('admin.accounts.stateTicket.saved') }}</p>
    <button v-if="!status && !loading" type="button" class="btn btn-secondary btn-sm" @click="load(true)">{{ t('admin.accounts.stateTicket.retry') }}</button>
  </section>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import Toggle from '@/components/common/Toggle.vue'
import {
  getCodexAccountTicket,
  harvestCodexAccountTicket,
  saveCodexAccountTicket,
  type CodexAccountTicketModelStatus,
  type CodexAccountTicketSlot,
  type CodexAccountTicketStatus,
  type CodexTicketModel,
  type CodexTicketPlan,
} from '@/api/admin/codexTickets'

const props = defineProps<{ accountId: number; visible: boolean; proxyChanged?: boolean }>()
const { t, locale } = useI18n()

const modelEntries: ReadonlyArray<{ model: CodexTicketModel; slug: 'astra' | 'sol' | 'terra' }> = [
  { model: 'gpt-6-astra', slug: 'astra' },
  { model: 'gpt-5.6-sol', slug: 'sol' },
  { model: 'gpt-5.6-terra', slug: 'terra' },
]

type Draft = { enabled: boolean; ticket_plan: CodexTicketPlan }
type DraftMap = Record<CodexTicketModel, Draft>
type StatusMap = Record<CodexTicketModel, CodexAccountTicketModelStatus>

const status = ref<CodexAccountTicketStatus | null>(null)
const modelStatuses = ref<StatusMap>(emptyStatusMap())
const drafts = reactive<DraftMap>(emptyDraftMap())
const loading = ref(false)
const saving = ref(false)
const harvestingModel = ref<CodexTicketModel | null>(null)
const error = ref('')
const saved = ref(false)
let generation = 0
let localRevision = 0
let timer: ReturnType<typeof setTimeout> | undefined

const busy = computed(() => saving.value || harvestingModel.value !== null)
const canEnable = computed(() => !!status.value?.proxy_configured && !!status.value?.fixed_proxy_configured && !props.proxyChanged)
const invalidEnabledDraft = computed(() => modelEntries.some(({ model }) => drafts[model].enabled) && !canEnable.value)
const dirty = computed(() => modelEntries.some(({ model }) => {
  const current = modelStatuses.value[model]
  return drafts[model].enabled !== current.enabled || drafts[model].ticket_plan !== current.ticket_plan
}))

function emptyDraftMap(): DraftMap {
  return {
    'gpt-6-astra': { enabled: false, ticket_plan: 'pro' },
    'gpt-5.6-sol': { enabled: false, ticket_plan: 'pro' },
    'gpt-5.6-terra': { enabled: false, ticket_plan: 'pro' },
  }
}

function defaultModelStatus(model: CodexTicketModel): CodexAccountTicketModelStatus {
  return {
    model,
    ticket_plan: 'pro',
    target_length: 292,
    enabled: false,
    state: 'disabled',
    active: null,
    ready: null,
    strikes: 0,
    refreshing: false,
    last_error: '',
    attempts: 0,
    watchdog: { enabled: false, trigger_count: 0 },
  }
}

function emptyStatusMap(): StatusMap {
  return {
    'gpt-6-astra': defaultModelStatus('gpt-6-astra'),
    'gpt-5.6-sol': defaultModelStatus('gpt-5.6-sol'),
    'gpt-5.6-terra': defaultModelStatus('gpt-5.6-terra'),
  }
}

function legacyModelStatus(response: CodexAccountTicketStatus, model: CodexTicketModel): CodexAccountTicketModelStatus | null {
  if (response.model !== model || response.enabled === undefined) return null
  const active = response.ticket_usable
    ? {
        remaining_seconds: response.remaining_seconds,
        captured_at: response.captured_at,
        expires_at: response.expires_at,
        ticket_usable: true,
      }
    : null
  return {
    ...defaultModelStatus(model),
    enabled: response.enabled,
    ticket_plan: response.ticket_plan ?? 'pro',
    target_length: response.target_length ?? (response.ticket_plan === 'team' ? 332 : 292),
    state: response.state ?? 'disabled',
    active,
    refreshing: response.refreshing ?? false,
    retry_after: response.retry_after,
    last_error: response.last_error ?? '',
    attempts: response.attempts ?? 0,
    watchdog: response.watchdog ?? { enabled: false, trigger_count: 0 },
  }
}

function normalizeResponse(response: CodexAccountTicketStatus): StatusMap {
  const next = emptyStatusMap()
  for (const { model } of modelEntries) {
    const current = response.models?.[model] ?? legacyModelStatus(response, model)
    if (!current) continue
    next[model] = {
      ...defaultModelStatus(model),
      ...current,
      model,
      strikes: current.strikes ?? 0,
      refreshing: current.refreshing ?? false,
      last_error: current.last_error ?? '',
      attempts: current.attempts ?? 0,
      watchdog: current.watchdog ?? { enabled: current.enabled && response.global_enabled, trigger_count: 0 },
    }
  }
  return next
}

function syncDrafts(next: StatusMap) {
  for (const { model } of modelEntries) {
    drafts[model].enabled = next[model].enabled
    drafts[model].ticket_plan = next[model].ticket_plan
  }
}

function modelStatus(model: CodexTicketModel) {
  return modelStatuses.value[model]
}

function activeSlot(model: CodexTicketModel): CodexAccountTicketSlot | null {
  const current = modelStatus(model)
  if (current.active) return current.active
  if (!current.ticket_usable) return null
  return {
    remaining_seconds: current.remaining_seconds,
    captured_at: current.captured_at,
    expires_at: current.expires_at,
    ticket_usable: true,
  }
}

function slotLabel(slot?: CodexAccountTicketSlot | null) {
  if (!slot) return t('admin.accounts.stateTicket.none')
  return t('admin.accounts.stateTicket.remaining', { time: formatRemaining(slot.remaining_seconds ?? 0) })
}

function stateLabel(current: CodexAccountTicketModelStatus) {
  if (current.refreshing && activeSlot(current.model as CodexTicketModel)) {
    return t('admin.accounts.stateTicket.refreshingModel')
  }
  return t(`admin.accounts.stateTicket.states.${current.state}`)
}

function stateClass(current: CodexAccountTicketModelStatus) {
  if (activeSlot(current.model as CodexTicketModel)) return 'text-emerald-600 dark:text-emerald-400'
  if (current.state === 'error') return 'text-amber-700 dark:text-amber-300'
  return 'text-gray-600 dark:text-gray-300'
}

function watchdogReason(current: CodexAccountTicketModelStatus) {
  if (current.watchdog.last_reason === 'model_mismatch') return t('admin.accounts.stateTicket.watchdogModelMismatch')
  if (current.watchdog.last_reason === 'state_312') return t('admin.accounts.stateTicket.watchdogState312')
  return ''
}

function formatRemaining(seconds: number) {
  const total = Math.max(0, Math.floor(seconds))
  return `${Math.floor(total / 60)}m ${String(total % 60).padStart(2, '0')}s`
}

function formatLocalDate(value?: string) {
  if (!value) return ''
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return ''
  return date.toLocaleString(locale.value, {
    year: 'numeric', month: '2-digit', day: '2-digit',
    hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false,
  })
}

function canHarvest(model: CodexTicketModel) {
  const current = modelStatus(model)
  return !busy.value
    && !dirty.value
    && !props.proxyChanged
    && !!status.value?.global_enabled
    && !!status.value?.proxy_configured
    && !!status.value?.fixed_proxy_configured
    && current.enabled
    && current.state !== 'harvesting'
}

watch(drafts, () => { saved.value = false }, { deep: true, flush: 'sync' })

async function load(initial = false) {
  const currentGeneration = generation
  const currentRevision = localRevision
  if (initial) loading.value = true
  try {
    const next = await getCodexAccountTicket(props.accountId)
    if (generation !== currentGeneration || localRevision !== currentRevision || !props.visible) return
    const normalized = normalizeResponse(next)
    status.value = next
    modelStatuses.value = normalized
    if (initial) syncDrafts(normalized)
    error.value = ''
  } catch {
    if (generation === currentGeneration && localRevision === currentRevision) error.value = t('admin.accounts.stateTicket.loadFailed')
  } finally {
    if (generation === currentGeneration) loading.value = false
  }
}

function schedulePoll() {
  timer = setTimeout(async () => {
    const currentGeneration = generation
    if (!props.visible) return
    if (!busy.value) await load()
    if (generation === currentGeneration && props.visible) schedulePoll()
  }, 3000)
}

async function save() {
  if (!status.value || busy.value || props.proxyChanged || invalidEnabledDraft.value) return
  saving.value = true
  saved.value = false
  error.value = ''
  localRevision++
  const currentGeneration = generation
  try {
    const next = await saveCodexAccountTicket(props.accountId, {
      models: {
        'gpt-6-astra': { ...drafts['gpt-6-astra'] },
        'gpt-5.6-sol': { ...drafts['gpt-5.6-sol'] },
        'gpt-5.6-terra': { ...drafts['gpt-5.6-terra'] },
      },
    })
    if (generation !== currentGeneration) return
    const normalized = normalizeResponse(next)
    status.value = next
    modelStatuses.value = normalized
    syncDrafts(normalized)
    saved.value = true
  } catch {
    if (generation === currentGeneration) error.value = t('admin.accounts.stateTicket.saveFailed')
  } finally {
    if (generation === currentGeneration) saving.value = false
  }
}

async function harvest(model: CodexTicketModel) {
  if (!canHarvest(model)) return
  harvestingModel.value = model
  saved.value = false
  error.value = ''
  localRevision++
  const currentGeneration = generation
  try {
    const next = await harvestCodexAccountTicket(props.accountId, model)
    if (generation !== currentGeneration) return
    status.value = next
    modelStatuses.value = normalizeResponse(next)
  } catch {
    if (generation === currentGeneration) error.value = t('admin.accounts.stateTicket.harvestFailed')
  } finally {
    if (generation === currentGeneration) harvestingModel.value = null
  }
}

watch(() => [props.accountId, props.visible] as const, async () => {
  generation++
  const currentGeneration = generation
  clearTimeout(timer)
  status.value = null
  modelStatuses.value = emptyStatusMap()
  syncDrafts(modelStatuses.value)
  error.value = ''
  saved.value = false
  saving.value = false
  harvestingModel.value = null
  loading.value = false
  if (!props.visible) return
  await load(true)
  if (generation === currentGeneration && props.visible) schedulePoll()
}, { immediate: true })

onBeforeUnmount(() => { generation++; clearTimeout(timer) })
</script>
