<template>
  <div class="mt-4 rounded-lg border border-dashed border-gray-200 bg-gray-50/80 p-3 dark:border-dark-600 dark:bg-dark-800/60" data-test="upstream-model-custom-rules">
    <p class="text-xs font-medium text-gray-700 dark:text-gray-200">
      {{ t('admin.accounts.upstreamModelCustomRules.title') }}
    </p>
    <p class="mt-1 text-xs leading-5 text-gray-500 dark:text-gray-400">
      {{ t('admin.accounts.upstreamModelCustomRules.description') }}
    </p>

    <div v-if="rules.length" class="mt-3 space-y-2">
      <div
        v-for="rule in rules"
        :key="rule.source"
        class="flex items-center gap-2 rounded-md bg-white px-2.5 py-2 text-xs shadow-sm dark:bg-dark-700"
        data-test="upstream-model-custom-rule"
      >
        <span :class="actionClass(rule.action)" class="shrink-0 rounded px-1.5 py-0.5 font-medium">
          {{ t(`admin.accounts.upstreamModelCustomRules.actions.${rule.action}`) }}
        </span>
        <code class="min-w-0 flex-1 break-all text-gray-700 dark:text-gray-200">
          {{ rule.source }}<template v-if="rule.action === 'map'"> → {{ rule.target }}</template>
        </code>
        <span
          v-if="!upstreamModelRuleAvailable(rule, autoMapping)"
          class="shrink-0 text-amber-600 dark:text-amber-400"
          data-test="upstream-model-custom-rule-waiting"
        >
          {{ t('admin.accounts.upstreamModelCustomRules.waiting') }}
        </span>
        <button
          type="button"
          class="shrink-0 text-red-500 transition-colors hover:text-red-700 dark:hover:text-red-300"
          :aria-label="t('admin.accounts.upstreamModelCustomRules.remove', { model: rule.source })"
          data-test="remove-upstream-model-custom-rule"
          @click="emit('remove', rule.source)"
        >
          <Icon name="trash" size="sm" />
        </button>
      </div>
    </div>
    <p v-else class="mt-3 text-xs text-gray-500 dark:text-gray-400" data-test="upstream-model-custom-rules-empty">
      {{ t('admin.accounts.upstreamModelCustomRules.empty') }}
    </p>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import type { UpstreamModelCustomRule, UpstreamModelCustomRuleAction } from '@/types'
import { upstreamModelRuleAvailable } from './upstreamModelCustomRules'

defineProps<{
  rules: UpstreamModelCustomRule[]
  autoMapping?: Record<string, string>
}>()

const emit = defineEmits<{ (event: 'remove', source: string): void }>()
const { t } = useI18n()

function actionClass(action: UpstreamModelCustomRuleAction) {
  if (action === 'deny') return 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-300'
  if (action === 'map') return 'bg-purple-100 text-purple-700 dark:bg-purple-900/30 dark:text-purple-300'
  return 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300'
}
</script>
