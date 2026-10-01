<template>
  <div data-testid="recharge-bonus-tier-editor">
    <div class="flex flex-col gap-2 sm:flex-row sm:items-start sm:justify-between">
      <div>
        <label class="input-label">
          {{ t('admin.settings.payment.rechargeBonus.label') }}
        </label>
        <p class="mt-0.5 text-xs text-gray-400">
          {{ t('admin.settings.payment.rechargeBonus.hint') }}
        </p>
      </div>
      <button
        type="button"
        class="btn btn-secondary btn-sm shrink-0"
        :disabled="tiers.length >= MAX_RECHARGE_BONUS_TIERS"
        data-testid="recharge-bonus-tier-add"
        @click="addTier"
      >
        <Icon name="plus" size="sm" class="mr-1" />
        {{ t('admin.settings.payment.rechargeBonus.addTier') }}
      </button>
    </div>

    <div
      v-if="tiers.length === 0"
      class="mt-3 rounded-lg border border-dashed border-gray-300 p-4 text-sm text-gray-500 dark:border-dark-600 dark:text-gray-400"
    >
      {{ t('admin.settings.payment.rechargeBonus.empty') }}
    </div>

    <div v-else class="mt-3 space-y-2">
      <div
        v-for="(tier, index) in tiers"
        :key="index"
        class="rounded-lg border border-gray-200 px-4 py-3 dark:border-dark-600"
        data-testid="recharge-bonus-tier-row"
      >
        <div class="flex flex-wrap items-center gap-2">
          <span class="text-sm text-gray-700 dark:text-gray-300">
            {{ t('admin.settings.payment.rechargeBonus.minAmountLabel') }}
          </span>
          <input
            :value="tier.min_amount ?? ''"
            type="number"
            step="0.01"
            min="0"
            class="input w-32"
            placeholder="100"
            data-testid="recharge-bonus-tier-min-input"
            @input="onMinAmountInput(index, $event)"
          />
          <span class="text-sm text-gray-700 dark:text-gray-300">
            {{ t('admin.settings.payment.rechargeBonus.percentLabel') }}
          </span>
          <div class="relative">
            <input
              :value="tier.bonus_percent ?? ''"
              type="number"
              step="0.01"
              min="0"
              :max="MAX_RECHARGE_BONUS_PERCENT"
              class="input w-28 pr-8"
              placeholder="20"
              data-testid="recharge-bonus-tier-percent-input"
              @input="onPercentInput(index, $event)"
            />
            <span class="pointer-events-none absolute inset-y-0 right-0 flex items-center pr-3 text-gray-400">%</span>
          </div>
          <button
            type="button"
            class="ml-auto rounded p-1 text-gray-400 hover:bg-red-50 hover:text-red-600 dark:hover:bg-dark-700 dark:hover:text-red-400"
            :title="t('admin.settings.payment.rechargeBonus.removeTier')"
            data-testid="recharge-bonus-tier-remove"
            @click="removeTier(index)"
          >
            <Icon name="x" size="sm" />
          </button>
        </div>
        <p
          v-if="rowError(index)"
          class="mt-1 text-xs text-red-600 dark:text-red-400"
          data-testid="recharge-bonus-tier-error"
        >
          {{ t(`admin.settings.payment.rechargeBonus.${rowError(index)}`) }}
        </p>
        <p
          v-else-if="rowIncomplete(index)"
          class="mt-1 text-xs text-gray-400 dark:text-gray-500"
          data-testid="recharge-bonus-tier-incomplete"
        >
          {{ t('admin.settings.payment.rechargeBonus.incompleteRow') }}
        </p>
      </div>
    </div>

    <div
      v-if="intervals.length > 0"
      class="mt-3 rounded-lg bg-gray-50 px-4 py-3 text-xs text-gray-600 dark:bg-dark-700/60 dark:text-gray-300"
      data-testid="recharge-bonus-tier-preview"
    >
      <p class="font-medium text-gray-700 dark:text-gray-200">
        {{ t('admin.settings.payment.rechargeBonus.previewTitle') }}
      </p>
      <ul class="mt-1 space-y-0.5">
        <li v-for="(interval, index) in intervals" :key="index">{{ intervalLabel(interval) }}</li>
      </ul>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

