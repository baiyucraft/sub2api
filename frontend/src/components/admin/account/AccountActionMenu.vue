<template>
  <Teleport to="body">
    <div v-if="show && anchorRect">
      <div class="fixed inset-0 z-[9998]" @click="emit('close')"></div>
      <div
        ref="menuRef"
        class="action-menu-content fixed z-[9999] w-56 overflow-y-auto overscroll-contain rounded-xl bg-white py-1 shadow-lg ring-1 ring-black/5 dark:bg-dark-800"
        :style="menuStyle"
        @click.stop
      >
        <div v-if="account" role="menu" aria-label="Account actions">
            <button v-if="canUseAction('test')" class="action-menu-item" role="menuitem" @click="$emit('test', account); emit('close')">
              <Icon name="play" size="sm" class="shrink-0 text-green-500" :stroke-width="2" />
              {{ t('admin.accounts.testConnection') }}
            </button>
            <button v-if="canUseAction('stats')" class="action-menu-item" role="menuitem" @click="$emit('stats', account); emit('close')">
              <Icon name="chart" size="sm" class="shrink-0 text-indigo-500" />
              {{ t('admin.accounts.viewStats') }}
            </button>
            <button v-if="canUseAction('schedule')" class="action-menu-item" role="menuitem" @click="$emit('schedule', account); emit('close')">
              <Icon name="clock" size="sm" class="shrink-0 text-orange-500" />
              {{ t('admin.scheduledTests.schedule') }}
            </button>
            <button v-if="canShowRateTrend" class="action-menu-item" role="menuitem" @click="$emit('rate-trend', account); emit('close')">
              <Icon name="trendingUp" size="sm" class="shrink-0 text-cyan-500" />
              {{ t('admin.upstreamConfigs.actions.rateTrend') }}
            </button>
            <button v-if="canDuplicate && canUseAction('duplicate')" class="action-menu-item" role="menuitem" @click="$emit('duplicate', account); emit('close')">
              <Icon name="copy" size="sm" class="shrink-0 text-sky-500" />
              {{ t('admin.accounts.duplicateAccount') }}
            </button>
            <template v-if="(account.type === 'oauth' || account.type === 'setup-token') && !isShadow">
              <button v-if="canUseAction('reauth')" class="action-menu-item text-blue-600" role="menuitem" @click="$emit('reauth', account); emit('close')">
                <Icon name="link" size="sm" class="shrink-0" />
                {{ t('admin.accounts.reAuthorize') }}
              </button>
              <button v-if="canUseAction('refresh_token')" class="action-menu-item text-purple-600" role="menuitem" @click="$emit('refresh-token', account); emit('close')">
                <Icon name="refresh" size="sm" class="shrink-0" />
                {{ t('admin.accounts.refreshToken') }}
              </button>
            </template>
            <button v-if="isOpenAIOAuthParent && canUseAction('create_spark_shadow')" class="action-menu-item text-amber-600" role="menuitem" @click="$emit('create-spark-shadow', account); emit('close')">
              <Icon name="sparkles" size="sm" class="shrink-0" />
              {{ t('admin.accounts.createSparkShadow') }}
            </button>
            <button v-if="supportsPrivacy && canUseAction('set_privacy')" class="action-menu-item text-emerald-600" role="menuitem" @click="$emit('set-privacy', account); emit('close')">
              <Icon name="shield" size="sm" class="shrink-0" />
              {{ t('admin.accounts.setPrivacy') }}
            </button>
            <div v-if="hasRecoverableState" data-menu-divider></div>
            <button v-if="hasRecoverableState && canUseAction('recover_state')" class="action-menu-item text-emerald-600" role="menuitem" @click="$emit('recover-state', account); emit('close')">
              <Icon name="sync" size="sm" class="shrink-0" />
              {{ t('admin.accounts.recoverState') }}
            </button>
            <button v-if="hasQuotaLimit && canUseAction('reset_quota')" class="action-menu-item text-teal-600" role="menuitem" @click="$emit('reset-quota', account); emit('close')">
              <Icon name="refresh" size="sm" class="shrink-0" />
              {{ t('admin.accounts.resetQuota') }}
            </button>
        </div>
      </div>
    </div>
  </Teleport>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useResizeObserver, useWindowSize } from '@vueuse/core'
import { useI18n } from 'vue-i18n'
import { Icon } from '@/components/icons'
import type { Account } from '@/types'

const props = defineProps<{ show: boolean; account: Account | null; anchorRect: DOMRect | null }>()
const emit = defineEmits(['close', 'test', 'stats', 'schedule', 'rate-trend', 'duplicate', 'reauth', 'refresh-token', 'recover-state', 'reset-quota', 'set-privacy', 'create-spark-shadow'])
const { t } = useI18n()
const menuRef = ref<HTMLElement | null>(null)
const { width: viewportWidth, height: viewportHeight } = useWindowSize()
const viewportPadding = 8
const menuPosition = ref({ top: viewportPadding, left: viewportPadding })
const menuStyle = computed(() => ({
  top: `${menuPosition.value.top}px`,
  left: `${menuPosition.value.left}px`,
  maxWidth: `${Math.max(0, viewportWidth.value - viewportPadding * 2)}px`,
  maxHeight: `${Math.max(0, viewportHeight.value - viewportPadding * 2)}px`
}))

