<template>
  <span>
    {{ enabled
      ? t('admin.accounts.autoRefreshCountdown', { seconds: countdown })
      : t('admin.accounts.autoRefresh') }}
  </span>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useIntervalFn } from '@vueuse/core'
import { useI18n } from 'vue-i18n'

const props = defineProps<{
  enabled: boolean
  deadline: number
}>()

const { t } = useI18n()
const now = ref(Date.now())
const countdown = computed(() => Math.max(0, Math.ceil((props.deadline - now.value) / 1000)))
const { pause, resume } = useIntervalFn(() => {
  if (typeof document !== 'undefined' && document.hidden) return
  now.value = Date.now()
}, 1000, { immediate: false })

watch(
  () => [props.enabled, props.deadline] as const,
  ([enabled]) => {
    now.value = Date.now()
    if (enabled) resume()
    else pause()
  },
  { immediate: true }
)
</script>
