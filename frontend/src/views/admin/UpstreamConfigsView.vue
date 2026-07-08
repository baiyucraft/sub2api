<template>
  <AppLayout>
    <TablePageLayout>
      <template #filters>
        <div class="flex flex-col justify-between gap-4 lg:flex-row lg:items-center">
          <div class="flex flex-1 flex-wrap items-center gap-3">
            <div class="relative w-full sm:w-72">
              <Icon
                name="search"
                size="md"
                class="absolute left-3 top-1/2 -translate-y-1/2 text-gray-400 dark:text-dark-500"
              />
              <input
                v-model="search"
                class="input pl-10"
                type="text"
                :placeholder="t('admin.upstreamConfigs.searchPlaceholder')"
                @input="debouncedReload"
                @keyup.enter="reloadFromFirstPage"
              />
            </div>

            <div class="w-full sm:w-44">
              <Select
                v-model="provider"
                :options="providerFilterOptions"
                @change="reloadFromFirstPage"
              />
            </div>
          </div>

          <div class="flex w-full flex-shrink-0 flex-wrap items-center justify-end gap-2 lg:w-auto">
            <button
              type="button"
              class="btn btn-secondary"
              :disabled="syncingAll"
              :title="t('admin.upstreamConfigs.actions.syncAll')"
              @click="handleSyncAll"
            >
              <Icon name="sync" size="md" :class="syncingAll ? 'mr-2 animate-spin' : 'mr-2'" />
              {{ t('admin.upstreamConfigs.actions.syncAll') }}
            </button>
            <button
              type="button"
              class="btn btn-secondary px-3"
              :disabled="loading"
              :title="t('common.refresh')"
              @click="loadConfigs"
            >
              <Icon name="refresh" size="md" :class="loading ? 'animate-spin' : ''" />
            </button>
            <button type="button" class="btn btn-primary" @click="openCreate">
              {{ t('admin.upstreamConfigs.actions.create') }}
            </button>
          </div>
        </div>
      </template>

      <template #table>
        <DataTable
          :columns="columns"
          :data="configs"
          :loading="loading"
          row-key="id"
          :actions-count="4"
          :estimate-row-height="72"
        >
          <template #cell-name="{ row }">
            <div class="min-w-0">
              <div class="font-medium text-gray-900 dark:text-gray-100">{{ row.name }}</div>
              <div class="mt-0.5 text-xs text-gray-500 dark:text-dark-400">#{{ row.id }}</div>
            </div>
          </template>

          <template #cell-provider="{ row }">
            <span
              :class="[
                'inline-flex items-center rounded-full px-2.5 py-1 text-xs font-medium',
                providerBadgeClass(row.provider)
              ]"
            >
              {{ providerLabel(row.provider) }}
            </span>
          </template>

          <template #cell-base_url="{ value }">
            <div class="max-w-[320px] truncate font-mono text-xs text-gray-700 dark:text-gray-300" :title="value">
              {{ value }}
            </div>
          </template>

          <template #cell-auth_mode="{ row }">
            <span class="inline-flex items-center gap-1.5 text-sm text-gray-700 dark:text-gray-300">
              <Icon :name="row.auth_mode === 'manual_jwt' ? 'key' : 'login'" size="sm" class="text-gray-400" />
              {{ authModeLabel(row.auth_mode) }}
            </span>
          </template>

          <template #cell-credentials="{ row }">
            <div class="flex flex-col gap-1 text-xs text-gray-600 dark:text-dark-300">
              <span v-for="line in credentialLines(row)" :key="line.label" class="inline-flex items-center gap-1.5">
                <Icon
                  :name="line.ok ? 'checkCircle' : 'exclamationCircle'"
                  size="xs"
                  :class="line.ok ? 'text-emerald-500' : 'text-amber-500'"
                />
                {{ line.label }}
              </span>
            </div>
          </template>

          <template #cell-last_success_at="{ row }">
            <div class="min-w-[160px]">
              <div class="text-sm text-gray-700 dark:text-gray-300">{{ formatTime(row.last_success_at) }}</div>
              <div
                v-if="row.last_error"
                class="mt-1 max-w-[240px] truncate text-xs text-red-500 dark:text-red-400"
                :title="row.last_error"
              >
                {{ row.last_error }}
              </div>
            </div>
          </template>

          <template #cell-actions="{ row }">
            <div class="flex items-center gap-1" @click.stop>
              <button
                type="button"
                class="table-action-button hover:text-primary-600 dark:hover:text-primary-400"
                :title="t('common.edit')"
                :aria-label="t('common.edit')"
                @click="openEdit(row)"
              >
                <Icon name="edit" size="sm" />
              </button>
              <button
                type="button"
                class="table-action-button hover:text-emerald-600 dark:hover:text-emerald-400"
                :disabled="syncingAll || isActionBusy(row.id, 'sync')"
                :title="t('admin.upstreamConfigs.actions.syncKeys')"
                :aria-label="t('admin.upstreamConfigs.actions.syncKeys')"
                @click="handleSync(row)"
              >
                <Icon name="sync" size="sm" :class="isActionBusy(row.id, 'sync') ? 'animate-spin' : ''" />
              </button>
              <button
                type="button"
                class="table-action-button hover:text-blue-600 dark:hover:text-blue-400"
                :disabled="isActionBusy(row.id, 'test')"
                :title="t('admin.upstreamConfigs.actions.test')"
                :aria-label="t('admin.upstreamConfigs.actions.test')"
                @click="handleTest(row)"
              >
                <Icon name="play" size="sm" />
              </button>
              <button
                type="button"
                class="table-action-button hover:text-red-600 dark:hover:text-red-400"
                :title="t('common.delete')"
                :aria-label="t('common.delete')"
                @click="askDelete(row)"
              >
                <Icon name="trash" size="sm" />
              </button>
            </div>
          </template>

          <template #empty>
            <div class="flex flex-col items-center py-10 text-center">
              <Icon name="server" size="xl" class="mb-3 text-gray-400 dark:text-dark-500" />
              <p class="text-sm font-medium text-gray-900 dark:text-gray-100">
                {{ t('admin.upstreamConfigs.emptyTitle') }}
              </p>
              <p class="mt-1 text-sm text-gray-500 dark:text-dark-400">
                {{ t('admin.upstreamConfigs.emptyDescription') }}
              </p>
              <button type="button" class="btn btn-primary mt-4" @click="openCreate">
                {{ t('admin.upstreamConfigs.actions.create') }}
              </button>
            </div>
          </template>
        </DataTable>
      </template>

      <template #pagination>
        <Pagination
          v-if="pagination.total > 0"
          :page="pagination.page"
          :total="pagination.total"
          :page-size="pagination.page_size"
          @update:page="handlePageChange"
          @update:pageSize="handlePageSizeChange"
        />
      </template>
    </TablePageLayout>

    <BaseDialog
      :show="dialogOpen"
      :title="editing ? t('admin.upstreamConfigs.dialog.editTitle') : t('admin.upstreamConfigs.dialog.createTitle')"
      width="wide"
      :close-on-escape="!saving"
      :show-close-button="!saving"
      @close="handleDialogClose"
    >
      <form id="upstream-config-form" class="max-h-[65vh] overflow-y-auto pr-1" @submit.prevent="saveConfig">
        <div class="grid gap-4 md:grid-cols-2">
          <label class="space-y-1">
            <span class="input-label">{{ t('admin.upstreamConfigs.fields.name') }}</span>
            <input v-model.trim="form.name" class="input" required />
          </label>

          <label class="space-y-1">
            <span class="input-label">{{ t('admin.upstreamConfigs.fields.provider') }}</span>
            <Select v-model="form.provider" :options="providerEditOptions" />
          </label>

          <label class="space-y-1 md:col-span-2">
            <span class="input-label">{{ t('admin.upstreamConfigs.fields.baseUrl') }}</span>
            <input
              v-model.trim="form.base_url"
              class="input font-mono text-sm"
              required
              placeholder="https://example.com"
            />
          </label>

          <label v-if="form.provider === 'sub2api'" class="space-y-1">
            <span class="input-label">{{ t('admin.upstreamConfigs.fields.authMode') }}</span>
            <Select v-model="form.auth_mode" :options="authModeOptions" />
          </label>

          <div class="space-y-1">
            <span class="input-label">{{ t('admin.upstreamConfigs.fields.proxy') }}</span>
            <ProxySelector v-model="form.proxy_id" :proxies="proxies" :disabled="loadingProxies" />
          </div>

          <template v-if="form.provider === 'sub2api' && form.auth_mode === 'user_login'">
            <label class="space-y-1">
              <span class="input-label">{{ t('admin.upstreamConfigs.fields.loginEmail') }}</span>
              <input
                v-model.trim="form.email"
                class="input"
                type="email"
                autocomplete="username"
                :required="!editing"
              />
            </label>
            <label class="space-y-1">
              <span class="input-label">{{ t('admin.upstreamConfigs.fields.loginPassword') }}</span>
              <input
                v-model="form.password"
                class="input"
                type="password"
                autocomplete="new-password"
                :required="!editing"
                :placeholder="editing ? t('admin.upstreamConfigs.fields.keepPasswordPlaceholder') : ''"
              />
            </label>
          </template>

          <template v-if="form.provider === 'sub2api' && form.auth_mode === 'manual_jwt'">
            <div class="md:col-span-2 flex flex-wrap items-center justify-between gap-2 rounded-lg border border-blue-200 bg-blue-50 p-3 text-xs text-blue-800 dark:border-blue-900/50 dark:bg-blue-900/20 dark:text-blue-200">
              <span class="inline-flex items-center gap-2">
                <Icon name="key" size="sm" />
                {{ t('admin.upstreamConfigs.tokenAssistant.inlineHint') }}
              </span>
              <button
                type="button"
                class="btn btn-secondary btn-sm"
                data-test="open-token-assistant"
                @click="openTokenAssistant"
              >
                <Icon name="clipboard" size="sm" class="mr-1.5" />
                {{ t('admin.upstreamConfigs.tokenAssistant.open') }}
              </button>
            </div>
            <label class="space-y-1 md:col-span-2">
              <span class="input-label">{{ t('admin.upstreamConfigs.fields.accessToken') }}</span>
              <textarea
                v-model.trim="form.access_token"
                class="input min-h-[92px] font-mono text-xs"
                :required="!editing"
                :placeholder="editing ? t('admin.upstreamConfigs.fields.keepAccessTokenPlaceholder') : ''"
              ></textarea>
            </label>
            <label class="space-y-1 md:col-span-2">
              <span class="input-label">{{ t('admin.upstreamConfigs.fields.refreshToken') }}</span>
              <textarea
                v-model.trim="form.refresh_token"
                class="input min-h-[92px] font-mono text-xs"
                :placeholder="editing ? t('admin.upstreamConfigs.fields.keepRefreshTokenPlaceholder') : ''"
              ></textarea>
            </label>
          </template>
        </div>

        <div class="mt-4 flex gap-2 rounded-lg border border-amber-200 bg-amber-50 p-3 text-xs text-amber-800 dark:border-amber-900/50 dark:bg-amber-900/20 dark:text-amber-200">
          <Icon name="shield" size="sm" class="mt-0.5 flex-shrink-0" />
          <p>{{ t('admin.upstreamConfigs.sensitiveHint') }}</p>
        </div>
      </form>

      <template #footer>
        <div class="flex justify-end gap-2">
          <button type="button" class="btn btn-secondary" :disabled="saving" @click="handleDialogClose">
            {{ t('common.cancel') }}
          </button>
          <button type="submit" form="upstream-config-form" class="btn btn-primary" :disabled="saving">
            <Icon v-if="saving" name="refresh" size="sm" class="mr-2 animate-spin" />
            {{ saving ? t('admin.upstreamConfigs.actions.saving') : t('common.save') }}
          </button>
        </div>
      </template>
    </BaseDialog>

    <BaseDialog
      :show="tokenAssistantOpen"
      :title="t('admin.upstreamConfigs.tokenAssistant.title')"
      width="wide"
      @close="closeTokenAssistant"
    >
      <div class="max-h-[65vh] overflow-y-auto pr-1">
        <div class="space-y-4">
          <div class="flex gap-2 rounded-lg border border-amber-200 bg-amber-50 p-3 text-xs text-amber-800 dark:border-amber-900/50 dark:bg-amber-900/20 dark:text-amber-200">
            <Icon name="shield" size="sm" class="mt-0.5 flex-shrink-0" />
            <p>{{ t('admin.upstreamConfigs.tokenAssistant.securityHint') }}</p>
          </div>

          <div class="flex flex-wrap items-center justify-between gap-3">
            <div class="text-sm text-gray-700 dark:text-gray-300">
              {{ t('admin.upstreamConfigs.tokenAssistant.loginPageHint') }}
            </div>
            <button
              type="button"
              class="btn btn-secondary"
              data-test="open-upstream-login"
              @click="openUpstreamLogin"
            >
              <Icon name="externalLink" size="sm" class="mr-2" />
              {{ t('admin.upstreamConfigs.tokenAssistant.openLoginPage') }}
            </button>
          </div>

          <label class="space-y-1">
            <span class="input-label">{{ t('admin.upstreamConfigs.tokenAssistant.pasteLabel') }}</span>
            <textarea
              v-model="tokenPaste"
              class="input min-h-[150px] font-mono text-xs"
              data-test="token-paste-input"
              spellcheck="false"
              :placeholder="t('admin.upstreamConfigs.tokenAssistant.pastePlaceholder')"
              @input="syncTokenSelections"
            ></textarea>
          </label>

          <div v-if="hasParsedTokenCandidates" class="grid gap-4 md:grid-cols-2">
            <label class="space-y-1">
              <span class="input-label">{{ t('admin.upstreamConfigs.tokenAssistant.accessCandidate') }}</span>
              <select
                v-model="selectedAccessTokenId"
                class="input"
                data-test="access-token-candidate"
              >
                <option value="">{{ t('admin.upstreamConfigs.tokenAssistant.doNotApply') }}</option>
                <option v-for="candidate in accessTokenCandidates" :key="candidate.id" :value="candidate.id">
                  {{ tokenCandidateLabel(candidate) }}
                </option>
              </select>
            </label>

            <label class="space-y-1">
              <span class="input-label">{{ t('admin.upstreamConfigs.tokenAssistant.refreshCandidate') }}</span>
              <select
                v-model="selectedRefreshTokenId"
                class="input"
                data-test="refresh-token-candidate"
              >
                <option value="">{{ t('admin.upstreamConfigs.tokenAssistant.doNotApply') }}</option>
                <option v-for="candidate in parsedTokenResult.refreshCandidates" :key="candidate.id" :value="candidate.id">
                  {{ tokenCandidateLabel(candidate) }}
                </option>
              </select>
            </label>
          </div>

          <div v-if="selectedAccessTokenMeta" class="flex gap-2 rounded-lg border border-gray-200 bg-gray-50 p-3 text-xs text-gray-700 dark:border-dark-700 dark:bg-dark-800 dark:text-dark-200">
            <Icon
              :name="selectedAccessTokenMeta.expired ? 'exclamationTriangle' : 'infoCircle'"
              size="sm"
              :class="selectedAccessTokenMeta.expired ? 'mt-0.5 flex-shrink-0 text-amber-500' : 'mt-0.5 flex-shrink-0 text-blue-500'"
            />
            <p>{{ tokenExpiryDescription(selectedAccessTokenMeta) }}</p>
          </div>

          <div v-else-if="tokenPaste.trim() && !hasParsedTokenCandidates" class="flex gap-2 rounded-lg border border-red-200 bg-red-50 p-3 text-xs text-red-700 dark:border-red-900/50 dark:bg-red-900/20 dark:text-red-200">
            <Icon name="exclamationCircle" size="sm" class="mt-0.5 flex-shrink-0" />
            <p>{{ t('admin.upstreamConfigs.tokenAssistant.noTokenFound') }}</p>
          </div>
        </div>
      </div>

      <template #footer>
        <div class="flex justify-end gap-2">
          <button type="button" class="btn btn-secondary" @click="closeTokenAssistant">
            {{ t('common.cancel') }}
          </button>
          <button
            type="button"
            class="btn btn-primary"
            data-test="apply-token-candidates"
            :disabled="!canApplyTokenCandidates"
            @click="applyTokenCandidates"
          >
            {{ t('admin.upstreamConfigs.tokenAssistant.apply') }}
          </button>
        </div>
      </template>
    </BaseDialog>

    <ConfirmDialog
      :show="deleteDialogOpen"
      :title="t('admin.upstreamConfigs.delete.title')"
      :message="t('admin.upstreamConfigs.delete.message', { name: deletingConfig?.name || '' })"
      :confirm-text="t('common.delete')"
      :cancel-text="t('common.cancel')"
      :danger="true"
      @confirm="confirmDelete"
      @cancel="cancelDelete"
    />
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import TablePageLayout from '@/components/layout/TablePageLayout.vue'
import DataTable from '@/components/common/DataTable.vue'
import Pagination from '@/components/common/Pagination.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import Select, { type SelectOption } from '@/components/common/Select.vue'
import ProxySelector from '@/components/common/ProxySelector.vue'
import Icon from '@/components/icons/Icon.vue'
import type { Column } from '@/components/common/types'
import { getPersistedPageSize } from '@/composables/usePersistedPageSize'
import { adminAPI } from '@/api/admin'
import upstreamAPI, { type UpstreamAuthMode, type UpstreamConfig, type UpstreamProvider } from '@/api/admin/upstreamConfigs'
import { useAppStore } from '@/stores/app'
import type { Proxy } from '@/types'
import {
  parseUpstreamTokenPaste,
  type ParsedTokenCandidate
} from '@/utils/upstreamTokenParser'

