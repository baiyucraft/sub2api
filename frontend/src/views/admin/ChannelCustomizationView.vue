<template>
  <AppLayout>
    <div class="mx-auto max-w-7xl space-y-6" data-testid="channel-customization-page">
      <header class="flex flex-col justify-between gap-4 rounded-lg bg-white p-5 shadow-sm dark:bg-dark-800 sm:flex-row sm:items-center">
        <div class="flex items-start gap-3">
          <div class="rounded-lg bg-primary-50 p-2 text-primary-600 dark:bg-primary-900/20 dark:text-primary-400">
            <Icon name="bolt" size="lg" />
          </div>
          <div>
            <h1 class="text-xl font-semibold text-gray-900 dark:text-white">
              {{ t('admin.customization.title') }}
            </h1>
            <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
              {{ t('admin.customization.description') }}
            </p>
          </div>
        </div>
        <div class="flex items-center gap-2">
          <button
            type="button"
            class="btn btn-secondary"
            :disabled="loading || saving"
            :title="t('admin.customization.refresh')"
            @click="loadSettings"
          >
            <Icon name="refresh" size="md" :class="loading ? 'animate-spin' : ''" />
          </button>
          <button type="button" class="btn btn-primary" :disabled="loading || saving" @click="saveSettings">
            <Icon name="check" size="md" class="mr-2" />
            {{ saving ? t('common.saving') : t('admin.customization.saveAll') }}
          </button>
        </div>
      </header>

      <div v-if="loading" class="flex items-center justify-center py-16 text-sm text-gray-500">
        <span class="mr-2 h-5 w-5 animate-spin rounded-full border-2 border-primary-600 border-t-transparent" />
        {{ t('common.loading') }}
      </div>

      <template v-else>
        <section class="rounded-lg bg-white shadow-sm dark:bg-dark-800" data-testid="customization-rules">
          <div class="flex flex-col justify-between gap-3 border-b border-gray-100 px-5 py-4 dark:border-dark-700 sm:flex-row sm:items-center">
            <div>
              <h2 class="font-semibold text-gray-900 dark:text-white">{{ t('admin.customization.rules') }}</h2>
              <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ t('admin.customization.rulesHint') }}</p>
            </div>
            <button type="button" class="btn btn-secondary btn-sm" @click="openCreate">
              <Icon name="plus" size="sm" class="mr-1.5" />
              {{ t('admin.customization.createRule') }}
            </button>
          </div>

          <div v-if="rules.length === 0" class="px-5 py-12 text-center text-sm text-gray-500 dark:text-gray-400">
            {{ t('admin.customization.noRules') }}
          </div>
          <div v-else class="divide-y divide-gray-100 dark:divide-dark-700">
            <article v-for="(rule, index) in rules" :key="`${index}-${rule.name}`" class="p-5" :data-testid="`customization-rule-${index}`">
              <div class="flex flex-col gap-4 xl:flex-row xl:items-start xl:justify-between">
                <div class="min-w-0 flex-1">
                  <div class="flex flex-wrap items-center gap-3">
                    <span class="text-xs font-semibold text-gray-400">{{ index + 1 }}</span>
                    <h3 class="font-medium text-gray-900 dark:text-white">
                      {{ rule.name || t('admin.customization.unnamedRule') }}
                    </h3>
                    <span class="rounded px-2 py-0.5 text-xs" :class="rule.enabled ? 'bg-green-50 text-green-700 dark:bg-green-900/20 dark:text-green-400' : 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-gray-400'">
                      {{ rule.enabled ? t('admin.customization.enabled') : t('admin.customization.disabled') }}
                    </span>
                  </div>
                  <div class="mt-3 grid gap-3 text-sm text-gray-600 dark:text-gray-400 md:grid-cols-2 xl:grid-cols-4">
                    <div><span class="font-medium text-gray-700 dark:text-gray-300">{{ t('admin.customization.targets') }}:</span> {{ summarizeTargets(rule) }}</div>
                    <div><span class="font-medium text-gray-700 dark:text-gray-300">{{ t('admin.customization.conditions') }}:</span> {{ summarizeConditions(rule) }}</div>
                    <div><span class="font-medium text-gray-700 dark:text-gray-300">{{ t('admin.customization.delay') }}:</span> {{ rule.min_delay_ms }}-{{ rule.max_delay_ms }}ms</div>
                    <div><span class="font-medium text-gray-700 dark:text-gray-300">{{ t('admin.customization.response') }}:</span> {{ rule.status_code }} / {{ rule.content_type }}</div>
                  </div>
                </div>
                <div class="flex shrink-0 items-center gap-1">
                  <Toggle :model-value="rule.enabled" :aria-label="rule.name" @update:model-value="rule.enabled = $event" />
                  <button type="button" class="icon-button" :disabled="index === 0" :title="t('admin.customization.moveUp')" @click="moveRule(index, -1)"><Icon name="arrowUp" size="sm" /></button>
                  <button type="button" class="icon-button" :disabled="index === rules.length - 1" :title="t('admin.customization.moveDown')" @click="moveRule(index, 1)"><Icon name="arrowDown" size="sm" /></button>
                  <button type="button" class="icon-button" :title="t('admin.customization.edit')" @click="openEdit(index)"><Icon name="edit" size="sm" /></button>
                  <button type="button" class="icon-button text-red-500 hover:text-red-600" :title="t('admin.customization.delete')" @click="removeRule(index)"><Icon name="trash" size="sm" /></button>
                </div>
              </div>
            </article>
          </div>
        </section>

        <section class="rounded-lg bg-white shadow-sm dark:bg-dark-800" data-testid="gateway-request-observer-settings">
          <div class="border-b border-gray-100 px-5 py-4 dark:border-dark-700">
            <h2 class="font-semibold text-gray-900 dark:text-white">{{ t('admin.customization.observer') }}</h2>
            <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">{{ t('admin.customization.observerHint') }}</p>
          </div>
          <div class="space-y-5 p-5">
            <div class="rounded-lg border border-sky-200 bg-sky-50 p-4 text-sm text-sky-700 dark:border-sky-800 dark:bg-sky-900/20 dark:text-sky-300">
              <Icon name="infoCircle" size="sm" class="mr-2 inline-block align-text-bottom" />
              {{ t('admin.customization.observerPrivacy') }}
            </div>
            <div class="flex items-center justify-between gap-4">
              <div>
                <label class="font-medium text-gray-900 dark:text-white">{{ t('admin.customization.observerEnabled') }}</label>
                <p class="text-sm text-gray-500 dark:text-gray-400">{{ t('admin.customization.observerEnabledHint') }}</p>
              </div>
              <Toggle v-model="observer.enabled" :aria-label="t('admin.customization.observerEnabled')" />
            </div>
            <div class="grid grid-cols-1 gap-5 border-t border-gray-100 pt-5 dark:border-dark-700 md:grid-cols-2 xl:grid-cols-4">
              <label class="block text-sm"><span class="label">{{ t('admin.customization.keyNames') }}</span><textarea v-model="observerKeyNames" class="input min-h-24 resize-y font-mono text-sm" :placeholder="t('admin.customization.keyNamesPlaceholder')" /></label>
              <label class="block text-sm"><span class="label">{{ t('admin.customization.keyIds') }}</span><textarea v-model="observerKeyIds" class="input min-h-24 resize-y font-mono text-sm" :placeholder="t('admin.customization.listPlaceholder')" /></label>
              <label class="block text-sm"><span class="label">{{ t('admin.customization.userIds') }}</span><textarea v-model="observerUserIds" class="input min-h-24 resize-y font-mono text-sm" :placeholder="t('admin.customization.listPlaceholder')" /></label>
              <label class="block text-sm"><span class="label">{{ t('admin.customization.userEmails') }}</span><textarea v-model="observerUserEmails" class="input min-h-24 resize-y font-mono text-sm" :placeholder="t('admin.customization.emailPlaceholder')" /></label>
            </div>
            <div class="flex flex-wrap gap-x-6 gap-y-2 border-t border-gray-100 pt-4 text-xs text-gray-500 dark:border-dark-700 dark:text-gray-400">
              <span>{{ t('admin.customization.status') }}: <strong :class="observer.enabled ? 'text-emerald-600 dark:text-emerald-400' : 'text-gray-600 dark:text-gray-300'">{{ observer.enabled ? t('admin.customization.observerActive') : t('admin.customization.observerDisabled') }}</strong></span>
              <span class="break-all">{{ t('admin.customization.observerOutput') }}: <code class="font-mono">{{ observer.output_path || defaultObserverPath }}</code></span>
            </div>
          </div>
        </section>
      </template>
    </div>

    <BaseDialog :show="editingIndex !== null" :title="t('admin.customization.editRule')" width="extra-wide" @close="closeEditor">
      <form v-if="editingIndex !== null" id="channel-customization-rule-form" class="grid gap-5" data-testid="customization-rule-form" @submit.prevent="saveRuleDraft">
        <div class="grid gap-4 md:grid-cols-[1fr_9rem]"><label class="block text-sm"><span class="label">{{ t('admin.customization.ruleName') }}</span><input v-model.trim="draft.name" class="input" required /></label><label class="block text-sm"><span class="label">{{ t('admin.customization.enabled') }}</span><Toggle v-model="draft.enabled" :aria-label="t('admin.customization.enabled')" /></label></div>
        <div><p class="label">{{ t('admin.customization.targets') }}</p><div class="grid gap-4 md:grid-cols-2"><label class="block text-sm"><span class="label">{{ t('admin.customization.keyNames') }}</span><textarea v-model="draft.api_key_names" class="input min-h-20" :placeholder="t('admin.customization.keyNamesPlaceholder')" /></label><label class="block text-sm"><span class="label">{{ t('admin.customization.userEmails') }}</span><textarea v-model="draft.user_emails" class="input min-h-20" :placeholder="t('admin.customization.emailPlaceholder')" /></label><label class="block text-sm"><span class="label">{{ t('admin.customization.keyIds') }}</span><textarea v-model="draft.api_key_ids" class="input min-h-20" :placeholder="t('admin.customization.listPlaceholder')" /></label><label class="block text-sm"><span class="label">{{ t('admin.customization.userIds') }}</span><textarea v-model="draft.user_ids" class="input min-h-20" :placeholder="t('admin.customization.listPlaceholder')" /></label></div></div>
        <div><p class="label">{{ t('admin.customization.conditions') }}</p><div class="grid gap-4 md:grid-cols-2"><label class="block text-sm"><span class="label">{{ t('admin.customization.methods') }}</span><textarea v-model="draft.methods" class="input min-h-20" placeholder="GET, POST" /></label><label class="block text-sm"><span class="label">{{ t('admin.customization.exactPaths') }}</span><textarea v-model="draft.exact_paths" class="input min-h-20" placeholder="/v1/models" /></label><label class="block text-sm"><span class="label">{{ t('admin.customization.pathPrefixes') }}</span><textarea v-model="draft.path_prefixes" class="input min-h-20" placeholder="/v1/" /></label><label class="block text-sm"><span class="label">{{ t('admin.customization.userAgentContains') }}</span><textarea v-model="draft.user_agent_contains" class="input min-h-20" /></label><label class="block text-sm md:col-span-2"><span class="label">{{ t('admin.customization.queryParams') }}</span><textarea v-model="draft.query_params" class="input min-h-20 font-mono text-sm" placeholder="check=health|ready" /></label></div></div>
        <div class="grid gap-4 border-t border-gray-100 pt-4 md:grid-cols-2 dark:border-dark-700"><label class="block text-sm"><span class="label">{{ t('admin.customization.minDelay') }}</span><input v-model.number="draft.min_delay_ms" class="input" type="number" min="0" max="10000" /></label><label class="block text-sm"><span class="label">{{ t('admin.customization.maxDelay') }}</span><input v-model.number="draft.max_delay_ms" class="input" type="number" min="0" max="10000" /></label><label class="block text-sm"><span class="label">{{ t('admin.customization.statusCode') }}</span><input v-model.number="draft.status_code" class="input" type="number" min="100" max="599" /></label><label class="block text-sm"><span class="label">{{ t('admin.customization.contentType') }}</span><input v-model.trim="draft.content_type" class="input" placeholder="application/json" /></label><label class="block text-sm md:col-span-2"><span class="label">{{ t('admin.customization.responseBody') }}</span><textarea v-model="draft.response_body" class="input min-h-32 resize-y font-mono text-sm" /></label></div>
      </form>
      <template #footer><div class="flex justify-end gap-3"><button type="button" class="btn btn-secondary" @click="closeEditor">{{ t('admin.customization.cancel') }}</button><button type="submit" form="channel-customization-rule-form" class="btn btn-primary">{{ t('admin.customization.applyRule') }}</button></div></template>
    </BaseDialog>
  </AppLayout>
