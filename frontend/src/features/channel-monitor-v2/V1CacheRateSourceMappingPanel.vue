<template>
  <section class="card overflow-hidden !rounded-3xl !border-0 shadow-sm ring-1 ring-gray-900/5 dark:!bg-dark-800 dark:ring-dark-700">
    <div class="card-header flex flex-wrap items-center justify-between gap-3 !py-3">
      <div>
        <h2 class="text-sm font-semibold text-gray-900 dark:text-white">
          {{ t('channelMonitorV2.admin.v1CacheRateAliases.title') }}
        </h2>
        <p class="mt-0.5 text-xs text-gray-500 dark:text-dark-400">
          {{ t('channelMonitorV2.admin.v1CacheRateAliases.hint') }}
        </p>
      </div>
      <button
        type="button"
        class="btn btn-primary btn-sm"
        :disabled="loading || saving || groups.length < 2 || !dirty"
        @click="save"
      >
        {{ t('channelMonitorV2.settings.save') }}
      </button>
    </div>

    <div v-if="loading" class="px-5 py-5 text-sm text-gray-400">
      {{ t('channelMonitorV2.settings.loading') }}
    </div>
    <div v-else class="space-y-3 px-5 py-4">
      <div class="flex flex-wrap items-center justify-between gap-2">
        <p class="text-xs text-gray-500 dark:text-dark-400">
          {{ t('channelMonitorV2.admin.v1CacheRateAliases.description') }}
        </p>
        <button
          type="button"
          class="btn btn-ghost btn-sm"
          :disabled="saving || groups.length < 2"
          @click="addCacheRateAlias"
        >
          {{ t('channelMonitorV2.admin.v1CacheRateAliases.add') }}
        </button>
      </div>

      <div v-if="cacheRateAliases.length" class="divide-y divide-gray-100 rounded-2xl border border-gray-100 dark:divide-dark-700 dark:border-dark-700">
        <div
          v-for="alias in cacheRateAliases"
          :key="alias.displayGroupID"
          class="grid grid-cols-1 items-center gap-2 px-4 py-3 sm:grid-cols-[minmax(0,1fr)_auto_minmax(0,1fr)_auto]"
        >
          <select class="input" :value="alias.displayGroupID" @change="updateCacheRateAliasDisplay(alias.displayGroupID, $event)">
            <option v-for="group in groups" :key="group.id" :value="group.id">
              {{ group.name }} (#{{ group.id }})
            </option>
          </select>
          <span class="text-center text-xs text-gray-400">
            {{ t('channelMonitorV2.admin.v1CacheRateAliases.uses') }}
          </span>
          <select class="input" :value="alias.sourceGroupID" @change="updateCacheRateAliasSource(alias.displayGroupID, $event)">
            <option
              v-for="group in groups"
              :key="group.id"
              :value="group.id"
              :disabled="group.id === alias.displayGroupID"
            >
              {{ group.name }} (#{{ group.id }})
            </option>
          </select>
          <button type="button" class="btn btn-ghost btn-sm text-red-600" @click="removeCacheRateAlias(alias.displayGroupID)">
            {{ t('common.delete') }}
          </button>
        </div>
      </div>
      <p v-else class="rounded-2xl border border-dashed border-gray-200 px-4 py-4 text-sm text-gray-400 dark:border-dark-600">
        {{ t('channelMonitorV2.admin.v1CacheRateAliases.empty') }}
      </p>

      <p v-if="groups.length < 2" class="text-xs text-gray-400">
        {{ t('channelMonitorV2.admin.v1CacheRateAliases.notEnoughGroups') }}
      </p>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { extractApiErrorMessage } from '@/utils/apiError'
import { adminAPI } from '@/api/admin'
import { getConfig, updateConfig, type MonitorConfig } from '@/api/channelMonitorV2'
import type { AdminGroup } from '@/types'

const { t } = useI18n()
const appStore = useAppStore()
const loading = ref(true)
const saving = ref(false)
const draft = ref<MonitorConfig | null>(null)
const original = ref('')
const groups = ref<AdminGroup[]>([])

const dirty = computed(() => (draft.value ? JSON.stringify(draft.value) !== original.value : false))
const cacheRateAliases = computed(() => {
  const aliases = draft.value?.v1_cache_rate_source_groups || {}
  return Object.entries(aliases)
    .map(([displayGroupID, sourceGroupID]) => ({ displayGroupID: Number(displayGroupID), sourceGroupID: Number(sourceGroupID) }))
    .filter((entry) => Number.isInteger(entry.displayGroupID) && Number.isInteger(entry.sourceGroupID))
    .sort((left, right) => left.displayGroupID - right.displayGroupID)
})

function ensureCacheRateAliases() {
  if (!draft.value) return null
  if (!draft.value.v1_cache_rate_source_groups) draft.value.v1_cache_rate_source_groups = {}
  return draft.value.v1_cache_rate_source_groups
}

function addCacheRateAlias() {
  const aliases = ensureCacheRateAliases()
  if (!aliases) return
  const available = groups.value.find((group) => !Object.prototype.hasOwnProperty.call(aliases, String(group.id)))
  const source = groups.value.find((group) => group.id !== available?.id)
  if (!available || !source) return
  aliases[String(available.id)] = source.id
}

function updateCacheRateAliasDisplay(previousDisplayGroupID: number, event: Event) {
  const aliases = ensureCacheRateAliases()
  if (!aliases) return
  const nextDisplayGroupID = Number((event.target as HTMLSelectElement).value)
  const sourceGroupID = aliases[String(previousDisplayGroupID)]
  delete aliases[String(previousDisplayGroupID)]
  if (nextDisplayGroupID > 0 && nextDisplayGroupID !== sourceGroupID) aliases[String(nextDisplayGroupID)] = sourceGroupID
}

function updateCacheRateAliasSource(displayGroupID: number, event: Event) {
  const aliases = ensureCacheRateAliases()
  if (!aliases) return
  const sourceGroupID = Number((event.target as HTMLSelectElement).value)
  if (sourceGroupID > 0 && sourceGroupID !== displayGroupID) aliases[String(displayGroupID)] = sourceGroupID
}

function removeCacheRateAlias(displayGroupID: number) {
  const aliases = ensureCacheRateAliases()
  if (aliases) delete aliases[String(displayGroupID)]
}

async function load() {
  loading.value = true
  try {
    const [config, groupRows] = await Promise.all([getConfig(), adminAPI.groups.getAllIncludingInactive()])
    draft.value = structuredClone(config)
    groups.value = groupRows
    original.value = JSON.stringify(config)
  } catch (error) {
    appStore.showError(extractApiErrorMessage(error, t('channelMonitorV2.settings.loadFailed')))
  } finally {
    loading.value = false
  }
}

async function save() {
  if (!draft.value || !dirty.value) return
  saving.value = true
  try {
    const saved = await updateConfig(draft.value)
    draft.value = structuredClone(saved)
    original.value = JSON.stringify(saved)
    appStore.showSuccess(t('channelMonitorV2.settings.saveSuccess'))
  } catch (error) {
    appStore.showError(extractApiErrorMessage(error, t('channelMonitorV2.settings.saveFailed')))
  } finally {
    saving.value = false
  }
}

onMounted(load)
</script>
