<template>
  <div class="space-y-4">
    <!-- Quick Amount Buttons -->
    <div>
      <label class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300">
        {{ t('payment.quickAmounts') }}
      </label>
      <div class="grid grid-cols-3 gap-3 pt-1">
        <button
          v-for="amt in filteredAmounts"
          :key="amt"
          type="button"
          :class="[
            'relative rounded-lg border-2 px-3 py-3 text-center font-medium transition-colors',
            modelValue === amt
              ? 'border-primary-500 bg-primary-50 text-primary-700 dark:border-primary-400 dark:bg-primary-900/40 dark:text-primary-300'
              : 'border-gray-200 bg-white text-gray-700 hover:border-gray-300 dark:border-dark-600 dark:bg-dark-800 dark:text-gray-200 dark:hover:border-dark-500',
          ]"
          :data-testid="`quick-amount-${amt}`"
          @click="selectAmount(amt)"
        >
          <!-- 赠送角标：仅命中档位的金额显示 -->
          <span
            v-if="quoteFor(amt).bonus > 0"
            class="absolute -right-2 -top-2.5 rounded-full bg-amber-500 px-1.5 py-0.5 text-[10px] font-semibold leading-none text-white shadow-sm dark:bg-amber-400 dark:text-amber-950"
            data-testid="quick-amount-bonus-badge"
          >+{{ formatRechargeBonusNumber(quoteFor(amt).percent) }}%</span>
          <span class="block">{{ amt }}</span>
          <!-- 配置了赠送阶梯时，所有按钮都显示到账金额，保持高度一致 -->
          <span
            v-if="showCredited"
            :class="[
              'mt-0.5 block text-[11px] font-normal leading-tight',
              quoteFor(amt).bonus > 0 ? 'text-amber-600 dark:text-amber-300' : 'text-gray-400 dark:text-gray-500',
            ]"
            data-testid="quick-amount-credited"
          >{{ t('payment.rechargeBonus.creditedShort', { amount: '$' + quoteFor(amt).credited.toFixed(2) }) }}</span>
        </button>
      </div>
    </div>

    <!-- Custom Amount Input -->
    <div>
      <label class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300">
        {{ t('payment.customAmount') }}
      </label>
      <div class="relative">
        <span class="absolute left-3 top-1/2 -translate-y-1/2 text-gray-400 dark:text-dark-500">
          $
        </span>
        <input
          type="text"
          inputmode="decimal"
          :value="customText"
          :placeholder="placeholderText"
          class="input w-full py-3 pl-8 pr-4"
          @input="handleInput"
        />
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import type { RechargeBonusTier } from '@/types/payment'
import { formatRechargeBonusNumber, quoteRechargeBonus } from '@/utils/rechargeBonus'

const props = withDefaults(defineProps<{
  amounts?: number[]
  modelValue: number | null
  min?: number
  max?: number
  /** 充值赠送阶梯（按 min_amount 升序）；为空时不显示角标与到账行 */
  bonusTiers?: RechargeBonusTier[]
  /** 充值倍率（1 支付币种 = multiplier USD），用于计算到账金额 */
  multiplier?: number
}>(), {
  amounts: () => [10, 20, 50, 100, 200, 500, 1000, 2000, 5000],
  min: 0,
  max: 0,
  bonusTiers: () => [],
  multiplier: 1,
})

const emit = defineEmits<{
  'update:modelValue': [value: number | null]
}>()

const { t } = useI18n()

const customText = ref('')

// 0 = no limit
const filteredAmounts = computed(() =>
  props.amounts.filter((a) => (props.min <= 0 || a >= props.min) && (props.max <= 0 || a <= props.max))
)

const showCredited = computed(() => props.bonusTiers.length > 0)

function quoteFor(amt: number) {
  return quoteRechargeBonus(props.bonusTiers, amt, props.multiplier)
}

const placeholderText = computed(() => {
  if (props.min > 0 && props.max > 0) return `${props.min} - ${props.max}`
  if (props.min > 0) return `≥ ${props.min}`
  if (props.max > 0) return `≤ ${props.max}`
  return t('payment.enterAmount')
})

const AMOUNT_PATTERN = /^\d*(\.\d{0,2})?$/

function selectAmount(amt: number) {
  customText.value = String(amt)
  emit('update:modelValue', amt)
}

function handleInput(e: Event) {
  const input = e.target as HTMLInputElement
  const val = input.value
  if (!AMOUNT_PATTERN.test(val)) {
    input.value = customText.value
    return
  }
  customText.value = val
  if (val === '') {
    emit('update:modelValue', null)
    return
  }
  const num = parseFloat(val)
  if (!isNaN(num) && num > 0) {
    emit('update:modelValue', num)
  } else {
    emit('update:modelValue', null)
  }
}

watch(() => props.modelValue, (v) => {
  if (v !== null && String(v) !== customText.value) {
    customText.value = String(v)
  }
}, { immediate: true })
</script>