type RowAction = 'test' | 'sync'

const { t } = useI18n()
const appStore = useAppStore()

const configs = ref<UpstreamConfig[]>([])
const loading = ref(false)
const saving = ref(false)
const syncingAll = ref(false)
const dialogOpen = ref(false)
const editing = ref<UpstreamConfig | null>(null)
const deletingConfig = ref<UpstreamConfig | null>(null)
const deleteDialogOpen = ref(false)
const tokenAssistantOpen = ref(false)
const busyAction = ref<{ id: number; action: RowAction } | null>(null)
const search = ref('')
const provider = ref<string>('')
const proxies = ref<Proxy[]>([])
const loadingProxies = ref(false)
const tokenPaste = ref('')
const selectedAccessTokenId = ref('')
const selectedRefreshTokenId = ref('')

const pagination = reactive({
  page: 1,
  page_size: getPersistedPageSize(),
  total: 0
})

const form = reactive({
  name: '',
  provider: 'sub2api' as UpstreamProvider,
  base_url: '',
  auth_mode: 'user_login' as UpstreamAuthMode,
  proxy_id: null as number | null,
  email: '',
  password: '',
  access_token: '',
  refresh_token: ''
})

let searchTimeout: ReturnType<typeof setTimeout> | null = null

const columns = computed<Column[]>(() => [
  { key: 'name', label: t('admin.upstreamConfigs.columns.name') },
  { key: 'provider', label: t('admin.upstreamConfigs.columns.provider') },
  { key: 'base_url', label: t('admin.upstreamConfigs.columns.baseUrl') },
  { key: 'auth_mode', label: t('admin.upstreamConfigs.columns.authMode') },
  { key: 'credentials', label: t('admin.upstreamConfigs.columns.credentials') },
  { key: 'last_success_at', label: t('admin.upstreamConfigs.columns.lastSync') },
  { key: 'actions', label: t('admin.upstreamConfigs.columns.actions') }
])

