<script setup lang="ts">
import { onBeforeUnmount, ref, useTemplateRef, nextTick, watch } from 'vue'

const props = withDefaults(defineProps<{
  content?: string
  trigger?: 'hover' | 'click'
  widthClass?: string
  triggerClass?: string
}>(), {
  trigger: 'hover',
  widthClass: 'w-64',
  triggerClass: '',
})

const show = ref(false)
const triggerRef = useTemplateRef<HTMLElement>('trigger')
const tooltipRef = useTemplateRef<HTMLElement>('tooltip')
const tooltipStyle = ref({ top: '0px', left: '0px', maxWidth: 'calc(100vw - 32px)' })
const placement = ref<'top' | 'bottom'>('top')
const arrowLeft = ref('50%')

function openTooltip() {
  show.value = true
}

function closeTooltip() {
  show.value = false
}

function onEnter() {
  if (props.trigger !== 'hover') return
  openTooltip()
}

function isInside(container: HTMLElement | null, target: EventTarget | null): boolean {
  return target instanceof Node && !!container?.contains(target)
}

// 悬停模式下指针在触发图标与提示框之间往返时保持打开，便于选中提示里的文字。
function onLeave(event: MouseEvent) {
  if (props.trigger !== 'hover') return
  if (isInside(tooltipRef.value, event.relatedTarget)) return
  closeTooltip()
}

function onTooltipLeave(event: MouseEvent) {
  if (props.trigger !== 'hover') return
  if (isInside(triggerRef.value, event.relatedTarget)) return
  closeTooltip()
}

function onClick(event: MouseEvent) {
  if (props.trigger !== 'click') return
  event.stopPropagation()
  if (show.value) {
    closeTooltip()
    return
  }
  openTooltip()
}

function onDocumentClick(event: MouseEvent) {
  if (props.trigger !== 'click' || !show.value) return
  const target = event.target as Node | null
  if (!target) return
  if (triggerRef.value?.contains(target) || tooltipRef.value?.contains(target)) return
  closeTooltip()
}

function onDocumentKeydown(event: KeyboardEvent) {
  if (props.trigger !== 'click') return
  if (event.key === 'Escape') {
    closeTooltip()
  }
}

function onViewportChange() {
  if (!show.value) return
  updatePosition()
}

function updatePosition() {
  if (!show.value) return
  const el = triggerRef.value
  if (!el) return
  const rect = el.getBoundingClientRect()
  const tooltipRect = tooltipRef.value?.getBoundingClientRect()
  const margin = 16
  const gap = 8
  const width = Math.min(tooltipRect?.width || 0, Math.max(window.innerWidth - margin * 2, 0))
  const height = tooltipRect?.height || 0
  const above = rect.top - gap - height
  placement.value = above >= margin ? 'top' : 'bottom'
  const desiredTop = placement.value === 'top' ? above : rect.bottom + gap
  const top = Math.max(margin, Math.min(desiredTop, window.innerHeight - margin - height))
  const center = rect.left + rect.width / 2
  const left = Math.max(margin + width / 2, Math.min(center, window.innerWidth - margin - width / 2))
  arrowLeft.value = `${Math.max(12, Math.min(center - left + width / 2, width - 12))}px`
  tooltipStyle.value = {
    top: `${top}px`,
    left: `${left}px`,
    maxWidth: 'calc(100vw - 32px)',
  }
}

let viewportListenersAttached = false

function attachViewportListeners() {
  if (viewportListenersAttached) return
  viewportListenersAttached = true
  document.addEventListener('click', onDocumentClick, true)
  document.addEventListener('keydown', onDocumentKeydown)
  window.addEventListener('resize', onViewportChange)
  window.addEventListener('scroll', onViewportChange, true)
}

function detachViewportListeners() {
  if (!viewportListenersAttached) return
  viewportListenersAttached = false
  document.removeEventListener('click', onDocumentClick, true)
  document.removeEventListener('keydown', onDocumentKeydown)
  window.removeEventListener('resize', onViewportChange)
  window.removeEventListener('scroll', onViewportChange, true)
}

watch(show, async (isOpen) => {
  if (!isOpen) {
    detachViewportListeners()
    return
  }

  attachViewportListeners()
  await nextTick()
  updatePosition()
})

onBeforeUnmount(() => {
  detachViewportListeners()
})
</script>

<template>
  <div
    ref="trigger"
    :class="['group relative ml-1 inline-flex items-center align-middle', props.triggerClass]"
    @mouseenter="onEnter"
    @mouseleave="onLeave"
    @click="onClick"
  >
    <!-- Trigger Icon -->
    <slot name="trigger">
      <svg
        class="h-4 w-4 cursor-help text-gray-400 transition-colors hover:text-primary-600 dark:text-gray-500 dark:hover:text-primary-400"
        fill="none"
        viewBox="0 0 24 24"
        stroke="currentColor"
        stroke-width="2"
      >
        <path
          stroke-linecap="round"
          stroke-linejoin="round"
          d="M13 16h-1v-4h-1m1-4h.01M21 12a9 9 0 11-18 0 9 9 0 0118 0z"
        />
      </svg>
    </slot>

    <!-- Only mount the Teleport while open: long tables otherwise create hundreds of hidden nodes. -->
    <Teleport v-if="show" to="body">
      <!-- before: 伪元素向下延伸一段透明区域，盖住提示框与触发图标之间的空隙，让指针能连续移入提示框。 -->
      <div
        ref="tooltip"
        role="tooltip"
        :data-placement="placement"
        :class="[
          'fixed z-[99999] -translate-x-1/2 rounded-lg bg-gray-900 p-3 text-xs leading-relaxed text-white shadow-xl ring-1 ring-white/10 selection:bg-primary-200 selection:text-gray-900 before:absolute before:inset-x-0 before:h-3 dark:bg-gray-800 dark:selection:bg-primary-200 dark:selection:text-gray-900',
          placement === 'top' ? 'before:top-full' : 'before:bottom-full',
          props.widthClass,
        ]"
        :style="tooltipStyle"
        @mouseleave="onTooltipLeave"
      >
        <button
          v-if="props.trigger === 'click'"
          type="button"
          class="absolute right-1.5 top-1.5 rounded p-1 text-gray-300 transition-colors hover:bg-white/10 hover:text-white"
          aria-label="Close"
          @click.stop="closeTooltip"
        >
          <svg class="h-3.5 w-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2">
            <path stroke-linecap="round" stroke-linejoin="round" d="M6 18L18 6M6 6l12 12" />
          </svg>
        </button>
        <slot>{{ content }}</slot>
        <div :style="{ left: arrowLeft }" :class="['absolute h-2 w-2 -translate-x-1/2 rotate-45 bg-gray-900 dark:bg-gray-800', placement === 'top' ? '-bottom-1' : '-top-1']"></div>
      </div>
    </Teleport>
  </div>
</template>
