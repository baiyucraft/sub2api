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

      <div v-if="previewAccountCount > 0 && proxyIPGroupEligible" class="space-y-3 rounded-xl border border-gray-200 p-4 dark:border-dark-700">
        <div>
          <div class="text-sm font-medium text-gray-900 dark:text-white">{{ t('admin.accounts.dataImportProxyGroup') }}</div>
          <div class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.accounts.dataImportProxyGroupHint') }}</div>
        </div>
        <Select
          v-model="proxyIPGroupId"
          :options="proxyIPGroupOptions"
          :placeholder="t('admin.accounts.dataImportSelectProxyGroup')"
          :loading="proxyIPGroupsLoading"
          searchable
          clearable
          data-testid="data-import-proxy-ip-group"
        />
      </div>

      <div v-if="previewAccountCount > 0" class="space-y-3 rounded-lg border border-gray-200 p-4 dark:border-dark-700">
        <div class="text-sm font-medium text-gray-900 dark:text-white">{{ t('admin.accounts.dataImportOverrides') }}</div>
        <div data-testid="data-import-numeric-overrides" class="grid gap-3 sm:grid-cols-3">
          <div>
            <label class="input-label">{{ t('admin.accounts.dataImportConcurrency') }}</label>
            <input v-model="overrideConcurrency" class="input" type="number" min="0" step="1" :placeholder="t('admin.accounts.dataImportKeepOriginal')" />
          </div>
          <div>
            <label class="input-label">{{ t('admin.accounts.dataImportRateMultiplier') }}</label>
            <input v-model="overrideRateMultiplier" class="input" type="number" min="0" step="0.001" :placeholder="t('admin.accounts.dataImportKeepOriginal')" />
          </div>
          <div>
            <label class="input-label">{{ t('admin.accounts.dataImportPriority') }}</label>
            <input v-model="overridePriority" class="input" type="number" min="1" step="1" :placeholder="t('admin.accounts.dataImportKeepOriginal')" />
          </div>
        </div>
        <div>
          <label class="input-label">{{ t('admin.accounts.dataImportCodexFingerprint') }}</label>
          <Select v-model="overrideCodexFingerprintMode" :options="codexFingerprintOptions" clearable :placeholder="t('admin.accounts.dataImportKeepOriginal')" />
        </div>
      </div>

      <div v-if="previewAccountCount > 0" class="space-y-3 rounded-xl border border-gray-200 p-4 dark:border-dark-700">
        <div>
          <div class="text-sm font-medium text-gray-900 dark:text-white">{{ t('admin.accounts.dataImportGroups') }}</div>
          <div class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.accounts.dataImportGroupsHint') }}</div>
        </div>
        <div v-if="groupsLoading" class="text-xs text-gray-500 dark:text-dark-400">{{ t('common.loading') }}</div>
        <div
          v-else-if="importPlatform === 'mixed'"
          data-testid="data-import-mixed-platform-warning"
          class="rounded-lg border border-amber-200 bg-amber-50 p-3 text-xs text-amber-700 dark:border-amber-800 dark:bg-amber-900/20 dark:text-amber-300"
        >
          {{ t('admin.accounts.dataImportMixedPlatformGroupsDisabled') }}
        </div>
        <GroupSelector
          v-else-if="importPlatform"
          v-model="groupIds"
          v-model:preferred-group-ids="preferredGroupIds"
          :groups="groups"
          :platform="importPlatform"
          data-testid="data-import-group-selector"
        />
        <div v-else class="text-xs text-gray-500 dark:text-dark-400">
          {{ t('admin.accounts.dataImportPlatformUnavailable') }}
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
import GroupSelector from '@/components/common/GroupSelector.vue'
import { adminAPI } from '@/api/admin'
import { useAppStore } from '@/stores/app'
import type { AccountPlatform, AdminDataImportResult, AdminDataPayload, AdminGroup, ProxyIPGroup } from '@/types'

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
const proxyIPGroups = ref<ProxyIPGroup[]>([])
const proxyIPGroupsLoading = ref(false)
const groups = ref<AdminGroup[]>([])
const groupsLoading = ref(false)
const proxyIPGroupId = ref<number | null>(null)
const proxyIPGroupEligible = ref(false)
const overrideConcurrency = ref('20')
const overrideRateMultiplier = ref('0')
const overridePriority = ref('1')
const overrideCodexFingerprintMode = ref<'off' | 'device' | 'session' | 'full' | null>('session')
const groupIds = ref<number[]>([])
const preferredGroupIds = ref<number[]>([])
const importPlatform = ref<AccountPlatform | 'mixed' | null>(null)
const previewAccountCount = ref(0)

const fileInput = ref<HTMLInputElement | null>(null)
const selectedFilesLabel = computed(() => {
  if (files.value.length === 0) return ''
  if (files.value.length === 1) return files.value[0]?.name || ''
  return t('admin.accounts.selectedCount', { count: files.value.length })
})
const fileListTitle = computed(() => files.value.map((item) => item.name).join(', '))

