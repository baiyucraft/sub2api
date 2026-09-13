<template>
  <div v-if="groups && groups.length > 0" class="account-groups-cell relative w-full min-w-0 max-w-full">
    <!-- 分组容器：固定最大宽度，最多显示2行；放不下的分组通过 +N 展开。 -->
    <div ref="groupsContainerRef" data-testid="account-groups-list" class="flex min-w-0 flex-wrap gap-1 overflow-hidden max-h-14">
      <div
        v-for="group in displayGroups"
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
      <!-- 更多数量徽章 -->
      <button
        v-if="hiddenCount > 0"
        ref="moreButtonRef"
        @click.stop="showPopover = !showPopover"
        class="inline-flex items-center gap-0.5 rounded-md px-1.5 py-0.5 text-xs font-medium bg-gray-100 text-gray-600 hover:bg-gray-200 dark:bg-dark-600 dark:text-gray-300 dark:hover:bg-dark-500 transition-colors cursor-pointer whitespace-nowrap"
      >
        <span>+{{ hiddenCount }}</span>
      </button>
    </div>

    <!-- Popover 显示完整列表 -->
    <Teleport to="body">
      <Transition
        enter-active-class="transition duration-150 ease-out"
        enter-from-class="opacity-0 scale-95"
        enter-to-class="opacity-100 scale-100"
        leave-active-class="transition duration-100 ease-in"
        leave-from-class="opacity-100 scale-100"
        leave-to-class="opacity-0 scale-95"
      >
        <div
          v-if="showPopover"
          ref="popoverRef"
          data-testid="account-groups-popover"
          class="fixed z-50 min-w-48 max-w-96 rounded-lg border border-gray-200 bg-white p-3 shadow-lg dark:border-dark-600 dark:bg-dark-800"
          :style="popoverStyle"
        >
          <div class="mb-2 flex items-center justify-between">
            <span class="text-xs font-medium text-gray-500 dark:text-gray-400">
              {{ t('admin.accounts.groupCountTotal', { count: groups.length }) }}
            </span>
            <button
              @click="showPopover = false"
              class="rounded p-0.5 text-gray-400 hover:bg-gray-100 hover:text-gray-600 dark:hover:bg-dark-700 dark:hover:text-gray-300"
            >
              <svg class="h-3.5 w-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2">
                <path stroke-linecap="round" stroke-linejoin="round" d="M6 18L18 6M6 6l12 12" />
              </svg>
            </button>
          </div>
          <div class="flex flex-wrap gap-1.5 max-h-64 overflow-y-auto">
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
                <Icon name="star" size="xs" :stroke-width="2" aria-hidden="true" />
              </span>
            </div>
          </div>
        </div>
      </Transition>
    </Teleport>

    <!-- 点击外部关闭 popover -->
    <div
      v-if="showPopover"
      class="fixed inset-0 z-40"
      @click="showPopover = false"
    />
  </div>
  <span v-else class="text-sm text-gray-400 dark:text-dark-500">-</span>
</template>