const providerFilterOptions = computed<SelectOption[]>(() => [
  { value: '', label: t('admin.upstreamConfigs.filters.allProviders') },
  { value: 'sub2api', label: providerLabel('sub2api') },
  { value: 'newapi', label: providerLabel('newapi') },
  { value: 'other', label: providerLabel('other') }
])

const providerEditOptions = computed<SelectOption[]>(() => [
  { value: 'sub2api', label: providerLabel('sub2api') },
  { value: 'newapi', label: providerLabel('newapi') },
  { value: 'other', label: providerLabel('other') }
])

const authModeOptions = computed<SelectOption[]>(() => [
  { value: 'user_login', label: t('admin.upstreamConfigs.authModes.userLogin') },
  { value: 'manual_jwt', label: t('admin.upstreamConfigs.authModes.manualJwt') }
])

const parsedTokenResult = computed(() => parseUpstreamTokenPaste(tokenPaste.value))

const accessTokenCandidates = computed(() => [
  ...parsedTokenResult.value.accessCandidates,
  ...parsedTokenResult.value.unknownCandidates
])

const hasParsedTokenCandidates = computed(() =>
  accessTokenCandidates.value.length > 0 || parsedTokenResult.value.refreshCandidates.length > 0
)

const selectedAccessTokenMeta = computed(() =>
  accessTokenCandidates.value.find((candidate) => candidate.id === selectedAccessTokenId.value) || null
)