const updatePosition = () => {
  if (!menuRef.value || !props.anchorRect) return

  const { width, height } = menuRef.value.getBoundingClientRect()
  const anchor = props.anchorRect
  const gap = 4
  const maxTop = viewportHeight.value - height - viewportPadding
  const top = anchor.bottom + gap <= maxTop
    ? anchor.bottom + gap
    : anchor.top - height - gap
  const left = viewportWidth.value < 768
    ? anchor.left + anchor.width / 2 - width / 2
    : anchor.right - width

  menuPosition.value.top = Math.max(viewportPadding, Math.min(top, maxTop))
  menuPosition.value.left = Math.max(viewportPadding, Math.min(left, viewportWidth.value - width - viewportPadding))
}

const handleWindowKeydown = (event: KeyboardEvent) => {
  if (props.show && event.key === 'Escape') {
    event.preventDefault()
    emit('close')
  }
}

// Measure after rendering; menu items and translated labels can change its size.
watch([menuRef, () => props.anchorRect, viewportWidth, viewportHeight], updatePosition, { flush: 'post' })
useResizeObserver(menuRef, updatePosition)
onMounted(() => window.addEventListener('keydown', handleWindowKeydown))
onUnmounted(() => window.removeEventListener('keydown', handleWindowKeydown))

const canDuplicate = computed(() => {
  if (
    !props.account ||
    props.account.parent_account_id != null ||
    props.account.upstream_config_id != null ||
    props.account.upstream_key_id != null
  ) return false
  return ['apikey', 'upstream', 'bedrock', 'service_account'].includes(props.account.type)
})
const canUseAction = (action: string) => {
  const actions = props.account?.available_actions
  return actions == null || actions.includes(action)
}
const canShowRateTrend = computed(() =>
  props.account?.upstream_config_id != null &&
  props.account?.upstream_key_id != null &&
  canUseAction('rate_trend')
)
const isRateLimited = computed(() => {
  if (props.account?.rate_limit_reset_at && new Date(props.account.rate_limit_reset_at) > new Date()) {
    return true
  }
  const modelLimits = (props.account?.extra as Record<string, unknown> | undefined)?.model_rate_limits as
    | Record<string, { rate_limit_reset_at: string }>
    | undefined
  if (modelLimits) {
    const now = new Date()
    return Object.values(modelLimits).some(info => new Date(info.rate_limit_reset_at) > now)
  }
  return false
})
const isOverloaded = computed(() => props.account?.overload_until && new Date(props.account.overload_until) > new Date())
const isTempUnschedulable = computed(() => props.account?.temp_unschedulable_until && new Date(props.account.temp_unschedulable_until) > new Date())
const hasRecoverableState = computed(() => {
  return props.account?.status === 'error' || Boolean(isRateLimited.value) || Boolean(isOverloaded.value) || Boolean(isTempUnschedulable.value)
})
const isAntigravityOAuth = computed(() => props.account?.platform === 'antigravity' && props.account?.type === 'oauth')
const isOpenAIOAuth = computed(() => props.account?.platform === 'openai' && props.account?.type === 'oauth')
// 影子账号(链接型,持 parent_account_id)不持凭据、type 不可变,凭据/隐私类操作对其无效。
const isShadow = computed(() => props.account?.parent_account_id != null)
// A "parent" OpenAI OAuth account is one that is NOT itself a shadow (parent_account_id == null)
const isOpenAIOAuthParent = computed(() => isOpenAIOAuth.value && !isShadow.value)
const supportsPrivacy = computed(() => (isAntigravityOAuth.value || isOpenAIOAuth.value) && !isShadow.value)
const hasQuotaLimit = computed(() => {
  return (props.account?.type === 'apikey' || props.account?.type === 'bedrock') && (
    (props.account?.quota_limit ?? 0) > 0 ||
    (props.account?.quota_daily_limit ?? 0) > 0 ||
    (props.account?.quota_weekly_limit ?? 0) > 0
  )
})
</script>

<style scoped>
.action-menu-item {
  display: flex;
  width: 100%;
  min-height: 2.5rem;
  align-items: center;
  gap: 0.625rem;
  padding: 0.625rem 0.875rem;
  text-align: left;
  font-size: 0.8125rem;
  line-height: 1.25rem;
  white-space: normal;
  color: rgb(55 65 81);
  transition: background-color 150ms ease, color 150ms ease;
}

.action-menu-item:hover,
.action-menu-item:focus-visible {
  background: rgb(243 244 246);
  color: rgb(17 24 39);
  outline: none;
}

[data-menu-divider] {
  margin: 0.25rem 0;
  border-top: 1px solid rgb(229 231 235);
}

:global(.dark) .action-menu-item {
  color: rgb(209 213 219);
}

:global(.dark) .action-menu-item:hover,
:global(.dark) .action-menu-item:focus-visible {
  background: rgb(55 65 81);
  color: rgb(255 255 255);
}

:global(.dark) [data-menu-divider] {
  border-color: rgb(75 85 99);
}
</style>
