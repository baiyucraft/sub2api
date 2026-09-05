<template>
  <BaseDialog :show="show" :title="t('admin.dashboard.extraCostsTitle')" width="wide" @close="close">
    <div class="space-y-5">
      <div class="rounded-xl border border-orange-200 bg-orange-50 p-4 text-sm text-orange-900 dark:border-orange-500/30 dark:bg-orange-500/10 dark:text-orange-100">
        <div class="flex gap-3">
          <Icon name="exclamationTriangle" size="md" class="mt-0.5 flex-shrink-0" />
          <div>
            <p class="font-semibold">{{ t('admin.dashboard.extraCostsAuditTitle') }}</p>
            <p class="mt-1 leading-5">{{ t('admin.dashboard.extraCostsAuditHint') }}</p>
          </div>
        </div>
      </div>

      <form class="grid gap-4 rounded-xl border border-gray-200 p-4 dark:border-dark-700 md:grid-cols-2" @submit.prevent="submit">
        <div>
          <label for="extra-cost-date" class="input-label">{{ t('admin.dashboard.extraCostDate') }}</label>
          <input id="extra-cost-date" v-model="form.cost_date" type="date" required class="input w-full" :disabled="submitting" />
        </div>
        <div>
          <label for="extra-cost-amount" class="input-label">{{ t('admin.dashboard.extraCostAmount') }}</label>
          <div class="relative">
            <span class="pointer-events-none absolute inset-y-0 left-3 flex items-center text-gray-400">$</span>
            <input id="extra-cost-amount" v-model="form.amount" type="number" min="0" step="any" required class="input w-full pl-8" :disabled="submitting" />
          </div>
        </div>
        <div>
          <label class="input-label" for="extra-cost-type">{{ t('admin.dashboard.extraCostType') }}</label>
          <Select id="extra-cost-type" v-model="form.category" :options="typeOptions" :disabled="submitting" :aria-label="t('admin.dashboard.extraCostType')" />
        </div>
        <div>
          <label for="extra-cost-notes" class="input-label">{{ t('admin.dashboard.extraCostNotes') }}</label>
          <input id="extra-cost-notes" v-model="form.notes" maxlength="500" class="input w-full" :placeholder="t('admin.dashboard.extraCostNotesPlaceholder')" :disabled="submitting" />
        </div>
        <div class="flex justify-end md:col-span-2">
          <button type="submit" class="btn btn-primary" :disabled="submitting || !canSubmit">
            {{ submitting ? t('common.saving') : t('admin.dashboard.addExtraCost') }}
          </button>
        </div>
      </form>

      <div class="grid gap-3 sm:grid-cols-2">
        <div class="rounded-xl bg-gray-50 p-4 dark:bg-dark-800/70">
          <p class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.dashboard.extraCostsDayTotal') }}</p>
          <p class="mt-1 text-xl font-bold text-orange-600 dark:text-orange-400">${{ formatCost(dayTotal) }}</p>
        </div>
        <div class="rounded-xl bg-gray-50 p-4 dark:bg-dark-800/70">
          <p class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.dashboard.extraCostsRangeTotal') }}</p>
          <p class="mt-1 text-xl font-bold text-orange-600 dark:text-orange-400">${{ formatCost(rangeTotal) }}</p>
        </div>
      </div>

      <div class="rounded-xl border border-gray-200 dark:border-dark-700">
        <div class="flex flex-wrap items-center justify-between gap-3 border-b border-gray-200 px-4 py-3 dark:border-dark-700">
          <h3 class="text-sm font-semibold text-gray-900 dark:text-white">{{ t('admin.dashboard.extraCostsLedger') }}</h3>
          <div class="flex flex-wrap items-center gap-2">
            <input v-model="filters.start_date" type="date" class="input h-9 w-36 text-sm" :aria-label="t('dates.startDate')" @change="reload" />
            <span class="text-xs text-gray-400">—</span>
            <input v-model="filters.end_date" type="date" class="input h-9 w-36 text-sm" :aria-label="t('dates.endDate')" @change="reload" />
            <div class="w-36">
            <Select v-model="filters.category" :options="filterTypeOptions" :aria-label="t('admin.dashboard.extraCostType')" @change="reload" />
            </div>
          </div>
        </div>

        <div v-if="loading" class="flex items-center justify-center py-10"><LoadingSpinner size="md" /></div>
        <div v-else-if="entries.length === 0" class="px-4 py-10 text-center text-sm text-gray-500 dark:text-dark-400">{{ t('admin.dashboard.extraCostsEmpty') }}</div>
        <div v-else class="overflow-x-auto">
          <table class="min-w-full divide-y divide-gray-200 text-sm dark:divide-dark-700">
            <thead class="bg-gray-50 text-left text-xs uppercase tracking-wide text-gray-500 dark:bg-dark-800/60 dark:text-dark-400">
              <tr>
                <th class="px-4 py-3 font-medium">{{ t('admin.dashboard.extraCostDate') }}</th>
                <th class="px-4 py-3 font-medium">{{ t('admin.dashboard.extraCostType') }}</th>
                <th class="px-4 py-3 text-right font-medium">{{ t('admin.dashboard.extraCostAmount') }}</th>
                <th class="px-4 py-3 font-medium">{{ t('admin.dashboard.extraCostNotes') }}</th>
                <th class="px-4 py-3 text-right font-medium">{{ t('admin.dashboard.extraCostActions') }}</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-gray-100 dark:divide-dark-800">
              <tr v-for="entry in entries" :key="entry.id" class="text-gray-700 dark:text-dark-200">
                <td class="whitespace-nowrap px-4 py-3">{{ entry.cost_date }}</td>
                <td class="whitespace-nowrap px-4 py-3">{{ typeLabel(entry.category) }}</td>
                <td class="whitespace-nowrap px-4 py-3 text-right font-mono">${{ formatCost(entry.amount) }}</td>
                <td class="max-w-xs truncate px-4 py-3" :title="entry.notes">{{ entry.notes || '—' }}</td>
                <td class="whitespace-nowrap px-4 py-3 text-right">
                  <span v-if="entry.reversal_of" class="text-xs text-gray-400">{{ t('admin.dashboard.extraCostReversed') }}</span>
                  <button v-else type="button" class="btn btn-ghost btn-sm text-red-600 hover:bg-red-50 dark:text-red-400 dark:hover:bg-red-500/10" :disabled="reversingId === entry.id" @click="reverseEntry(entry)">
                    {{ reversingId === entry.id ? t('common.saving') : t('admin.dashboard.extraCostReverse') }}
                  </button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <Pagination v-if="total > 0" :page="pagination.page" :total="total" :page-size="pagination.page_size" @update:page="changePage" />
      </div>
    </div>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import type { ExtraCostEntry, ExtraCostType } from '@/api/admin/extraCosts'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Icon from '@/components/icons/Icon.vue'