const canApplyTokenCandidates = computed(() =>
  Boolean(selectedAccessTokenId.value || selectedRefreshTokenId.value)
)

onMounted(() => {
  loadConfigs()
  loadProxies()
})

onUnmounted(() => {
  if (searchTimeout) clearTimeout(searchTimeout)
})

async function loadConfigs() {
  loading.value = true
  try {
    const res = await upstreamAPI.list(pagination.page, pagination.page_size, {
      provider: provider.value,
      search: search.value.trim()
    })
    configs.value = res.items || []
    pagination.total = res.total || 0
    pagination.page = res.page || pagination.page
    pagination.page_size = res.page_size || pagination.page_size
  } catch (error: any) {
    appStore.showError(apiErrorMessage(error, t('admin.upstreamConfigs.messages.loadFailed')))
  } finally {
    loading.value = false
  }
}

async function loadProxies() {
  loadingProxies.value = true
  try {
    proxies.value = await adminAPI.proxies.getAllWithCount()
  } catch (error: any) {
    proxies.value = []
    appStore.showError(apiErrorMessage(error, t('admin.upstreamConfigs.messages.loadProxiesFailed')))
  } finally {
    loadingProxies.value = false
  }
}

function debouncedReload() {
  if (searchTimeout) clearTimeout(searchTimeout)
  searchTimeout = setTimeout(() => reloadFromFirstPage(), 300)
}