</template>

<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Icon from '@/components/icons/Icon.vue'
import Toggle from '@/components/common/Toggle.vue'
import { adminAPI } from '@/api/admin'
import type { ChannelCustomizationRule, ChannelCustomizationSettings } from '@/api/admin/channelCustomization'
import { extractApiErrorMessage } from '@/utils/apiError'
import { useAppStore } from '@/stores/app'

interface RuleDraft {
  name: string
  enabled: boolean
  api_key_ids: string
  api_key_names: string
  user_ids: string
  user_emails: string
  methods: string
  exact_paths: string
  path_prefixes: string
  user_agent_contains: string
  query_params: string
  min_delay_ms: number
  max_delay_ms: number
  status_code: number
  content_type: string
  response_body: string
}

const { t } = useI18n()
const appStore = useAppStore()
const loading = ref(true)
const saving = ref(false)
const editingIndex = ref<number | null>(null)
const newRulePending = ref(false)
const rules = ref<ChannelCustomizationRule[]>([])
const defaultObserverPath = '/app/.tmp/maibon-probe-observation/requests.jsonl'
const observer = reactive({ enabled: false, api_key_ids: [] as number[], api_key_names: [] as string[], user_ids: [] as number[], user_emails: [] as string[], output_path: defaultObserverPath })
const observerKeyNames = ref('')
const observerKeyIds = ref('')
const observerUserIds = ref('')
const observerUserEmails = ref('')
const draft = reactive<RuleDraft>(emptyDraft())

