<template>
  <span
    data-test="platform-badge"
    :data-platform="normalizedPlatform"
    :class="[
      'inline-flex items-center gap-1 rounded-full border px-2.5 py-1 text-xs font-medium',
      platformBadgeClass(normalizedPlatform)
    ]"
  >
    <PlatformIcon :platform="iconPlatform" size="xs" />
    <span>{{ label || platformLabel(normalizedPlatform) }}</span>
  </span>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import type { GroupPlatform } from '@/types'
import { platformBadgeClass, platformLabel } from '@/utils/platformColors'
import PlatformIcon from './PlatformIcon.vue'

const props = defineProps<{
  platform?: GroupPlatform | string | null
  label?: string
}>()

const normalizedPlatform = computed(() => props.platform?.trim().toLowerCase() || '')
const iconPlatform = computed<GroupPlatform | undefined>(() =>
  normalizedPlatform.value ? normalizedPlatform.value as GroupPlatform : undefined
)
</script>
