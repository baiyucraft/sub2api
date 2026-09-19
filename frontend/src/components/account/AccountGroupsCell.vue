<template>
  <div v-if="groups && groups.length > 0" class="account-groups-cell w-full min-w-0 max-w-full">
    <div data-testid="account-groups-list" class="flex min-w-0 flex-wrap gap-1">
      <div
        v-for="group in groups"
        :key="group.id"
        class="inline-flex min-w-0 max-w-full items-center gap-0.5"
      >
        <GroupBadge
          :name="group.name"
          :title="group.name"
          :platform="group.platform"
          :subscription-type="group.subscription_type"
          :rate-multiplier="group.rate_multiplier"
          :show-rate="false"
          class="min-w-0 max-w-[10rem]"
        />
        <button
          v-if="shouldShowPreferredState && interactive && accountId !== null && accountId !== undefined"
          type="button"
          class="inline-flex h-5 w-5 shrink-0 items-center justify-center rounded text-sm leading-none transition-colors hover:bg-amber-50 focus:outline-none focus:ring-1 focus:ring-amber-400 dark:hover:bg-amber-900/30"
          :class="isPreferred(group.id) ? 'text-amber-500 dark:text-amber-400' : 'text-gray-300 hover:text-amber-500 dark:text-dark-500 dark:hover:text-amber-400'"
          :title="preferredToggleLabel(group.id)"
          :aria-label="preferredToggleLabel(group.id)"
          :aria-pressed="isPreferred(group.id)"
          :data-account-id="accountId"
          :data-group-id="group.id"
          @click.stop="togglePreferred(group.id)"
        >
          <Icon
            name="star"
            size="xs"
            :stroke-width="2"
            :filled="isPreferred(group.id)"
            aria-hidden="true"
          />
        </button>
        <span
          v-else-if="shouldShowPreferredState"
          class="inline-flex h-5 w-5 shrink-0 items-center justify-center text-sm leading-none"
          :class="isPreferred(group.id) ? 'text-amber-500 dark:text-amber-400' : 'text-gray-300 dark:text-dark-500'"
          :title="preferredToggleLabel(group.id)"
          :aria-label="preferredToggleLabel(group.id)"
          :aria-pressed="isPreferred(group.id)"
          :data-account-id="accountId"
          :data-group-id="group.id"
          role="img"
        >
          <Icon
            name="star"
            size="xs"
            :stroke-width="2"
            :filled="isPreferred(group.id)"
            aria-hidden="true"
          />
        </span>
      </div>
    </div>
  </div>
  <span v-else class="text-sm text-gray-400 dark:text-dark-500">-</span>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import GroupBadge from '@/components/common/GroupBadge.vue'
import Icon from '@/components/icons/Icon.vue'
import type { Group } from '@/types'

interface Props {
  groups: Group[] | null | undefined
  preferredGroupIds?: number[]
  accountId?: number | string | null
  interactive?: boolean
}

const props = withDefaults(defineProps<Props>(), {
  interactive: false
})

const emit = defineEmits<{
  (event: 'toggle-preferred', payload: { groupId: number; preferred: boolean }): void
}>()

const { t } = useI18n()

const accountId = computed(() => props.accountId)
const interactive = computed(() => props.interactive)
const shouldShowPreferredState = computed(() => props.preferredGroupIds !== undefined)

const isPreferred = (groupId: number) => props.preferredGroupIds?.includes(groupId) ?? false

const preferredToggleLabel = (groupId: number) => {
  return isPreferred(groupId) ? t('admin.accounts.preferredEnabled') : t('admin.accounts.preferredDisabled')
}

const togglePreferred = (groupId: number) => {
  if (!props.interactive || props.accountId === null || props.accountId === undefined) return
  emit('toggle-preferred', { groupId, preferred: !isPreferred(groupId) })
}
</script>