function emptyDraft(): RuleDraft {
  return { name: '', enabled: false, api_key_ids: '', api_key_names: '', user_ids: '', user_emails: '', methods: '', exact_paths: '', path_prefixes: '', user_agent_contains: '', query_params: '', min_delay_ms: 0, max_delay_ms: 0, status_code: 200, content_type: 'application/json', response_body: '' }
}

function tokens(value: string): string[] {
  return Array.from(new Set(value.split(/[,;\n，；]+/).map(item => item.trim()).filter(Boolean)))
}

function ids(value: string): number[] {
  return tokens(value).map(Number).filter(value => Number.isSafeInteger(value) && value > 0)
}

function parseQueryParams(value: string): Record<string, string[]> {
  const result: Record<string, string[]> = {}
  for (const line of value.split(/\r?\n/).map(item => item.trim()).filter(Boolean)) {
    const separator = line.indexOf('=')
    const key = (separator < 0 ? line : line.slice(0, separator)).trim()
    if (!key) continue
    result[key] = tokens(separator < 0 ? '' : line.slice(separator + 1).replace(/\|/g, ','))
  }
  return result
}

function formatQueryParams(value: Record<string, string[]>): string {
  return Object.entries(value || {}).map(([key, values]) => `${key}=${values.join('|')}`).join('\n')
}

