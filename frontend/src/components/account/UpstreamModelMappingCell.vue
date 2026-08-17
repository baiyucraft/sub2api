<template>
  <div class="flex max-w-[260px] flex-col items-center gap-1 text-[11px]">
    <div v-if="entries.length" class="flex w-full flex-col gap-1">
      <div
        v-for="entry in entries.slice(0, 4)"
        :key="entry"
        class="truncate text-center font-mono text-gray-600 dark:text-dark-300"
        :title="entry"
      >
        {{ entry }}
      </div>
      <div v-if="entries.length > 4" class="text-center text-gray-400 dark:text-dark-500">
        +{{ entries.length - 4 }}
      </div>
    </div>
    <span v-else class="text-sm text-gray-400 dark:text-dark-500">-</span>
    <UpstreamModelSyncStatus v-if="account.upstream_model_sync" :sync="account.upstream_model_sync" />
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import type { Account } from '@/types'
import UpstreamModelSyncStatus from '@/components/account/UpstreamModelSyncStatus.vue'

const props = defineProps<{ account: Account }>()

const entries = computed(() => {
  const mapping = props.account.credentials?.model_mapping
  if (!mapping || typeof mapping !== 'object' || Array.isArray(mapping)) return []
  return Object.entries(mapping as Record<string, unknown>)
    .map(([source, target]) => `${source} → ${String(target)}`)
    .sort((a, b) => a.localeCompare(b))
})
</script>