const errorItems = computed(() => result.value?.errors || [])
const proxyIPGroupOptions = computed<SelectOption[]>(() => proxyIPGroups.value.map(group => ({
  value: group.id,
  label: t('admin.accounts.proxyBinding.groupOption', {
    name: group.name,
    count: group.member_count ?? group.proxy_ids?.length ?? group.members?.length ?? 0,
    limit: group.per_ip_concurrency
  })
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

const SUPPORTED_ACCOUNT_PLATFORMS = new Set<AccountPlatform>([
  'anthropic',
  'openai',
  'gemini',
  'antigravity',
  'grok',
  'kimi',
  'zhipu',
  'deepseek',
  'minimax',
  'opencode_go'
])

const resolveImportPlatform = (payloads: AdminDataPayload[]): AccountPlatform | 'mixed' | null => {
  const platforms = new Set<AccountPlatform>()
  for (const payload of payloads) {
    for (const account of payload.accounts) {
      const platform = String(account.platform || '').trim().toLowerCase() as AccountPlatform
      if (!SUPPORTED_ACCOUNT_PLATFORMS.has(platform)) return null
      platforms.add(platform)
    }
  }
  if (platforms.size === 1) return Array.from(platforms)[0] ?? null
  return platforms.size > 1 ? 'mixed' : null
}

const supportsProxyIPGroupImport = (payloads: AdminDataPayload[]): boolean => {
  const accounts = payloads.flatMap(payload => payload.accounts)
  return accounts.length > 0 && accounts.every(account =>
    String(account.platform || '').trim().toLowerCase() === 'openai' &&
    (account.type === 'oauth' || account.type === 'setup-token')
  )
}

watch(
  () => props.show,
  (open) => {
    if (open) {
      files.value = []
      dragDepth.value = 0
      hasCreatedData.value = false
      result.value = null
      proxyIPGroupId.value = null
      proxyIPGroupEligible.value = false
      overrideConcurrency.value = '20'
      overrideRateMultiplier.value = '0'
      overridePriority.value = '1'
      overrideCodexFingerprintMode.value = 'session'
      groupIds.value = []
      preferredGroupIds.value = []
      importPlatform.value = null
      previewAccountCount.value = 0
      proxyIPGroupsLoading.value = true
      groupsLoading.value = true
      Promise.resolve().then(() => adminAPI.proxyIpGroups.list()).then((items) => {
        proxyIPGroups.value = items
      }).catch(() => {
        proxyIPGroups.value = []
      }).finally(() => {
        proxyIPGroupsLoading.value = false
      })
      Promise.resolve().then(() => adminAPI.groups.getAll()).then((items) => {
        groups.value = items
      }).catch(() => {
        groups.value = []
      }).finally(() => {
        groupsLoading.value = false
      })
      if (fileInput.value) {
        fileInput.value.value = ''
      }
    }
  },
  { immediate: true }
)

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
  importPlatform.value = null
  proxyIPGroupId.value = null
  proxyIPGroupEligible.value = false
  groupIds.value = []
  preferredGroupIds.value = []
  void updatePreview(picked)
}

const updatePreview = async (sourceFiles: File[]) => {
  let count = 0
  const payloads: AdminDataPayload[] = []
  for (const sourceFile of sourceFiles) {
    try {
      const parsed = JSON.parse(await readFileAsText(sourceFile))
      if (isValidDataPayload(parsed)) {
        payloads.push(parsed)
        count += parsed.accounts.length
      }
    } catch {
      // Full validation and user-facing errors remain in handleImport.
    }
  }
  const nextPlatform = resolveImportPlatform(payloads)
  const nextProxyIPGroupEligible = supportsProxyIPGroupImport(payloads)
  if (nextPlatform !== importPlatform.value) {
    groupIds.value = []
    preferredGroupIds.value = []
  }
  if (!nextProxyIPGroupEligible) proxyIPGroupId.value = null
  importPlatform.value = nextPlatform
  proxyIPGroupEligible.value = nextProxyIPGroupEligible
  previewAccountCount.value = count
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
    let priorityOverride: number | undefined
    const priorityText = optionalNumberInputText(overridePriority.value)
    if (priorityText !== '') {
      priorityOverride = Number(priorityText)
      if (!Number.isInteger(priorityOverride) || priorityOverride < 1) {
        appStore.showError(t('admin.accounts.dataImportInvalidPriority'))
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
    const resolvedPlatform = resolveImportPlatform(dataPayloads)
    const resolvedProxyIPGroupEligible = supportsProxyIPGroupImport(dataPayloads)
    proxyIPGroupEligible.value = resolvedProxyIPGroupEligible
    if (!resolvedProxyIPGroupEligible) proxyIPGroupId.value = null
    if (proxyIPGroupId.value !== null && !proxyIPGroups.value.some(group => group.id === proxyIPGroupId.value)) {
      appStore.showError(t('admin.accounts.dataImportInvalidProxyGroup'))
      return
    }
    if (resolvedPlatform !== importPlatform.value) {
      importPlatform.value = resolvedPlatform
      groupIds.value = []
      preferredGroupIds.value = []
    }

    const importOptions: Parameters<typeof adminAPI.accounts.importData>[0] = {
      data: dataPayload,
      skip_default_group_bind: true
    }
    if (resolvedProxyIPGroupEligible && proxyIPGroupId.value !== null) {
      importOptions.proxy_ip_group_id = proxyIPGroupId.value
    }
    if (concurrencyOverride !== undefined) importOptions.override_concurrency = concurrencyOverride
    if (rateOverride !== undefined) importOptions.override_rate_multiplier = rateOverride
    if (priorityOverride !== undefined) importOptions.override_priority = priorityOverride
    if (overrideCodexFingerprintMode.value) {
      importOptions.override_codex_fingerprint_mode = overrideCodexFingerprintMode.value
    }
    if (resolvedPlatform && resolvedPlatform !== 'mixed') {
      importOptions.group_ids = [...groupIds.value]
      importOptions.preferred_group_ids = [...preferredGroupIds.value]
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