function reloadFromFirstPage() {
  pagination.page = 1
  loadConfigs()
}

function handlePageChange(page: number) {
  pagination.page = page
  loadConfigs()
}

function handlePageSizeChange(pageSize: number) {
  pagination.page_size = pageSize
  pagination.page = 1
  loadConfigs()
}

function resetForm() {
  Object.assign(form, {
    name: '',
    provider: 'sub2api',
    base_url: '',
    auth_mode: 'user_login',
    proxy_id: null,
    email: '',
    password: '',
    access_token: '',
    refresh_token: ''
  })
}

function openCreate() {
  editing.value = null
  resetForm()
  dialogOpen.value = true
}

function openEdit(item: UpstreamConfig) {
  editing.value = item
  Object.assign(form, {
    name: item.name,
    provider: item.provider,
    base_url: item.base_url,
    auth_mode: item.auth_mode,
    proxy_id: item.proxy_id ?? null,
    email: '',
    password: '',
    access_token: '',
    refresh_token: ''
  })
  dialogOpen.value = true
}

function handleDialogClose() {
  if (saving.value) return
  dialogOpen.value = false
}

function openTokenAssistant() {
  tokenAssistantOpen.value = true
  tokenPaste.value = ''
  selectedAccessTokenId.value = ''
  selectedRefreshTokenId.value = ''
}

