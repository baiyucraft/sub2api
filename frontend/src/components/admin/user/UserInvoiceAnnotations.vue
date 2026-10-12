<template>
  <div class="flex flex-wrap items-center gap-2">
    <button
      type="button"
      data-testid="invoice-summary"
      class="inline-flex items-center gap-1.5 rounded-lg bg-purple-100 px-2.5 py-1.5 text-xs font-medium text-purple-700 transition-colors hover:bg-purple-200 dark:bg-purple-900/30 dark:text-purple-300 dark:hover:bg-purple-900/50"
      :disabled="busy"
      @click="openList"
    >
      {{ t('admin.users.invoices.totalInvoiced') }}
      <span v-if="loading">{{ t('common.loading') }}</span>
      <span v-else-if="loadError">{{ t('admin.users.invoices.loadFailedShort') }}</span>
      <span v-else-if="totalInvoiced !== null">${{ money(totalInvoiced) }}</span>
      <span v-else>—</span>
    </button>
    <button v-if="!readOnly" type="button" data-testid="invoice-register" class="text-xs font-medium text-purple-600 hover:text-purple-800 disabled:opacity-50 dark:text-purple-400 dark:hover:text-purple-300" :disabled="busy" @click="openEditor()">
      {{ t('admin.users.invoices.register') }}
    </button>
  <BaseDialog
    :show="dialogOpen && show"
    :title="dialogTitle"
    :width="mode === 'list' ? 'wide' : 'normal'"
    :z-index="50"
    :close-on-click-outside="!busy"
    :close-on-escape="!busy"
    :show-close-button="!busy"
    @close="closeDialog"
  >
    <div class="space-y-4">
      <p class="truncate text-sm font-medium text-gray-900 dark:text-white">{{ user?.email }}</p>
      <p class="rounded-lg bg-purple-50 px-3 py-2 text-xs leading-relaxed text-purple-700 dark:bg-purple-900/20 dark:text-purple-300">{{ t('admin.users.invoices.annotationHint') }}</p>

      <template v-if="mode === 'list'">
        <div class="flex flex-wrap items-center justify-between gap-2">
          <p class="text-sm text-gray-600 dark:text-dark-300">
            {{ t('admin.users.invoices.totalInvoiced') }}:
            <strong class="text-purple-600 dark:text-purple-400">{{ loading ? t('common.loading') : loadError || totalInvoiced === null ? '—' : `$${money(totalInvoiced)}` }}</strong>
          </p>
          <button v-if="!readOnly" type="button" class="btn btn-secondary text-sm" @click="openEditor()">{{ t('admin.users.invoices.register') }}</button>
        </div>
        <div v-if="loadError" role="alert" class="rounded-lg bg-red-50 p-3 text-sm text-red-600 dark:bg-red-900/20 dark:text-red-400">
          {{ loadError }}
          <button type="button" class="ml-2 underline" @click="loadInvoices(currentPage)">{{ t('common.retry') }}</button>
        </div>
        <p v-else-if="loading" class="py-6 text-center text-sm text-gray-500">{{ t('common.loading') }}</p>
        <p v-else-if="!items.length" class="py-6 text-center text-sm text-gray-500 dark:text-dark-400">{{ t('admin.users.invoices.empty') }}</p>
        <div v-else class="max-h-[24rem] space-y-3 overflow-y-auto">
          <article v-for="item in items" :key="item.id" class="rounded-xl border border-gray-200 p-4 dark:border-dark-600" :data-testid="`invoice-${item.id}`">
            <div class="flex flex-wrap items-start justify-between gap-3">
              <div class="min-w-0 flex-1">
                <div class="flex flex-wrap items-center gap-2">
                  <span class="font-semibold" :class="item.status === 'voided' ? 'text-gray-400 line-through' : 'text-purple-600 dark:text-purple-400'">${{ money(item.amount) }}</span>
                  <span class="rounded px-1.5 py-0.5 text-xs" :class="item.status === 'voided' ? 'bg-gray-100 text-gray-500 dark:bg-dark-700 dark:text-dark-400' : 'bg-purple-50 text-purple-600 dark:bg-purple-900/20 dark:text-purple-400'">{{ t(`admin.users.invoices.status.${item.status}`) }}</span>
                  <span class="text-xs text-gray-500 dark:text-dark-400">{{ item.invoice_date }}</span>
                </div>
                <p v-if="item.invoice_number" class="mt-2 break-all text-sm text-gray-700 dark:text-dark-300">{{ t('admin.users.invoices.number') }}: {{ item.invoice_number }}</p>
                <p v-if="item.notes" class="mt-1 whitespace-pre-wrap break-words text-xs text-gray-500 dark:text-dark-400">{{ item.notes }}</p>
                <p class="mt-2 text-xs text-gray-400 dark:text-dark-500">{{ t('admin.users.invoices.recordedBy', { id: item.created_by, date: formatDateTime(item.created_at) }) }}</p>
                <p v-if="item.updated_at !== item.created_at && item.status !== 'voided'" class="mt-1 text-xs text-gray-400 dark:text-dark-500">{{ t('admin.users.invoices.updatedBy', { id: item.updated_by, date: formatDateTime(item.updated_at) }) }}</p>
                <template v-if="item.status === 'voided'">
                  <p class="mt-2 break-words text-xs text-amber-600 dark:text-amber-400">{{ t('admin.users.invoices.voidReason') }}: {{ item.void_reason }}</p>
                  <p class="mt-1 text-xs text-gray-400 dark:text-dark-500">{{ t('admin.users.invoices.voidedBy', { id: item.voided_by, date: formatDateTime(item.voided_at) }) }}</p>
                </template>
              </div>
              <div v-if="!readOnly && item.status === 'active'" class="flex gap-3">
                <button type="button" class="text-xs text-purple-600 hover:underline dark:text-purple-400" @click="openEditor(item)">{{ t('admin.users.invoices.edit') }}</button>
                <button type="button" class="text-xs text-red-600 hover:underline dark:text-red-400" @click="openVoid(item)">{{ t('admin.users.invoices.void') }}</button>
              </div>
            </div>
          </article>
        </div>
        <div v-if="totalPages > 1 && !loadError" class="flex items-center justify-center gap-2">
          <button type="button" :disabled="loading || currentPage <= 1" class="btn btn-secondary px-3 py-1 text-sm" @click="loadInvoices(currentPage - 1)">{{ t('pagination.previous') }}</button>
          <span class="text-sm text-gray-500 dark:text-dark-400">{{ currentPage }} / {{ totalPages }}</span>
          <button type="button" :disabled="loading || currentPage >= totalPages" class="btn btn-secondary px-3 py-1 text-sm" @click="loadInvoices(currentPage + 1)">{{ t('pagination.next') }}</button>
        </div>
      </template>

      <form v-else :id="formId" class="space-y-4" @submit.prevent="submit">
        <template v-if="mode === 'void'">
          <p class="text-sm text-gray-700 dark:text-dark-300">{{ t('admin.users.invoices.voidConfirmation', { amount: `$${money(selected!.amount)}` }) }}</p>
          <div>
            <label :for="`${formId}-reason`" class="input-label">{{ t('admin.users.invoices.voidReason') }} <span class="text-red-500">*</span></label>
            <textarea :id="`${formId}-reason`" v-model="voidReason" required maxlength="500" rows="3" class="input" :disabled="busy" />
          </div>
        </template>
        <template v-else>
          <div>
            <label :for="`${formId}-amount`" class="input-label">{{ t('admin.users.invoices.amount') }} <span class="text-red-500">*</span></label>
            <div class="relative">
              <span class="absolute left-3 top-1/2 -translate-y-1/2 text-gray-500">$</span>
              <input :id="`${formId}-amount`" v-model="form.amount" type="number" step="any" min="0.00000001" required class="input pl-8" :disabled="busy || staleInvoice" />
            </div>
          </div>
          <div>
            <label :for="`${formId}-date`" class="input-label">{{ t('admin.users.invoices.date') }} <span class="text-red-500">*</span></label>
            <input :id="`${formId}-date`" v-model="form.invoice_date" type="date" required class="input" :disabled="busy || staleInvoice" />
          </div>
          <div>
            <label :for="`${formId}-number`" class="input-label">{{ t('admin.users.invoices.numberOptional') }}</label>
            <input :id="`${formId}-number`" v-model="form.invoice_number" maxlength="100" class="input" :disabled="busy || staleInvoice" />
          </div>
          <div>
            <label :for="`${formId}-notes`" class="input-label">{{ t('admin.users.invoices.notesOptional') }}</label>
            <textarea :id="`${formId}-notes`" v-model="form.notes" maxlength="1000" rows="3" class="input" :disabled="busy || staleInvoice" />
          </div>
        </template>
        <div v-if="submitError" role="alert" class="rounded-lg bg-red-50 p-3 text-sm text-red-600 dark:bg-red-900/20 dark:text-red-400">
          {{ submitError }}
          <button v-if="staleInvoice" type="button" class="ml-2 underline" @click="reloadAfterConflict">{{ t('admin.users.invoices.reload') }}</button>
        </div>
        <p v-if="resultUnknown" role="status" class="rounded-lg bg-amber-50 p-3 text-sm text-amber-700 dark:bg-amber-900/20 dark:text-amber-400">{{ t('admin.users.invoices.resultUnknown') }}</p>
      </form>
    </div>
    <template v-if="mode !== 'list'" #footer>
      <div class="flex justify-end gap-3">
        <button type="button" class="btn btn-secondary" :disabled="busy" @click="mode = 'list'">{{ t('common.cancel') }}</button>
        <button type="submit" :form="formId" class="btn" :class="mode === 'void' ? 'btn-danger' : 'btn-primary'" :disabled="submitting || readOnly || staleInvoice">{{ submitting ? t('common.saving') : mode === 'void' ? t('admin.users.invoices.void') : t('common.save') }}</button>
      </div>
    </template>
  </BaseDialog>
  </div>