import LoadingSpinner from '@/components/common/LoadingSpinner.vue'
import Pagination from '@/components/common/Pagination.vue'
import Select from '@/components/common/Select.vue'
import { useAppStore } from '@/stores/app'

const props = defineProps<{ show: boolean; startDate?: string; endDate?: string }>()
const emit = defineEmits<{ (event: 'close'): void; (event: 'changed'): void }>()
const { t } = useI18n()
const appStore = useAppStore()

const today = () => new Date().toISOString().slice(0, 10)
const form = reactive<{ cost_date: string; amount: string | number; category: ExtraCostType; notes: string }>({ cost_date: today(), amount: '', category: 'account', notes: '' })
const filters = reactive<{ start_date: string; end_date: string; category?: ExtraCostType }>({ start_date: props.startDate || today(), end_date: props.endDate || today() })
const entries = ref<ExtraCostEntry[]>([])
const loading = ref(false)
const submitting = ref(false)
const reversingId = ref<number | null>(null)
const total = ref(0)
const dayTotal = ref(0)
const rangeTotal = ref(0)
const pagination = reactive({ page: 1, page_size: 10 })

const typeOptions = computed(() => [
  { value: 'account', label: t('admin.dashboard.extraCostTypes.account') },
  { value: 'proxy', label: t('admin.dashboard.extraCostTypes.proxy') },
  { value: 'server', label: t('admin.dashboard.extraCostTypes.server') },
  { value: 'other', label: t('admin.dashboard.extraCostTypes.other') },
  { value: 'adjustment', label: t('admin.dashboard.extraCostTypes.adjustment') }
])
const filterTypeOptions = computed(() => [{ value: undefined, label: t('admin.dashboard.extraCostTypes.all') }, ...typeOptions.value])
const canSubmit = computed(() => form.cost_date.length > 0 && String(form.amount).trim() !== '' && Number.isFinite(Number(form.amount)) && Number(form.amount) >= 0)

