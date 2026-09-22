<template>
  <AppLayout>
    <div class="space-y-5">
      <div class="flex flex-wrap items-center justify-between gap-3 border-b border-gray-200 pb-4 dark:border-dark-700">
        <div class="min-w-0">
          <button type="button" class="mb-2 inline-flex items-center gap-1 text-sm text-gray-500 hover:text-gray-900 dark:hover:text-white" @click="router.push('/admin/plugins')">
            <Icon name="chevronLeft" size="sm" />{{ t('admin.plugins.backToList') }}
          </button>
          <h1 class="truncate text-xl font-semibold text-gray-900 dark:text-white">{{ plugin?.name || t('admin.plugins.nativePageTitle') }}</h1>
          <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">{{ definition?.description || plugin?.description || t('admin.plugins.nativePageDescription') }}</p>
        </div>
        <div class="flex items-center gap-2">
          <span v-if="plugin" class="rounded bg-gray-100 px-2 py-1 text-xs text-gray-600 dark:bg-dark-700 dark:text-gray-300">v{{ plugin.version }}</span>
          <span v-if="plugin" class="rounded px-2 py-1 text-xs font-medium" :class="stateClass(plugin.state)">{{ t(`admin.plugins.${plugin.state}`) }}</span>
          <button type="button" class="btn btn-secondary btn-sm" :disabled="loading || saving || refreshing || actionRunning" @click="refreshPage">
            <Icon name="refresh" size="sm" :class="{ 'animate-spin': refreshing }" />{{ t('common.refresh') }}
          </button>
        </div>
      </div>

      <div class="flex flex-wrap items-center justify-between gap-3 text-xs text-gray-500 dark:text-gray-400" role="status">
        <span v-if="statusUpdatedAt">{{ t('admin.plugins.lastUpdated') }}: {{ formatStatusTime(statusUpdatedAt) }}</span>
        <span v-if="statusStale" class="inline-flex items-center gap-2 text-amber-700 dark:text-amber-300">
          {{ t('admin.plugins.statusStale') }}
          <button type="button" class="font-medium underline" :disabled="refreshing" @click="refreshStatus">{{ t('common.retry') }}</button>
        </span>
        <span v-if="actionRunning" class="text-primary-700 dark:text-primary-300">{{ t('admin.plugins.actionRunning') }}</span>
      </div>

      <div v-if="error" class="border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700 dark:border-red-900/50 dark:bg-red-950/20 dark:text-red-300" role="alert">{{ error }}</div>
      <div v-if="validationSummary.length" class="border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700 dark:border-red-900/50 dark:bg-red-950/20 dark:text-red-300" role="alert">
        <p class="font-medium">{{ t('admin.plugins.validationFailed') }}</p>
        <ul class="mt-1 list-inside list-disc"><li v-for="item in validationSummary" :key="item">{{ item }}</li></ul>
      </div>
      <div v-else-if="loading" class="flex min-h-64 items-center justify-center text-sm text-gray-500">{{ t('common.loading') }}</div>
      <div v-else-if="definition" class="space-y-5">
        <div class="flex flex-wrap items-start justify-between gap-3">
          <div>
            <h2 class="text-lg font-semibold text-gray-900 dark:text-white">{{ localizedText(definition.title_key, definition.title) }}</h2>
            <p v-if="definition.description" class="mt-1 text-sm text-gray-500 dark:text-gray-400">{{ localizedText(definition.description_key, definition.description) }}</p>
          </div>
          <span v-if="!isRunning" class="rounded bg-amber-100 px-2 py-1 text-xs text-amber-800 dark:bg-amber-900/30 dark:text-amber-200">{{ t('admin.plugins.actionUnavailable') }}</span>
        </div>

        <NativePluginNode
          v-for="(node, index) in definition.layout"
          :key="node.id || `${node.type}-${index}`"
          :node="node"
          :context="emptyContext"
          :model="model"
          :errors="validationErrors"
          :translations="definition.translations || {}"
          :running-action-key="runningActionKey"
          :disabled="saving"
          :actions-disabled="actionRunning || !isRunning"
          @write="writeValue"
          @action="runAction"
        />

        <div v-if="dirty" class="sticky bottom-4 z-10 flex flex-wrap items-center justify-between gap-3 border border-amber-200 bg-amber-50 px-4 py-3 text-sm shadow-sm dark:border-amber-900/50 dark:bg-amber-950/30">
          <span class="text-amber-800 dark:text-amber-200">{{ t('admin.plugins.unsavedNativeConfig') }}</span>
          <div class="flex gap-2">
            <button type="button" class="btn btn-secondary btn-sm" :disabled="saving" @click="resetDraft">{{ t('common.cancel') }}</button>
            <button type="button" class="btn btn-primary btn-sm" :disabled="saving" @click="() => saveConfigDraft()"><Icon name="check" size="sm" />{{ saving ? t('common.processing') : t('common.save') }}</button>
          </div>
        </div>
      </div>
    </div>

    <PluginSecretsDialog v-if="secretsEditor" :key="secretsEditor.generation" :name="plugin?.name || ''" :fields="secretsEditor.fields" :configured="secretFlags" :loading="secretsLoading" :ready="secretsReady" :saving="secretsSaving" :error="secretsError" @cancel="closeSecretsEditor" @save="saveSecrets" />
    <TotpStepUpDialog :controller="stepUp" />
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, defineComponent, h, onBeforeUnmount, onMounted, reactive, ref, watch, type VNode } from 'vue'
import { onBeforeRouteLeave, onBeforeRouteUpdate, useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import Icon from '@/components/icons/Icon.vue'
import PluginSecretsDialog from '@/components/admin/plugins/PluginSecretsDialog.vue'
import TotpStepUpDialog from '@/components/auth/TotpStepUpDialog.vue'
import { adminAPI, type NativePluginAdminUI, type NativePluginUINode, type NativePluginUIValidation, type PluginInstallation } from '@/api/admin'
import { useAppStore } from '@/stores'
import { isStepUpBlocked, isStepUpCancelled, stepUpBlockReason, useStepUp } from '@/composables/useStepUp'

interface NativeModel { config: Record<string, unknown>; resources: unknown; status: unknown; local: Record<string, unknown>; secrets: Record<string, boolean> }
interface NativeContext { item?: unknown; key?: string | number }

function record(value: unknown): Record<string, unknown> { return value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : {} }

function safeSegments(path: string): string[] | null {
  if (!path.startsWith('/') || path.includes('\0')) return null
  const segments: string[] = []
  for (const rawSegment of path.slice(1).split('/')) {
    let segment = ''
    for (let index = 0; index < rawSegment.length; index += 1) {
      if (rawSegment[index] !== '~') { segment += rawSegment[index]; continue }
      const escape = rawSegment[index + 1]
      if (escape !== '0' && escape !== '1') return null
      segment += escape === '0' ? '~' : '/'; index += 1
    }
    segments.push(segment)
  }
  return segments.some(segment => ['__proto__', 'prototype', 'constructor'].includes(segment)) ? null : segments
}

function resolvePath(model: NativeModel, path: string, context: NativeContext): unknown {
  const segments = safeSegments(path)
  if (!segments?.length) return undefined
  const root = segments.shift()
  let value: unknown = root === 'config' ? model.config : root === 'resources' ? model.resources : root === 'status' ? model.status : root === 'local' ? model.local : root === 'secrets' ? model.secrets : root === 'item' ? context.item : undefined
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
  if (!segments?.length || !['config', 'local'].includes(segments[0]) || segments.includes('_host_secrets')) return false
  let cursor: Record<string, unknown> = segments[0] === 'config' ? model.config : model.local
  for (let index = 1; index < segments.length - 1; index += 1) {
    const next = cursor[segments[index]]
    cursor = next && typeof next === 'object' && !Array.isArray(next) ? next as Record<string, unknown> : (cursor[segments[index]] = {}) as Record<string, unknown>
  }
  cursor[segments.at(-1)!] = value
  return true
}

function sanitizeSecrets(value: unknown, secretFields: Set<string>): unknown {
  if (Array.isArray(value)) return value.map(item => sanitizeSecrets(item, secretFields))
  if (!value || typeof value !== 'object') return value
  const result: Record<string, unknown> = {}
  for (const [key, item] of Object.entries(value)) {
    if (key === '_host_secrets' || secretFields.has(key)) continue
    result[key] = sanitizeSecrets(item, secretFields)
  }
  return result
}

function sanitizeConfig(value: unknown, secretFields: string[]): Record<string, unknown> {
  return record(sanitizeSecrets(value, new Set(secretFields)))
}

function secretFlagsFromConfig(value: unknown, secretFields: string[]): Record<string, boolean> {
  const flags = record(record(value)._host_secrets)
  return Object.fromEntries(secretFields.map(field => [field, flags[field] === true]))
}

function conditionMatches(node: NativePluginUINode, model: NativeModel, context: NativeContext): boolean {
  const condition = node.condition
  if (!condition) return true
  const actual = resolvePath(model, condition.path, context)
  if (condition.op === 'truthy') return Boolean(actual)
  if (condition.op === 'falsy') return !actual
  if (condition.op === 'nonempty') return Array.isArray(actual) ? actual.length > 0 : typeof actual === 'string' ? actual.length > 0 : Boolean(actual)
  if (condition.op === 'empty') return Array.isArray(actual) ? actual.length === 0 : typeof actual === 'string' ? actual.length === 0 : !actual
  if (condition.op === 'eq') return actual === condition.value
  if (condition.op === 'ne') return actual !== condition.value
  return Array.isArray(condition.value) && condition.value.includes(actual)
}

function isHostAction(action?: string): boolean { return action === 'config.save' || action === 'config.refresh' || action === 'plugin.secrets.edit' }
function rowKey(row: unknown, key = 'id'): string { const value = record(row)[key]; return value === null || value === undefined ? '' : String(value) }
function formatValue(value: unknown, format?: NativePluginUINode['format'], yes = 'Yes', no = 'No'): string {
  if (value === undefined || value === null || value === '') return '-'
  if (format === 'count') return Number(value).toLocaleString()
  if (format === 'boolean') return value === true ? yes : no
  if (format === 'date') {
    const date = new Date(String(value))
    return Number.isNaN(date.getTime()) ? String(value) : date.toLocaleString()
  }
  if (format === 'duration') {
    const seconds = Math.max(0, Number(value))
    if (!Number.isFinite(seconds)) return String(value)
    if (seconds < 60) return `${Math.round(seconds)}s`
    if (seconds < 3600) return `${Math.floor(seconds / 60)}m ${Math.round(seconds % 60)}s`
    return `${Math.floor(seconds / 3600)}h ${Math.floor((seconds % 3600) / 60)}m`
  }
  return String(value)
}
function validationMessage(value: unknown, rules: NativePluginUIValidation[] | undefined): string {
  for (const rule of rules || []) {
    if (rule.op === 'required' && (value === undefined || value === null || String(value).trim() === '')) return rule.message
    if (rule.op === 'min_length' && String(value ?? '').length < Number(rule.value || 0)) return rule.message
    if (rule.op === 'max_length' && String(value ?? '').length > Number(rule.value || 0)) return rule.message
    if (rule.op === 'in' && Array.isArray(rule.value) && !rule.value.includes(value)) return rule.message
  }
  return ''
}

let NativePluginNode: ReturnType<typeof defineComponent>
NativePluginNode = defineComponent({
  name: 'NativePluginNode',
  props: {
    node: { type: Object as () => NativePluginUINode, required: true },
    context: { type: Object as () => NativeContext, required: true },
    model: { type: Object as () => NativeModel, required: true },
    errors: { type: Object as () => Record<string, string>, default: () => ({}) },
    translations: { type: Object as () => Record<string, Record<string, string>>, default: () => ({}) },
    runningActionKey: { type: String, default: '' },
    disabled: { type: Boolean, default: false },
    actionsDisabled: { type: Boolean, default: false },
  },
  emits: ['write', 'action'],
  setup(props, { emit }) {
    const { t: translate, locale: currentLocale } = useI18n()
    const tabs = ref(0)
    const expanded = ref(true)
    const tablePage = ref(1)
    const expandedRows = reactive(new Set<string>())
    const value = computed(() => props.node.bind ? resolvePath(props.model, props.node.bind, props.context) : undefined)
    const visible = computed(() => conditionMatches(props.node, props.model, props.context))
    const children = computed(() => props.node.children || [])
    const tableFilterSignature = computed(() => props.node.type === 'data_table' ? JSON.stringify([
      props.node.search_bind ? resolvePath(props.model, props.node.search_bind, props.context) : '',
      props.node.filter_bind ? resolvePath(props.model, props.node.filter_bind, props.context) : '',
      ...(props.node.filters || []).map(filter => resolvePath(props.model, filter.bind, props.context)),
      props.node.presence_filter_bind ? resolvePath(props.model, props.node.presence_filter_bind, props.context) : false,
    ]) : '')
    watch(tableFilterSignature, () => { tablePage.value = 1 })
    const write = (next: unknown) => { if (!props.disabled && props.node.write) emit('write', props.node.write, next) }
    const action = () => emit('action', props.node, props.context)
    const pluginText = (key: string | undefined, fallback = '') => {
      if (!key) return fallback
      const locale = String((currentLocale.value || 'en')).toLowerCase()
      const language = locale.startsWith('zh') ? 'zh' : locale.split('-')[0]
      return props.translations[language]?.[key] || props.translations.en?.[key] || fallback
    }
    const nodeTitle = (node: NativePluginUINode) => pluginText(node.title_key, node.title || '')
    const nodeDescription = (node: NativePluginUINode) => pluginText(node.description_key, node.description || '')
    const childProps = (node: NativePluginUINode, context: NativeContext) => ({ node, context, model: props.model, errors: props.errors, translations: props.translations, runningActionKey: props.runningActionKey, disabled: props.disabled, actionsDisabled: props.actionsDisabled, onWrite: (path: string, next: unknown) => emit('write', path, next), onAction: (childNode: NativePluginUINode, childContext: NativeContext) => emit('action', childNode, childContext) })
    const renderChildren: (nodes?: NativePluginUINode[], context?: NativeContext) => VNode[] = (nodes = children.value, context = props.context) => nodes.map((child, index) => h(NativePluginNode, { key: child.id || `${child.type}-${index}`, ...childProps(child, context) }))

    return () => {
      if (!visible.value) return null
      const node = props.node
      const icon = node.icon ? h(Icon, { name: node.icon as never, size: 'sm' }) : null
      const localizedTitle = nodeTitle(node)
      const localizedDescription = nodeDescription(node)
      const title = localizedTitle ? h('h3', { class: 'mb-2 flex items-center gap-2 text-sm font-semibold text-gray-900 dark:text-white' }, [icon, localizedTitle]) : null
      const description = localizedDescription ? h('p', { class: 'mb-3 text-xs text-gray-500 dark:text-gray-400' }, localizedDescription) : null
      if (['stack', 'grid', 'section', 'action_group'].includes(node.type)) {
        const className = node.type === 'grid' ? 'grid grid-cols-1 gap-4 md:grid-cols-2' : 'space-y-4'
        return h('section', { class: node.type === 'section' ? 'border border-gray-200 p-4 dark:border-dark-700' : className }, [title, description, ...renderChildren()])
      }
      if (node.type === 'tabs') {
        const selected = children.value[tabs.value] || children.value[0]
        return h('section', { class: 'space-y-3' }, [h('div', { class: 'flex flex-wrap gap-1 border-b border-gray-200 dark:border-dark-700' }, children.value.map((child, index) => h('button', { type: 'button', class: ['inline-flex items-center gap-1 px-3 py-2 text-sm', tabs.value === index ? 'border-b-2 border-primary-600 text-primary-700 dark:text-primary-300' : 'text-gray-500'], onClick: () => { tabs.value = index } }, [child.icon ? h(Icon, { name: child.icon as never, size: 'sm' }) : null, nodeTitle(child) || child.id || String(index + 1)]))), selected ? h(NativePluginNode, childProps(selected, props.context)) : null])
      }
      if (node.type === 'accordion') return h('section', { class: 'border border-gray-200 dark:border-dark-700' }, [h('button', { type: 'button', class: 'flex w-full items-center justify-between px-4 py-3 text-left text-sm font-medium', onClick: () => { expanded.value = !expanded.value } }, [localizedTitle || node.id || '', h('span', expanded.value ? '−' : '+')]), expanded.value ? h('div', { class: 'border-t border-gray-200 p-4 dark:border-dark-700' }, [description, ...renderChildren()]) : null])
      if (node.type === 'repeat' || node.type === 'matrix') return h('section', { class: 'space-y-3' }, [title, description, ...(Array.isArray(value.value) ? value.value : []).map((item, index) => h('div', { key: index, class: 'border border-gray-200 p-4 dark:border-dark-700' }, renderChildren(node.children, { item, key: index })))])
      if (node.type === 'data_table') {
        const allRows = Array.isArray(value.value) ? value.value : []
        const query = String(node.search_bind ? resolvePath(props.model, node.search_bind, props.context) ?? '' : '').trim().toLowerCase()
        const filter = String(node.filter_bind ? resolvePath(props.model, node.filter_bind, props.context) ?? '' : '')
        const columns = node.columns || []
        const filterAllValue = node.options?.find(option => option.value === '__all__')?.value || '__all__'
        const dynamicFilterOptions = node.filter_options_bind ? resolvePath(props.model, node.filter_options_bind, props.context) : undefined
        const filterOptions = [
          ...(node.options || []).map(option => ({ value: String(option.value), label: pluginText(option.label_key, option.label) })),
          ...(Array.isArray(dynamicFilterOptions) ? dynamicFilterOptions.map(option => {
            if (typeof option === 'string' || typeof option === 'number') return { value: String(option), label: String(option) }
            const item = record(option)
            return { value: String(item.id ?? item.value ?? ''), label: String(item.name ?? item.label ?? item.id ?? item.value ?? '') }
          }) : []),
        ].filter((option, index, options) => option.value && options.findIndex(item => item.value === option.value) === index)
        const filtered = allRows.filter(row => {
          const matchesQuery = !query || columns.some(column => {
            const value = column.bind ? resolvePath(props.model, column.bind, { item: row }) : record(row)[column.key]
            return String(value ?? '').toLowerCase().includes(query)
          })
          const rawFilter = node.filter_key ? record(row)[node.filter_key] : undefined
          const matchesFilter = !filter || filter === filterAllValue || (Array.isArray(rawFilter) ? rawFilter.map(String).includes(filter) : String(rawFilter ?? '') === filter)
          const matchesExtraFilters = (node.filters || []).every(spec => {
            const expected = String(resolvePath(props.model, spec.bind, props.context) ?? '')
            return !expected || expected === (spec.all_value || '__all__') || String(record(row)[spec.key] ?? '') === expected
          })
          const requirePresence = node.presence_filter_bind ? resolvePath(props.model, node.presence_filter_bind, props.context) === true : false
          const matchesPresence = !requirePresence || Boolean(record(row)[node.presence_key || ''])
          return matchesQuery && matchesFilter && matchesExtraFilters && matchesPresence
        }).sort((left, right) => {
          if (!node.sort_key) return 0
          const a = record(left)[node.sort_key]
          const b = record(right)[node.sort_key]
          const compared = typeof a === 'number' && typeof b === 'number' ? a - b : String(a ?? '').localeCompare(String(b ?? ''))
          return node.sort_desc ? -compared : compared
        })
        const pageSize = Math.max(1, node.page_size || 25)
        const pages = Math.max(1, Math.ceil(filtered.length / pageSize))
        if (tablePage.value > pages) tablePage.value = pages
        const start = (tablePage.value - 1) * pageSize
        const rows = filtered.slice(start, start + pageSize)
        const selectedRaw = Array.isArray(node.selection_bind ? resolvePath(props.model, node.selection_bind, props.context) : undefined)
          ? (resolvePath(props.model, node.selection_bind!, props.context) as unknown[]).map(String)
          : []
        const availableIDs = new Set(allRows.map(row => rowKey(row, node.row_key)).filter(Boolean))
        const selected = [...new Set(selectedRaw)].filter(id => availableIDs.has(id))
        const filteredIDs = new Set(filtered.map(row => rowKey(row, node.row_key)).filter(Boolean))
        const hiddenSelected = selected.filter(id => !filteredIDs.has(id)).length
        const writeSelection = (ids: string[]) => {
          if (node.selection_bind) emit('write', node.selection_bind, [...new Set(ids)].map(id => /^\d+$/.test(id) ? Number(id) : id))
        }
        const toggle = (row: unknown, checked: boolean) => {
          const id = rowKey(row, node.row_key)
          if (!id) return
          const next = new Set(selected)
          if (checked) next.add(id)
          else next.delete(id)
          writeSelection([...next])
        }
        const pageIDs = rows.map(row => rowKey(row, node.row_key)).filter(Boolean)
        const pageSelected = pageIDs.length > 0 && pageIDs.every(id => selected.includes(id))
        const pagePartlySelected = !pageSelected && pageIDs.some(id => selected.includes(id))
        const togglePage = (checked: boolean) => {
          const next = new Set(selected)
          pageIDs.forEach(id => { if (checked) next.add(id); else next.delete(id) })
          writeSelection([...next])
        }
        const toggleDetails = (id: string) => { expandedRows.has(id) ? expandedRows.delete(id) : expandedRows.add(id) }
        const actionCell = (context: NativeContext, id: string, open: boolean) => h('div', { class: 'flex flex-wrap justify-end gap-1' }, [
          ...(node.row_actions || []).map((actionNode, actionIndex) => h(NativePluginNode, { key: actionNode.id || actionIndex, ...childProps(actionNode, context) })),
          node.children?.length ? h('button', { type: 'button', class: 'btn btn-secondary btn-sm', 'aria-label': translate('admin.plugins.toggleDetails'), onClick: () => toggleDetails(id) }, open ? '−' : '+') : null,
        ])
        const renderCell = (row: unknown, context: NativeContext, column: (typeof columns)[number]) => {
          const cellValue = column.bind ? resolvePath(props.model, column.bind, context) : record(row)[column.key]
          return formatValue(cellValue, column.format || node.format, translate('common.yes'), translate('common.no'))
        }
        const desktopRows = rows.flatMap((row, index) => {
          const id = rowKey(row, node.row_key)
          const renderID = id || `row-${start + index}`
          const context = { item: row, key: start + index }
          const open = expandedRows.has(renderID)
          const main = h('tr', { key: renderID, class: 'border-t border-gray-100 dark:border-dark-700' }, [
            node.selection_bind ? h('td', { class: 'sticky left-0 bg-white px-3 py-2 dark:bg-dark-900' }, h('input', { type: 'checkbox', checked: Boolean(id) && selected.includes(id), disabled: !id, 'aria-label': `${translate('admin.plugins.select')} ${id || renderID}`, onChange: (event: Event) => toggle(row, (event.target as HTMLInputElement).checked) })) : null,
            ...columns.map(column => h('td', { key: column.key, class: 'whitespace-nowrap px-3 py-2 text-gray-700 dark:text-gray-200' }, renderCell(row, context, column))),
            node.row_actions?.length || node.children?.length ? h('td', { class: 'sticky right-0 bg-white px-3 py-2 dark:bg-dark-900' }, actionCell(context, renderID, open)) : null,
          ])
          const details = open && node.children?.length ? h('tr', { key: `${renderID}-details` }, h('td', { colspan: columns.length + (node.selection_bind ? 1 : 0) + (node.row_actions?.length || node.children?.length ? 1 : 0), class: 'bg-gray-50 px-4 py-3 dark:bg-dark-800' }, renderChildren(node.children, context))) : null
          return details ? [main, details] : [main]
        })
        const mobileRows = rows.map((row, index) => {
          const id = rowKey(row, node.row_key)
          const renderID = id || `row-${start + index}`
          const context = { item: row, key: start + index }
          const open = expandedRows.has(renderID)
          return h('article', { key: `mobile-${renderID}`, class: 'space-y-3 border border-gray-200 p-3 dark:border-dark-700 md:hidden' }, [
            h('div', { class: 'flex items-start justify-between gap-3' }, [
              h('div', { class: 'min-w-0 space-y-1' }, columns.map(column => h('p', { key: column.key, class: 'text-sm' }, [h('span', { class: 'mr-2 text-xs text-gray-500' }, pluginText(column.label_key, column.label)), h('span', { class: 'break-all text-gray-800 dark:text-gray-200' }, renderCell(row, context, column))]))),
              node.selection_bind ? h('input', { type: 'checkbox', checked: Boolean(id) && selected.includes(id), disabled: !id, 'aria-label': `${translate('admin.plugins.select')} ${id || renderID}`, onChange: (event: Event) => toggle(row, (event.target as HTMLInputElement).checked) }) : null,
            ]),
            node.row_actions?.length || node.children?.length ? actionCell(context, renderID, open) : null,
            open && node.children?.length ? h('div', { class: 'border-t border-gray-200 pt-3 dark:border-dark-700' }, renderChildren(node.children, context)) : null,
          ])
        })
        const controlNodes: VNode[] = []
        if (node.search_bind) controlNodes.push(h('label', { class: 'min-w-[12rem] flex-1 space-y-1' }, [h('span', { class: 'sr-only' }, translate('admin.plugins.searchRows')), h('input', { class: 'input w-full', type: 'search', value: query, placeholder: translate('admin.plugins.searchRows'), onInput: (event: Event) => emit('write', node.search_bind, (event.target as HTMLInputElement).value) })]))
        if (node.filter_bind && filterOptions.length) controlNodes.push(h('label', { class: 'min-w-[10rem] space-y-1' }, [h('span', { class: 'sr-only' }, translate('admin.plugins.filterRows')), h('select', { class: 'input w-full', value: filter || filterAllValue, 'aria-label': translate('admin.plugins.filterRows'), onChange: (event: Event) => emit('write', node.filter_bind, (event.target as HTMLSelectElement).value) }, filterOptions.map(option => h('option', { value: option.value }, option.label)))]))
        const selectionTools = node.selection_bind ? h('div', { class: 'flex flex-wrap items-center gap-2 border border-gray-200 bg-gray-50 px-3 py-2 text-xs dark:border-dark-700 dark:bg-dark-800' }, [
          h('span', { class: 'font-medium text-gray-700 dark:text-gray-200' }, `${selected.length} ${translate('admin.plugins.selectedRows')}${hiddenSelected ? ` · ${hiddenSelected} ${translate('admin.plugins.hiddenSelected')}` : ''}`),
          h('button', { type: 'button', class: 'btn btn-secondary btn-sm', disabled: pageIDs.length === 0, onClick: () => togglePage(true) }, translate('admin.plugins.selectPage')),
          h('button', { type: 'button', class: 'btn btn-secondary btn-sm', disabled: filtered.length === 0, onClick: () => { const next = new Set(selected); filtered.forEach(row => { const id = rowKey(row, node.row_key); if (id) next.add(id) }); writeSelection([...next]) } }, translate('admin.plugins.selectFiltered')),
          h('button', { type: 'button', class: 'btn btn-secondary btn-sm', disabled: selected.length === 0, onClick: () => writeSelection([]) }, translate('admin.plugins.clearSelection')),
        ]) : null
        const empty = rows.length === 0 ? h('div', { class: 'border border-dashed border-gray-300 px-4 py-10 text-center text-sm text-gray-500 dark:border-dark-600' }, translate('admin.plugins.noMatchingRows')) : null
        const controls = controlNodes.length ? h('div', { class: 'flex flex-wrap items-end gap-3' }, controlNodes) : null
        return h('section', { class: 'space-y-3' }, [title, description, controls, selectionTools, empty, rows.length ? h('div', { class: 'hidden overflow-x-auto border border-gray-200 dark:border-dark-700 md:block' }, [h('table', { class: 'min-w-full text-left text-sm' }, [h('thead', { class: 'bg-gray-50 dark:bg-dark-800' }, h('tr', [node.selection_bind ? h('th', { class: 'sticky left-0 bg-gray-50 px-3 py-2 dark:bg-dark-800' }, h('input', { type: 'checkbox', checked: pageSelected, indeterminate: pagePartlySelected, 'aria-label': translate('admin.plugins.selectPage'), onChange: (event: Event) => togglePage((event.target as HTMLInputElement).checked) })) : null, ...columns.map(column => h('th', { key: column.key, class: 'whitespace-nowrap px-3 py-2 font-medium text-gray-500' }, pluginText(column.label_key, column.label))), node.row_actions?.length || node.children?.length ? h('th', { class: 'sticky right-0 bg-gray-50 px-3 py-2 text-right font-medium text-gray-500 dark:bg-dark-800' }, translate('admin.plugins.actions')) : null])), h('tbody', desktopRows)])]) : null, ...mobileRows, h('div', { class: 'flex items-center justify-between text-xs text-gray-500' }, [h('span', `${filtered.length} ${translate('admin.plugins.rows')}`), h('div', { class: 'flex items-center gap-2' }, [h('button', { type: 'button', class: 'btn btn-secondary btn-sm', disabled: tablePage.value <= 1, 'aria-label': translate('admin.plugins.previous'), onClick: () => { tablePage.value -= 1 } }, '<'), h('span', `${tablePage.value} / ${pages}`), h('button', { type: 'button', class: 'btn btn-secondary btn-sm', disabled: tablePage.value >= pages, 'aria-label': translate('admin.plugins.next'), onClick: () => { tablePage.value += 1 } }, '>')])])])
      }      if (node.type === 'text' || node.type === 'empty') return h('p', { class: 'text-sm text-gray-600 dark:text-gray-300' }, localizedTitle || String(value.value ?? ''))
      if (node.type === 'alert') return h('div', { class: 'border border-amber-200 bg-amber-50 px-3 py-2 text-sm text-amber-800 dark:border-amber-900/50 dark:bg-amber-950/20 dark:text-amber-200' }, [title, localizedDescription || String(value.value ?? '')])
      if (node.type === 'badge') return h('span', { class: 'inline-flex rounded bg-gray-100 px-2 py-1 text-xs text-gray-700 dark:bg-dark-700 dark:text-gray-200' }, localizedTitle || String(value.value ?? ''))
      if (node.type === 'stat') return h('div', { class: 'border border-gray-200 p-4 dark:border-dark-700' }, [h('p', { class: 'text-xs text-gray-500' }, localizedTitle), h('p', { class: 'mt-1 text-xl font-semibold text-gray-900 dark:text-white' }, formatValue(value.value, node.format, translate('common.yes'), translate('common.no')))])
      if (node.type === 'key_value') return h('dl', { class: 'grid grid-cols-[auto,1fr] gap-x-4 gap-y-2 text-sm' }, Object.entries(record(value.value)).map(([key, item]) => [h('dt', { class: 'text-gray-500' }, key), h('dd', { class: 'text-gray-900 dark:text-gray-200' }, String(item ?? '-'))]))
      if (node.type === 'table' || node.type === 'log_list') {
        const rows = Array.isArray(value.value) ? value.value : []
        const columns = node.columns || []
        return h('div', { class: 'overflow-x-auto border border-gray-200 dark:border-dark-700' }, [
          h('table', { class: 'min-w-full text-left text-sm' }, [
            h('thead', { class: 'bg-gray-50 dark:bg-dark-800' }, h('tr', columns.map(column => h('th', { key: column.key, class: 'px-3 py-2 font-medium text-gray-500' }, pluginText(column.label_key, column.label))))),
            h('tbody', rows.map((row, index) => h('tr', { key: index, class: 'border-t border-gray-100 dark:border-dark-700' }, columns.map(column => h('td', { key: column.key, class: 'px-3 py-2 text-gray-700 dark:text-gray-200' }, formatValue(column.bind ? resolvePath(props.model, column.bind, { item: row, key: index }) : record(row)[column.key], column.format || node.format, translate('common.yes'), translate('common.no'))))))),
          ]),
        ])
      }
      if (['text_input', 'number_input', 'search'].includes(node.type)) return h('label', { class: 'block space-y-1' }, [title || h('span', { class: 'text-sm font-medium' }, node.id || ''), h('input', { class: 'input w-full', type: node.type === 'number_input' ? 'number' : node.type === 'search' ? 'search' : 'text', value: value.value ?? '', readonly: node.read_only || props.disabled, onInput: (event: Event) => write(node.type === 'number_input' ? Number((event.target as HTMLInputElement).value) : (event.target as HTMLInputElement).value) }), description, props.errors[node.id || node.write || ''] ? h('p', { class: 'text-xs text-red-600', role: 'alert' }, props.errors[node.id || node.write || '']) : null])
      if (node.type === 'switch' || node.type === 'checkbox') return h('label', { class: 'flex items-center gap-2 text-sm text-gray-700 dark:text-gray-200' }, [h('input', { type: 'checkbox', checked: value.value === true, disabled: node.read_only || props.disabled, onChange: (event: Event) => write((event.target as HTMLInputElement).checked) }), localizedTitle || node.id || '', props.errors[node.id || node.write || ''] ? h('span', { class: 'text-xs text-red-600', role: 'alert' }, props.errors[node.id || node.write || '']) : null])
      if (node.type === 'select' || node.type === 'multiselect') { const boundOptions = Array.isArray(node.options_bind ? resolvePath(props.model, node.options_bind, props.context) : undefined) ? resolvePath(props.model, node.options_bind!, props.context) as unknown[] : []; const options = [...(node.options || []).map(option => ({ ...option, label: pluginText(option.label_key, option.label) })), ...boundOptions.map(option => { const item = record(option); return { value: String(item.id ?? item.value ?? ''), label: String(item.name ?? item.label ?? item.id ?? item.value ?? '') } })]; return h('label', { class: 'block space-y-1' }, [title || h('span', { class: 'text-sm font-medium' }, node.id || ''), h('select', { class: 'input w-full', multiple: node.type === 'multiselect', disabled: node.read_only || props.disabled, value: value.value as string | string[] | undefined, onChange: (event: Event) => { const target = event.target as HTMLSelectElement; write(node.type === 'multiselect' ? [...target.selectedOptions].map(option => option.value) : target.value) } }, options.map(option => h('option', { value: option.value }, option.label))), props.errors[node.id || node.write || ''] ? h('p', { class: 'text-xs text-red-600', role: 'alert' }, props.errors[node.id || node.write || '']) : null]) }
      if (node.type === 'button') { const key = `${node.id || node.action || 'action'}:${rowKey(props.context.item) || props.context.key || 'global'}`; const running = props.runningActionKey === key; return h('button', { type: 'button', class: 'btn btn-secondary btn-sm inline-flex items-center gap-1', disabled: props.disabled || (props.actionsDisabled && !isHostAction(node.action)), onClick: action }, [running ? h(Icon, { name: 'refresh', size: 'sm', class: 'animate-spin' }) : icon, localizedTitle || node.action || '']) }
      return h('div', { class: 'text-sm text-gray-500' }, node.description || node.title || node.type)
    }
  },
})

const route = useRoute()
const router = useRouter()
const { t, locale } = useI18n()
const appStore = useAppStore()
const stepUp = useStepUp()
const plugin = ref<PluginInstallation | null>(null)
const definition = ref<NativePluginAdminUI | null>(null)
const loading = ref(true)
const refreshing = ref(false)
const saving = ref(false)
const actionRunning = ref(false)
const runningActionKey = ref('')
const error = ref('')
const draft = ref<Record<string, unknown>>({})
const savedSnapshot = ref('')
const resources = ref<unknown>({})
const status = ref<unknown>({})
const local = reactive<Record<string, unknown>>({})
const validationErrors = ref<Record<string, string>>({})
const statusUpdatedAt = ref(0)
const statusLoadFailed = ref(false)
const now = ref(Date.now())
let timer: ReturnType<typeof setInterval> | undefined
let loadGeneration = 0
let statusGeneration = 0
let statusRequest: Promise<void> | null = null
let statusRequestPluginID: number | null = null

function clearStatusTimer(): void { if (timer) clearInterval(timer); timer = undefined }
const model = computed<NativeModel>(() => ({ config: draft.value, resources: resources.value, status: status.value, local, secrets: secretFlags.value }))
const emptyContext: NativeContext = {}
const dirty = computed(() => JSON.stringify(draft.value) !== savedSnapshot.value)
const isRunning = computed(() => plugin.value?.runtime_healthy === true && plugin.value.state === 'enabled')
const statusStale = computed(() => statusLoadFailed.value || Boolean(statusUpdatedAt.value && now.value - statusUpdatedAt.value > Math.max(30_000, (definition.value?.poll_interval_seconds || 10) * 3000)))
const validationSummary = computed(() => Object.values(validationErrors.value).filter(Boolean))
const secretsEditor = ref<{ generation: number; fields: string[] } | null>(null)
const secretFlags = ref<Record<string, boolean>>({})
const secretsLoading = ref(false)
const secretsReady = ref(false)
const secretsSaving = ref(false)
const secretsError = ref('')

function stateClass(state: PluginInstallation['state']): string { if (state === 'enabled') return 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300'; if (state === 'error' || state === 'incompatible') return 'bg-red-100 text-red-700 dark:bg-red-950/20 dark:text-red-300'; if (state === 'starting') return 'bg-amber-100 text-amber-700 dark:bg-amber-900/30 dark:text-amber-200'; return 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-gray-300' }
function localizedText(key?: string, fallback = ''): string { if (!key) return fallback; const current = String(locale.value || 'en').toLowerCase(); const language = current.startsWith('zh') ? 'zh' : current.split('-')[0]; return definition.value?.translations?.[language]?.[key] || definition.value?.translations?.en?.[key] || fallback }
function formatStatusTime(value: number): string { return new Date(value).toLocaleTimeString() }
function message(errorValue: unknown): string { return errorValue instanceof Error && errorValue.message ? errorValue.message : t('admin.plugins.nativePageLoadFailed') }
function writeValue(path: string, value: unknown): void { const segments = safeSegments(path); if (!segments?.length) return; if (segments[0] === 'config' && plugin.value?.manifest.config_secrets?.includes(segments[1])) return; if (!setPath(model.value, path, value)) return; validationErrors.value = { ...validationErrors.value, [path]: '' }; draft.value = { ...draft.value } }
function collectValidation(nodes: NativePluginUINode[], errors: Record<string, string>): void { for (const node of nodes) { if (node.validation?.length) { const value = node.bind ? resolvePath(model.value, node.bind, emptyContext) : undefined; const issue = validationMessage(value, node.validation); if (issue) errors[node.id || node.write || node.bind || node.type] = issue } collectValidation(node.children || [], errors); collectValidation(node.row_actions || [], errors) } }
async function loadStatus(): Promise<void> {
  if (!plugin.value) return
  const installationID = plugin.value.id
  if (statusRequest && statusRequestPluginID === installationID) return statusRequest
  const generation = ++statusGeneration
  const request = (async () => {
    try {
      const response = await adminAPI.plugins.status(installationID)
      if (generation !== statusGeneration || plugin.value?.id !== installationID) return
      let parsed: unknown = {}
      if (response.status_json) {
        try { parsed = JSON.parse(response.status_json) } catch { parsed = {} }
      }
      status.value = { ...record(parsed), healthy: response.healthy, message: response.message }
      statusUpdatedAt.value = Date.now()
      statusLoadFailed.value = false
    } catch {
      if (generation === statusGeneration && plugin.value?.id === installationID) statusLoadFailed.value = true
    } finally {
      if (generation === statusGeneration) {
        statusRequest = null
        statusRequestPluginID = null
      }
    }
  })()
  statusRequest = request
  statusRequestPluginID = installationID
  return request
}
async function loadAll(): Promise<void> { const generation = ++loadGeneration; statusGeneration += 1; const key = String(route.params.pluginKey || ''); if (!key) { error.value = t('admin.plugins.nativePageNotFound'); loading.value = false; return } clearStatusTimer(); loading.value = true; error.value = ''; for (const key of Object.keys(local)) delete local[key]; validationErrors.value = {}; try { const current = await adminAPI.plugins.byKey(key); if (current.manifest.ui?.type !== 'native' || !current.manifest.ui.definition) throw new Error(t('admin.plugins.nativePageUnavailable')); const [page, config] = await Promise.all([adminAPI.plugins.adminUI(current.id), adminAPI.plugins.getConfig(current.id)]); if (generation !== loadGeneration) return; plugin.value = current; definition.value = page; const secretFields = current.manifest.config_secrets || []; draft.value = sanitizeConfig(config, secretFields); savedSnapshot.value = JSON.stringify(draft.value); secretFlags.value = secretFlagsFromConfig(config, secretFields); try { resources.value = await adminAPI.plugins.resources(current.id) } catch { resources.value = {} } if (generation !== loadGeneration) return; await loadStatus(); timer = setInterval(() => { now.value = Date.now(); if (document.visibilityState === 'visible' && !dirty.value && !saving.value && !secretsSaving.value && !actionRunning.value && !refreshing.value) void loadStatus() }, page.poll_interval_seconds * 1000) } catch (errorValue) { if (generation === loadGeneration) error.value = message(errorValue) } finally { if (generation === loadGeneration) loading.value = false } }
async function refreshPage(): Promise<void> { if (dirty.value && !window.confirm(t('admin.plugins.leaveNativePageConfirm'))) return; await loadAll() }
async function refreshStatus(): Promise<void> { if (refreshing.value) return; refreshing.value = true; try { await loadStatus() } finally { refreshing.value = false } }
function resetDraft(): void { try { draft.value = JSON.parse(savedSnapshot.value) as Record<string, unknown> } catch { draft.value = {} } validationErrors.value = {} }
async function saveConfigDraft(showSuccess = true): Promise<boolean> { if (!plugin.value || saving.value) return false; if (!dirty.value) return true; const errors: Record<string, string> = {}; collectValidation(definition.value?.layout || [], errors); validationErrors.value = errors; if (Object.keys(errors).length) return false; const outgoing = sanitizeConfig(draft.value, plugin.value.manifest.config_secrets || []); saving.value = true; try { const result = await stepUp.run(() => adminAPI.plugins.saveConfig(plugin.value!.id, outgoing)); const next = sanitizeConfig(result, plugin.value!.manifest.config_secrets || []); draft.value = next; savedSnapshot.value = JSON.stringify(next); if (showSuccess) appStore.showSuccess(t('common.saved')); return true } catch (errorValue) { if (!isStepUpCancelled(errorValue)) reportError(errorValue); return false } finally { saving.value = false } }
function reportError(errorValue: unknown): void { if (isStepUpBlocked(errorValue)) appStore.showError(stepUpBlockReason(errorValue) === 'STEP_UP_ADMIN_API_KEY_FORBIDDEN' ? t('stepUp.adminApiKeyForbidden') : t('stepUp.notEnabled')); else appStore.showError(message(errorValue)) }
async function runAction(node: NativePluginUINode, context: NativeContext): Promise<void> {
  if (!plugin.value || !node.action || saving.value || actionRunning.value) return
  if (node.confirm && !window.confirm(node.description || t('admin.plugins.nativeActionConfirm'))) return
  if (node.action === 'config.save') { await saveConfigDraft(); return }
  if (node.action === 'config.refresh') { refreshing.value = true; try { await loadStatus() } finally { refreshing.value = false } return }
  if (node.action === 'plugin.secrets.edit') { await openSecretsEditor(); return }
  if (node.apply_result === 'config' && dirty.value && !await saveConfigDraft(false)) return
  const payload: Record<string, unknown> = { ...(node.values || {}) }
  for (const [key, path] of Object.entries(node.payload || {})) payload[key] = resolvePath(model.value, path, context)
  actionRunning.value = true
  runningActionKey.value = `${node.id || node.action}:${rowKey(context.item) || context.key || 'global'}`
  try {
    const result = await stepUp.run(() => adminAPI.plugins.action(plugin.value!.id, { action_id: `native-${crypto.randomUUID()}`, name: node.action!, payload }))
    if (node.apply_result === 'config') {
      const returned = record(result.result)
      const nextConfig = record(returned.config)
      if (Object.keys(nextConfig).length) {
        draft.value = sanitizeConfig(nextConfig, plugin.value.manifest.config_secrets || [])
        validationErrors.value = {}
        if (!await saveConfigDraft(false)) return
      }
    }
    await loadStatus()
    appStore.showSuccess(t('admin.plugins.actionAccepted'))
  } catch (errorValue) { if (!isStepUpCancelled(errorValue)) reportError(errorValue) } finally { actionRunning.value = false; runningActionKey.value = '' }
}
async function openSecretsEditor(): Promise<void> { if (!plugin.value) return; const fields = plugin.value.manifest.config_secrets || []; if (!fields.length) return; secretsEditor.value = { generation: Date.now(), fields }; secretsLoading.value = false; secretsReady.value = true }
function closeSecretsEditor(): void { secretsEditor.value = null; secretsError.value = '' }
async function saveSecrets(values: Record<string, string>): Promise<void> { if (!plugin.value || !secretsEditor.value) return; secretsSaving.value = true; try { const result = await stepUp.run(() => adminAPI.plugins.saveSecrets(plugin.value!.id, values)); secretFlags.value = secretFlagsFromConfig(result, plugin.value.manifest.config_secrets || []); closeSecretsEditor() } catch (errorValue) { if (!isStepUpCancelled(errorValue)) secretsError.value = message(errorValue) } finally { secretsSaving.value = false; for (const key of Object.keys(values)) delete values[key] } }
watch(() => route.params.pluginKey, () => { void loadAll() })
onMounted(() => { void loadAll(); window.addEventListener('beforeunload', handleBeforeUnload); document.addEventListener('visibilitychange', handleVisibilityChange) })
onBeforeRouteUpdate(() => { if (!dirty.value || window.confirm(t('admin.plugins.leaveNativePageConfirm'))) return true; return false })
onBeforeRouteLeave(() => { if (!dirty.value || window.confirm(t('admin.plugins.leaveNativePageConfirm'))) return true; return false })
onBeforeUnmount(() => { clearStatusTimer(); window.removeEventListener('beforeunload', handleBeforeUnload); document.removeEventListener('visibilitychange', handleVisibilityChange) })
function handleBeforeUnload(event: BeforeUnloadEvent): void { if (!dirty.value) return; event.preventDefault(); event.returnValue = '' }
function handleVisibilityChange(): void { if (document.visibilityState === 'visible' && !dirty.value && !saving.value && !secretsSaving.value && !actionRunning.value && !refreshing.value) void loadStatus() }
</script>
