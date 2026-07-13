<template>
  <div class="space-y-2">
    <div class="relative" ref="containerRef">
      <button
        type="button"
        :class="[
          'select-trigger',
          isOpen && 'select-trigger-open',
          disabled && 'select-trigger-disabled'
        ]"
        :disabled="disabled"
        @click="toggle"
      >
        <span class="min-w-0 flex-1 text-left">
          <span v-if="selectedKey" class="block truncate font-medium">
            {{ optionTitle(selectedKey) }}
          </span>
          <span v-else class="block truncate text-gray-500 dark:text-dark-400">
            {{ placeholder }}
          </span>
        </span>
        <Icon
          name="chevronDown"
          size="md"
          :class="['flex-shrink-0 text-gray-400 transition-transform duration-200', isOpen && 'rotate-180']"
        />
      </button>

      <Transition name="select-dropdown">
        <div v-if="isOpen" class="select-dropdown">
          <div class="select-header">
            <Icon name="search" size="sm" class="text-gray-400" />
            <input
              ref="searchInputRef"
              v-model="searchQuery"
              class="select-search-input"
              type="text"
              :placeholder="searchPlaceholder"
              @click.stop
            />
          </div>

          <div class="select-options">
            <button
              v-for="key in filteredKeys"
              :key="key.id"
              type="button"
              :class="['select-option', modelValue === key.id && 'select-option-selected', !keyIsBindable(key) && 'select-option-disabled']"
              :disabled="!keyIsBindable(key)"
              @click="selectKey(key.id)"
            >
              <div class="min-w-0 flex-1 text-left">
                <div class="flex min-w-0 flex-wrap items-center gap-2">
                  <span class="truncate font-medium text-gray-900 dark:text-gray-100">
                    {{ keyName(key) }}
                  </span>
                  <span class="meta-pill">{{ groupLabel(key) }}</span>
                  <span class="meta-pill">{{ rateLabel(key) }}</span>
                  <span v-if="key.status === 'stale'" class="meta-pill meta-pill-warning">{{ t('admin.accounts.upstreamKeySelector.staleBadge') }}</span>
                </div>
                <div class="mt-1 flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1 text-xs text-gray-500 dark:text-dark-400">
                  <span>{{ platformLabel(key) }}</span>
                  <span>{{ remoteIdLabel(key) }}</span>
                  <span>{{ keySuffixLabel(key) }}</span>
                  <span>{{ seenAtLabel(key) }}</span>
                </div>
              </div>
              <Icon v-if="modelValue === key.id" name="check" size="sm" class="flex-shrink-0 text-primary-500" />
            </button>

            <div v-if="filteredKeys.length === 0" class="select-empty">
              {{ emptyText }}
            </div>
          </div>
        </div>
      </Transition>
    </div>

    <div
      v-if="selectedKey"
      :class="selectedKey.status === 'stale'
        ? 'rounded-lg border border-amber-200 bg-amber-50 px-3 py-2 text-xs text-amber-800 dark:border-amber-900/50 dark:bg-amber-900/20 dark:text-amber-200'
        : 'rounded-lg border border-emerald-100 bg-emerald-50 px-3 py-2 text-xs text-emerald-800 dark:border-emerald-900/50 dark:bg-emerald-900/20 dark:text-emerald-200'"
    >
      <div class="font-medium">{{ selectedSummaryTitle }}</div>
      <div class="mt-1 text-emerald-700/80 dark:text-emerald-200/80">
        {{ selectedSummaryMeta }}
      </div>
      <div v-if="selectedKey.status === 'stale'" class="mt-2 font-medium" data-test="stale-key-warning">
        {{ t('admin.accounts.upstreamKeySelector.staleWarning') }}
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import type { UpstreamKey } from '@/api/admin/upstreamConfigs'

const { t } = useI18n()

const props = withDefaults(defineProps<{
  modelValue: number | null
  keys: UpstreamKey[]
  platform: string
  assignedKeyId?: number | null
  disabled?: boolean
  placeholder?: string
  emptyText?: string
  searchPlaceholder?: string
}>(), {
  disabled: false,
  assignedKeyId: null,
  placeholder: '',
  emptyText: '',
  searchPlaceholder: ''
})

const emit = defineEmits<{
  'update:modelValue': [value: number | null]
}>()

const isOpen = ref(false)
const searchQuery = ref('')
const containerRef = ref<HTMLElement | null>(null)
const searchInputRef = ref<HTMLInputElement | null>(null)

const placeholder = computed(() => props.placeholder || t('admin.accounts.upstreamKeySelector.placeholder'))
const emptyText = computed(() => props.emptyText || t('admin.accounts.upstreamKeySelector.empty'))
const searchPlaceholder = computed(() => props.searchPlaceholder || t('admin.accounts.upstreamKeySelector.searchPlaceholder'))

const availableKeys = computed(() => props.keys.filter((key) =>
  keyMatchesPlatform(key, props.platform) &&
  (keyIsBindable(key) || key.id === props.assignedKeyId)
))