<script setup lang="ts">
import { ref, computed, nextTick, onMounted, onUnmounted, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import GroupBadge from '@/components/common/GroupBadge.vue'
import Icon from '@/components/icons/Icon.vue'
import type { Group } from '@/types'

interface Props {
  groups: Group[] | null | undefined
  maxDisplay?: number
  preferredGroupIds?: number[]
  accountId?: number | string | null
  interactive?: boolean
}

const props = withDefaults(defineProps<Props>(), {
  maxDisplay: 4,
  interactive: false
})

const emit = defineEmits<{
  (event: 'toggle-preferred', payload: { groupId: number; preferred: boolean }): void
}>()

const { t } = useI18n()

const moreButtonRef = ref<HTMLElement | null>(null)
const popoverRef = ref<HTMLElement | null>(null)
const groupsContainerRef = ref<HTMLElement | null>(null)
const showPopover = ref(false)
const visibleGroupCount = ref<number | null>(null)
let groupsResizeObserver: ResizeObserver | null = null
let groupMeasureFrame: number | null = null
let measuringGroups = false

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

const maxVisibleGroupCount = computed(() => {
  if (!props.groups?.length) return 0
  const maxDisplay = Math.max(1, Math.floor(props.maxDisplay))
  // Preserve the existing contract: when the count limit is exceeded, reserve
  // one slot for the +N control so the hidden groups remain discoverable.
  const candidateCount = props.groups.length > maxDisplay ? maxDisplay - 1 : props.groups.length
  return Math.max(1, candidateCount)
})

// 显示的分组：先按数量上限渲染，再根据实际列宽收缩，避免内容被 max-h-14 静默裁掉。
const displayGroups = computed(() => {
  if (!props.groups) return []
  const count = visibleGroupCount.value ?? maxVisibleGroupCount.value
  return props.groups.slice(0, count)
})

// 隐藏的数量
const hiddenCount = computed(() => {
  if (!props.groups) return 0
  return Math.max(0, props.groups.length - displayGroups.value.length)
})

const measureGroupLayout = async () => {
  if (measuringGroups) return
  measuringGroups = true

  try {
    const container = groupsContainerRef.value
    const maxCount = maxVisibleGroupCount.value

    if (!container || maxCount === 0) {
      visibleGroupCount.value = null
      return
    }

    // Always retry from the full candidate set after a resize. A wider column may
    // make a previously hidden group visible again.
    visibleGroupCount.value = maxCount
    await nextTick()

    // jsdom and hidden tabs report zero dimensions; defer to the browser's next
    // ResizeObserver notification instead of treating that as overflow.
    if (container.clientWidth <= 0 || container.clientHeight <= 0) return

    while (
      visibleGroupCount.value > 1 &&
      container.scrollHeight > container.clientHeight + 1
    ) {
      visibleGroupCount.value -= 1
      await nextTick()
    }
  } finally {
    measuringGroups = false
  }
}

const scheduleGroupLayoutMeasure = () => {
  if (groupMeasureFrame !== null) return

  const run = () => {
    groupMeasureFrame = null
    void measureGroupLayout()
  }

  groupMeasureFrame = typeof window !== 'undefined' && typeof window.requestAnimationFrame === 'function'
    ? window.requestAnimationFrame(run)
    : setTimeout(run, 0) as unknown as number
}

// Popover 位置样式
const popoverStyle = computed(() => {
  if (!moreButtonRef.value) return {}
  const rect = moreButtonRef.value.getBoundingClientRect()
  const viewportHeight = window.innerHeight
  const viewportWidth = window.innerWidth

  let top = rect.bottom + 8
  let left = rect.left

  // 如果下方空间不足，显示在上方
  if (top + 280 > viewportHeight) {
    top = Math.max(8, rect.top - 280)
  }

  // 如果右侧空间不足，向左偏移
  if (left + 384 > viewportWidth) {
    left = Math.max(8, viewportWidth - 392)
  }

  return {
    top: `${top}px`,
    left: `${left}px`
  }
})

// 关闭 popover 的键盘事件
const handleKeydown = (e: KeyboardEvent) => {
  if (e.key === 'Escape') {
    showPopover.value = false
  }
}

onMounted(() => {
  window.addEventListener('keydown', handleKeydown)
  scheduleGroupLayoutMeasure()

  if (groupsContainerRef.value && typeof ResizeObserver !== 'undefined') {
    groupsResizeObserver = new ResizeObserver(() => {
      scheduleGroupLayoutMeasure()
    })
    groupsResizeObserver.observe(groupsContainerRef.value)
  }
})

onUnmounted(() => {
  window.removeEventListener('keydown', handleKeydown)
  groupsResizeObserver?.disconnect()
  groupsResizeObserver = null
  if (groupMeasureFrame !== null) {
    if (typeof window !== 'undefined' && typeof window.cancelAnimationFrame === 'function') {
      window.cancelAnimationFrame(groupMeasureFrame)
    } else {
      clearTimeout(groupMeasureFrame)
    }
    groupMeasureFrame = null
  }
})

watch(
  [() => props.groups, () => props.maxDisplay],
  () => scheduleGroupLayoutMeasure(),
  { deep: true, flush: 'post' }
)
</script>
