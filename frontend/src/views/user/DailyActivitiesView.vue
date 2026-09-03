<template>
  <AppLayout>
    <div class="mx-auto max-w-6xl space-y-6">
      <div class="flex flex-col gap-4 sm:flex-row sm:items-end sm:justify-between">
        <div>
          <div class="flex items-center gap-3">
            <div class="flex h-11 w-11 items-center justify-center rounded-2xl bg-amber-100 text-2xl shadow-sm ring-1 ring-amber-200 dark:bg-amber-900/30 dark:ring-amber-700/50" aria-hidden="true">
              🎁
            </div>
            <div>
              <h1 class="text-2xl font-bold text-gray-900 dark:text-white">{{ t('activities.title') }}</h1>
              <p class="mt-1 text-sm text-gray-500 dark:text-dark-400">{{ t('activities.description') }}</p>
            </div>
          </div>
          <p v-if="summary" class="mt-3 text-sm text-gray-500 dark:text-dark-400">
            {{ t('activities.activityDate', { date: summary.activity_date }) }}
            <span class="mx-1" aria-hidden="true">·</span>
            {{ t('activities.resetAt', { time: formatDateTime(summary.next_reset_at) }) }}
            <span class="mx-1" aria-hidden="true">·</span>
            {{ t('activities.countdown', { time: resetCountdown }) }}
            <span class="mx-1" aria-hidden="true">·</span>
            {{ t('activities.balance', { amount: formatCurrency(summary.balance ?? 0) }) }}
          </p>
        </div>
        <button class="btn btn-secondary self-start sm:self-auto" :disabled="loading" @click="load">
          <Icon name="refresh" size="sm" :class="{ 'animate-spin': loading }" />
          {{ t('activities.refresh') }}
        </button>
      </div>

      <div v-if="loading" class="flex justify-center py-16"><div class="h-9 w-9 animate-spin rounded-full border-2 border-primary-500 border-t-transparent" /></div>
      <div v-else-if="error" class="card border border-red-200 p-6 text-center dark:border-red-900/50">
        <p class="text-sm text-red-600 dark:text-red-400">{{ error }}</p>
        <button class="btn btn-primary mt-4" @click="load">{{ t('common.retry') }}</button>
      </div>
      <template v-else-if="summary && summary.enabled">
        <div class="grid gap-4 md:grid-cols-2">
          <section class="card border-l-4 border-l-amber-400 p-5">
            <div class="flex items-start justify-between gap-4">
              <div><p class="text-lg font-semibold text-gray-900 dark:text-white">{{ t('activities.dailyGift.title') }}</p><p class="mt-1 text-sm text-gray-500 dark:text-dark-400">{{ t('activities.dailyGift.description', { amount: formatCurrency(summary.daily_gift.threshold) }) }}</p><p class="mt-1 text-xs font-medium text-amber-700 dark:text-amber-300">{{ t('activities.rewardRange', { range: formatRewardRange(summary.daily_gift.reward_min, summary.daily_gift.reward_max) }) }}</p></div>
              <Icon name="gift" size="lg" class="text-amber-500" />
            </div>
            <p class="mt-5 text-sm" :class="summary.daily_gift.eligible ? 'text-emerald-600 dark:text-emerald-400' : 'text-gray-500 dark:text-dark-400'">{{ summary.daily_gift.claimed ? t('activities.dailyGift.claimed') : summary.daily_gift.eligible ? t('activities.dailyGift.eligible') : t('activities.dailyGift.unavailable') }}</p>
            <div class="mt-4 flex flex-col gap-2 sm:flex-row">
              <RouterLink class="btn btn-secondary flex-1" to="/recharge-store">
                <Icon name="dollar" size="sm" />
                {{ t('activities.goRecharge') }}
              </RouterLink>
              <button class="btn btn-primary flex-1" :disabled="!summary.daily_gift.eligible || summary.daily_gift.claimed || busy" @click="claimGift">{{ t('activities.dailyGift.button') }}</button>
            </div>
          </section>

          <ActivityDrawCard type="recharge" :progress="summary.recharge" :title="t('activities.recharge.title')" :description="t('activities.recharge.description', { amount: formatCurrency(summary.recharge.threshold) })" :reward-range-label="t('activities.rewardRange', { range: formatRewardRange(summary.recharge.reward_min, summary.recharge.reward_max) })" :progress-label="t('activities.progress')" :available-draws-label="t('activities.availableDraws', { count: summary.recharge.available_draws })" :draw-one-label="t('activities.drawOne')" :draw-all-label="t('activities.drawAll')" :recharge-label="t('activities.goRecharge')" :busy="busy" @draw="draw('recharge', $event)" />
          <ActivityDrawCard type="consumption" :progress="summary.consumption" :title="t('activities.consumption.title')" :description="t('activities.consumption.description', { amount: formatCurrency(summary.consumption.threshold) })" :reward-range-label="t('activities.rewardRange', { range: formatRewardRange(summary.consumption.reward_min, summary.consumption.reward_max) })" :progress-label="t('activities.progress')" :available-draws-label="t('activities.availableDraws', { count: summary.consumption.available_draws })" :draw-one-label="t('activities.drawOne')" :draw-all-label="t('activities.drawAll')" :busy="busy" @draw="draw('consumption', $event)" />

          <section class="card border-l-4 border-l-violet-500 p-5">
            <div class="flex items-start justify-between gap-4"><div><p class="text-lg font-semibold text-gray-900 dark:text-white">{{ t('activities.invite.title') }}</p><p class="mt-1 text-sm text-gray-500 dark:text-dark-400">{{ t('activities.invite.description', { amount: formatCurrency(summary.invite.qualification_amount) }) }}</p><p class="mt-1 text-xs font-medium text-violet-700 dark:text-violet-300">{{ t('activities.rewardRange', { range: formatRewardRange(summary.invite.reward_min, summary.invite.reward_max) }) }}</p></div><Icon name="users" size="lg" class="text-violet-500" /></div>
            <div class="mt-5 flex items-baseline justify-between"><span class="text-sm text-gray-500 dark:text-dark-400">{{ t('activities.invite.qualified') }}</span><strong class="text-xl text-gray-900 dark:text-white">{{ summary.invite.qualified_count }} / {{ summary.invite.required_count }}</strong></div>
            <div class="mt-3 h-2 overflow-hidden rounded-full bg-gray-100 dark:bg-dark-700"><div class="h-full rounded-full bg-violet-500 transition-all" :style="{ width: `${invitePercent}%` }" /></div>
            <p class="mt-3 text-sm text-gray-500 dark:text-dark-400">{{ t('activities.invite.requirement', { count: summary.invite.required_count }) }} · {{ t('activities.availableDraws', { count: summary.invite.available_draws }) }}</p>
            <div class="mt-4 flex flex-col gap-2 sm:flex-row"><button v-if="affiliateCode" class="btn btn-secondary flex-1" @click="copyInviteLink"><Icon name="copy" size="sm" />{{ t('activities.copyLink') }}</button><button class="btn btn-primary flex-1" :disabled="summary.invite.available_draws < 1 || busy" @click="draw('invite', 1)">{{ t('activities.drawOne') }}</button><button class="btn btn-secondary flex-1" :disabled="summary.invite.available_draws < 1 || busy" @click="draw('invite', summary.invite.available_draws)">{{ t('activities.drawAll') }}</button></div>
          </section>
        </div>

        <section class="card p-5">
          <div class="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between"><div><h2 class="text-lg font-semibold text-gray-900 dark:text-white">{{ t('activities.rewards.title') }}</h2><p class="mt-1 text-sm text-gray-500 dark:text-dark-400">{{ t('activities.rules') }}</p></div><div class="flex flex-wrap items-center gap-2"><select v-model="rewardTypeFilter" class="input input-sm" :aria-label="t('activities.rewards.filterLabel')"><option value="all">{{ t('activities.rewards.all') }}</option><option value="daily_gift">{{ t('activities.rewards.dailyGift') }}</option><option value="recharge_draw">{{ t('activities.rewards.recharge') }}</option><option value="spend_draw">{{ t('activities.rewards.consumption') }}</option><option value="invite_draw">{{ t('activities.rewards.invite') }}</option></select></div></div>
          <div v-if="rewards.length === 0" class="mt-5 rounded-xl border border-dashed border-gray-300 p-8 text-center text-sm text-gray-500 dark:border-dark-700 dark:text-dark-400">{{ t('activities.rewards.empty') }}</div>
          <div v-else class="mt-4 overflow-x-auto"><table class="w-full min-w-[480px] text-left text-sm"><thead><tr class="border-b border-gray-200 text-gray-500 dark:border-dark-700 dark:text-dark-400"><th class="px-3 py-2 font-medium">{{ t('activities.rewards.date') }}</th><th class="px-3 py-2 font-medium">{{ t('activities.rewards.type') }}</th><th class="px-3 py-2 text-right font-medium">{{ t('activities.rewards.amount') }}</th></tr></thead><tbody><tr v-for="reward in rewards" :key="reward.id" class="border-b border-gray-100 last:border-0 dark:border-dark-800"><td class="px-3 py-3 text-gray-600 dark:text-dark-300">{{ formatDateTime(reward.created_at) }}</td><td class="px-3 py-3 text-gray-900 dark:text-white">{{ rewardLabel(reward.type) }}</td><td class="px-3 py-3 text-right font-semibold text-emerald-600 dark:text-emerald-400">{{ formatCurrency(reward.amount) }}</td></tr></tbody></table></div>
          <div v-if="rewardsTotal > rewardsPageSize" class="mt-4 flex items-center justify-between gap-3 text-sm text-gray-500 dark:text-dark-400"><span>{{ rewardsPage }} / {{ rewardsPages }}</span><div class="flex gap-2"><button class="btn btn-secondary btn-sm" :disabled="rewardsPage <= 1 || loading" @click="loadRewards(rewardsPage - 1)">{{ t('activities.rewards.previous') }}</button><button class="btn btn-secondary btn-sm" :disabled="rewardsPage >= rewardsPages || loading" @click="loadRewards(rewardsPage + 1)">{{ t('activities.rewards.next') }}</button></div></div>
        </section>
      </template>
      <div v-else class="card p-8 text-center text-sm text-gray-500 dark:text-dark-400">{{ t('activities.disabled') }}</div>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, defineComponent, h, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { RouterLink } from 'vue-router'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import Icon from '@/components/icons/Icon.vue'
