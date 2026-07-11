<template>
  <Teleport to="body">
    <Transition name="upstream-drawer">
      <div v-if="show" class="fixed inset-0 z-[60]" data-test="upstream-operations-drawer">
        <div class="absolute inset-0 bg-gray-950/45" @click="emit('close')"></div>
        <section
          ref="panelRef"
          class="absolute inset-y-0 right-0 flex w-full max-w-3xl flex-col bg-white shadow-2xl dark:bg-dark-900"
          role="dialog"
          aria-modal="true"
          :aria-labelledby="titleId"
        >
          <header class="flex min-h-16 items-center justify-between border-b border-gray-200 px-5 dark:border-dark-700">
            <div class="min-w-0">
              <h2 :id="titleId" class="truncate text-base font-semibold text-gray-900 dark:text-white">{{ title }}</h2>
              <p v-if="subtitle" class="mt-0.5 truncate text-xs text-gray-500 dark:text-dark-400">{{ subtitle }}</p>
            </div>
            <button
              type="button"
              class="table-action-button ml-3 flex-shrink-0"
              :aria-label="t('common.close')"
              @click="emit('close')"
            >
              <Icon name="x" size="md" />
            </button>
          </header>

          <div v-if="$slots.toolbar" class="border-b border-gray-200 px-5 py-3 dark:border-dark-700">
            <slot name="toolbar" />
          </div>

          <div class="min-h-0 flex-1 overflow-y-auto p-5">
            <slot />
          </div>

          <footer v-if="$slots.footer" class="border-t border-gray-200 px-5 py-3 dark:border-dark-700">
            <slot name="footer" />
          </footer>
        </section>
      </div>
    </Transition>
  </Teleport>
</template>

<script setup lang="ts">
import { nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'

const props = defineProps<{
  show: boolean
  title: string
  subtitle?: string
}>()

const emit = defineEmits<{
  (event: 'close'): void
}>()

const { t } = useI18n()
const panelRef = ref<HTMLElement | null>(null)
const titleId = `upstream-drawer-title-${Math.random().toString(36).slice(2)}`
let previousActiveElement: HTMLElement | null = null

function handleKeydown(event: KeyboardEvent) {
  if (props.show && event.key === 'Escape') emit('close')
}

watch(
  () => props.show,
  async (show) => {
    if (show) {
      previousActiveElement = document.activeElement as HTMLElement
      document.body.classList.add('modal-open')
      await nextTick()
      panelRef.value?.querySelector<HTMLElement>('button, input, select, textarea, [tabindex]:not([tabindex="-1"])')?.focus()
      return
    }
    document.body.classList.remove('modal-open')
    previousActiveElement?.focus?.()
    previousActiveElement = null
  },
  { immediate: true }
)

document.addEventListener('keydown', handleKeydown)

onBeforeUnmount(() => {
  document.removeEventListener('keydown', handleKeydown)
  document.body.classList.remove('modal-open')
})
</script>

<style scoped>
.upstream-drawer-enter-active,
.upstream-drawer-leave-active {
  transition: opacity 180ms ease;
}

.upstream-drawer-enter-active section,
.upstream-drawer-leave-active section {
  transition: transform 220ms ease;
}

.upstream-drawer-enter-from,
.upstream-drawer-leave-to {
  opacity: 0;
}

.upstream-drawer-enter-from section,
.upstream-drawer-leave-to section {
  transform: translateX(100%);
}

.table-action-button {
  @apply inline-flex h-9 w-9 items-center justify-center rounded-lg text-gray-500 transition-colors;
  @apply hover:bg-gray-100 dark:text-dark-300 dark:hover:bg-dark-700;
}
</style>
