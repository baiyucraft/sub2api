<template>
  <BaseDialog
    :show="show"
    :title="t('admin.accounts.dataImportTitle')"
    width="normal"
    close-on-click-outside
    @close="handleClose"
  >
    <form id="import-data-form" class="space-y-4" @submit.prevent="handleImport">
      <div class="text-sm text-gray-600 dark:text-dark-300">
        {{ t('admin.accounts.dataImportHint') }}
      </div>
      <div
        class="rounded-lg border border-amber-200 bg-amber-50 p-3 text-xs text-amber-600 dark:border-amber-800 dark:bg-amber-900/20 dark:text-amber-400"
      >
        {{ t('admin.accounts.dataImportWarning') }}
      </div>

      <div>
        <label class="input-label">{{ t('admin.accounts.dataImportFile') }}</label>
        <div
          class="flex items-center justify-between gap-3 rounded-lg border border-dashed px-4 py-3 transition-colors"
          :class="dragActive
            ? 'border-primary-400 bg-primary-50/70 dark:border-primary-500 dark:bg-primary-900/20'
            : 'border-gray-300 bg-gray-50 dark:border-dark-600 dark:bg-dark-800'"
          @dragenter.prevent="handleDragEnter"
          @dragover.prevent
          @dragleave.prevent="handleDragLeave"
          @drop.prevent="handleDrop"
        >
          <div class="min-w-0">
            <div class="truncate text-sm text-gray-700 dark:text-dark-200" :title="fileListTitle">
              {{ selectedFilesLabel || t('admin.accounts.dataImportSelectFile') }}
            </div>
            <div class="text-xs text-gray-500 dark:text-dark-400">
              JSON (.json)
              <span v-if="files.length > 1"> · {{ fileListTitle }}</span>
            </div>
          </div>
          <button type="button" class="btn btn-secondary shrink-0" @click="openFilePicker">
            {{ t('common.chooseFile') }}
          </button>
        </div>
        <input
          ref="fileInput"
          type="file"
          class="hidden"
          accept="application/json,.json"
          multiple
          @change="handleFileChange"
        />
      </div>

      <div v-if="previewAccountCount > 0" class="space-y-3 rounded-xl border border-gray-200 p-4 dark:border-dark-700">
        <div class="flex items-center justify-between gap-3">
          <div>
            <div class="text-sm font-medium text-gray-900 dark:text-white">{{ t('admin.accounts.dataImportCopyProxies') }}</div>
            <div class="text-xs text-gray-500 dark:text-dark-400">
              {{ copyProxyIds.length
                ? t('admin.accounts.dataImportCopyCount', { count: previewAccountCount * copyProxyIds.length })
                : t('admin.accounts.dataImportCompatMode') }}
            </div>
          </div>
          <button type="button" class="btn btn-ghost shrink-0" :disabled="copyProxyIds.length >= 50 || proxiesLoading" @click="addCopyProxy">
            {{ t('admin.accounts.dataImportAddCopy') }}
          </button>
        </div>
        <div v-if="proxiesLoading" class="text-xs text-gray-500 dark:text-dark-400">{{ t('common.loading') }}</div>
        <div v-else-if="copyProxyIds.length" class="space-y-2">
          <div v-for="(proxyId, index) in copyProxyIds" :key="`copy-proxy-${index}`" class="flex items-center gap-2">
            <span class="w-6 text-center text-xs text-gray-500">{{ index + 1 }}</span>
            <div class="min-w-0 flex-1">
              <Select
                :model-value="proxyId"
                :options="proxyOptions"
                :placeholder="t('admin.accounts.dataImportSelectProxy')"
                searchable
                :aria-label="t('admin.accounts.dataImportSelectProxy')"
                @update:model-value="(value) => setCopyProxy(index, value)"
              />
            </div>
            <button
              type="button"
              class="btn btn-secondary inline-flex h-9 w-9 items-center justify-center p-0"
              :aria-label="t('admin.accounts.dataImportRemoveCopy')"
              :title="t('admin.accounts.dataImportRemoveCopy')"
              @click="removeCopyProxy(index)"
            >
              <Icon name="x" size="sm" />
            </button>
          </div>
          <div v-if="previewName" class="text-xs text-gray-500 dark:text-dark-400">
            {{ t('admin.accounts.dataImportPreviewName', { name: previewName }) }}
          </div>
        </div>
        <div v-else class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.accounts.dataImportCompatMode') }}</div>
      </div>

      <div v-if="previewAccountCount > 0" class="space-y-3 rounded-xl border border-gray-200 p-4 dark:border-dark-700">
        <div class="text-sm font-medium text-gray-900 dark:text-white">{{ t('admin.accounts.dataImportOverrides') }}</div>
        <div class="grid gap-3 sm:grid-cols-2">
          <div>
            <label class="input-label">{{ t('admin.accounts.dataImportConcurrency') }}</label>
            <input v-model="overrideConcurrency" class="input" type="number" min="0" step="1" :placeholder="t('admin.accounts.dataImportKeepOriginal')" />
          </div>
          <div>
            <label class="input-label">{{ t('admin.accounts.dataImportRateMultiplier') }}</label>
            <input v-model="overrideRateMultiplier" class="input" type="number" min="0" step="0.001" :placeholder="t('admin.accounts.dataImportKeepOriginal')" />
          </div>
        </div>
        <div>
          <label class="input-label">{{ t('admin.accounts.dataImportCodexFingerprint') }}</label>
          <Select v-model="overrideCodexFingerprintMode" :options="codexFingerprintOptions" clearable :placeholder="t('admin.accounts.dataImportKeepOriginal')" />
        </div>
      </div>

      <div
        v-if="result"
        class="space-y-2 rounded-xl border border-gray-200 p-4 dark:border-dark-700"
      >
        <div class="text-sm font-medium text-gray-900 dark:text-white">
          {{ t('admin.accounts.dataImportResult') }}
        </div>
        <div class="text-sm text-gray-700 dark:text-dark-300">
          {{ t('admin.accounts.dataImportResultSummary', result) }}
        </div>

        <div v-if="errorItems.length" class="mt-2">
          <div class="text-sm font-medium text-red-600 dark:text-red-400">
            {{ t('admin.accounts.dataImportErrors') }}
          </div>
          <div
            class="mt-2 max-h-48 overflow-auto rounded-lg bg-gray-50 p-3 font-mono text-xs dark:bg-dark-800"
          >
            <div v-for="(item, idx) in errorItems" :key="idx" class="whitespace-pre-wrap">
              {{ item.kind }} {{ item.name || item.proxy_key || '-' }} — {{ item.message }}
            </div>
          </div>
        </div>
      </div>
    </form>

    <template #footer>
      <div class="flex justify-end gap-3">
        <button class="btn btn-secondary" type="button" :disabled="importing" @click="handleClose">
          {{ t('common.cancel') }}
        </button>
        <button
          class="btn btn-primary"
          type="submit"
          form="import-data-form"
          :disabled="importing"
        >
          {{ importing ? t('admin.accounts.dataImporting') : t('admin.accounts.dataImportButton') }}
        </button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Select, { type SelectOption } from '@/components/common/Select.vue'
