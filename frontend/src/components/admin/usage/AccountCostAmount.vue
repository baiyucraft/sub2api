<template>
  <span
    class="relative inline-flex cursor-help rounded font-medium text-red-600 outline-none focus-visible:ring-2 focus-visible:ring-primary-500 dark:text-red-400"
    tabindex="0"
    :aria-label="`${t('admin.dashboard.totalAccountCost')} $${totalCost}`"
    :aria-describedby="open ? tooltipId : undefined"
    @mouseenter="open = true"
    @mouseleave="open = false"
    @focus="open = true"
    @blur="open = false"
    @click="open = true"
    @keydown.esc.stop="open = false"
  >
    <span data-testid="account-cost-total">${{ totalCost }}</span>
    <span
      v-if="open"
      :id="tooltipId"
      role="tooltip"
      class="pointer-events-none absolute left-0 top-full z-30 mt-2 flex w-max max-w-[calc(100vw-2rem)] flex-wrap items-center gap-1 rounded-lg border border-gray-200 bg-white px-3 py-2 text-xs font-normal shadow-lg dark:border-dark-600 dark:bg-dark-800"
    >
      <span class="text-orange-500 dark:text-orange-400"><span class="sr-only">{{ t('admin.dashboard.usageAccountCost') }} </span>${{ usageCost }}</span>
      <span class="text-gray-400">+</span>
      <span class="text-amber-600 dark:text-amber-400"><span class="sr-only">{{ t('admin.dashboard.extraCost') }} </span>${{ extraCost }}</span>
    </span>
  </span>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'

defineProps<{ totalCost: string; usageCost: string; extraCost: string; tooltipId: string }>()
const { t } = useI18n()
const open = ref(false)
</script>