function normalizeRule(rule: Partial<ChannelCustomizationRule> = {}): ChannelCustomizationRule {
  return { name: '', enabled: false, api_key_ids: [], api_key_names: [], user_ids: [], user_emails: [], methods: [], exact_paths: [], path_prefixes: [], user_agent_contains: [], query_params: {}, min_delay_ms: 0, max_delay_ms: 0, status_code: 200, content_type: 'application/json', response_body: '', ...rule }
}

function setDraft(rule: Partial<ChannelCustomizationRule> = {}) {
  const value = normalizeRule(rule)
  Object.assign(draft, { name: value.name, enabled: value.enabled, api_key_ids: value.api_key_ids.join('\n'), api_key_names: value.api_key_names.join('\n'), user_ids: value.user_ids.join('\n'), user_emails: value.user_emails.join('\n'), methods: value.methods.join('\n'), exact_paths: value.exact_paths.join('\n'), path_prefixes: value.path_prefixes.join('\n'), user_agent_contains: value.user_agent_contains.join('\n'), query_params: formatQueryParams(value.query_params), min_delay_ms: value.min_delay_ms, max_delay_ms: value.max_delay_ms, status_code: value.status_code, content_type: value.content_type, response_body: value.response_body })
}

function draftRule(): ChannelCustomizationRule {
  return normalizeRule({ name: draft.name.trim(), enabled: draft.enabled, api_key_ids: ids(draft.api_key_ids), api_key_names: tokens(draft.api_key_names), user_ids: ids(draft.user_ids), user_emails: tokens(draft.user_emails).map(value => value.toLowerCase()), methods: tokens(draft.methods).map(value => value.toUpperCase()), exact_paths: tokens(draft.exact_paths), path_prefixes: tokens(draft.path_prefixes), user_agent_contains: tokens(draft.user_agent_contains), query_params: parseQueryParams(draft.query_params), min_delay_ms: Number(draft.min_delay_ms) || 0, max_delay_ms: Number(draft.max_delay_ms) || 0, status_code: Number(draft.status_code) || 200, content_type: draft.content_type.trim() || 'application/json', response_body: draft.response_body })
}