import Icon from '@/components/icons/Icon.vue'
import { adminAPI } from '@/api/admin'
import { useAppStore } from '@/stores/app'
import type { AdminDataImportResult, AdminDataPayload, Proxy } from '@/types'

interface Props {
  show: boolean
}

interface Emits {
  (e: 'close'): void
  (e: 'imported'): void
}

const props = defineProps<Props>()
const emit = defineEmits<Emits>()

const { t } = useI18n()
const appStore = useAppStore()

const importing = ref(false)
const files = ref<File[]>([])
const dragDepth = ref(0)
const dragActive = computed(() => dragDepth.value > 0)
const hasCreatedData = ref(false)
const result = ref<AdminDataImportResult | null>(null)
const proxies = ref<Proxy[]>([])
const proxiesLoading = ref(false)
const copyProxyIds = ref<number[]>([])
const overrideConcurrency = ref('')
const overrideRateMultiplier = ref('')
const overrideCodexFingerprintMode = ref<'off' | 'device' | 'session' | 'full' | null>(null)
const previewAccountCount = ref(0)
const previewName = ref('')
const previewBaseName = ref('')

const fileInput = ref<HTMLInputElement | null>(null)
const selectedFilesLabel = computed(() => {
  if (files.value.length === 0) return ''
  if (files.value.length === 1) return files.value[0]?.name || ''
  return t('admin.accounts.selectedCount', { count: files.value.length })
})
const fileListTitle = computed(() => files.value.map((item) => item.name).join(', '))