function closeTokenAssistant() {
  tokenAssistantOpen.value = false
  tokenPaste.value = ''
  selectedAccessTokenId.value = ''
  selectedRefreshTokenId.value = ''
}

function openUpstreamLogin() {
  const url = buildUpstreamLoginURL()
  if (!url) {
    appStore.showError(t('admin.upstreamConfigs.tokenAssistant.invalidBaseUrl'))
    return
  }
  window.open(url, '_blank', 'noopener,noreferrer')
}

function buildUpstreamLoginURL(): string | null {
  try {
    const url = new URL(form.base_url.trim())
    if (!['http:', 'https:'].includes(url.protocol)) return null
    url.pathname = '/login'
    url.search = ''
    url.hash = ''
    return url.toString()
  } catch {
    return null
  }
}

function syncTokenSelections() {
  selectedAccessTokenId.value = syncCandidateSelection(
    selectedAccessTokenId.value,
    accessTokenCandidates.value
  )
  selectedRefreshTokenId.value = syncCandidateSelection(
    selectedRefreshTokenId.value,
    parsedTokenResult.value.refreshCandidates
  )
}

function syncCandidateSelection(current: string, candidates: ParsedTokenCandidate[]): string {
  if (current && candidates.some((candidate) => candidate.id === current)) return current
  return candidates.length === 1 ? candidates[0].id : ''
}