function normalizeSettings(data: Partial<ChannelCustomizationSettings>) {
  const value: Partial<ChannelCustomizationSettings['observer']> = data.observer || {}
  Object.assign(observer, { enabled: value.enabled === true, api_key_ids: Array.isArray(value.api_key_ids) ? value.api_key_ids : [], api_key_names: Array.isArray(value.api_key_names) ? value.api_key_names : [], user_ids: Array.isArray(value.user_ids) ? value.user_ids : [], user_emails: Array.isArray(value.user_emails) ? value.user_emails : [], output_path: value.output_path || defaultObserverPath })
  observerKeyNames.value = observer.api_key_names.join('\n')
  observerKeyIds.value = observer.api_key_ids.join('\n')
  observerUserIds.value = observer.user_ids.join('\n')
  observerUserEmails.value = observer.user_emails.join('\n')
  rules.value = Array.isArray(data.rules) ? data.rules.map(normalizeRule) : []
}

async function loadSettings() {
  loading.value = true
  try { normalizeSettings(await adminAPI.channelCustomization.getSettings()) } catch (error) { appStore.showError(extractApiErrorMessage(error, t('admin.customization.loadError'))) } finally { loading.value = false }
}

function openCreate() { setDraft(); newRulePending.value = true; editingIndex.value = rules.value.length }
function openEdit(index: number) { setDraft(rules.value[index]); newRulePending.value = false; editingIndex.value = index }
function closeEditor() { editingIndex.value = null; newRulePending.value = false }
function saveRuleDraft() {
  if (editingIndex.value === null) return
  const value = draftRule()
  if (newRulePending.value) rules.value.push(value)
  else rules.value[editingIndex.value] = value
  closeEditor()
}
function removeRule(index: number) { rules.value.splice(index, 1); if (editingIndex.value === index) closeEditor() }
function moveRule(index: number, offset: number) { const target = index + offset; if (target < 0 || target >= rules.value.length) return; const [rule] = rules.value.splice(index, 1); rules.value.splice(target, 0, rule); if (editingIndex.value === index) editingIndex.value = target }
function summarizeTargets(rule: ChannelCustomizationRule) { const values = [...rule.api_key_names, ...rule.user_emails, ...rule.api_key_ids.map(id => `key:${id}`), ...rule.user_ids.map(id => `user:${id}`)]; return values.length ? values.join(', ') : t('admin.customization.noTargets') }
function summarizeConditions(rule: ChannelCustomizationRule) { const count = rule.methods.length + rule.exact_paths.length + rule.path_prefixes.length + rule.user_agent_contains.length + Object.keys(rule.query_params || {}).length; return count ? t('admin.customization.conditionCount', { count }) : t('admin.customization.noConditions') }

function buildPayload(): ChannelCustomizationSettings {
  return { observer: { enabled: observer.enabled, api_key_ids: ids(observerKeyIds.value), api_key_names: tokens(observerKeyNames.value), user_ids: ids(observerUserIds.value), user_emails: tokens(observerUserEmails.value).map(value => value.toLowerCase()), output_path: observer.output_path }, rules: rules.value.map(normalizeRule) }
}

async function saveSettings() {
  const payload = buildPayload()
  const invalid = payload.rules.some(rule => !rule.name || (!rule.api_key_ids.length && !rule.api_key_names.length && !rule.user_ids.length && !rule.user_emails.length) || (!rule.methods.length && !rule.exact_paths.length && !rule.path_prefixes.length && !rule.user_agent_contains.length && !Object.keys(rule.query_params).length) || rule.min_delay_ms < 0 || rule.min_delay_ms > 10000 || rule.max_delay_ms < 0 || rule.max_delay_ms > 10000 || rule.max_delay_ms < rule.min_delay_ms || rule.status_code < 100 || rule.status_code > 599 || ([204, 205, 304].includes(rule.status_code) && rule.response_body.trim() !== ''))
  if (invalid) { appStore.showError(t('admin.customization.validationError')); return }
  saving.value = true
  try { normalizeSettings(await adminAPI.channelCustomization.updateSettings(payload)); appStore.showSuccess(t('admin.customization.saveSuccess')) } catch (error) { appStore.showError(extractApiErrorMessage(error, t('admin.customization.saveError'))) } finally { saving.value = false }
}

onMounted(loadSettings)
</script>

<style scoped>
.label { @apply mb-1.5 block font-medium text-gray-700 dark:text-gray-300; }
.icon-button { @apply rounded p-2 text-gray-500 transition-colors hover:bg-gray-100 disabled:cursor-not-allowed disabled:opacity-40 dark:hover:bg-dark-700; }
</style>