const errorItems = computed(() => result.value?.errors || [])
const proxyOptions = computed<SelectOption[]>(() => proxies.value.map((proxy) => ({
  value: proxy.id,
  label: proxy.name || `${proxy.host}:${proxy.port}`
})))
const codexFingerprintOptions = computed<SelectOption[]>(() => [
  { value: 'off', label: t('admin.accounts.codexFingerprintOff') },
  { value: 'device', label: t('admin.accounts.codexFingerprintDevice') },
  { value: 'session', label: t('admin.accounts.codexFingerprintSession') },
  { value: 'full', label: t('admin.accounts.codexFingerprintFull') }
])

const optionalNumberInputText = (value: unknown): string => {
  if (value === null || value === undefined) return ''
  return String(value).trim()
}

watch(
  () => props.show,
  (open) => {
    if (open) {
      files.value = []
      dragDepth.value = 0
      hasCreatedData.value = false
      result.value = null
      copyProxyIds.value = []
      overrideConcurrency.value = ''
      overrideRateMultiplier.value = ''
      overrideCodexFingerprintMode.value = null
      previewAccountCount.value = 0
      previewName.value = ''
      previewBaseName.value = ''
      proxiesLoading.value = true
      Promise.resolve().then(() => adminAPI.proxies.getAll()).then((items) => {
        proxies.value = items.filter((proxy) => proxy.status === 'active')
        if (files.value.length) void updatePreview(files.value)
      }).catch(() => {
        proxies.value = []
      }).finally(() => {
        proxiesLoading.value = false
      })
      if (fileInput.value) {
        fileInput.value.value = ''
      }
    }
  },
  { immediate: true }
)

watch([copyProxyIds, proxies], () => {
  if (!previewBaseName.value || !copyProxyIds.value.length) return
  const suffix = proxies.value.find((item) => item.id === copyProxyIds.value[0])
  if (!suffix) return
  previewName.value = `${previewBaseName.value} - ${suffix.name || `${suffix.host}:${suffix.port}`}`
}, { deep: true })

const addCopyProxy = () => {
  if (copyProxyIds.value.length >= 50 || proxies.value.length === 0) return
  copyProxyIds.value.push(proxies.value[0]?.id ?? 0)
}

const setCopyProxy = (index: number, value: unknown) => {
  const id = Number(value)
  if (!Number.isInteger(id) || id <= 0) return
  copyProxyIds.value[index] = id
}

const removeCopyProxy = (index: number) => {
  copyProxyIds.value.splice(index, 1)
  if (copyProxyIds.value.length === 0) previewName.value = ''
}

const openFilePicker = () => {
  fileInput.value?.click()
}

const handleFileChange = (event: Event) => {
  const target = event.target as HTMLInputElement
  setSelectedFiles(target.files)
  target.value = ''
}

const handleClose = () => {
  if (importing.value) return
  if (hasCreatedData.value) {
    hasCreatedData.value = false
    emit('imported')
  }
  emit('close')
}

const isJsonFile = (sourceFile: File) => {
  const name = sourceFile.name.toLowerCase()
  return name.endsWith('.json') || sourceFile.type === 'application/json'
}