const selectedKey = computed(() => availableKeys.value.find((key) => key.id === props.modelValue) || null)

const filteredKeys = computed(() => {
  const query = searchQuery.value.trim().toLowerCase()
  if (!query) return availableKeys.value
  return availableKeys.value.filter((key) => optionSearchText(key).includes(query))
})

const selectedSummaryTitle = computed(() => {
  if (!selectedKey.value) return ''
  return t('admin.accounts.upstreamKeySelector.selectedTitle', {
    name: keyName(selectedKey.value)
  })
})

const selectedSummaryMeta = computed(() => {
  if (!selectedKey.value) return ''
  return [
    groupLabel(selectedKey.value),
    rateLabel(selectedKey.value),
    keySuffixLabel(selectedKey.value)
  ].join(' / ')
})

function toggle() {
  if (props.disabled) return
  isOpen.value = !isOpen.value
  if (isOpen.value) {
    nextTick(() => searchInputRef.value?.focus())
  }
}

function selectKey(id: number) {
  const key = availableKeys.value.find((item) => item.id === id)
  if (!key || !keyIsBindable(key)) return
  emit('update:modelValue', id)
  isOpen.value = false
  searchQuery.value = ''
}

function keyIsBindable(key: UpstreamKey): boolean {
  if (key.status !== 'active') return false
  if ((key.platform_source || '').trim().toLowerCase() === 'manual') return true
  const detectionStatus = (key.platform_detection_status || '').trim().toLowerCase()
  return !['unresolved', 'ambiguous', 'conflict'].includes(detectionStatus)
}

function keyMatchesPlatform(key: UpstreamKey, platform: string): boolean {
  const keyPlatform = (key.platform || '').trim().toLowerCase()
  const currentPlatform = platform.trim().toLowerCase()
  return keyPlatform !== '' && currentPlatform !== '' && keyPlatform === currentPlatform
}

function optionTitle(key: UpstreamKey): string {
  return [
    keyName(key),
    groupLabel(key),
    rateLabel(key)
  ].join(' · ')
}

function keyName(key: UpstreamKey): string {
  const name = (key.name || '').trim()
  if (name && !isSuffixOnlyName(name, key.key_status?.suffix)) return name
  return key.remote_key_id != null
    ? t('admin.accounts.upstreamKeySelector.unnamedRemote', { id: key.remote_key_id })
    : t('admin.accounts.upstreamKeySelector.unnamedLocal', { id: key.id })
}

function isSuffixOnlyName(name: string, suffix?: string): boolean {
  const normalizedName = name.trim()
  const normalizedSuffix = (suffix || '').trim()
  if (!normalizedName || !normalizedSuffix) return false
  return (
    normalizedName === normalizedSuffix ||
    normalizedName === `...${normalizedSuffix}` ||
    normalizedName === `****${normalizedSuffix}`
  )
}

function groupLabel(key: UpstreamKey): string {
  const name = (key.upstream_group_name || '').trim()
  if (name) return t('admin.accounts.upstreamKeySelector.group', { group: name })
  if (key.upstream_group_id != null) {
    return t('admin.accounts.upstreamKeySelector.groupId', { id: key.upstream_group_id })
  }
  return t('admin.accounts.upstreamKeySelector.ungrouped')
}

function rateLabel(key: UpstreamKey): string {
	const defaultRate = numberExtra(key, 'default_rate_multiplier')
	const dedicatedRate = numberExtra(key, 'dedicated_rate_multiplier')
	if (hasDedicatedRate(key) && dedicatedRate != null) {
		if (defaultRate != null) {
			return t('admin.accounts.upstreamKeySelector.rateOverride', {
				defaultRate: formatRate(defaultRate),
				dedicatedRate: formatRate(dedicatedRate)
			})
		}
		return t('admin.accounts.upstreamKeySelector.rateDedicated', {
			dedicatedRate: formatRate(dedicatedRate)
		})
	}
  if (key.rate_multiplier == null || !Number.isFinite(key.rate_multiplier)) {
    return t('admin.accounts.upstreamKeySelector.rateUnknown')
  }
  return t('admin.accounts.upstreamKeySelector.rate', {
    rate: formatRate(key.rate_multiplier)
  })
}

function platformLabel(key: UpstreamKey): string {
  const platform = (key.platform || '').trim()
  return platform || t('admin.accounts.upstreamKeySelector.platformUnknown')
}

function remoteIdLabel(key: UpstreamKey): string {
  return key.remote_key_id != null
    ? t('admin.accounts.upstreamKeySelector.remoteId', { id: key.remote_key_id })
    : t('admin.accounts.upstreamKeySelector.localId', { id: key.id })
}

function keySuffixLabel(key: UpstreamKey): string {
  const suffix = key.key_status?.suffix
  return suffix
    ? t('admin.accounts.upstreamKeySelector.suffix', { suffix })
    : t('admin.accounts.upstreamKeySelector.suffixMissing')
}