import Icon from '@/components/icons/Icon.vue'
import {
  MAX_RECHARGE_BONUS_PERCENT,
  MAX_RECHARGE_BONUS_TIERS,
  describeRechargeBonusIntervals,
  formatRechargeBonusNumber,
  isDuplicateRechargeBonusMinAmount,
  isRechargeBonusMinAmountValid,
  isRechargeBonusPercentValid,
  sanitizeRechargeBonusTiersForSubmit,
  type RechargeBonusInterval,
  type RechargeBonusTierDraft,
} from '@/utils/rechargeBonus'

const { t } = useI18n()

const props = defineProps<{
  modelValue?: RechargeBonusTierDraft[] | null
}>()

const emit = defineEmits<{
  (e: 'update:modelValue', value: RechargeBonusTierDraft[]): void
}>()

const tiers = computed(() => props.modelValue ?? [])

const intervals = computed(() => describeRechargeBonusIntervals(sanitizeRechargeBonusTiersForSubmit(tiers.value)))

function cloneTiers(): RechargeBonusTierDraft[] {
  return tiers.value.map((tier) => ({ min_amount: tier.min_amount, bonus_percent: tier.bonus_percent }))
}

function addTier() {
  if (tiers.value.length >= MAX_RECHARGE_BONUS_TIERS) return
  emit('update:modelValue', [...cloneTiers(), { min_amount: null, bonus_percent: null }])
}

function removeTier(index: number) {
  const next = cloneTiers()
  next.splice(index, 1)
  emit('update:modelValue', next)
}

function parseNumberInput(event: Event): number | null {
  const raw = (event.target as HTMLInputElement).value
  if (raw === '') return null
  const parsed = Number(raw)
  return Number.isFinite(parsed) ? parsed : null
}

function onMinAmountInput(index: number, event: Event) {
  const next = cloneTiers()
  next[index]!.min_amount = parseNumberInput(event)
  emit('update:modelValue', next)
}

function onPercentInput(index: number, event: Event) {
  const next = cloneTiers()
  next[index]!.bonus_percent = parseNumberInput(event)
  emit('update:modelValue', next)
}

type RowError = '' | 'invalidMinAmount' | 'invalidPercent' | 'duplicateMinAmount'

function rowError(index: number): RowError {
  const tier = tiers.value[index]
  if (!tier) return ''
  if (tier.min_amount !== null && !isRechargeBonusMinAmountValid(tier.min_amount)) return 'invalidMinAmount'
  if (tier.bonus_percent !== null && !isRechargeBonusPercentValid(tier.bonus_percent)) return 'invalidPercent'
  if (isDuplicateRechargeBonusMinAmount(tiers.value, index)) return 'duplicateMinAmount'
  return ''
}

function rowIncomplete(index: number): boolean {
  const tier = tiers.value[index]
  return !!tier && (tier.min_amount === null || tier.bonus_percent === null)
}

function intervalLabel(interval: RechargeBonusInterval): string {
  const from = formatRechargeBonusNumber(interval.from)
  const percent = formatRechargeBonusNumber(interval.percent)
  if (interval.to === null) {
    return interval.percent > 0
      ? t('admin.settings.payment.rechargeBonus.previewOpen', { from, percent })
      : t('admin.settings.payment.rechargeBonus.previewOpenNone', { from })
  }
  const to = formatRechargeBonusNumber(interval.to)
  return interval.percent > 0
    ? t('admin.settings.payment.rechargeBonus.previewRange', { from, to, percent })
    : t('admin.settings.payment.rechargeBonus.previewRangeNone', { from, to })
}
</script>