import userAPI from '@/api/user'
import type { UserActivityProgress, UserActivityReward, UserActivityRewardType, UserActivitySummary, UserActivityType } from '@/types'
import { useAppStore } from '@/stores/app'
import { useClipboard } from '@/composables/useClipboard'
import { formatCurrency, formatDateTime } from '@/utils/format'
import { extractApiErrorMessage } from '@/utils/apiError'

const ActivityDrawCard = defineComponent({
  props: { type: { type: String, required: true }, progress: { type: Object as () => UserActivityProgress, required: true }, title: { type: String, required: true }, description: { type: String, required: true }, rewardRangeLabel: { type: String, required: true }, progressLabel: { type: String, required: true }, availableDrawsLabel: { type: String, required: true }, drawOneLabel: { type: String, required: true }, drawAllLabel: { type: String, required: true }, rechargeLabel: { type: String, default: '' }, busy: { type: Boolean, default: false } },
  emits: ['draw'],
  setup(props, { emit }) { return () => h('section', { class: 'card border-l-4 border-l-emerald-400 p-5' }, [h('div', { class: 'flex items-start justify-between gap-4' }, [h('div', [h('p', { class: 'text-lg font-semibold text-gray-900 dark:text-white' }, props.title), h('p', { class: 'mt-1 text-sm text-gray-500 dark:text-dark-400' }, props.description), h('p', { class: 'mt-1 text-xs font-medium text-emerald-700 dark:text-emerald-300' }, props.rewardRangeLabel)]), h(Icon, { name: props.type === 'recharge' ? 'dollar' : 'chartBar', size: 'lg', class: 'text-emerald-500' })]), h('div', { class: 'mt-5 flex items-baseline justify-between' }, [h('span', { class: 'text-sm text-gray-500 dark:text-dark-400' }, props.progressLabel), h('strong', { class: 'text-xl text-gray-900 dark:text-white' }, `${formatCurrency(props.progress.amount)} / ${formatCurrency(props.progress.threshold)}`)]), h('div', { class: 'mt-3 h-2 overflow-hidden rounded-full bg-gray-100 dark:bg-dark-700' }, [h('div', { class: 'h-full rounded-full bg-emerald-500 transition-all', style: { width: `${Math.min(100, props.progress.amount / Math.max(props.progress.threshold, 0.01) * 100)}%` } })]), h('p', { class: 'mt-3 text-sm text-gray-500 dark:text-dark-400' }, props.availableDrawsLabel), props.type === 'recharge' && props.rechargeLabel ? h(RouterLink, { class: 'btn btn-secondary mt-4 w-full', to: '/recharge-store' }, { default: () => [h(Icon, { name: 'dollar', size: 'sm' }), props.rechargeLabel] }) : null, h('div', { class: props.type === 'recharge' ? 'mt-2 flex gap-2' : 'mt-4 flex gap-2' }, [h('button', { class: 'btn btn-primary flex-1', disabled: props.progress.available_draws < 1 || props.busy, onClick: () => emit('draw', 1) }, props.drawOneLabel), h('button', { class: 'btn btn-secondary flex-1', disabled: props.progress.available_draws < 1 || props.busy, onClick: () => emit('draw', props.progress.available_draws) }, props.drawAllLabel)])]) }
})