function seenAtLabel(key: UpstreamKey): string {
  const value = key.last_seen_at || key.updated_at
  if (!value) return t('admin.accounts.upstreamKeySelector.neverSeen')
  return t('admin.accounts.upstreamKeySelector.lastSeen', { time: formatDate(value) })
}

function formatRate(value: number): string {
  return Number.isInteger(value) ? value.toFixed(0) : value.toFixed(4).replace(/0+$/, '').replace(/\.$/, '')
}

function formatDate(value: string): string {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return '-'
  return date.toLocaleString()
}

function hasDedicatedRate(key: UpstreamKey): boolean {
  return key.extra?.has_dedicated_rate_multiplier === true
}

function numberExtra(key: UpstreamKey, name: string): number | null {
  const value = key.extra?.[name]
  if (typeof value === 'number' && Number.isFinite(value)) return value
  if (typeof value === 'string' && value.trim()) {
    const parsed = Number(value)
    if (Number.isFinite(parsed)) return parsed
  }
  return null
}

function optionSearchText(key: UpstreamKey): string {
  return [
    keyName(key),
    groupLabel(key),
    key.upstream_group_name || '',
    key.upstream_group_id == null ? '' : String(key.upstream_group_id),
    key.platform || '',
    key.rate_multiplier == null ? '' : String(key.rate_multiplier),
    numberExtra(key, 'default_rate_multiplier') == null ? '' : String(numberExtra(key, 'default_rate_multiplier')),
    numberExtra(key, 'dedicated_rate_multiplier') == null ? '' : String(numberExtra(key, 'dedicated_rate_multiplier')),
    key.key_status?.suffix || '',
    key.remote_key_id == null ? '' : String(key.remote_key_id)
  ].join(' ').toLowerCase()
}

function handleClickOutside(event: MouseEvent) {
  if (containerRef.value && !containerRef.value.contains(event.target as Node)) {
    isOpen.value = false
    searchQuery.value = ''
  }
}

function handleEscape(event: KeyboardEvent) {
  if (event.key === 'Escape' && isOpen.value) {
    isOpen.value = false
    searchQuery.value = ''
  }
}

onMounted(() => {
  document.addEventListener('click', handleClickOutside)
  document.addEventListener('keydown', handleEscape)
})

onUnmounted(() => {
  document.removeEventListener('click', handleClickOutside)
  document.removeEventListener('keydown', handleEscape)
})
</script>

<style scoped>
.select-trigger {
  @apply flex w-full items-center justify-between gap-2 rounded-xl border border-gray-200 bg-white px-4 py-2.5 text-sm text-gray-900 transition-all duration-200 hover:border-gray-300 focus:border-primary-500 focus:outline-none focus:ring-2 focus:ring-primary-500/30 dark:border-dark-600 dark:bg-dark-800 dark:text-gray-100 dark:hover:border-dark-500;
}

.select-trigger-open {
  @apply border-primary-500 ring-2 ring-primary-500/30;
}

.select-trigger-disabled {
  @apply cursor-not-allowed bg-gray-100 opacity-60 dark:bg-dark-900;
}

.select-dropdown {
  @apply absolute z-[100] mt-2 w-full overflow-hidden rounded-xl border border-gray-200 bg-white shadow-lg shadow-black/10 dark:border-dark-700 dark:bg-dark-800 dark:shadow-black/30;
}

.select-header {
  @apply flex items-center gap-2 border-b border-gray-100 px-3 py-2 dark:border-dark-700;
}

.select-search-input {
  @apply flex-1 bg-transparent text-sm text-gray-900 placeholder:text-gray-400 focus:outline-none dark:text-gray-100 dark:placeholder:text-dark-400;
}

.select-options {
  @apply max-h-72 overflow-y-auto py-1;
}

.select-option {
  @apply flex w-full cursor-pointer items-center justify-between gap-2 px-4 py-3 text-sm transition-colors duration-150 hover:bg-gray-50 dark:hover:bg-dark-700;
}

.select-option-selected {
  @apply bg-primary-50 dark:bg-primary-900/20;
}

.select-option-disabled {
  @apply cursor-not-allowed opacity-60;
}

.meta-pill {
  @apply inline-flex flex-shrink-0 items-center rounded bg-gray-100 px-1.5 py-0.5 text-xs text-gray-600 dark:bg-dark-600 dark:text-gray-300;
}

.meta-pill-warning {
  @apply border-amber-200 bg-amber-50 text-amber-700 dark:border-amber-800 dark:bg-amber-900/30 dark:text-amber-300;
}

.select-empty {
  @apply px-4 py-8 text-center text-sm text-gray-500 dark:text-dark-400;
}

.select-dropdown-enter-active,
.select-dropdown-leave-active {
  transition: all 0.2s ease;
}

.select-dropdown-enter-from,
.select-dropdown-leave-to {
  opacity: 0;
  transform: translateY(-8px);
}
</style>