</template>

<script setup lang="ts">
import { computed, onUnmounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI, type UserInvoiceAnnotation, type UserInvoiceAnnotationInput } from '@/api/admin'
import type { AdminUser } from '@/types'
import { extractApiErrorCode, extractApiErrorMessage } from '@/utils/apiError'
import { formatDateTime } from '@/utils/format'
import BaseDialog from '@/components/common/BaseDialog.vue'

const props = defineProps<{ show: boolean; user: AdminUser | null; readOnly: boolean }>()
const emit = defineEmits<{ busy: [value: boolean] }>()
const { t } = useI18n()
const dialogOpen = ref(false)
const mode = ref<'list' | 'create' | 'edit' | 'void'>('list')
const items = ref<UserInvoiceAnnotation[]>([])
const totalInvoiced = ref<number | null>(null)
const currentPage = ref(1)
const total = ref(0)
const pageSize = 10
const loading = ref(false)
const loadError = ref('')
const submitting = ref(false)
const resultUnknown = ref(false)
const busy = computed(() => submitting.value || resultUnknown.value)
const submitError = ref('')
const staleInvoice = ref(false)
const expectedUpdatedAt = ref('')
const selected = ref<UserInvoiceAnnotation | null>(null)
const voidReason = ref('')
const form = reactive({ amount: '', invoice_date: '', invoice_number: '', notes: '' })
let requestVersion = 0
let contextVersion = 0
let pendingCreate: { signature: string; input: UserInvoiceAnnotationInput & { operation_id: string } } | null = null
const formId = computed(() => `user-invoice-form-${props.user?.id ?? 0}`)
const totalPages = computed(() => Math.ceil(total.value / pageSize) || 1)
const dialogTitle = computed(() => t(`admin.users.invoices.${mode.value === 'list' ? 'title' : mode.value === 'create' ? 'register' : mode.value === 'edit' ? 'edit' : 'void'}`))
const money = (value: number) => value.toFixed(2)

