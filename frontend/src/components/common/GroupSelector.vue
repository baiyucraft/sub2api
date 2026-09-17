<template>
  <div>
    <label class="input-label">
      {{ t('admin.users.groups') }}
      <span class="font-normal text-gray-400">{{ t('common.selectedCount', { count: modelValue.length }) }}</span>
    </label>
    <div
      v-if="isSearchable"
      class="flex items-center gap-2 rounded-t-lg border border-b-0 border-gray-200 bg-gray-50 px-3 py-2 dark:border-dark-600 dark:bg-dark-800"
    >
      <Icon name="search" size="sm" class="shrink-0 text-gray-400" />
      <input
        v-model="searchText"
        type="text"
        :disabled="disabled"
        :placeholder="t('common.searchPlaceholder')"
        class="flex-1 bg-transparent text-sm text-gray-900 placeholder:text-gray-400 focus:outline-none disabled:cursor-not-allowed dark:text-gray-100 dark:placeholder:text-dark-400"
      />
    </div>
    <div
      :class="[
        'grid max-h-32 grid-cols-2 gap-1 overflow-y-auto p-2',
        isSearchable
          ? 'rounded-b-lg border border-t-0 border-gray-200 bg-gray-50 dark:border-dark-600 dark:bg-dark-800'
          : 'rounded-lg border border-gray-200 bg-gray-50 dark:border-dark-600 dark:bg-dark-800'
      ]"
    >
      <div
        v-for="group in filteredGroups"
        :key="group.id"
        :class="[
          'flex items-center gap-1 rounded px-2 py-1.5 transition-colors',
          disabled ? 'opacity-60' : 'hover:bg-white dark:hover:bg-dark-700'
        ]"
        :title="group.rate_multiplier == null ? group.name : t('admin.groups.rateAndAccounts', { rate: group.rate_multiplier, count: group.account_count || 0 })"
      >
        <label :class="['flex min-w-0 flex-1 items-center gap-2', disabled ? 'cursor-not-allowed' : 'cursor-pointer']">
          <input
            type="checkbox"
            :value="group.id"
            :checked="modelValue.includes(group.id)"
            :disabled="disabled"
            @change="handleChange(group.id, ($event.target as HTMLInputElement).checked)"
            class="h-3.5 w-3.5 shrink-0 rounded border-gray-300 text-primary-500 focus:ring-primary-500 dark:border-dark-500"
          />
          <GroupBadge
            :name="group.name"
            :platform="group.platform"
            :subscription-type="group.subscription_type || undefined"
            :rate-multiplier="group.rate_multiplier == null ? undefined : group.rate_multiplier"
            class="min-w-0 flex-1"
          />
          <span class="shrink-0 text-xs text-gray-400">{{ group.account_count || 0 }}</span>
        </label>
        <button
          v-if="preferredGroupIds !== undefined"
          type="button"
          :disabled="disabled || !modelValue.includes(group.id)"
          :aria-label="t(preferredGroupIds.includes(group.id) ? 'common.unmarkPreferredGroup' : 'common.markPreferredGroup', { name: group.name })"
          :title="!disabled && modelValue.includes(group.id)
            ? t(preferredGroupIds.includes(group.id) ? 'common.unmarkPreferredGroup' : 'common.markPreferredGroup', { name: group.name })
            : t('common.preferredGroupRequiresSelection')"
          :data-testid="`preferred-group-${group.id}`"
          :class="[
            'inline-flex h-7 w-7 shrink-0 items-center justify-center rounded transition-colors focus:outline-none focus:ring-2 focus:ring-primary-500 focus:ring-offset-1 dark:focus:ring-offset-dark-800',
            preferredGroupIds.includes(group.id)
              ? 'text-amber-500 hover:bg-amber-50 dark:text-amber-400 dark:hover:bg-amber-900/20'
              : !disabled && modelValue.includes(group.id)
                ? 'text-gray-400 hover:bg-gray-100 hover:text-amber-500 dark:text-dark-300 dark:hover:bg-dark-600 dark:hover:text-amber-400'
                : 'cursor-not-allowed text-gray-300 opacity-60 dark:text-dark-500'
          ]"
          @click="togglePreferred(group.id)"
        >
          <Icon
            name="star"
            size="sm"
            :filled="preferredGroupIds.includes(group.id)"
            :stroke-width="1.8"
          />
        </button>
      </div>
      <div
        v-if="filteredGroups.length === 0"
        class="col-span-2 py-2 text-center text-sm text-gray-500 dark:text-gray-400"
      >
        {{ t('common.noGroupsAvailable') }}
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import GroupBadge from './GroupBadge.vue'
import Icon from '@/components/icons/Icon.vue'
import type { Group, GroupPlatform } from '@/types'
import { useAuthStore } from '@/stores'