const close = () => emit('close')
const formatCost = (value: number | null | undefined) => (Number.isFinite(Number(value)) ? Number(value).toFixed(4) : '0.0000')
const typeLabel = (type: ExtraCostType) => typeOptions.value.find((item) => item.value === type)?.label || type

async function load(): Promise<void> {
  if (!props.show) return
  loading.value = true
  try {
    const response = await adminAPI.extraCosts.list({ ...filters, page: pagination.page, page_size: pagination.page_size })
    entries.value = response.items || []
    total.value = response.total || 0
    dayTotal.value = response.daily_total ?? entries.value.filter((entry) => entry.cost_date === today()).reduce((sum, entry) => sum + entry.amount, 0)
    rangeTotal.value = response.range_total ?? entries.value.reduce((sum, entry) => sum + entry.amount, 0)
  } catch (error) {
    console.error('Failed to load extra costs:', error)
    appStore.showError(t('admin.dashboard.extraCostsLoadFailed'))
  } finally {
    loading.value = false
  }
}

async function submit(): Promise<void> {
  if (!canSubmit.value || submitting.value) return
  submitting.value = true
  try {
    await adminAPI.extraCosts.create({ cost_date: form.cost_date, amount: Number(form.amount), category: form.category, notes: form.notes.trim() || undefined, idempotency_key: `extra-cost-${Date.now()}-${Math.random().toString(36).slice(2)}` })
    appStore.showSuccess(t('admin.dashboard.extraCostAdded'))
    form.amount = ''
    form.notes = ''
    await load()
    emit('changed')
  } catch (error) {
    console.error('Failed to create extra cost:', error)
    appStore.showError(t('admin.dashboard.extraCostSaveFailed'))
  } finally {
    submitting.value = false
  }
}

async function reverseEntry(entry: ExtraCostEntry): Promise<void> {
  const reason = window.prompt(t('admin.dashboard.extraCostReversePrompt'))
  if (reason === null || !reason.trim() || reversingId.value !== null) return
  reversingId.value = entry.id
  try {
    await adminAPI.extraCosts.reverse(entry.id, { reason: reason.trim(), idempotency_key: `extra-cost-reverse-${entry.id}-${Date.now()}` })
    appStore.showSuccess(t('admin.dashboard.extraCostReversedSuccess'))
    await load()
    emit('changed')
  } catch (error) {
    console.error('Failed to reverse extra cost:', error)
    appStore.showError(t('admin.dashboard.extraCostReverseFailed'))
  } finally {
    reversingId.value = null
  }
}

async function reload(): Promise<void> {
  pagination.page = 1
  await load()
}

async function changePage(page: number): Promise<void> {
  pagination.page = page
  await load()
}

watch(() => props.show, (show) => {
  if (show) {
    filters.start_date = props.startDate || today()
    filters.end_date = props.endDate || today()
    form.cost_date = today()
    void load()
  }
})
</script>
