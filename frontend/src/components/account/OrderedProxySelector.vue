<template>
  <div class="space-y-2" :aria-disabled="disabled || undefined">
    <div role="list" class="space-y-2">
      <div
        v-for="(proxyId, index) in modelValue"
        :key="`proxy-${proxyId}`"
        role="listitem"
        class="grid grid-cols-[1.5rem_minmax(0,1fr)_auto] items-center gap-2"
      >
        <span class="w-6 text-center text-xs text-gray-500">{{ index + 1 }}</span>
        <div class="min-w-0 flex-1">
          <Select
            :model-value="proxyId"
            :options="optionsFor(index)"
            searchable
            :disabled="disabled"
            :placeholder="t('admin.accounts.proxyBindingPlaceholder')"
            :aria-label="t('admin.accounts.proxyBindingPosition', { position: index + 1 })"
            @update:model-value="setProxy(index, $event)"
          />
        </div>
        <div class="flex shrink-0 items-center gap-1">
          <button
            type="button"
            class="btn btn-secondary inline-flex h-11 w-11 touch-manipulation items-center justify-center p-0"
            :disabled="disabled || index === 0"
            :aria-label="t('admin.accounts.proxyBindingMoveUp')"
            :title="t('admin.accounts.proxyBindingMoveUp')"
            @click="move(index, -1)"
          ><Icon name="chevronUp" size="sm" /></button>
          <button
            type="button"
            class="btn btn-secondary inline-flex h-11 w-11 touch-manipulation items-center justify-center p-0"
            :disabled="disabled || index === modelValue.length - 1"
            :aria-label="t('admin.accounts.proxyBindingMoveDown')"
            :title="t('admin.accounts.proxyBindingMoveDown')"
            @click="move(index, 1)"
          ><Icon name="chevronDown" size="sm" /></button>
          <button
            type="button"
            class="btn btn-secondary inline-flex h-11 w-11 touch-manipulation items-center justify-center p-0"
            :disabled="disabled"
            :aria-label="t('admin.accounts.proxyBindingRemove')"
            :title="t('admin.accounts.proxyBindingRemove')"
            @click="remove(index)"
          ><Icon name="x" size="sm" /></button>
        </div>
      </div>
    </div>

    <div class="flex items-center justify-between gap-3">
      <span v-if="modelValue.length === 0" class="text-xs text-gray-500 dark:text-dark-400">
        {{ emptyText || t('admin.accounts.noProxy') }}
      </span>
      <span v-else />
      <button
        type="button"
        class="btn btn-ghost min-h-11 shrink-0 touch-manipulation"
        :disabled="disabled || modelValue.length >= max || availableProxyIds.length === 0"
        @click="add"
      >{{ t('admin.accounts.proxyBindingAdd') }}</button>
    </div>
    <span class="sr-only" aria-live="polite">
      {{ t('admin.accounts.proxyBindingSelectedCount', { count: modelValue.length }) }}
    </span>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Select, { type SelectOption } from '@/components/common/Select.vue'
import Icon from '@/components/icons/Icon.vue'
import type { Proxy } from '@/types'

const props = withDefaults(defineProps<{
  modelValue: number[]
  proxies: Proxy[]
  max?: number
  disabled?: boolean
  emptyText?: string
}>(), {
  max: 50,
  disabled: false,
  emptyText: ''
})

const emit = defineEmits<{
  'update:modelValue': [value: number[]]
}>()
const { t } = useI18n()

const availableProxyIds = computed(() => {
  const selected = new Set(props.modelValue)
  return props.proxies
    .filter((proxy) => isSelectableProxy(proxy) && !selected.has(proxy.id))
    .map((proxy) => proxy.id)
})

const proxyIsExpired = (proxy: Proxy): boolean => {
  if (proxy.status === 'expired') return true
  if (!proxy.expires_at) return false
  const expiresAt = Date.parse(proxy.expires_at)
  return Number.isFinite(expiresAt) && expiresAt <= Date.now()
}

const isSelectableProxy = (proxy: Proxy): boolean => proxy.status === 'active' && !proxyIsExpired(proxy)

const proxyStatusLabel = (proxy: Proxy): string => {
  if (proxyIsExpired(proxy)) return t('admin.accounts.proxyBindingExpired')
  if (proxy.status !== 'active') return t('admin.accounts.proxyBindingInactive')
  return ''
}

const proxyOption = (proxy: Proxy): SelectOption => ({
  value: proxy.id,
  label: [proxy.name || `${proxy.host}:${proxy.port}`, proxyStatusLabel(proxy)].filter(Boolean).join(' · ')
})

const optionsFor = (index: number): SelectOption[] => {
  const currentProxyId = props.modelValue[index]
  const selected = new Set(props.modelValue.filter((_, selectedIndex) => selectedIndex !== index))
  const options = props.proxies
    .filter((proxy) => isSelectableProxy(proxy) || proxy.id === currentProxyId)
    .map((proxy) => ({
      ...proxyOption(proxy),
      disabled: selected.has(proxy.id)
    }))
  if (currentProxyId != null && !props.proxies.some((proxy) => proxy.id === currentProxyId)) {
    options.unshift({
      value: currentProxyId,
      label: `${t('admin.accounts.proxyBindingUnknown', { id: currentProxyId })} · ${t('admin.accounts.proxyBindingInactive')}`,
      disabled: false
    })
  }
  return options
}

const add = () => {
  if (props.disabled) return
  const next = availableProxyIds.value[0]
  if (next == null || props.modelValue.length >= props.max) return
  emit('update:modelValue', [...props.modelValue, next])
}

const setProxy = (index: number, value: unknown) => {
  if (props.disabled) return
  const proxyId = Number(value)
  if (!Number.isInteger(proxyId) || proxyId <= 0) return
  if (props.modelValue.some((selected, selectedIndex) => selectedIndex !== index && selected === proxyId)) return
  const next = [...props.modelValue]
  next[index] = proxyId
  emit('update:modelValue', next)
}

const remove = (index: number) => {
  if (props.disabled) return
  emit('update:modelValue', props.modelValue.filter((_, selectedIndex) => selectedIndex !== index))
}

const move = (index: number, offset: -1 | 1) => {
  if (props.disabled) return
  const target = index + offset
  if (target < 0 || target >= props.modelValue.length) return
  const next = [...props.modelValue]
  const current = next[index]!
  next[index] = next[target]!
  next[target] = current
  emit('update:modelValue', next)
}
</script>