function operationId() {
  // randomUUID is restricted to secure contexts; VM previews may use HTTP.
  if (typeof crypto.randomUUID === 'function') return crypto.randomUUID()
  const bytes = crypto.getRandomValues(new Uint8Array(16))
  bytes[6] = (bytes[6]! & 0x0f) | 0x40
  bytes[8] = (bytes[8]! & 0x3f) | 0x80
  const hex = Array.from(bytes, (byte) => byte.toString(16).padStart(2, '0')).join('')
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`
}

async function loadInvoices(page: number) {
  if (!props.show || !props.user) return
  const version = ++requestVersion
  const userId = props.user.id
  loading.value = true
  loadError.value = ''
  try {
    const res = await adminAPI.users.getUserInvoices(userId, page, pageSize)
    if (version !== requestVersion) return
    items.value = res.items ?? []
    total.value = res.total
    totalInvoiced.value = res.total_invoiced
    currentPage.value = res.page
  } catch (error) {
    if (version !== requestVersion) return
    totalInvoiced.value = null
    loadError.value = extractApiErrorMessage(error, t('admin.users.invoices.loadFailed'))
  } finally {
    if (version === requestVersion) loading.value = false
  }
}

function openList() {
  if (busy.value) return
  mode.value = 'list'
  dialogOpen.value = true
  void loadInvoices(1)
}

function openEditor(item?: UserInvoiceAnnotation) {
  if (props.readOnly || busy.value || !props.user) return
  pendingCreate = null
  selected.value = item ?? null
  expectedUpdatedAt.value = item?.updated_at ?? ''
  staleInvoice.value = false
  form.amount = item ? String(item.amount) : ''
  const today = new Date()
  form.invoice_date = item?.invoice_date ?? `${today.getFullYear()}-${String(today.getMonth() + 1).padStart(2, '0')}-${String(today.getDate()).padStart(2, '0')}`
  form.invoice_number = item?.invoice_number ?? ''
  form.notes = item?.notes ?? ''
  submitError.value = ''
  mode.value = item ? 'edit' : 'create'
  dialogOpen.value = true
}

function openVoid(item: UserInvoiceAnnotation) {
  if (props.readOnly || busy.value) return
  selected.value = item
  voidReason.value = ''
  submitError.value = ''
  staleInvoice.value = false
  mode.value = 'void'
}

function reloadAfterConflict() {
  if (busy.value) return
  staleInvoice.value = false
  submitError.value = ''
  mode.value = 'list'
  void loadInvoices(1)
}

function closeDialog() {
  if (!busy.value) dialogOpen.value = false
}

async function submit() {
  if (submitting.value || staleInvoice.value || props.readOnly || !props.show || !props.user) return
  const userId = props.user.id
  const context = contextVersion
  const invoiceId = selected.value?.id
  const operation = mode.value
  const amount = Number(form.amount)
  if (operation === 'void' ? !voidReason.value.trim() : !Number.isFinite(amount) || amount <= 0 || !form.invoice_date) {
    submitError.value = t(operation === 'void' ? 'admin.users.invoices.reasonRequired' : 'admin.users.invoices.requiredFields')
    return
  }
  const input = { amount, invoice_date: form.invoice_date, invoice_number: form.invoice_number.trim(), notes: form.notes.trim() }
  if (operation === 'create') {
    const signature = JSON.stringify(input)
    if (!pendingCreate || pendingCreate.signature !== signature) {
      pendingCreate = { signature, input: { ...input, operation_id: operationId() } }
    }
  }
  submitting.value = true
  submitError.value = ''
  emit('busy', true)
  try {
    if (operation === 'void' && invoiceId !== undefined) await adminAPI.users.voidUserInvoice(userId, invoiceId, voidReason.value.trim())
    else if (operation === 'edit' && invoiceId !== undefined) await adminAPI.users.updateUserInvoice(userId, invoiceId, { ...input, expected_updated_at: expectedUpdatedAt.value })
    else if (operation === 'create' && pendingCreate) await adminAPI.users.createUserInvoice(userId, pendingCreate.input)
    else return
    if (context !== contextVersion) return
    resultUnknown.value = false
    pendingCreate = null
    mode.value = 'list'
    await loadInvoices(1)
  } catch (error) {
    if (context !== contextVersion) return
    submitError.value = extractApiErrorMessage(error, t('admin.users.invoices.saveFailed'))
    const apiError = error as { status?: number; response?: { status?: number } }
    const status = apiError?.status ?? apiError?.response?.status
    resultUnknown.value = !status || status >= 500
    if (operation === 'edit' && status === 409 && extractApiErrorCode(error) === 'USER_INVOICE_STALE') {
      staleInvoice.value = true
      submitError.value = t('admin.users.invoices.stale')
    }
  } finally {
    if (context === contextVersion) {
      submitting.value = false
      emit('busy', resultUnknown.value)
    }
  }
}

watch(() => [props.show, props.user?.id] as const, () => {
  const wasBusy = busy.value
  requestVersion++
  contextVersion++
  submitting.value = false
  dialogOpen.value = false
  mode.value = 'list'
  items.value = []
  total.value = 0
  totalInvoiced.value = null
  loading.value = false
  loadError.value = ''
  submitError.value = ''
  staleInvoice.value = false
  expectedUpdatedAt.value = ''
  resultUnknown.value = false
  pendingCreate = null
  selected.value = null
  if (wasBusy) emit('busy', false)
  if (props.show && props.user) void loadInvoices(1)
}, { immediate: true })

watch(() => props.readOnly, (readOnly) => {
  if (readOnly && !busy.value) mode.value = 'list'
})

onUnmounted(() => { requestVersion++; contextVersion++; emit('busy', false) })
</script>
