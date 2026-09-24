<template>
  <BaseDialog
    :show="show"
    :title="t('admin.proxies.ipGroups.title')"
    width="wide"
    close-on-click-outside
    @close="emit('close')"
  >
    <div class="grid min-h-[420px] gap-5 lg:grid-cols-[minmax(240px,0.8fr)_minmax(0,1.2fr)]">
      <section class="border-b border-gray-200 pb-5 dark:border-dark-600 lg:border-b-0 lg:border-r lg:pb-0 lg:pr-5">
        <div class="mb-3 flex items-center justify-between gap-3">
          <div>
            <h3 class="text-sm font-semibold text-gray-900 dark:text-white">{{ t('admin.proxies.ipGroups.listTitle') }}</h3>
            <p class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.proxies.ipGroups.listHint') }}</p>
          </div>
          <button type="button" class="btn btn-secondary px-2.5" @click="startCreate">
            <Icon name="plus" size="sm" />
            <span class="ml-1">{{ t('common.add') }}</span>
          </button>
        </div>

        <div v-if="loading" class="py-10 text-center text-sm text-gray-500">{{ t('common.loading') }}</div>
        <div v-else-if="groups.length === 0" class="rounded-lg border border-dashed border-gray-300 px-4 py-10 text-center text-sm text-gray-500 dark:border-dark-600">
          {{ t('admin.proxies.ipGroups.empty') }}
        </div>
        <div v-else class="space-y-2">
          <button
            v-for="group in groups"
            :key="group.id"
            type="button"
            class="flex w-full items-center justify-between gap-3 rounded-lg border px-3 py-2.5 text-left transition-colors"
            :class="editingId === group.id
              ? 'border-primary-400 bg-primary-50 dark:border-primary-600 dark:bg-primary-900/20'
              : 'border-gray-200 hover:border-gray-300 dark:border-dark-600 dark:hover:border-dark-500'"
            @click="startEdit(group)"
          >
            <span class="min-w-0">
              <span class="block truncate text-sm font-medium text-gray-900 dark:text-white">{{ group.name }}</span>
              <span class="block text-xs text-gray-500 dark:text-dark-400">
                {{ t('admin.proxies.ipGroups.summary', { count: group.member_count, limit: group.per_ip_concurrency }) }}
              </span>
            </span>
            <Icon name="chevronRight" size="sm" class="shrink-0 text-gray-400" />
          </button>
        </div>
      </section>

      <form class="space-y-4" data-testid="proxy-ip-group-form" @submit.prevent="saveGroup">
        <div>
          <h3 class="text-sm font-semibold text-gray-900 dark:text-white">
            {{ editingId ? t('admin.proxies.ipGroups.editTitle') : t('admin.proxies.ipGroups.createTitle') }}
          </h3>
          <p class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.proxies.ipGroups.formHint') }}</p>
        </div>

        <div>
          <label class="input-label">{{ t('admin.proxies.ipGroups.name') }}</label>
          <input v-model="form.name" class="input" type="text" maxlength="100" :placeholder="t('admin.proxies.ipGroups.namePlaceholder')" />
        </div>

        <div>
          <label class="input-label">{{ t('admin.proxies.ipGroups.perIpConcurrency') }}</label>
          <input v-model.number="form.per_ip_concurrency" class="input" type="number" min="1" max="1000" step="1" />
          <p class="input-hint">{{ t('admin.proxies.ipGroups.perIpConcurrencyHint') }}</p>
        </div>

        <div>
          <label class="input-label">{{ t('admin.proxies.ipGroups.members') }}</label>
          <Select
            v-model="form.proxy_ids"
            :options="proxyOptions"
            multiple
            searchable
            :placeholder="t('admin.proxies.ipGroups.membersPlaceholder')"
            data-testid="proxy-ip-group-members"
          />
          <p class="input-hint">{{ t('admin.proxies.ipGroups.membersHint', { count: form.proxy_ids.length }) }}</p>
        </div>

        <div class="flex flex-wrap items-center justify-between gap-3 border-t border-gray-200 pt-4 dark:border-dark-600">
          <button
            v-if="editingId"
            type="button"
            class="btn btn-danger"
            :disabled="saving"
            @click="deletingGroup = groups.find(group => group.id === editingId) || null"
          >
            <Icon name="trash" size="sm" class="mr-1.5" />
            {{ t('common.delete') }}
          </button>
          <span v-else />
          <button type="submit" class="btn btn-primary" :disabled="saving">
            {{ saving ? t('common.saving') : t('common.save') }}
          </button>
        </div>
      </form>
    </div>
  </BaseDialog>

  <ConfirmDialog
    :show="Boolean(deletingGroup)"
    :title="t('admin.proxies.ipGroups.deleteTitle')"
    :message="t('admin.proxies.ipGroups.deleteConfirm', { name: deletingGroup?.name || '' })"
    danger
    @confirm="deleteGroup"
    @cancel="deletingGroup = null"
  />
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import { useAppStore } from '@/stores/app'
import BaseDialog from '@/components/common/BaseDialog.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import Select, { type SelectOption } from '@/components/common/Select.vue'
import Icon from '@/components/icons/Icon.vue'
import type { Proxy, ProxyIPGroup } from '@/types'
import { filterRealProxies } from '@/utils/proxy'

