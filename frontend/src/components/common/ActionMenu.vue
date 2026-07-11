<template>
  <Teleport to="body">
    <div v-if="show && anchorEl">
      <div class="fixed inset-0 z-[9998]" data-testid="action-menu-backdrop" @click="close"></div>
      <div
        ref="menuRef"
        class="action-menu fixed z-[9999] overflow-hidden bg-white shadow-lg ring-1 ring-black/5 dark:bg-dark-800 dark:ring-white/10"
        :class="widthClass"
        :style="menuStyle"
        role="menu"
        tabindex="-1"
        @click.stop
        @keydown="handleKeydown"
      >
        <slot :close="close" />
      </div>
    </div>
  </Teleport>
</template>

<script setup lang="ts">
import { computed, nextTick, onUnmounted, ref, watch } from 'vue'
import type { CSSProperties } from 'vue'

const VIEWPORT_PADDING = 8
const MENU_GAP = 4

const props = withDefaults(defineProps<{
  show: boolean
  anchorEl: HTMLElement | null
  width?: 'normal' | 'wide'
}>(), {
  width: 'normal'
})

const emit = defineEmits<{
  close: []
}>()

const menuRef = ref<HTMLElement | null>(null)
const top = ref(VIEWPORT_PADDING)
const left = ref(VIEWPORT_PADDING)
const positioned = ref(false)
let restoreFocusEl: HTMLElement | null = null
let anchorObserver: MutationObserver | null = null

const widthClass = computed(() => props.width === 'wide' ? 'w-52' : 'w-48')
const menuStyle = computed<CSSProperties>(() => ({
  top: `${top.value}px`,
  left: `${left.value}px`,
  visibility: positioned.value ? 'visible' : 'hidden'
}))

const close = () => emit('close')

const getMenuItems = () => {
  if (!menuRef.value) return []
  return Array.from(menuRef.value.querySelectorAll<HTMLElement>('[role="menuitem"]'))
    .filter(item => !item.hasAttribute('disabled') && item.getAttribute('aria-disabled') !== 'true')
}

const setActiveItem = (items: HTMLElement[], activeIndex: number) => {
  menuRef.value?.querySelectorAll<HTMLElement>('[role="menuitem"]')
    .forEach(item => item.setAttribute('tabindex', '-1'))
  items.forEach((item, index) => item.setAttribute('tabindex', index === activeIndex ? '0' : '-1'))
}

const focusItem = (index: number) => {
  const items = getMenuItems()
  if (items.length === 0) {
    menuRef.value?.focus()
    return
  }
  const activeIndex = (index + items.length) % items.length
  setActiveItem(items, activeIndex)
  items[activeIndex].focus()
}

const handleKeydown = (event: KeyboardEvent) => {
  if (event.key === 'Escape') {
    event.preventDefault()
    close()
    return
  }

  if (event.key === 'Tab') {
    event.preventDefault()
    close()
    return
  }

  const items = getMenuItems()
  if (items.length === 0) return
  const currentIndex = items.indexOf(document.activeElement as HTMLElement)

  if (event.key === 'ArrowDown') {
    event.preventDefault()
    focusItem(currentIndex + 1)
  } else if (event.key === 'ArrowUp') {
    event.preventDefault()
    focusItem(currentIndex < 0 ? items.length - 1 : currentIndex - 1)
  } else if (event.key === 'Home') {
    event.preventDefault()
    focusItem(0)
  } else if (event.key === 'End') {
    event.preventDefault()
    focusItem(items.length - 1)
  }
}

const updatePosition = async () => {
  await nextTick()
  const anchor = props.anchorEl
  const menu = menuRef.value
  if (!props.show || !anchor || !menu) return
  if (!anchor.isConnected) {
    close()
    return
  }

  const anchorRect = anchor.getBoundingClientRect()
  const menuRect = menu.getBoundingClientRect()
  const menuWidth = menuRect.width || menu.offsetWidth
  const menuHeight = menuRect.height || menu.offsetHeight
  const maxLeft = Math.max(VIEWPORT_PADDING, window.innerWidth - menuWidth - VIEWPORT_PADDING)
  const maxTop = Math.max(VIEWPORT_PADDING, window.innerHeight - menuHeight - VIEWPORT_PADDING)

  let nextLeft = anchorRect.right - menuWidth
  let nextTop = anchorRect.bottom + MENU_GAP

  if (nextTop + menuHeight > window.innerHeight - VIEWPORT_PADDING) {
    nextTop = anchorRect.top - menuHeight - MENU_GAP
  }

  left.value = Math.min(Math.max(nextLeft, VIEWPORT_PADDING), maxLeft)
  top.value = Math.min(Math.max(nextTop, VIEWPORT_PADDING), maxTop)
  positioned.value = true
}

const startListeners = () => {
  window.addEventListener('scroll', updatePosition, { capture: true, passive: true })
  window.addEventListener('resize', updatePosition)
  anchorObserver = new MutationObserver(() => {
    if (props.show && props.anchorEl && !props.anchorEl.isConnected) close()
  })
  anchorObserver.observe(document.documentElement, { childList: true, subtree: true })
}

const stopListeners = () => {
  window.removeEventListener('scroll', updatePosition, { capture: true })
  window.removeEventListener('resize', updatePosition)
  anchorObserver?.disconnect()
  anchorObserver = null
}

watch(
  () => [props.show, props.anchorEl] as const,
  async ([show], previous) => {
    const wasShown = previous?.[0] ?? false
    stopListeners()

    if (!show || !props.anchorEl) {
      positioned.value = false
      if (wasShown) {
        await nextTick()
        const modalHasFocus = document.activeElement instanceof HTMLElement && Boolean(document.activeElement.closest('[role="dialog"][aria-modal="true"]'))
        const modalIsOpen = Boolean(document.querySelector('[role="dialog"][aria-modal="true"]'))
        if (!modalHasFocus && !modalIsOpen && restoreFocusEl?.isConnected) restoreFocusEl.focus()
        restoreFocusEl = null
      }
      return
    }

    restoreFocusEl = document.activeElement instanceof HTMLElement && document.activeElement !== document.body
      ? document.activeElement
      : props.anchorEl
    positioned.value = false
    startListeners()
    await updatePosition()
    await nextTick()
    focusItem(0)
  },
  { immediate: true }
)

onUnmounted(() => {
  stopListeners()
  const modalIsOpen = Boolean(document.querySelector('[role="dialog"][aria-modal="true"]'))
  if (!modalIsOpen && restoreFocusEl?.isConnected) restoreFocusEl.focus()
})
</script>

<style scoped>
.action-menu {
  @apply rounded-xl py-1;
}

.action-menu :deep([role='menuitem']) {
  @apply flex w-full items-center gap-2 px-4 py-2 text-sm;
  @apply hover:bg-gray-100 dark:hover:bg-dark-700;
}

.action-menu :deep([data-menu-divider]) {
  @apply my-1 border-t border-gray-100 dark:border-dark-700;
}
</style>
