<template>
  <div class="space-y-2" data-testid="proxy-binding-selector">
    <div class="inline-flex w-full rounded-lg bg-gray-100 p-1 dark:bg-dark-800" role="group">
      <button
        v-for="option in modeOptions"
        :key="option.value"
        type="button"
        class="flex-1 rounded-md px-3 py-1.5 text-xs font-medium transition-colors"
        :class="mode === option.value
          ? 'bg-white text-gray-900 shadow-sm dark:bg-dark-600 dark:text-white'
          : 'text-gray-500 hover:text-gray-800 dark:text-dark-300 dark:hover:text-white'"
        :disabled="disabled"
        :data-testid="`proxy-binding-mode-${option.value}`"
        @click="setMode(option.value)"
      >
        {{ option.label }}
      </button>
    </div>

    <ProxySelector
      v-if="mode === 'proxy'"
      :model-value="proxyId"
      :proxies="proxies"
      :disabled="disabled"
      @update:model-value="selectProxy"
    />

    <div v-else-if="mode === 'group'" class="space-y-1.5">
      <Select
        :model-value="proxyIpGroupId"
        :options="groupOptions"
        :placeholder="t('admin.accounts.proxyBinding.selectGroup')"
        :disabled="disabled || groupsLoading"
        :loading="groupsLoading"
        searchable
        clearable
        data-testid="proxy-ip-group-select"
        @update:model-value="selectGroup"
      />
      <p class="text-xs text-gray-500 dark:text-dark-400">
        {{ groupsError ? t('admin.accounts.proxyBinding.loadFailed') : t('admin.accounts.proxyBinding.groupHint') }}
      </p>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import ProxySelector from '@/components/common/ProxySelector.vue'
import Select, { type SelectOption } from '@/components/common/Select.vue'
import type { Proxy, ProxyIPGroup } from '@/types'

type BindingMode = 'none' | 'proxy' | 'group'

const props = withDefaults(defineProps<{
  proxyId: number | null
  proxyIpGroupId: number | null
  proxies: Proxy[]
  disabled?: boolean
}>(), {
  disabled: false
})

const emit = defineEmits<{
  'update:proxyId': [value: number | null]
  'update:proxyIpGroupId': [value: number | null]
}>()

const { t } = useI18n()
const groups = ref<ProxyIPGroup[]>([])
const groupsLoading = ref(false)
const groupsError = ref(false)

const resolveMode = (): BindingMode => {
  if (Number(props.proxyIpGroupId) > 0) return 'group'
  if (Number(props.proxyId) > 0) return 'proxy'
  return 'none'
}

const mode = ref<BindingMode>(resolveMode())
const modeOptions = computed(() => [
  { value: 'none' as const, label: t('admin.accounts.proxyBinding.none') },
  { value: 'proxy' as const, label: t('admin.accounts.proxyBinding.single') },
  { value: 'group' as const, label: t('admin.accounts.proxyBinding.group') }
])
const groupOptions = computed<SelectOption[]>(() => groups.value.map(group => ({
  value: group.id,
  label: t('admin.accounts.proxyBinding.groupOption', {
    name: group.name,
    count: group.member_count ?? group.proxy_ids?.length ?? group.members?.length ?? 0,
    limit: group.per_ip_concurrency
  })
})))

watch(
  () => [props.proxyId, props.proxyIpGroupId] as const,
  () => {
    const next = resolveMode()
    if (next !== 'none' || mode.value === 'none') mode.value = next
  }
)

const loadGroups = async () => {
  groupsLoading.value = true
  groupsError.value = false
  try {
    groups.value = await adminAPI.proxyIpGroups.list()
  } catch {
    groups.value = []
    groupsError.value = true
  } finally {
    groupsLoading.value = false
  }
}

const setMode = (next: BindingMode) => {
  if (props.disabled) return
  mode.value = next
  if (next === 'none') {
    emit('update:proxyId', null)
    emit('update:proxyIpGroupId', null)
  } else if (next === 'proxy') {
    emit('update:proxyIpGroupId', null)
  } else {
    emit('update:proxyId', null)
  }
}

const selectProxy = (value: number | null) => {
  emit('update:proxyId', value)
  if (value != null) emit('update:proxyIpGroupId', null)
}

const selectGroup = (value: unknown) => {
  const id = Number(value)
  emit('update:proxyIpGroupId', Number.isInteger(id) && id > 0 ? id : null)
  if (Number.isInteger(id) && id > 0) emit('update:proxyId', null)
}

onMounted(loadGroups)
</script>