const setSelectedFiles = (sourceFiles: FileList | File[] | null | undefined) => {
  if (importing.value) return
  const incoming = Array.from(sourceFiles || [])
  const picked = incoming.filter(isJsonFile)
  if (!picked.length) {
    appStore.showError(t('admin.accounts.dataImportSelectFile'))
    return
  }
  if (picked.length < incoming.length) {
    appStore.showWarning(
      t('admin.accounts.dataImportIgnoredFiles', { count: incoming.length - picked.length })
    )
  }
  files.value = picked
  result.value = null
  previewAccountCount.value = 0
  previewName.value = ''
  previewBaseName.value = ''
  void updatePreview(picked)
}

const updatePreview = async (sourceFiles: File[]) => {
  let count = 0
  let firstName = ''
  for (const sourceFile of sourceFiles) {
    try {
      const parsed = JSON.parse(await readFileAsText(sourceFile))
      if (isValidDataPayload(parsed)) {
        count += parsed.accounts.length
        if (!firstName && parsed.accounts[0]?.name) firstName = parsed.accounts[0].name
      }
    } catch {
      // Full validation and user-facing errors remain in handleImport.
    }
  }
  previewAccountCount.value = count
  const proxy = proxies.value.find((item) => item.id === copyProxyIds.value[0])
  previewBaseName.value = firstName
  previewName.value = firstName && proxy ? `${firstName} - ${proxy.name || `${proxy.host}:${proxy.port}`}` : ''
}

const handleDragEnter = () => {
  if (importing.value) return
  dragDepth.value += 1
}

const handleDragLeave = () => {
  dragDepth.value = Math.max(0, dragDepth.value - 1)
}

const handleDrop = (event: DragEvent) => {
  dragDepth.value = 0
  if (importing.value) return
  setSelectedFiles(event.dataTransfer?.files)
}

const readFileAsText = async (sourceFile: File): Promise<string> => {
  if (typeof sourceFile.text === 'function') {
    return sourceFile.text()
  }

  if (typeof sourceFile.arrayBuffer === 'function') {
    const buffer = await sourceFile.arrayBuffer()
    return new TextDecoder().decode(buffer)
  }

  return await new Promise<string>((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => resolve(String(reader.result ?? ''))
    reader.onerror = () => reject(reader.error || new Error('Failed to read file'))
    reader.readAsText(sourceFile)
  })
}

const SUPPORTED_DATA_TYPES = ['sub2api-data', 'sub2api-bundle']
const SUPPORTED_DATA_VERSION = 1

// 与后端 validateDataHeader 对齐:合并前逐文件校验,避免坏文件混入合并 payload 后
// 报错无法定位来源,或绕过后端本会对单文件做的 type/version 检查。
const isValidDataPayload = (payload: unknown): payload is AdminDataPayload => {
  if (!payload || typeof payload !== 'object' || Array.isArray(payload)) return false
  const candidate = payload as Record<string, unknown>
  if (
    candidate.type !== undefined &&
    candidate.type !== '' &&
    !SUPPORTED_DATA_TYPES.includes(candidate.type as string)
  ) {
    return false
  }
  if (
    candidate.version !== undefined &&
    candidate.version !== 0 &&
    candidate.version !== SUPPORTED_DATA_VERSION
  ) {
    return false
  }
  return Array.isArray(candidate.proxies) && Array.isArray(candidate.accounts)
}

const mergeDataPayloads = (payloads: AdminDataPayload[]): AdminDataPayload => {
  const [firstPayload] = payloads
  if (payloads.length === 1 && firstPayload) return firstPayload

  return {
    type: payloads.find((item) => typeof item.type === 'string')?.type,
    version: payloads.find((item) => typeof item.version === 'number')?.version,
    exported_at: new Date().toISOString(),
    proxies: payloads.flatMap((item) => item.proxies),
    accounts: payloads.flatMap((item) => item.accounts),
    skipped_shadows: payloads.reduce((sum, item) => {
      const count = Number(item.skipped_shadows || 0)
      return Number.isFinite(count) ? sum + count : sum
    }, 0),
    skipped_upstream_accounts: payloads.reduce((sum, item) => {
      const count = Number(item.skipped_upstream_accounts || 0)
      return Number.isFinite(count) ? sum + count : sum
    }, 0)
  }
}

