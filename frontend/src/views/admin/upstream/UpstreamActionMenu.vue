<template>
  <ActionMenu :show="show" :anchor-el="anchorEl" :width="width" @close="emit('close')">
    <template #default="{ close }">
      <template v-if="config">
        <button role="menuitem" class="menu-item" @click="emitAndClose('test', close)">
          <Icon name="play" size="sm" class="text-emerald-500" />
          {{ t('admin.upstreamConfigs.actions.test') }}
        </button>
        <button role="menuitem" class="menu-item" @click="emitAndClose('rateTrend', close)">
          <Icon name="chart" size="sm" class="text-indigo-500" />
          {{ t('admin.upstreamConfigs.actions.rateTrend') }}
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
  rateTrend: [config: UpstreamConfig]
  delete: [config: UpstreamConfig]
}>()

const { t } = useI18n()
function emitAndClose(event: 'test' | 'rateTrend' | 'delete', close: () => void) {
  if (!props.config) return
  if (event === 'test') emit('test', props.config)
  else if (event === 'rateTrend') emit('rateTrend', props.config)
  else emit('delete', props.config)
  close()
}
</script>