function applyTokenCandidates() {
  const access = accessTokenCandidates.value.find((candidate) => candidate.id === selectedAccessTokenId.value)
  const refresh = parsedTokenResult.value.refreshCandidates.find((candidate) => candidate.id === selectedRefreshTokenId.value)
  if (!access && !refresh) {
    appStore.showError(t('admin.upstreamConfigs.tokenAssistant.noSelection'))
    return
  }
  if (access) form.access_token = access.value
  if (refresh) form.refresh_token = refresh.value
  appStore.showSuccess(t('admin.upstreamConfigs.tokenAssistant.applied'))
  closeTokenAssistant()
}

async function saveConfig() {
  if (saving.value) return
  saving.value = true
  try {
    const credentials: Record<string, string> = {}
    if (form.provider === 'sub2api' && form.auth_mode === 'user_login') {
      if (form.email) credentials.sub2api_login_email = form.email
      if (form.password) credentials.sub2api_login_password = form.password
    }
    if (form.provider === 'sub2api' && form.auth_mode === 'manual_jwt') {
      if (form.access_token) credentials.sub2api_access_token = form.access_token
      if (form.refresh_token) credentials.sub2api_refresh_token = form.refresh_token
    }

    const payload = {
      name: form.name,
      provider: form.provider,
      base_url: form.base_url,
      auth_mode: form.provider === 'sub2api' ? form.auth_mode : 'user_login',
      proxy_id: form.proxy_id,
      credentials
    }

    if (editing.value) {
      await upstreamAPI.update(editing.value.id, payload)
      appStore.showSuccess(t('admin.upstreamConfigs.messages.updated'))
    } else {
      await upstreamAPI.create(payload)
      appStore.showSuccess(t('admin.upstreamConfigs.messages.created'))
    }
    dialogOpen.value = false
    await loadConfigs()
  } catch (error: any) {
    appStore.showError(apiErrorMessage(error, t('admin.upstreamConfigs.messages.saveFailed')))
  } finally {
    saving.value = false
  }
}

async function handleTest(item: UpstreamConfig) {
  busyAction.value = { id: item.id, action: 'test' }
  try {
    await upstreamAPI.test(item.id)
    appStore.showSuccess(t('admin.upstreamConfigs.messages.testSuccess'))
    await loadConfigs()
  } catch (error: any) {
    appStore.showError(apiErrorMessage(error, t('admin.upstreamConfigs.messages.testFailed')))
  } finally {
    busyAction.value = null
  }
}

async function handleSync(item: UpstreamConfig) {
  busyAction.value = { id: item.id, action: 'sync' }
  try {
    const res = await upstreamAPI.syncKeys(item.id)
    const count = res.keys?.length || 0
    appStore.showSuccess(t('admin.upstreamConfigs.messages.syncSuccess', { count }))
    await loadConfigs()
  } catch (error: any) {
    appStore.showError(apiErrorMessage(error, t('admin.upstreamConfigs.messages.syncFailed')))
  } finally {
    busyAction.value = null
  }
}

async function handleSyncAll() {
  syncingAll.value = true
  try {
    const res = await upstreamAPI.syncAllKeys()
    const results = res.results || []
    const success = results.filter((item) => item.success).length
    const failed = results.length - success
    const keys = results.reduce((sum, item) => sum + (item.key_count || 0), 0)
    if (failed > 0) {
      appStore.showError(t('admin.upstreamConfigs.messages.syncAllPartial', { success, failed, keys }))
    } else {
      appStore.showSuccess(t('admin.upstreamConfigs.messages.syncAllSuccess', { success, keys }))
    }
    await loadConfigs()
  } catch (error: any) {
    appStore.showError(apiErrorMessage(error, t('admin.upstreamConfigs.messages.syncAllFailed')))
  } finally {
    syncingAll.value = false
  }
}

function askDelete(item: UpstreamConfig) {
  deletingConfig.value = item
  deleteDialogOpen.value = true
}

function cancelDelete() {
  deleteDialogOpen.value = false
  deletingConfig.value = null
}

async function confirmDelete() {
  if (!deletingConfig.value) return
  const item = deletingConfig.value
  deleteDialogOpen.value = false
  try {
    await upstreamAPI.remove(item.id)
    appStore.showSuccess(t('admin.upstreamConfigs.messages.deleted'))
    if (configs.value.length <= 1 && pagination.page > 1) {
      pagination.page -= 1
    }
    await loadConfigs()
  } catch (error: any) {
    appStore.showError(apiErrorMessage(error, t('admin.upstreamConfigs.messages.deleteFailed')))
  } finally {
    deletingConfig.value = null
  }
}

