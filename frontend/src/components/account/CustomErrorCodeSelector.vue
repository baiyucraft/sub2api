<template>
  <section class="space-y-3">
    <div class="flex items-center justify-between gap-4">
      <div class="min-w-0">
        <label class="input-label mb-0">{{ title }}</label>
        <p v-if="hint" class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ hint }}</p>
      </div>
      <button
        type="button"
        :aria-label="title"
        :aria-pressed="enabled"
        @click="setEnabled(!enabled)"
        :class="[
          'relative inline-flex h-6 w-11 flex-shrink-0 cursor-pointer rounded-full border-2 border-transparent transition-colors duration-200 ease-in-out focus:outline-none focus:ring-2 focus:ring-primary-500 focus:ring-offset-2',
          enabled ? 'bg-primary-600' : 'bg-gray-200 dark:bg-dark-600'
        ]"
      >
        <span
          :class="[
            'pointer-events-none inline-block h-5 w-5 transform rounded-full bg-white shadow ring-0 transition duration-200 ease-in-out',
            enabled ? 'translate-x-5' : 'translate-x-0'
          ]"
        />
      </button>
    </div>

    <div v-if="enabled" class="space-y-3">
      <div v-if="warning" class="rounded-lg bg-amber-50 p-3 dark:bg-amber-900/20">
        <p class="text-xs text-amber-700 dark:text-amber-400">
          <Icon name="exclamationTriangle" size="sm" class="mr-1 inline" :stroke-width="2" />
          {{ warning }}
        </p>
      </div>

      <div class="flex flex-wrap gap-2">
        <button
          v-for="code in commonErrorCodes"
          :key="code.value"
          type="button"
          @click="toggleCode(code.value)"
          :class="[
            'rounded-lg px-3 py-1.5 text-sm font-medium transition-colors',
            selectedCodes.includes(code.value)
              ? 'bg-red-100 text-red-700 ring-1 ring-red-500 dark:bg-red-900/30 dark:text-red-400'
              : 'bg-gray-100 text-gray-600 hover:bg-gray-200 dark:bg-dark-600 dark:text-gray-400 dark:hover:bg-dark-500'
          ]"
        >
          {{ code.value }} {{ code.label }}
        </button>
      </div>

      <div class="flex items-center gap-2">
        <input
          v-model.number="inputValue"
          type="number"
          min="100"
          max="599"
          class="input flex-1"
          :placeholder="inputPlaceholder"
          @keyup.enter="addCode"
        />
        <button type="button" class="btn btn-secondary px-3" @click="addCode" :aria-label="addLabel">
          <Icon name="plus" size="sm" :stroke-width="2" />
        </button>
      </div>

      <div class="flex flex-wrap gap-1.5">
        <span
          v-for="code in sortedCodes"
          :key="code"
          class="inline-flex items-center gap-1 rounded-full bg-red-100 px-2.5 py-0.5 text-sm font-medium text-red-700 dark:bg-red-900/30 dark:text-red-400"
        >
          {{ code }}
          <button type="button" class="hover:text-red-900 dark:hover:text-red-300" :aria-label="`${removeLabel} ${code}`" @click="removeCode(code)">
            <Icon name="x" size="sm" :stroke-width="2" />
          </button>
        </span>
        <span v-if="selectedCodes.length === 0" class="text-xs text-gray-400">{{ noneSelectedText }}</span>
      </div>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import Icon from '@/components/icons/Icon.vue'
import { commonErrorCodes } from '@/composables/useModelWhitelist'

const props = withDefaults(defineProps<{
  enabled: boolean
  modelValue: number[]
  title: string
  hint?: string
  warning?: string
  inputPlaceholder?: string
  noneSelectedText?: string
  addLabel?: string
  removeLabel?: string
  confirm429Message?: string
  confirm529Message?: string
  invalidErrorMessage?: string
  duplicateErrorMessage?: string
}>(), {
  hint: '',
  warning: '',
  inputPlaceholder: 'HTTP status code (100–599)',
  noneSelectedText: 'No custom codes selected',
  addLabel: 'Add error code',
  removeLabel: 'Remove error code',
  confirm429Message: '',
  confirm529Message: '',
  invalidErrorMessage: 'Invalid HTTP status code',
  duplicateErrorMessage: 'This error code is already selected'
})
const emit = defineEmits<{
  (event: 'update:enabled', value: boolean): void
  (event: 'update:modelValue', value: number[]): void
  (event: 'error', message: string): void
  (event: 'info', message: string): void
}>()
const inputValue = ref<number | null>(null)
const selectedCodes = computed(() => Array.from(new Set(props.modelValue || [])))
const sortedCodes = computed(() => [...selectedCodes.value].sort((a, b) => a - b))

function setEnabled(value: boolean) {
  emit('update:enabled', value)
}

function confirmSpecialCode(code: number): boolean {
  if (code === 429 && props.confirm429Message) return window.confirm(props.confirm429Message)
  if (code === 529 && props.confirm529Message) return window.confirm(props.confirm529Message)
  return true
}

function toggleCode(code: number) {
  const next = [...selectedCodes.value]
  const index = next.indexOf(code)
  if (index >= 0) next.splice(index, 1)
  else {
    if (!confirmSpecialCode(code)) return
    next.push(code)
  }
  emit('update:modelValue', next.sort((a, b) => a - b))
}

function addCode() {
  const code = inputValue.value
  if (code == null || !Number.isInteger(code) || code < 100 || code > 599) {
    emit('error', props.invalidErrorMessage)
    return
  }
  if (selectedCodes.value.includes(code)) {
    emit('info', props.duplicateErrorMessage)
    return
  }
  if (!confirmSpecialCode(code)) return
  emit('update:modelValue', [...selectedCodes.value, code].sort((a, b) => a - b))
  inputValue.value = null
}

function removeCode(code: number) {
  emit('update:modelValue', selectedCodes.value.filter(item => item !== code))
}
</script>