const { t } = useI18n(); const appStore = useAppStore(); const { copyToClipboard } = useClipboard(); const loading = ref(true); const busy = ref(false); const error = ref(''); const summary = ref<UserActivitySummary | null>(null); const rewards = ref<UserActivityReward[]>([]); const affiliateCode = ref(''); const rewardTypeFilter = ref<'all' | UserActivityRewardType>('all'); const rewardsPage = ref(1); const rewardsPageSize = 20; const rewardsTotal = ref(0)
const rewardsPages = computed(() => Math.max(1, Math.ceil(rewardsTotal.value / rewardsPageSize)))
const countdownNow = ref(Date.now())
let countdownTimer: ReturnType<typeof setInterval> | undefined
const resetCountdown = computed(() => {
  if (!summary.value?.next_reset_at) return '--:--:--'
  const seconds = Math.max(0, Math.floor((new Date(summary.value.next_reset_at).getTime() - countdownNow.value) / 1000))
  return `${Math.floor(seconds / 3600).toString().padStart(2, '0')}:${Math.floor((seconds % 3600) / 60).toString().padStart(2, '0')}:${(seconds % 60).toString().padStart(2, '0')}`
})
const invitePercent = computed(() => summary.value ? Math.min(100, summary.value.invite.qualified_count / Math.max(1, summary.value.invite.required_count) * 100) : 0)
function formatRewardRange(min?: number, max?: number): string {
  if (!Number.isFinite(min) || !Number.isFinite(max)) return '-'
  return `${formatCurrency(min as number)} – ${formatCurrency(max as number)}`
}
function rewardLabel(type: string): string {
  const key = ({ daily_gift: 'dailyGift', recharge_draw: 'recharge', spend_draw: 'consumption', invite_draw: 'invite' } as Record<string, string>)[type] || type
  return t(`activities.rewards.${key}`)
}
async function loadRewards(page = 1): Promise<void> { const nextRewards = await userAPI.getActivityRewards({ page, page_size: rewardsPageSize, ...(rewardTypeFilter.value === 'all' ? {} : { type: rewardTypeFilter.value }) }); rewards.value = nextRewards.items; rewardsTotal.value = nextRewards.total; rewardsPage.value = nextRewards.page }
async function load(): Promise<void> { loading.value = true; error.value = ''; try { const [nextSummary, aff] = await Promise.all([userAPI.getActivitySummary(), userAPI.getAffiliateDetail().catch(() => null)]); summary.value = nextSummary; affiliateCode.value = aff?.aff_code || ''; await loadRewards(1) } catch (err) { error.value = extractApiErrorMessage(err, t('activities.loadFailed')); } finally { loading.value = false } }
function idempotencyKey(prefix: string): string { if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') return `${prefix}-${crypto.randomUUID()}`; return `${prefix}-${Date.now()}-${Math.random().toString(36).slice(2)}` }
async function applyAction(action: () => Promise<{ reward?: UserActivityReward; rewards?: UserActivityReward[]; summary?: UserActivitySummary }>): Promise<void> { if (busy.value) return; busy.value = true; try { const response = await action(); const added = response.rewards || (response.reward ? [response.reward] : []); if (response.summary) summary.value = response.summary; await load(); appStore.showSuccess(t('activities.rewardClaimed', { amount: added.length ? formatCurrency(added.reduce((sum, item) => sum + item.amount, 0)) : '-' })); } catch (err) { appStore.showError(extractApiErrorMessage(err, t('activities.actionFailed'))) } finally { busy.value = false } }
function claimGift(): Promise<void> { return applyAction(() => userAPI.openDailyActivityGift(idempotencyKey('daily-gift'))) }
function draw(type: UserActivityType, count: number): Promise<void> { return applyAction(() => userAPI.drawDailyActivity(type === 'consumption' ? 'consumption' : type, count, idempotencyKey(`draw-${type}`))) }
async function copyInviteLink(): Promise<void> { if (!affiliateCode.value) return; const link = `${window.location.origin}/register?aff=${encodeURIComponent(affiliateCode.value)}`; await copyToClipboard(link, t('activities.linkCopied')) }
onMounted(() => { void load() })
onMounted(() => { countdownTimer = setInterval(() => { countdownNow.value = Date.now() }, 1000) })
onBeforeUnmount(() => { if (countdownTimer) clearInterval(countdownTimer) })
watch(rewardTypeFilter, () => { void loadRewards(1) })
</script>
