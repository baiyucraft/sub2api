<template>
  <AppLayout>
    <div class="space-y-6">
      <div class="flex flex-wrap items-center justify-between gap-3 border-b border-gray-200 pb-4 dark:border-dark-700">
        <div class="min-w-0">
          <button type="button" class="mb-2 inline-flex items-center gap-1 text-sm text-gray-500 hover:text-gray-900 dark:hover:text-white" @click="router.push('/admin/plugins')">
            <Icon name="chevronLeft" size="sm" />
            {{ t('admin.plugins.backToList') }}
          </button>
          <h1 class="truncate text-xl font-semibold text-gray-900 dark:text-white">{{ plugin?.name || t('admin.plugins.nativePageTitle') }}</h1>
          <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">{{ definition?.description || plugin?.description || t('admin.plugins.nativePageDescription') }}</p>
        </div>
        <div class="flex items-center gap-2">
          <span v-if="plugin" class="rounded bg-gray-100 px-2 py-1 text-xs text-gray-600 dark:bg-dark-700 dark:text-gray-300">v{{ plugin.version }}</span>
          <span v-if="plugin" class="rounded px-2 py-1 text-xs font-medium" :class="stateClass(plugin.state)">{{ t(`admin.plugins.${plugin.state}`) }}</span>
          <button type="button" class="btn btn-secondary btn-sm" :disabled="loading || saving || refreshing || actionRunning" @click="refreshPage">
            <Icon name="refresh" size="sm" :class="{ 'animate-spin': refreshing }" />
            {{ t('common.refresh') }}
          </button>
        </div>
      </div>

      <div v-if="error" class="border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700 dark:border-red-900/50 dark:bg-red-950/20 dark:text-red-300" role="alert">
        {{ error }}
      </div>
      <div v-else-if="loading" class="flex min-h-64 items-center justify-center text-sm text-gray-500">{{ t('common.loading') }}</div>
      <div v-else-if="definition" class="space-y-5">
        <div class="flex flex-wrap items-start justify-between gap-3">
          <div>
            <h2 class="text-lg font-semibold text-gray-900 dark:text-white">{{ definition.title }}</h2>
            <p v-if="definition.description" class="mt-1 text-sm text-gray-500 dark:text-gray-400">{{ definition.description }}</p>
          </div>
          <span v-if="!isRunning" class="rounded bg-amber-100 px-2 py-1 text-xs text-amber-800 dark:bg-amber-900/30 dark:text-amber-200">{{ t('admin.plugins.actionUnavailable') }}</span>
        </div>

        <NativePluginNode
          v-for="(node, index) in definition.layout"
          :key="node.id || `${node.type}-${index}`"
          :node="node"
          :context="emptyContext"
          :model="model"
          :disabled="saving"
          :actions-disabled="actionRunning || !isRunning"
          @write="writeValue"
          @action="runAction"
        />

        <div v-if="dirty" class="sticky bottom-4 flex flex-wrap items-center justify-between gap-3 border border-amber-200 bg-amber-50 px-4 py-3 text-sm shadow-sm dark:border-amber-900/50 dark:bg-amber-950/30">
          <span class="text-amber-800 dark:text-amber-200">{{ t('admin.plugins.unsavedNativeConfig') }}</span>
          <div class="flex gap-2">
            <button type="button" class="btn btn-secondary btn-sm" :disabled="saving" @click="resetDraft">{{ t('common.cancel') }}</button>
            <button type="button" class="btn btn-primary btn-sm" :disabled="saving" @click="saveConfigDraft"><Icon name="check" size="sm" />{{ saving ? t('common.processing') : t('common.save') }}</button>
          </div>
        </div>
      </div>
    </div>

    <PluginSecretsDialog
      v-if="secretsEditor"
      :key="secretsEditor.generation"
      :name="plugin?.name || ''"
      :fields="secretsEditor.fields"
      :configured="secretFlags"
      :loading="secretsLoading"
      :ready="secretsReady"
      :saving="secretsSaving"
      :error="secretsError"
      @cancel="closeSecretsEditor"
      @save="saveSecrets"
    />
    <TotpStepUpDialog :controller="stepUp" />
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, defineComponent, h, onBeforeUnmount, onMounted, reactive, ref, type VNode, watch } from 'vue'
import { onBeforeRouteLeave, useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import Icon from '@/components/icons/Icon.vue'
import PluginSecretsDialog from '@/components/admin/plugins/PluginSecretsDialog.vue'
import TotpStepUpDialog from '@/components/auth/TotpStepUpDialog.vue'
import { adminAPI, type NativePluginAdminUI, type NativePluginUINode, type NativePluginUITableColumn, type PluginInstallation } from '@/api/admin'
import { useAppStore } from '@/stores'
import { isStepUpBlocked, isStepUpCancelled, stepUpBlockReason, useStepUp } from '@/composables/useStepUp'

interface NativeModel {
  config: Record<string, unknown>
  resources: unknown
  status: unknown
  local: Record<string, unknown>
}

interface NativeContext {
  item?: unknown
  key?: string | number
}

function record(value: unknown): Record<string, unknown> {
  return value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : {}
}

function safeSegments(path: string): string[] | null {
  if (!path.startsWith('/') || path.includes('\0')) return null
  const rawSegments = path.slice(1).split('/')
  const segments: string[] = []
  for (const rawSegment of rawSegments) {
    let segment = ''
    for (let index = 0; index < rawSegment.length; index += 1) {
      if (rawSegment[index] !== '~') {
        segment += rawSegment[index]
        continue
      }
      const escape = rawSegment[index + 1]
      if (escape !== '0' && escape !== '1') return null
      segment += escape === '0' ? '~' : '/'
      index += 1
    }
    segments.push(segment)
  }
  if (segments.some(segment => ['__proto__', 'prototype', 'constructor'].includes(segment))) return null
  return segments
}

function resolvePath(model: NativeModel, path: string, context: NativeContext): unknown {
  const segments = safeSegments(path)
  if (!segments?.length) return undefined
  const root = segments.shift()
  let value: unknown = root === 'config' ? model.config : root === 'resources' ? model.resources : root === 'status' ? model.status : root === 'local' ? model.local : root === 'item' ? context.item : undefined
  for (const segment of segments) {
    if (value === null || value === undefined) return undefined
    if (Array.isArray(value)) value = value[Number(segment)]
    else if (typeof value === 'object' && Object.prototype.hasOwnProperty.call(value, segment)) value = (value as Record<string, unknown>)[segment]
    else return undefined
  }
  return value
}

function setPath(model: NativeModel, path: string, value: unknown): boolean {
  const segments = safeSegments(path)
  if (!segments?.length || segments[0] !== 'config' || segments.includes('_host_secrets')) return false
  let cursor: Record<string, unknown> = model.config
  for (let index = 1; index < segments.length - 1; index += 1) {
    const segment = segments[index]
    const next = cursor[segment]
    if (next && typeof next === 'object' && !Array.isArray(next)) cursor = next as Record<string, unknown>
    else cursor = cursor[segment] = {}
  }
  cursor[segments.at(-1)!] = value
  return true
}

function conditionMatches(node: NativePluginUINode, model: NativeModel, context: NativeContext): boolean {
  const condition = node.condition
  if (!condition) return true
  const actual = resolvePath(model, condition.path, context)
  if (condition.op === 'truthy') return Boolean(actual)
  if (condition.op === 'falsy') return !actual
  if (condition.op === 'eq') return actual === condition.value
  if (condition.op === 'ne') return actual !== condition.value
  return Array.isArray(condition.value) && condition.value.includes(actual)
}

function isHostAction(action?: string): boolean {
  return action === 'config.save' || action === 'config.refresh' || action === 'plugin.secrets.edit'
}

let NativePluginNode: ReturnType<typeof defineComponent>
NativePluginNode = defineComponent({
  name: 'NativePluginNode',
  props: {
    node: { type: Object as () => NativePluginUINode, required: true },
    context: { type: Object as () => NativeContext, required: true },
    model: { type: Object as () => NativeModel, required: true },
    disabled: { type: Boolean, default: false },
    actionsDisabled: { type: Boolean, default: false },
  },
  emits: ['write', 'action'],
  setup(props, { emit }) {
    const tabs = ref(0)
    const expanded = ref(true)
    const value = computed(() => props.node.bind ? resolvePath(props.model, props.node.bind, props.context) : undefined)
    const visible = computed(() => conditionMatches(props.node, props.model, props.context))
    const children = computed(() => props.node.children || [])
    const write = (next: unknown) => { if (!props.disabled && props.node.write) emit('write', props.node.write, next) }
    const action = () => emit('action', props.node, props.context)
    const renderChildren: (nodes?: NativePluginUINode[], context?: NativeContext) => VNode[] = (nodes = children.value, context = props.context) => nodes.map((child: NativePluginUINode, index: number) => h(NativePluginNode, {
      key: child.id || `${child.type}-${index}`,
      node: child,
      context,
      model: props.model,
      disabled: props.disabled,
      actionsDisabled: props.actionsDisabled,
      onWrite: (path: string, next: unknown) => emit('write', path, next),
      onAction: (node: NativePluginUINode, nodeContext: NativeContext) => emit('action', node, nodeContext),
    }))
    return () => {
      if (!visible.value) return null
      const node = props.node
      const icon = node.icon ? h(Icon, { name: node.icon as never, size: 'sm' }) : null
      const title = node.title ? h('h3', { class: 'mb-2 flex items-center gap-2 text-sm font-semibold text-gray-900 dark:text-white' }, [icon, node.title]) : null
      const description = node.description ? h('p', { class: 'mb-3 text-xs text-gray-500 dark:text-gray-400' }, node.description) : null
      if (['stack', 'grid', 'section', 'action_group'].includes(node.type)) {
        const className = node.type === 'grid' ? 'grid grid-cols-1 gap-4 md:grid-cols-2' : 'space-y-4'
        return h('section', { class: node.type === 'section' ? 'border border-gray-200 p-4 dark:border-dark-700' : className }, [title, description, ...renderChildren()])
      }
      if (node.type === 'tabs') {
        const selected = children.value[tabs.value] || children.value[0]
        return h('section', { class: 'space-y-3' }, [
          h('div', { class: 'flex flex-wrap gap-1 border-b border-gray-200 dark:border-dark-700' }, children.value.map((child: NativePluginUINode, index: number) => h('button', { type: 'button', class: ['inline-flex items-center gap-1 px-3 py-2 text-sm', tabs.value === index ? 'border-b-2 border-primary-600 text-primary-700 dark:text-primary-300' : 'text-gray-500'], onClick: () => { tabs.value = index } }, [child.icon ? h(Icon, { name: child.icon as never, size: 'sm' }) : null, child.title || child.id || String(index + 1)]))),
          selected ? h(NativePluginNode, { node: selected, context: props.context, model: props.model, disabled: props.disabled, actionsDisabled: props.actionsDisabled, onWrite: (path: string, next: unknown) => emit('write', path, next), onAction: (child: NativePluginUINode, childContext: NativeContext) => emit('action', child, childContext) }) : null,
        ])
      }
      if (node.type === 'accordion') return h('section', { class: 'border border-gray-200 dark:border-dark-700' }, [h('button', { type: 'button', class: 'flex w-full items-center justify-between px-4 py-3 text-left text-sm font-medium', onClick: () => { expanded.value = !expanded.value } }, [node.title || node.id || '', h('span', expanded.value ? '−' : '+')]), expanded.value ? h('div', { class: 'border-t border-gray-200 p-4 dark:border-dark-700' }, [description, ...renderChildren()]) : null])
      if (node.type === 'repeat' || node.type === 'matrix') {
        const items = Array.isArray(value.value) ? value.value : []
        return h('section', { class: 'space-y-3' }, [title, description, ...items.map((item, index) => h('div', { key: index, class: 'border border-gray-200 p-4 dark:border-dark-700' }, renderChildren(node.children, { item, key: index })))])
      }
      if (node.type === 'text' || node.type === 'empty') return h('p', { class: 'text-sm text-gray-600 dark:text-gray-300' }, node.title || String(value.value ?? ''))
      if (node.type === 'alert') return h('div', { class: 'border border-amber-200 bg-amber-50 px-3 py-2 text-sm text-amber-800 dark:border-amber-900/50 dark:bg-amber-950/20 dark:text-amber-200' }, [title, node.description || String(value.value ?? '')])
      if (node.type === 'badge') return h('span', { class: 'inline-flex rounded bg-gray-100 px-2 py-1 text-xs text-gray-700 dark:bg-dark-700 dark:text-gray-200' }, node.title || String(value.value ?? ''))
      if (node.type === 'stat') return h('div', { class: 'border border-gray-200 p-4 dark:border-dark-700' }, [h('p', { class: 'text-xs text-gray-500' }, node.title), h('p', { class: 'mt-1 text-xl font-semibold text-gray-900 dark:text-white' }, String(value.value ?? '-'))])
      if (node.type === 'key_value') return h('dl', { class: 'grid grid-cols-[auto,1fr] gap-x-4 gap-y-2 text-sm' }, Object.entries(record(value.value)).map(([key, item]) => [h('dt', { class: 'text-gray-500' }, key), h('dd', { class: 'text-gray-900 dark:text-gray-200' }, String(item ?? '-'))]))
      if (node.type === 'table' || node.type === 'log_list') {
        const rows = Array.isArray(value.value) ? value.value : []
        const columns = node.columns || []
        return h('div', { class: 'overflow-x-auto border border-gray-200 dark:border-dark-700' }, [h('table', { class: 'min-w-full text-left text-sm' }, [h('thead', { class: 'bg-gray-50 dark:bg-dark-800' }, h('tr', columns.map((column: NativePluginUITableColumn) => h('th', { class: 'px-3 py-2 font-medium text-gray-500' }, column.label)))), h('tbody', rows.map((row, index) => h('tr', { key: index, class: 'border-t border-gray-100 dark:border-dark-700' }, columns.map((column: NativePluginUITableColumn) => h('td', { class: 'px-3 py-2 text-gray-700 dark:text-gray-200' }, String(column.bind ? resolvePath(props.model, column.bind, { item: row, key: index }) ?? '-' : record(row)[column.key] ?? '-'))))))])])
      }
      if (['text_input', 'number_input', 'search'].includes(node.type)) return h('label', { class: 'block space-y-1' }, [title || h('span', { class: 'text-sm font-medium' }, node.id || ''), h('input', { class: 'input w-full', type: node.type === 'number_input' ? 'number' : node.type === 'search' ? 'search' : 'text', value: value.value ?? '', readonly: node.read_only || props.disabled, onInput: (event: Event) => write(node.type === 'number_input' ? Number((event.target as HTMLInputElement).value) : (event.target as HTMLInputElement).value) }), description])
      if (node.type === 'switch' || node.type === 'checkbox') return h('label', { class: 'flex items-center gap-2 text-sm text-gray-700 dark:text-gray-200' }, [h('input', { type: 'checkbox', checked: value.value === true, disabled: node.read_only || props.disabled, onChange: (event: Event) => write((event.target as HTMLInputElement).checked) }), node.title || node.id || ''])
      if (node.type === 'select' || node.type === 'multiselect') return h('label', { class: 'block space-y-1' }, [title || h('span', { class: 'text-sm font-medium' }, node.id || ''), h('select', { class: 'input w-full', multiple: node.type === 'multiselect', disabled: node.read_only || props.disabled, value: value.value as string | string[] | undefined, onChange: (event: Event) => { const target = event.target as HTMLSelectElement; write(node.type === 'multiselect' ? [...target.selectedOptions].map((option: HTMLOptionElement) => option.value) : target.value) } }, (node.options || []).map(option => h('option', { value: option.value }, option.label)))])
      if (node.type === 'button') return h('button', { type: 'button', class: 'btn btn-secondary btn-sm inline-flex items-center gap-1', disabled: props.disabled || (props.actionsDisabled && !isHostAction(node.action)), onClick: action }, [icon, node.title || node.action || ''])
      return h('div', { class: 'text-sm text-gray-500' }, node.description || node.title || node.type)
    }
  },
})

const route = useRoute()
const router = useRouter()
const { t } = useI18n()
const appStore = useAppStore()
const stepUp = useStepUp()
const plugin = ref<PluginInstallation | null>(null)
const definition = ref<NativePluginAdminUI | null>(null)
const loading = ref(true)
const refreshing = ref(false)
const saving = ref(false)
const actionRunning = ref(false)
const error = ref('')
const draft = ref<Record<string, unknown>>({})
const savedSnapshot = ref('')
const resources = ref<unknown>({})
const status = ref<unknown>({})
const local = reactive<Record<string, unknown>>({})
let timer: ReturnType<typeof setInterval> | undefined
let loadGeneration = 0

function clearStatusTimer(): void {
  if (timer) clearInterval(timer)
  timer = undefined
}

const model = computed<NativeModel>(() => ({ config: draft.value, resources: resources.value, status: status.value, local }))
const emptyContext: NativeContext = {}
const dirty = computed(() => JSON.stringify(draft.value) !== savedSnapshot.value)
const isRunning = computed(() => plugin.value?.runtime_healthy === true && plugin.value.state === 'enabled')
const secretsEditor = ref<{ generation: number; fields: string[] } | null>(null)
const secretFlags = ref<Record<string, boolean>>({})
const secretsLoading = ref(false)
const secretsReady = ref(false)
const secretsSaving = ref(false)
const secretsError = ref('')

function stateClass(state: PluginInstallation['state']): string {
  if (state === 'enabled') return 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300'
  if (state === 'error' || state === 'incompatible') return 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-300'
  if (state === 'starting') return 'bg-amber-100 text-amber-700 dark:bg-amber-900/30 dark:text-amber-300'
  return 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-gray-300'
}

function message(errorValue: unknown): string {
  return errorValue instanceof Error && errorValue.message ? errorValue.message : t('admin.plugins.nativePageLoadFailed')
}

function writeValue(path: string, value: unknown): void {
  const segments = safeSegments(path)
  if (!segments?.length || segments[0] !== 'config' || segments.includes('_host_secrets')) return
  if (!setPath(model.value, path, value)) return
  draft.value = { ...draft.value }
}

async function loadStatus(): Promise<void> {
  if (!plugin.value) return
  try { status.value = await adminAPI.plugins.status(plugin.value.id) } catch { status.value = { healthy: false } }
}

async function loadAll(): Promise<void> {
  const generation = ++loadGeneration
  const key = String(route.params.pluginKey || '')
  if (!key) { error.value = t('admin.plugins.nativePageNotFound'); loading.value = false; return }
  clearStatusTimer()
  loading.value = true
  error.value = ''
  try {
    const current = await adminAPI.plugins.byKey(key)
    if (current.manifest.ui?.type !== 'native' || !current.manifest.ui.definition) throw new Error(t('admin.plugins.nativePageUnavailable'))
    const [page, config] = await Promise.all([adminAPI.plugins.adminUI(current.id), adminAPI.plugins.getConfig(current.id)])
    if (generation !== loadGeneration) return
    plugin.value = current
    definition.value = page
    draft.value = { ...config }
    delete draft.value._host_secrets
    savedSnapshot.value = JSON.stringify(draft.value)
    secretFlags.value = { ...(config._host_secrets || {}) }
    try { resources.value = await adminAPI.plugins.resources(current.id) } catch { resources.value = {} }
    if (generation !== loadGeneration) return
    await loadStatus()
    timer = setInterval(() => {
      if (document.visibilityState === 'visible' && !saving.value && !actionRunning.value && !refreshing.value) void loadStatus()
    }, page.poll_interval_seconds * 1000)
  } catch (errorValue) {
    if (generation === loadGeneration) error.value = message(errorValue)
  } finally {
    if (generation === loadGeneration) loading.value = false
  }
}

async function refreshPage(): Promise<void> {
  if (dirty.value && !window.confirm(t('admin.plugins.leaveNativePageConfirm'))) return
  await loadAll()
}

function resetDraft(): void {
  try { draft.value = JSON.parse(savedSnapshot.value) as Record<string, unknown> } catch { draft.value = {} }
}

async function saveConfigDraft(): Promise<void> {
  if (!plugin.value || !dirty.value || saving.value) return
  saving.value = true
  try {
    const result = await stepUp.run(() => adminAPI.plugins.saveConfig(plugin.value!.id, draft.value))
    const next = { ...result }
    delete next._host_secrets
    draft.value = next
    savedSnapshot.value = JSON.stringify(next)
    appStore.showSuccess(t('common.saved'))
  } catch (errorValue) {
    if (!isStepUpCancelled(errorValue)) reportError(errorValue)
  } finally { saving.value = false }
}

function reportError(errorValue: unknown): void {
  if (isStepUpBlocked(errorValue)) {
    appStore.showError(stepUpBlockReason(errorValue) === 'STEP_UP_ADMIN_API_KEY_FORBIDDEN' ? t('stepUp.adminApiKeyForbidden') : t('stepUp.notEnabled'))
  } else appStore.showError(message(errorValue))
}

async function runAction(node: NativePluginUINode, context: NativeContext): Promise<void> {
  if (!plugin.value || !node.action || saving.value || actionRunning.value) return
  if (node.confirm && !window.confirm(node.description || t('admin.plugins.nativeActionConfirm'))) return
  if (node.action === 'config.save') { await saveConfigDraft(); return }
  if (node.action === 'config.refresh') {
    refreshing.value = true
    try { await loadStatus() } finally { refreshing.value = false }
    return
  }
  if (node.action === 'plugin.secrets.edit') { await openSecretsEditor(); return }
  const payload: Record<string, unknown> = {}
  for (const [key, path] of Object.entries(node.payload || {})) payload[key] = resolvePath(model.value, path, context)
  actionRunning.value = true
  try {
    await stepUp.run(() => adminAPI.plugins.action(plugin.value!.id, { action_id: `native-${crypto.randomUUID()}`, name: node.action!, payload }))
    await loadStatus()
  } catch (errorValue) { if (!isStepUpCancelled(errorValue)) reportError(errorValue) }
  finally { actionRunning.value = false }
}

async function openSecretsEditor(): Promise<void> {
  if (!plugin.value) return
  const fields = plugin.value.manifest.config_secrets || []
  if (!fields.length) return
  secretsEditor.value = { generation: Date.now(), fields }
  secretsLoading.value = true
  secretsReady.value = true
  secretsLoading.value = false
}

function closeSecretsEditor(): void {
  secretsEditor.value = null
  secretsError.value = ''
}

async function saveSecrets(values: Record<string, string>): Promise<void> {
  if (!plugin.value || !secretsEditor.value) return
  secretsSaving.value = true
  try {
    const result = await stepUp.run(() => adminAPI.plugins.saveSecrets(plugin.value!.id, values))
    secretFlags.value = { ...(result._host_secrets || {}) }
    closeSecretsEditor()
  } catch (errorValue) { if (!isStepUpCancelled(errorValue)) secretsError.value = message(errorValue) }
  finally { secretsSaving.value = false; for (const key of Object.keys(values)) delete values[key] }
}

onMounted(() => {
  void loadAll()
  window.addEventListener('beforeunload', handleBeforeUnload)
})
watch(() => route.params.pluginKey, () => { void loadAll() })
onBeforeRouteLeave(() => {
  if (!dirty.value || window.confirm(t('admin.plugins.leaveNativePageConfirm'))) return true
  return false
})
onBeforeUnmount(() => {
  clearStatusTimer()
  window.removeEventListener('beforeunload', handleBeforeUnload)
})

function handleBeforeUnload(event: BeforeUnloadEvent): void {
  if (!dirty.value) return
  event.preventDefault()
  event.returnValue = ''
}
</script>