const { t } = useI18n()
const authStore = useAuthStore()

interface Props {
  modelValue: number[]
  preferredGroupIds?: number[]
  groups: (Group & { account_count?: number })[]
  platform?: GroupPlatform // Optional platform filter
  mixedScheduling?: boolean // For antigravity accounts: allow anthropic/gemini groups
  searchable?: boolean | 'auto'
  disabled?: boolean
}

const props = withDefaults(defineProps<Props>(), {
  searchable: 'auto',
  disabled: false
})
const emit = defineEmits<{
  'update:modelValue': [value: number[]]
  'update:preferredGroupIds': [value: number[]]
}>()

const searchText = ref('')

const isSearchable = computed(() => {
  if (props.searchable === 'auto') return props.groups.length > 5
  return props.searchable
})

// Filter groups by platform if specified
const filteredGroups = computed(() => {
  let result = authStore.isSimpleMode
    ? props.groups.filter((g) => g.platform !== 'composite')
    : props.groups
  if (props.platform) {
    // antigravity 账户启用混合调度后，可选择 anthropic/gemini 分组
    if (props.platform === 'antigravity' && props.mixedScheduling) {
      result = result.filter(
        (g) => g.platform === 'antigravity' || g.platform === 'anthropic' || g.platform === 'gemini' || g.platform === 'composite'
      )
    } else {
      // 默认：只能选择同 platform 的分组；composite 分组可接收任意具体平台账号
      result = result.filter((g) => g.platform === props.platform || g.platform === 'composite')
    }
  }
  if (isSearchable.value && searchText.value) {
    const q = searchText.value.toLowerCase()
    result = result.filter(
      (g) => g.name.toLowerCase().includes(q) || g.description?.toLowerCase().includes(q)
    )
  }
  return result
})

watch(
  () => [authStore.isSimpleMode, props.groups, props.modelValue] as const,
  () => {
    if (!authStore.isSimpleMode || props.groups.length === 0) return
    const visibleIDs = new Set(props.groups.filter((group) => group.platform !== 'composite').map((group) => group.id))
    const cleaned = props.modelValue.filter((id) => visibleIDs.has(id))
    if (cleaned.length !== props.modelValue.length) {
      emit('update:modelValue', cleaned)
      if (props.preferredGroupIds !== undefined) {
        emit('update:preferredGroupIds', props.preferredGroupIds.filter((id) => visibleIDs.has(id)))
      }
    }
  },
  { immediate: true, deep: true }
)

watch(
  () => [props.modelValue, props.preferredGroupIds] as const,
  () => {
    if (props.preferredGroupIds === undefined) return
    const selected = new Set(props.modelValue)
    const cleaned = Array.from(new Set(props.preferredGroupIds.filter((id) => selected.has(id))))
    if (
      cleaned.length !== props.preferredGroupIds.length ||
      cleaned.some((id, index) => id !== props.preferredGroupIds?.[index])
    ) {
      emit('update:preferredGroupIds', cleaned)
    }
  },
  { immediate: true, deep: true }
)

const handleChange = (groupId: number, checked: boolean) => {
  if (props.disabled) return
  const newValue = checked
    ? Array.from(new Set([...props.modelValue, groupId]))
    : props.modelValue.filter((id) => id !== groupId)
  emit('update:modelValue', newValue)
  if (!checked && props.preferredGroupIds !== undefined && props.preferredGroupIds.includes(groupId)) {
    emit('update:preferredGroupIds', props.preferredGroupIds.filter((id) => id !== groupId))
  }
}

const togglePreferred = (groupId: number) => {
  if (props.disabled || props.preferredGroupIds === undefined || !props.modelValue.includes(groupId)) return
  const newValue = props.preferredGroupIds.includes(groupId)
    ? props.preferredGroupIds.filter((id) => id !== groupId)
    : [...props.preferredGroupIds, groupId]
  emit('update:preferredGroupIds', Array.from(new Set(newValue)))
}
</script>