function isActionBusy(id: number, action: RowAction) {
  return busyAction.value?.id === id && busyAction.value.action === action
}

function providerLabel(value: UpstreamProvider | string): string {
  switch (value) {
    case 'sub2api':
      return t('admin.upstreamConfigs.providers.sub2api')
    case 'newapi':
      return t('admin.upstreamConfigs.providers.newapi')
    default:
      return t('admin.upstreamConfigs.providers.other')
  }
}

function authModeLabel(value: UpstreamAuthMode | string): string {
  return value === 'manual_jwt'
    ? t('admin.upstreamConfigs.authModes.manualJwtShort')
    : t('admin.upstreamConfigs.authModes.userLoginShort')
}

function providerBadgeClass(value: UpstreamProvider | string): string {
  switch (value) {
    case 'sub2api':
      return 'bg-emerald-50 text-emerald-700 ring-1 ring-emerald-200 dark:bg-emerald-900/20 dark:text-emerald-300 dark:ring-emerald-800'
    case 'newapi':
      return 'bg-blue-50 text-blue-700 ring-1 ring-blue-200 dark:bg-blue-900/20 dark:text-blue-300 dark:ring-blue-800'
    default:
      return 'bg-gray-100 text-gray-700 ring-1 ring-gray-200 dark:bg-dark-700 dark:text-dark-200 dark:ring-dark-600'
  }
}

function credentialLines(item: UpstreamConfig) {
  const status = item.credentials_status || {}
  if (item.auth_mode === 'manual_jwt') {
    return [
      { label: t('admin.upstreamConfigs.credentialStatus.accessToken', { status: statusLabel(!!status.has_access_token) }), ok: !!status.has_access_token },
      { label: t('admin.upstreamConfigs.credentialStatus.refreshToken', { status: statusLabel(!!status.has_refresh_token) }), ok: !!status.has_refresh_token }
    ]
  }
  return [
    { label: t('admin.upstreamConfigs.credentialStatus.email', { status: statusLabel(!!status.has_login_email) }), ok: !!status.has_login_email },
    { label: t('admin.upstreamConfigs.credentialStatus.password', { status: statusLabel(!!status.has_login_password) }), ok: !!status.has_login_password }
  ]
}

function statusLabel(ok: boolean) {
  return ok ? t('admin.upstreamConfigs.credentialStatus.configured') : t('admin.upstreamConfigs.credentialStatus.missing')
}

function tokenCandidateLabel(candidate: ParsedTokenCandidate): string {
  return t('admin.upstreamConfigs.tokenAssistant.candidateLabel', {
    source: tokenSourceLabel(candidate),
    suffix: tokenSuffix(candidate.value)
  })
}

function tokenSourceLabel(candidate: ParsedTokenCandidate): string {
  if (candidate.source === 'bearer') return t('admin.upstreamConfigs.tokenAssistant.sources.bearer')
  if (candidate.source === 'jwt') return t('admin.upstreamConfigs.tokenAssistant.sources.jwt')
  return candidate.label
}

function tokenSuffix(value: string): string {
  return `****${value.slice(-8)}`
}

function tokenExpiryDescription(candidate: ParsedTokenCandidate): string {
  if (!candidate.expiresAt) return t('admin.upstreamConfigs.tokenAssistant.jwtUnverifiedNoExp')
  const time = new Date(candidate.expiresAt).toLocaleString()
  return candidate.expired
    ? t('admin.upstreamConfigs.tokenAssistant.jwtExpired', { time })
    : t('admin.upstreamConfigs.tokenAssistant.jwtExpiresAt', { time })
}

function formatTime(value?: string | null): string {
  if (!value) return '-'
  return new Date(value).toLocaleString()
}

function apiErrorMessage(error: any, fallback: string): string {
  return error?.response?.data?.detail || error?.response?.data?.message || error?.message || fallback
}
</script>

<style scoped>
.table-action-button {
  @apply inline-flex h-8 w-8 items-center justify-center rounded-lg text-gray-500 transition-colors;
  @apply hover:bg-gray-100 disabled:cursor-not-allowed disabled:opacity-50;
  @apply dark:text-dark-300 dark:hover:bg-dark-700;
}
</style>