const handleImport = async () => {
  if (files.value.length === 0) {
    appStore.showError(t('admin.accounts.dataImportSelectFile'))
    return
  }

  importing.value = true
  try {
    if (copyProxyIds.value.length > 0) {
      const validProxyIds = new Set(proxies.value.map((proxy) => proxy.id))
      if (copyProxyIds.value.some((id) => !Number.isInteger(id) || id <= 0 || !validProxyIds.has(id))) {
        appStore.showError(t('admin.accounts.dataImportInvalidProxy'))
        return
      }
    }
    let concurrencyOverride: number | undefined
    const concurrencyText = optionalNumberInputText(overrideConcurrency.value)
    if (concurrencyText !== '') {
      concurrencyOverride = Number(concurrencyText)
      if (!Number.isInteger(concurrencyOverride) || concurrencyOverride < 0) {
        appStore.showError(t('admin.accounts.dataImportInvalidConcurrency'))
        return
      }
    }
    let rateOverride: number | undefined
    const rateText = optionalNumberInputText(overrideRateMultiplier.value)
    if (rateText !== '') {
      rateOverride = Number(rateText)
      if (!Number.isFinite(rateOverride) || rateOverride < 0) {
        appStore.showError(t('admin.accounts.dataImportInvalidRateMultiplier'))
        return
      }
    }
    const dataPayloads: AdminDataPayload[] = []
    for (const sourceFile of files.value) {
      let parsed: unknown
      try {
        parsed = JSON.parse(await readFileAsText(sourceFile))
      } catch {
        appStore.showError(
          t('admin.accounts.dataImportParseFailedFile', { name: sourceFile.name })
        )
        return
      }
      if (!isValidDataPayload(parsed)) {
        appStore.showError(t('admin.accounts.dataImportInvalidFile', { name: sourceFile.name }))
        return
      }
      dataPayloads.push(parsed)
    }
    const dataPayload = mergeDataPayloads(dataPayloads)

    const importOptions: Parameters<typeof adminAPI.accounts.importData>[0] = {
      data: dataPayload,
      skip_default_group_bind: true
    }
    if (copyProxyIds.value.length > 0) importOptions.copy_proxy_ids = [...copyProxyIds.value]
    if (concurrencyOverride !== undefined) importOptions.override_concurrency = concurrencyOverride
    if (rateOverride !== undefined) importOptions.override_rate_multiplier = rateOverride
    if (overrideCodexFingerprintMode.value) {
      importOptions.override_codex_fingerprint_mode = overrideCodexFingerprintMode.value
    }
    const res = await adminAPI.accounts.importData(importOptions)

    result.value = res

    const msgParams: Record<string, unknown> = {
      account_created: res.account_created,
      account_failed: res.account_failed,
      proxy_created: res.proxy_created,
      proxy_reused: res.proxy_reused,
      proxy_failed: res.proxy_failed,
    }
    if (res.account_failed > 0 || res.proxy_failed > 0) {
      // 部分成功也创建了数据;弹窗关闭时通过 imported 通知父组件刷新列表
      if (res.account_created > 0 || res.proxy_created > 0) {
        hasCreatedData.value = true
      }
      appStore.showError(t('admin.accounts.dataImportCompletedWithErrors', msgParams))
    } else {
      appStore.showSuccess(t('admin.accounts.dataImportSuccess', msgParams))
      emit('imported')
    }
  } catch (error: any) {
    appStore.showError(error?.message || t('admin.accounts.dataImportFailed'))
  } finally {
    importing.value = false
  }
}
</script>