const props = defineProps<{ show: boolean }>()
const emit = defineEmits<{
  close: []
  updated: []
}>()

const { t } = useI18n()
const appStore = useAppStore()
const groups = ref<ProxyIPGroup[]>([])
const proxies = ref<Proxy[]>([])
const loading = ref(false)
const saving = ref(false)
const editingId = ref<number | null>(null)
const deletingGroup = ref<ProxyIPGroup | null>(null)
const form = reactive({
  name: '',
  per_ip_concurrency: 10,
  proxy_ids: [] as number[]
})

const proxyOptions = computed<SelectOption[]>(() => {
  const byId = new Map<number, Proxy>()
  for (const proxy of proxies.value) byId.set(proxy.id, proxy)
  const editing = groups.value.find(group => group.id === editingId.value)
  for (const proxy of editing?.members || []) byId.set(proxy.id, proxy)
  return Array.from(byId.values()).map(proxy => ({
    value: proxy.id,
    label: `${proxy.name} (${proxy.host}:${proxy.port})`
  }))
})

const resetForm = () => {
  editingId.value = null
  form.name = ''
  form.per_ip_concurrency = 10
  form.proxy_ids = []
}

const loadData = async () => {
  loading.value = true
  try {
    const [groupRows, proxyRows] = await Promise.all([
      adminAPI.proxyIpGroups.list(),
      adminAPI.proxies.getAll()
    ])
    groups.value = groupRows
    proxies.value = filterRealProxies(proxyRows)
    if (editingId.value) {
      const refreshed = groupRows.find(group => group.id === editingId.value)
      if (refreshed) await startEdit(refreshed)
      else resetForm()
    }
  } catch (error: any) {
    appStore.showError(error?.response?.data?.message || t('admin.proxies.ipGroups.loadFailed'))
  } finally {
    loading.value = false
  }
}

const startCreate = () => resetForm()

const startEdit = async (group: ProxyIPGroup) => {
  editingId.value = group.id
  form.name = group.name
  form.per_ip_concurrency = Number.isInteger(group.per_ip_concurrency) && group.per_ip_concurrency >= 1 && group.per_ip_concurrency <= 1000
    ? group.per_ip_concurrency
    : 10
  form.proxy_ids = group.proxy_ids?.slice() || group.members?.map(proxy => proxy.id) || []
  if (!group.proxy_ids && !group.members) {
    try {
      const detail = await adminAPI.proxyIpGroups.getById(group.id)
      form.proxy_ids = detail.proxy_ids?.slice() || detail.members?.map(proxy => proxy.id) || []
    } catch {
      appStore.showError(t('admin.proxies.ipGroups.loadDetailFailed'))
    }
  }
}

const saveGroup = async () => {
  const name = form.name.trim()
  if (!name) {
    appStore.showError(t('admin.proxies.ipGroups.nameRequired'))
    return
  }
  if (!Number.isInteger(form.per_ip_concurrency) || form.per_ip_concurrency < 1 || form.per_ip_concurrency > 1000) {
    appStore.showError(t('admin.proxies.ipGroups.concurrencyInvalid'))
    return
  }
  saving.value = true
  try {
    const payload = {
      name,
      per_ip_concurrency: form.per_ip_concurrency,
      proxy_ids: Array.from(new Set(form.proxy_ids))
    }
    if (editingId.value) await adminAPI.proxyIpGroups.update(editingId.value, payload)
    else await adminAPI.proxyIpGroups.create(payload)
    appStore.showSuccess(t('admin.proxies.ipGroups.saved'))
    emit('updated')
    resetForm()
    await loadData()
  } catch (error: any) {
    appStore.showError(error?.response?.data?.message || t('admin.proxies.ipGroups.saveFailed'))
  } finally {
    saving.value = false
  }
}

const deleteGroup = async () => {
  if (!deletingGroup.value) return
  saving.value = true
  try {
    await adminAPI.proxyIpGroups.delete(deletingGroup.value.id)
    appStore.showSuccess(t('admin.proxies.ipGroups.deleted'))
    deletingGroup.value = null
    resetForm()
    emit('updated')
    await loadData()
  } catch (error: any) {
    appStore.showError(error?.response?.data?.message || t('admin.proxies.ipGroups.deleteFailed'))
  } finally {
    saving.value = false
  }
}

watch(() => props.show, open => {
  if (!open) return
  resetForm()
  void loadData()
}, { immediate: true })
</script>
