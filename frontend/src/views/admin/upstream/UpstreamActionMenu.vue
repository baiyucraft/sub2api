<template>
  <ActionMenu :show="show" :anchor-el="anchorEl" :width="width" @close="emit('close')">
    <template #default="{ close }">
      <template v-if="config">
        <button role="menuitem" class="menu-item" @click="emitAndClose('test', close)">
          <Icon name="play" size="sm" class="text-emerald-500" />
          {{ t('admin.upstreamConfigs.actions.test') }}
        </button>
        <button role="menuitem" class="menu-item" @click="emitAndClose('keyPlatforms', close)">
          <Icon name="key" size="sm" class="text-violet-500" />
          {{ t('admin.upstreamConfigs.actions.keyPlatforms') }}
        </button>
        <button
          v-if="supportsDashboard"
          role="menuitem"
          class="menu-item"
          @click="emitAndClose('dashboard', close)"
        >
          <Icon name="externalLink" size="sm" class="text-sky-500" />
          {{ t('admin.upstreamConfigs.actions.openDashboard') }}
        </button>
        <div data-menu-divider></div>
        <button role="menuitem" class="text-red-600 dark:text-red-400" @click="emitAndClose('delete', close)">
          <Icon name="trash" size="sm" />
          {{ t('common.delete') }}
        </button>
      </template>
    </template>
  </ActionMenu>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import ActionMenu from '@/components/common/ActionMenu.vue'
import Icon from '@/components/icons/Icon.vue'
import type { UpstreamConfig } from '@/api/admin/upstreamConfigs'

const props = withDefaults(defineProps<{
  show: boolean
  anchorEl: HTMLElement | null
  config: UpstreamConfig | null
  width?: 'normal' | 'wide'
}>(), {
  width: 'normal'
})

const emit = defineEmits<{
  close: []
  test: [config: UpstreamConfig]
  keyPlatforms: [config: UpstreamConfig]
  dashboard: [config: UpstreamConfig]
  delete: [config: UpstreamConfig]
}>()

const { t } = useI18n()
const supportsDashboard = computed(() => props.config?.provider === 'sub2api' || props.config?.provider === 'newapi')

function emitAndClose(event: 'test' | 'keyPlatforms' | 'dashboard' | 'delete', close: () => void) {
  if (!props.config) return
  if (event === 'test') emit('test', props.config)
  else if (event === 'keyPlatforms') emit('keyPlatforms', props.config)
  else if (event === 'dashboard') emit('dashboard', props.config)
  else emit('delete', props.config)
  close()
}
</script>
