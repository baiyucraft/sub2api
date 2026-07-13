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
              data-test="open-sync-runs"
              @click="openSyncRuns"
            >
              <Icon name="clock" size="sm" class="mr-2" />
              {{ t('admin.upstreamConfigs.actions.syncRuns') }}
            </button>
            <button
              type="button"
              class="btn btn-secondary"
              data-test="open-upstream-events"
              @click="openEvents"
            >
              <Icon name="bell" size="sm" class="mr-2" />
              {{ t('admin.upstreamConfigs.actions.events') }}
            </button>
            <button
              type="button"
              class="btn btn-secondary"
              data-test="open-cost-trend"
              @click="openTrend()"
            >
              <Icon name="trendingUp" size="sm" class="mr-2" />
              {{ t('admin.upstreamConfigs.actions.costTrend') }}
            </button>
            <button
              type="button"
              class="btn btn-secondary"
              data-test="open-rate-trend"
              @click="openRateTrend()"
            >
              <Icon name="trendingUp" size="sm" class="mr-2" />
              {{ t('admin.upstreamConfigs.actions.rateTrend') }}
            </button>
            <button
              type="button"
              class="btn btn-secondary px-3"
              data-test="open-upstream-settings"
              :title="t('admin.upstreamConfigs.actions.settings')"
              :aria-label="t('admin.upstreamConfigs.actions.settings')"
              @click="openSettings"
            >
              <Icon name="cog" size="md" />
            </button>
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
            <div ref="columnDropdownRef" class="relative">
              <button
                type="button"
                class="btn btn-secondary"
                :title="t('admin.upstreamConfigs.columnSettings')"
                @click="showColumnDropdown = !showColumnDropdown"
              >
                <Icon name="grid" size="md" class="mr-2" />
                <span class="hidden md:inline">{{ t('admin.upstreamConfigs.columnSettings') }}</span>
              </button>
              <div
                v-if="showColumnDropdown"
                class="absolute right-0 top-full z-50 mt-1 max-h-80 w-48 overflow-y-auto rounded-lg border border-gray-200 bg-white py-1 shadow-lg dark:border-dark-600 dark:bg-dark-800"
              >
                <button
                  v-for="column in toggleableColumns"
                  :key="column.key"
                  type="button"
                  class="flex w-full items-center justify-between px-4 py-2 text-left text-sm text-gray-700 hover:bg-gray-100 dark:text-gray-300 dark:hover:bg-dark-700"
                  @click="toggleColumn(column.key)"
                >
                  <span>{{ column.label }}</span>
                  <Icon
                    v-if="isColumnVisible(column.key)"
                    name="check"
                    size="sm"
                    class="text-primary-500"
                    :stroke-width="2"
                  />
                </button>
              </div>
            </div>
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
          :estimate-row-height="72"
        >
          <template #header-upstream_concurrency>
            <span :title="t('admin.upstreamConfigs.concurrency.headerTitle')">
              {{ t('admin.upstreamConfigs.columns.upstreamConcurrency') }}
            </span>
          </template>

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

          <template #cell-urls="{ row }">
            <div class="max-w-[340px] space-y-1 font-mono text-xs text-gray-700 dark:text-gray-300">
              <div class="flex min-w-0 items-center gap-2" :title="row.site_url">
                <span class="w-7 flex-none font-sans text-gray-400 dark:text-dark-500">{{ t('admin.upstreamConfigs.address.site') }}</span>
                <span class="truncate">{{ row.site_url }}</span>
              </div>
              <div v-if="row.api_url" class="flex min-w-0 items-center gap-2" :title="row.api_url">
                <span class="w-7 flex-none font-sans text-gray-400 dark:text-dark-500">{{ t('admin.upstreamConfigs.address.api') }}</span>
                <span class="truncate">{{ row.api_url }}</span>
              </div>
            </div>
          </template>

          <template #cell-balance="{ row }">
            <div class="min-w-[120px]" :title="balanceTitle(row)">
              <div
                :class="[
                  'text-sm font-semibold tabular-nums',
                  isLowBalance(row)
                    ? 'text-red-600 dark:text-red-400'
                    : 'text-gray-900 dark:text-gray-100'
                ]"
              >
                {{ formatCNY(upstreamBalanceCNY(row)) }}
              </div>
              <div v-if="upstreamTotalAmountCNY(row) !== null" class="mt-0.5 text-xs text-gray-500 dark:text-dark-400">
                {{ t(row.provider === 'newapi' ? 'admin.upstreamConfigs.balance.totalQuota' : 'admin.upstreamConfigs.balance.totalRecharged', { amount: formatCNY(upstreamTotalAmountCNY(row)) }) }}
              </div>
              <div v-if="isLowBalance(row)" class="mt-0.5 text-xs font-medium text-red-600 dark:text-red-400">
                {{ t('admin.upstreamConfigs.balance.lowBalance') }}
              </div>
              <div
                v-if="upstreamBalanceError(row)"
                class="mt-0.5 max-w-[160px] truncate text-xs text-amber-600 dark:text-amber-400"
                :title="upstreamBalanceError(row) || ''"
              >
                {{ upstreamBalanceError(row) }}
              </div>
            </div>
          </template>

          <template #cell-upstream_concurrency="{ row }">
            <div
              class="min-w-[112px]"
              data-test="upstream-concurrency"
              :title="upstreamConcurrencyTitle(row)"
            >
              <div
                :class="[
                  'text-sm tabular-nums',
                  upstreamConcurrencyTextClass(row)
                ]"
              >
                {{ upstreamConcurrencyLabel(row) }}
              </div>
            </div>
          </template>

          <template #cell-rates="{ row }">
            <div class="min-w-[145px] text-xs tabular-nums">
              <div class="text-gray-700 dark:text-gray-300">
                {{ t('admin.upstreamConfigs.rates.raw', { value: rateRangeLabel(rawRateRange(row)) }) }}
              </div>
              <div class="mt-1 text-gray-500 dark:text-dark-400">
                {{ t('admin.upstreamConfigs.rates.cost', { value: rateRangeLabel(costRateRange(row)) }) }}
              </div>
              <div class="mt-1 text-[11px] text-gray-400 dark:text-dark-500">
                {{ t('admin.upstreamConfigs.rates.recharge', { value: formatRate(row.recharge_rate || 1) }) }}
              </div>
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
            <div class="flex min-w-[300px] items-start gap-1" @click.stop>
              <button
                type="button"
                class="table-action-button hover:text-primary-600 dark:hover:text-primary-400"
                data-test="edit-upstream"
                :title="t('common.edit')"
                :aria-label="t('common.edit')"
                @click="openEdit(row)"
              >
                <Icon name="edit" size="sm" />
                <span>{{ t('common.edit') }}</span>
              </button>
              <button
                type="button"
                class="table-action-button hover:text-emerald-600 dark:hover:text-emerald-400"
                data-test="sync-upstream"
                :disabled="syncingAll || isActionBusy(row.id, 'sync')"
                :title="t('admin.upstreamConfigs.actions.syncKeys')"
                :aria-label="t('admin.upstreamConfigs.actions.syncKeys')"
                @click="handleSync(row)"
              >
                <Icon name="sync" size="sm" :class="isActionBusy(row.id, 'sync') ? 'animate-spin' : ''" />
                <span>{{ t('admin.upstreamConfigs.actions.syncKeys') }}</span>
              </button>
              <button
                type="button"
                class="table-action-button hover:text-blue-600 dark:hover:text-blue-400"
                data-test="row-cost-trend"
                :title="t('admin.upstreamConfigs.actions.costTrend')"
                :aria-label="t('admin.upstreamConfigs.actions.costTrend')"
                @click="openTrend(row)"
              >
                <Icon name="trendingUp" size="sm" />
                <span>{{ t('admin.upstreamConfigs.actions.costTrend') }}</span>
              </button>
              <button
                type="button"
                class="table-action-button hover:text-indigo-600 dark:hover:text-indigo-400"
                data-test="row-rate-trend"
                :title="t('admin.upstreamConfigs.actions.rateTrend')"
                :aria-label="t('admin.upstreamConfigs.actions.rateTrend')"
                @click="openRateTrend(row)"
              >
                <Icon name="chart" size="sm" />
                <span>{{ t('admin.upstreamConfigs.actions.rateTrend') }}</span>
              </button>
              <button
                type="button"
                class="table-action-button hover:text-gray-900 dark:hover:text-white"
                data-test="more-upstream-actions"
                :title="t('admin.upstreamConfigs.actions.more')"
                :aria-label="t('admin.upstreamConfigs.actions.more')"
                @click="openActionMenu(row, $event)"
              >
                <Icon name="more" size="sm" />
                <span>{{ t('admin.upstreamConfigs.actions.more') }}</span>
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
      <form id="upstream-config-form" class="max-h-[68vh] space-y-6 overflow-y-auto pr-1" @submit.prevent="saveConfig">
        <section class="space-y-4">
          <div class="border-b border-gray-200 pb-2 dark:border-dark-700">
            <h3 class="text-sm font-semibold text-gray-900 dark:text-gray-100">{{ t('admin.upstreamConfigs.sections.basicInfo') }}</h3>
          </div>
          <div class="grid gap-4 md:grid-cols-2">
            <label class="space-y-1">
              <span class="input-label">{{ t('admin.upstreamConfigs.fields.name') }}</span>
              <input v-model.trim="form.name" class="input" data-test="upstream-name-input" required />
            </label>
            <label class="space-y-1">
              <span class="input-label">{{ t('admin.upstreamConfigs.fields.provider') }}</span>
              <Select v-model="form.provider" :options="providerEditOptions" />
            </label>
            <label class="space-y-1 md:col-span-2">
              <span class="input-label">{{ t('admin.upstreamConfigs.fields.siteUrl') }}</span>
              <input v-model.trim="form.site_url" class="input font-mono text-sm" data-test="upstream-site-url-input" required placeholder="https://example.com" />
            </label>
            <label class="space-y-1 md:col-span-2">
              <span class="input-label">{{ t('admin.upstreamConfigs.fields.apiUrl') }}</span>
              <input v-model.trim="form.api_url" class="input font-mono text-sm" data-test="upstream-api-url-input" placeholder="https://api.example.com/v1" />
              <span class="block text-xs text-gray-500 dark:text-dark-400">{{ t('admin.upstreamConfigs.fields.apiUrlHint') }}</span>
            </label>
          </div>
        </section>

        <section class="space-y-4">
          <div class="border-b border-gray-200 pb-2 dark:border-dark-700">
            <h3 class="text-sm font-semibold text-gray-900 dark:text-gray-100">{{ t('admin.upstreamConfigs.sections.connectionAndAuth') }}</h3>
          </div>
          <div class="grid gap-4 md:grid-cols-2">
            <div class="space-y-1 md:col-span-2">
              <span class="input-label">{{ t('admin.upstreamConfigs.fields.proxy') }}</span>
              <ProxySelector v-model="form.proxy_id" :proxies="proxies" :disabled="loadingProxies" />
            </div>

            <div v-if="form.provider !== 'other'" class="space-y-2 md:col-span-2">
              <span class="input-label">{{ t('admin.upstreamConfigs.fields.authMode') }}</span>
              <div class="inline-flex rounded-lg border border-gray-200 p-1 dark:border-dark-700" data-test="upstream-auth-mode-control">
                <button
                  v-for="option in authModeOptions"
                  :key="String(option.value)"
                  type="button"
                  :class="[
                    'rounded-md px-4 py-2 text-sm font-medium transition-colors',
                    form.auth_mode === option.value
                      ? 'bg-primary-600 text-white'
                      : 'text-gray-600 hover:bg-gray-100 dark:text-dark-300 dark:hover:bg-dark-700'
                  ]"
                  :data-test="`upstream-auth-mode-${option.value}`"
                  @click="form.auth_mode = option.value as UpstreamAuthMode"
                >
                  {{ option.label }}
                </button>
              </div>
            </div>

          <template v-if="form.provider === 'sub2api' && form.auth_mode === 'user_login'">
            <label class="space-y-1">
              <span class="input-label">{{ t('admin.upstreamConfigs.fields.loginEmail') }}</span>
              <input
                v-model.trim="form.email"
                class="input"
                data-test="upstream-email-input"
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
                data-test="upstream-password-input"
                type="password"
                autocomplete="new-password"
                :required="!editing"
                :placeholder="editing ? t('admin.upstreamConfigs.fields.keepPasswordPlaceholder') : ''"
              />
            </label>
          </template>

          <template v-if="form.provider === 'newapi' && form.auth_mode === 'user_login'">
            <label class="space-y-1">
              <span class="input-label">{{ t('admin.upstreamConfigs.fields.loginUsername') }}</span>
              <input
                v-model.trim="form.username"
                class="input"
                data-test="upstream-username-input"
                type="text"
                autocomplete="username"
                :required="!editing"
              />
            </label>
            <label class="space-y-1">
              <span class="input-label">{{ t('admin.upstreamConfigs.fields.loginPassword') }}</span>
              <input
                v-model="form.password"
                class="input"
                data-test="upstream-password-input"
                type="password"
                autocomplete="new-password"
                :required="!editing"
                :placeholder="editing ? t('admin.upstreamConfigs.fields.keepPasswordPlaceholder') : ''"
              />
            </label>
          </template>

          <template v-if="form.provider === 'newapi' && form.auth_mode === 'cookie'">
            <label class="space-y-1">
              <span class="input-label">{{ t('admin.upstreamConfigs.fields.newapiUserId') }}</span>
              <input
                v-model.trim="form.newapi_user_id"
                class="input font-mono"
                data-test="upstream-newapi-user-id-input"
                inputmode="numeric"
                :required="!editing"
                :placeholder="editing ? t('admin.upstreamConfigs.fields.keepUserIdPlaceholder') : ''"
              />
            </label>
            <label class="space-y-1 md:col-span-2">
              <span class="input-label">{{ t('admin.upstreamConfigs.fields.cookie') }}</span>
              <textarea
                v-model.trim="form.cookie"
                class="input min-h-[92px] font-mono text-xs"
                data-test="upstream-newapi-cookie-input"
                :required="!editing"
                :placeholder="editing ? t('admin.upstreamConfigs.fields.keepCookiePlaceholder') : ''"
              ></textarea>
            </label>
          </template>

          <template v-if="form.provider === 'newapi' && form.auth_mode === 'access_token'">
            <label class="space-y-1">
              <span class="input-label">{{ t('admin.upstreamConfigs.fields.newapiUserId') }}</span>
              <input
                v-model.trim="form.newapi_user_id"
                class="input font-mono"
                data-test="upstream-newapi-user-id-input"
                inputmode="numeric"
                :required="!editing"
                :placeholder="editing ? t('admin.upstreamConfigs.fields.keepUserIdPlaceholder') : ''"
              />
            </label>
            <label class="space-y-1 md:col-span-2">
              <span class="input-label">{{ t('admin.upstreamConfigs.fields.newapiAccessToken') }}</span>
              <textarea
                v-model.trim="form.newapi_access_token"
                class="input min-h-[92px] font-mono text-xs"
                data-test="upstream-newapi-access-token-input"
                :required="!editing"
                :placeholder="editing ? t('admin.upstreamConfigs.fields.keepNewapiAccessTokenPlaceholder') : ''"
              ></textarea>
            </label>
          </template>

          <template v-if="form.provider === 'sub2api' && form.auth_mode === 'manual_jwt'">
            <div class="md:col-span-2 flex flex-wrap items-center justify-between gap-2 border-l-2 border-blue-500 bg-blue-50 px-3 py-2.5 text-xs text-blue-800 dark:bg-blue-900/20 dark:text-blue-200">
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
                :required="!editing && !form.refresh_token"
                :placeholder="editing ? t('admin.upstreamConfigs.fields.keepAccessTokenPlaceholder') : ''"
              ></textarea>
            </label>
            <label class="space-y-1 md:col-span-2">
              <span class="input-label">{{ t('admin.upstreamConfigs.fields.refreshToken') }}</span>
              <textarea
                v-model.trim="form.refresh_token"
                class="input min-h-[92px] font-mono text-xs"
                :required="!editing && !form.access_token"
                :placeholder="editing ? t('admin.upstreamConfigs.fields.keepRefreshTokenPlaceholder') : ''"
              ></textarea>
            </label>
          </template>
          </div>

          <div v-if="form.provider !== 'other'" class="flex gap-2 border-l-2 border-amber-500 bg-amber-50 px-3 py-2.5 text-xs text-amber-800 dark:bg-amber-900/20 dark:text-amber-200">
            <Icon name="shield" size="sm" class="mt-0.5 flex-shrink-0" />
            <p>{{ t('admin.upstreamConfigs.sensitiveHint') }}</p>
          </div>
        </section>

        <section class="space-y-4">
          <div class="border-b border-gray-200 pb-2 dark:border-dark-700">
            <h3 class="text-sm font-semibold text-gray-900 dark:text-gray-100">{{ t('admin.upstreamConfigs.sections.costSettings') }}</h3>
          </div>
          <div class="grid gap-4 md:grid-cols-2">
            <label class="space-y-1">
              <span class="input-label">{{ t('admin.upstreamConfigs.fields.rechargeRate') }}</span>
              <input v-model.number="form.recharge_rate" class="input tabular-nums" data-test="recharge-rate-input" type="number" min="0.0001" max="100" step="0.0001" required />
              <span class="block text-xs text-gray-500 dark:text-dark-400">{{ t('admin.upstreamConfigs.fields.rechargeRateHint') }}</span>
            </label>
            <label class="space-y-1">
              <span class="input-label">{{ t('admin.upstreamConfigs.fields.balanceToCnyRate') }}</span>
              <input v-model.number="form.balance_to_cny_rate" class="input tabular-nums" data-test="balance-to-cny-rate-input" type="number" min="0.0001" step="0.0001" :placeholder="t('admin.upstreamConfigs.fields.balanceToCnyRatePlaceholder')" />
              <span class="block text-xs text-gray-500 dark:text-dark-400">{{ t('admin.upstreamConfigs.fields.balanceToCnyRateHint') }}</span>
            </label>
          </div>
        </section>
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

    <UpstreamActionMenu
      :show="actionMenu.show"
      :anchor-el="actionMenu.anchorEl"
      :config="actionMenu.config"
      width="normal"
      @close="closeActionMenu"
      @test="handleTest"
      @dashboard="openUpstreamDashboard"
      @delete="askDelete"
    />

    <BaseDialog
      :show="operationsDrawerMode !== null"
      :title="operationsDrawerTitle"
      :width="operationsDialogWidth"
      :close-on-escape="!settingsSaving"
      :show-close-button="!settingsSaving"
      @close="closeOperationsDrawer"
    >
      <div v-if="operationsDrawerSubtitle" class="mb-4 text-xs text-gray-500 dark:text-dark-400">
        {{ operationsDrawerSubtitle }}
      </div>
      <template v-if="operationsDrawerMode === 'syncRuns'">
        <div class="mb-4 flex items-center justify-between gap-3">
          <button
            v-if="selectedSyncRun"
            type="button"
            class="btn btn-secondary btn-sm"
            data-test="back-to-sync-runs"
            @click="selectedSyncRun = null"
          >
            <Icon name="arrowLeft" size="sm" class="mr-1.5" />
            {{ t('common.back') }}
          </button>
          <span v-else class="text-xs text-gray-500 dark:text-dark-400">
            {{ t('admin.upstreamConfigs.operations.syncRunsHint') }}
          </span>
          <button type="button" class="btn btn-secondary btn-sm ml-auto" :disabled="operationLoading.syncRuns" @click="loadSyncRuns">
            <Icon name="refresh" size="sm" :class="operationLoading.syncRuns ? 'mr-1.5 animate-spin' : 'mr-1.5'" />
            {{ t('common.refresh') }}
          </button>
        </div>
      </template>

      <template v-else-if="operationsDrawerMode === 'events'">
        <div class="mb-4 flex flex-wrap items-center gap-3">
          <label class="min-w-[220px] flex-1">
            <span class="sr-only">{{ t('admin.upstreamConfigs.operations.selectUpstream') }}</span>
            <Select v-model="selectedOperationsConfigId" :options="operationConfigOptions" data-test="events-upstream-select" @change="handleOperationsConfigChange('events')" />
          </label>
          <button type="button" class="btn btn-secondary btn-sm" :disabled="operationLoading.events" @click="loadEventsAndIncidents">
            <Icon name="refresh" size="sm" :class="operationLoading.events ? 'mr-1.5 animate-spin' : 'mr-1.5'" />
            {{ t('common.refresh') }}
          </button>
        </div>
      </template>

      <template v-else-if="operationsDrawerMode === 'trend'">
        <div class="mb-4 flex flex-wrap items-center justify-between gap-3">
          <label class="min-w-[220px] flex-1">
            <span class="sr-only">{{ t('admin.upstreamConfigs.operations.selectUpstream') }}</span>
            <Select v-model="selectedOperationsConfigId" :options="operationConfigOptions" data-test="trend-upstream-select" @change="handleOperationsConfigChange('trend')" />
          </label>
          <div class="inline-flex rounded-lg border border-gray-200 p-1 dark:border-dark-700" data-test="trend-range-control">
            <button
              v-for="range in trendRanges"
              :key="range"
              type="button"
              :class="[
                'min-w-12 rounded-md px-3 py-1.5 text-xs font-medium transition-colors',
                trendRange === range
                  ? 'bg-primary-600 text-white'
                  : 'text-gray-600 hover:bg-gray-100 dark:text-dark-300 dark:hover:bg-dark-700'
              ]"
              :data-test="`trend-range-${range}`"
              @click="setTrendRange(range)"
            >
              {{ range }}
            </button>
          </div>
        </div>
      </template>

      <template v-else-if="operationsDrawerMode === 'rateTrend'">
        <div class="mb-4 flex flex-wrap items-center gap-3">
          <label class="min-w-[220px] flex-1">
            <span class="sr-only">{{ t('admin.upstreamConfigs.operations.selectUpstream') }}</span>
            <Select v-model="selectedOperationsConfigId" :options="operationConfigOptions" data-test="rate-trend-upstream-select" @change="handleOperationsConfigChange('rateTrend')" />
          </label>
          <label class="min-w-[220px] flex-1">
            <span class="sr-only">{{ t('admin.upstreamConfigs.operations.selectKey') }}</span>
            <Select v-model="selectedRateKeyId" :options="rateKeyOptions" data-test="rate-trend-key-select" @change="loadKeyRateTrend()" />
          </label>
          <div class="inline-flex rounded-lg border border-gray-200 p-1 dark:border-dark-700" data-test="rate-trend-range-control">
            <button
              v-for="range in trendRanges"
              :key="range"
              type="button"
              :class="[
                'min-w-12 rounded-md px-3 py-1.5 text-xs font-medium transition-colors',
                rateTrendRange === range
                  ? 'bg-primary-600 text-white'
                  : 'text-gray-600 hover:bg-gray-100 dark:text-dark-300 dark:hover:bg-dark-700'
              ]"
              :data-test="`rate-trend-range-${range}`"
              @click="setRateTrendRange(range)"
            >
              {{ range }}
            </button>
          </div>
        </div>
      </template>

      <template v-if="operationsDrawerMode === 'syncRuns'">
        <div v-if="operationLoading.syncRuns" class="drawer-state">{{ t('common.loading') }}</div>
        <div v-else-if="selectedSyncRun" class="space-y-4" data-test="sync-run-detail">
          <div class="grid grid-cols-2 gap-3 sm:grid-cols-4">
            <div class="metric-block"><span>{{ t('admin.upstreamConfigs.operations.success') }}</span><strong>{{ selectedSyncRun.success_configs }}</strong></div>
            <div class="metric-block"><span>{{ t('admin.upstreamConfigs.operations.partial') }}</span><strong>{{ selectedSyncRun.partial_configs }}</strong></div>
            <div class="metric-block"><span>{{ t('admin.upstreamConfigs.operations.failed') }}</span><strong>{{ selectedSyncRun.failed_configs }}</strong></div>
            <div class="metric-block"><span>{{ t('admin.upstreamConfigs.operations.duration') }}</span><strong>{{ syncRunDuration(selectedSyncRun) }}</strong></div>
          </div>
          <div v-for="record in selectedSyncRun.results || []" :key="record.id" class="operation-row">
            <div class="flex items-start justify-between gap-3">
              <div>
                <div class="font-medium text-gray-900 dark:text-gray-100">{{ record.config_name }}</div>
                <div class="mt-1 text-xs text-gray-500 dark:text-dark-400">{{ record.stage || record.provider }} · {{ record.duration_ms }}ms</div>
              </div>
              <span :class="statusBadgeClass(record.status)">{{ statusLabel(record.status) }}</span>
            </div>
            <p v-if="record.safe_message" class="mt-2 text-sm text-red-600 dark:text-red-400">{{ record.safe_message }}</p>
            <div class="mt-2 text-xs text-gray-500 dark:text-dark-400">
              {{ t('admin.upstreamConfigs.operations.syncRecordSummary', {
                remote: record.remote_key_count,
                persisted: record.persisted_key_count,
                accounts: record.updated_account_count
              }) }}
            </div>
          </div>
          <div v-if="!(selectedSyncRun.results || []).length" class="drawer-state">{{ t('admin.upstreamConfigs.operations.emptySyncResults') }}</div>
        </div>
        <div v-else class="space-y-3">
          <button
            v-for="run in syncRuns"
            :key="run.id"
            type="button"
            class="operation-row block w-full text-left hover:border-primary-300 dark:hover:border-primary-700"
            :data-test="`sync-run-${run.id}`"
            @click="openSyncRunById(run.id)"
          >
            <div class="flex items-start justify-between gap-3">
              <div>
                <div class="font-medium text-gray-900 dark:text-gray-100">#{{ run.id }} · {{ syncTriggerLabel(run.trigger) }}</div>
                <div class="mt-1 text-xs text-gray-500 dark:text-dark-400">{{ formatTime(run.started_at) }}</div>
              </div>
              <span :class="statusBadgeClass(run.status)">{{ statusLabel(run.status) }}</span>
            </div>
            <div class="mt-2 text-xs text-gray-500 dark:text-dark-400">
              {{ t('admin.upstreamConfigs.operations.syncRunSummary', {
                total: run.total_configs,
                success: run.success_configs,
                partial: run.partial_configs,
                failed: run.failed_configs
              }) }}
            </div>
          </button>
          <div v-if="!syncRuns.length" class="drawer-state">{{ t('admin.upstreamConfigs.operations.emptySyncRuns') }}</div>
          <button v-if="syncRuns.length < syncRunsTotal" type="button" class="btn btn-secondary w-full" @click="loadMoreSyncRuns">
            {{ t('admin.upstreamConfigs.operations.loadMore') }}
          </button>
        </div>
      </template>

      <template v-else-if="operationsDrawerMode === 'events'">
        <div v-if="operationLoading.events" class="drawer-state">{{ t('common.loading') }}</div>
        <div v-else class="space-y-5">
          <section v-if="incidents.length" class="space-y-3">
            <h3 class="section-title">{{ t('admin.upstreamConfigs.operations.openIncidents') }}</h3>
            <div v-for="incident in incidents" :key="incident.id" class="operation-row border-red-200 bg-red-50/40 dark:border-red-900/50 dark:bg-red-900/10">
              <div class="flex items-start justify-between gap-3">
                <div class="font-medium text-red-700 dark:text-red-300">{{ incident.type }}</div>
                <span :class="statusBadgeClass(incident.status)">{{ statusLabel(incident.status) }}</span>
              </div>
              <div class="mt-2 text-xs text-gray-600 dark:text-dark-300">{{ formatIncidentMetric(incident) }}</div>
            </div>
          </section>
          <section class="space-y-3">
            <h3 class="section-title">{{ t('admin.upstreamConfigs.operations.recentEvents') }}</h3>
            <div v-for="event in events" :key="event.id" class="operation-row">
              <div class="flex items-start justify-between gap-3">
                <div>
                  <div class="font-medium text-gray-900 dark:text-gray-100">{{ event.type }}</div>
                  <p class="mt-1 text-sm text-gray-600 dark:text-dark-300">{{ event.message }}</p>
                </div>
                <span :class="severityBadgeClass(event.severity)">{{ event.severity }}</span>
              </div>
              <div class="mt-2 text-xs text-gray-500 dark:text-dark-400">{{ formatTime(event.created_at) }}</div>
            </div>
            <div v-if="!events.length" class="drawer-state">{{ t('admin.upstreamConfigs.operations.emptyEvents') }}</div>
            <button v-if="events.length < eventsTotal" type="button" class="btn btn-secondary w-full" @click="loadMoreEvents">
              {{ t('admin.upstreamConfigs.operations.loadMore') }}
            </button>
          </section>
          <section class="space-y-3">
            <h3 class="section-title">{{ t('admin.upstreamConfigs.operations.balanceHistory') }}</h3>
            <div v-for="snapshot in balanceHistory" :key="snapshot.id" class="operation-row">
              <div class="flex items-center justify-between gap-3">
                <span class="font-medium text-gray-900 dark:text-gray-100">{{ formatCNY(snapshot.balance_cny ?? null) }}</span>
                <span class="text-xs text-gray-500 dark:text-dark-400">{{ formatTime(snapshot.observed_at) }}</span>
              </div>
              <div class="mt-1 text-xs text-gray-500 dark:text-dark-400">
                {{ snapshot.currency_source || '-' }} · {{ snapshot.currency_rate_source || '-' }}
              </div>
              <div v-if="snapshot.provider === 'newapi' && snapshot.used_cny != null" class="mt-1 text-xs text-gray-500 dark:text-dark-400">
                {{ t('admin.upstreamConfigs.balance.totalUsed', { amount: formatCNY(snapshot.used_cny) }) }}
              </div>
              <div v-else-if="snapshot.provider === 'sub2api' && snapshot.total_recharged_cny != null" class="mt-1 text-xs text-gray-500 dark:text-dark-400">
                {{ t('admin.upstreamConfigs.balance.totalRecharged', { amount: formatCNY(snapshot.total_recharged_cny) }) }}
              </div>
            </div>
            <div v-if="!balanceHistory.length" class="drawer-state">{{ t('admin.upstreamConfigs.operations.emptyBalanceHistory') }}</div>
            <button v-if="balanceHistory.length < balanceHistoryTotal" type="button" class="btn btn-secondary w-full" @click="loadMoreBalanceHistory">
              {{ t('admin.upstreamConfigs.operations.loadMore') }}
            </button>
          </section>
        </div>
      </template>

      <template v-else-if="operationsDrawerMode === 'trend'">
        <div class="space-y-4">
          <div class="grid grid-cols-2 gap-3 sm:grid-cols-5">
            <div class="metric-block"><span>{{ t('admin.upstreamConfigs.operations.requests') }}</span><strong>{{ trendRequests }}</strong></div>
            <div class="metric-block"><span>{{ t('admin.upstreamConfigs.operations.upstreamCost') }}</span><strong>{{ formatCNY(trendTotals.upstreamCost) }}</strong></div>
            <div class="metric-block"><span>{{ t('admin.upstreamConfigs.operations.billedCost') }}</span><strong>{{ formatCNY(trendTotals.billedCost) }}</strong></div>
            <div class="metric-block"><span>{{ t('admin.upstreamConfigs.operations.grossProfit') }}</span><strong>{{ formatCNY(trendTotals.grossProfit) }}</strong></div>
            <div class="metric-block"><span>{{ t('admin.upstreamConfigs.operations.unconvertedCost') }}</span><strong>{{ formatBalanceAmount(trendTotals.unconvertedCost) }}</strong></div>
          </div>
          <UpstreamCostTrendChart :points="usageTrend?.points || []" :loading="operationLoading.trend" />
          <p v-if="usageTrend?.legacy_attributed_requests" class="text-xs text-amber-600 dark:text-amber-400">
            {{ t('admin.upstreamConfigs.operations.legacyAttributed', { count: usageTrend.legacy_attributed_requests }) }}
          </p>
        </div>
      </template>

      <template v-else-if="operationsDrawerMode === 'rateTrend'">
        <div v-if="operationLoading.rateTrend" class="drawer-state">{{ t('common.loading') }}</div>
        <div v-else-if="!selectedRateKeyId" class="drawer-state">{{ t('admin.upstreamConfigs.operations.emptyRateKeys') }}</div>
        <div v-else class="space-y-4">
          <div class="grid grid-cols-2 gap-3 sm:grid-cols-5">
            <div class="metric-block"><span>{{ t('admin.upstreamConfigs.operations.currentRawRate') }}</span><strong>{{ formatRateValue(keyRateTrend?.current_raw_rate) }}</strong></div>
            <div class="metric-block"><span>{{ t('admin.upstreamConfigs.operations.currentEffectiveRate') }}</span><strong>{{ formatRateValue(keyRateTrend?.current_effective_rate) }}</strong></div>
            <div class="metric-block"><span>{{ t('admin.upstreamConfigs.operations.previousRate') }}</span><strong>{{ formatRateValue(keyRateTrend?.previous_raw_rate) }}</strong></div>
            <div class="metric-block"><span>{{ t('admin.upstreamConfigs.operations.lastChanged') }}</span><strong>{{ formatTime(keyRateTrend?.last_changed_at || null) }}</strong></div>
            <div class="metric-block"><span>{{ t('admin.upstreamConfigs.operations.observedSince') }}</span><strong>{{ formatTime(keyRateTrend?.first_observed_at || null) }}</strong></div>
          </div>
          <UpstreamKeyRateTrendChart :points="keyRateTrend?.points || []" :loading="operationLoading.rateTrend" />
          <section class="space-y-3">
            <h3 class="section-title">{{ t('admin.upstreamConfigs.operations.rateChanges') }}</h3>
            <div v-for="change in keyRateTrend?.changes || []" :key="`${change.type}-${change.occurred_at}`" class="operation-row">
              <div class="flex items-center justify-between gap-3">
                <span class="font-medium text-gray-900 dark:text-gray-100">{{ change.type }}</span>
                <span class="text-xs text-gray-500 dark:text-dark-400">{{ formatTime(change.occurred_at) }}</span>
              </div>
              <div class="mt-1 text-xs text-gray-600 dark:text-dark-300">
                {{ formatRateValue(change.old_raw_rate) }} → {{ formatRateValue(change.new_raw_rate) }}
              </div>
            </div>
            <div v-if="!(keyRateTrend?.changes || []).length" class="drawer-state">{{ t('admin.upstreamConfigs.operations.emptyRateChanges') }}</div>
          </section>
        </div>
      </template>

      <template v-else-if="operationsDrawerMode === 'settings'">
        <div v-if="operationLoading.settings" class="drawer-state">{{ t('common.loading') }}</div>
        <div v-else-if="!settingsLoaded" class="drawer-state" data-test="upstream-settings-unavailable">
          {{ t('admin.upstreamConfigs.messages.loadSettingsFailed') }}
        </div>
        <form v-else class="space-y-5" data-test="upstream-settings-form" @submit.prevent="saveUpstreamSettings">
          <label class="block space-y-2">
            <span class="input-label">{{ t('admin.upstreamConfigs.settings.lowBalanceThreshold') }}</span>
            <input
              v-model.number="settingsForm.balance_low_threshold_cny"
              class="input tabular-nums"
              data-test="low-balance-threshold-input"
              type="number"
              min="0"
              step="0.01"
              required
            />
            <span class="block text-xs text-gray-500 dark:text-dark-400">{{ t('admin.upstreamConfigs.settings.lowBalanceThresholdHint') }}</span>
          </label>
          <label class="flex cursor-pointer items-start gap-3 border-t border-gray-200 pt-5 dark:border-dark-700">
            <input
              v-model="settingsForm.sub2api_not_in_cn_confirmed"
              type="checkbox"
              class="mt-0.5 h-4 w-4 rounded border-gray-300 text-primary-600 focus:ring-primary-500 dark:border-dark-600 dark:bg-dark-800"
              data-test="sub2api-not-in-cn-confirmed"
            />
            <span class="min-w-0">
              <span class="block text-sm font-medium text-gray-900 dark:text-gray-100">{{ t('admin.upstreamConfigs.settings.sub2apiNotInCNConfirmed') }}</span>
              <span class="mt-1 block text-xs leading-5 text-gray-500 dark:text-dark-400">{{ t('admin.upstreamConfigs.settings.sub2apiNotInCNConfirmedHint') }}</span>
            </span>
          </label>
          <div class="flex justify-end">
            <button type="submit" class="btn btn-primary" :disabled="settingsSaving">
              <Icon v-if="settingsSaving" name="refresh" size="sm" class="mr-2 animate-spin" />
              {{ t('common.save') }}
            </button>
          </div>
        </form>
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
import { computed, onMounted, onUnmounted, reactive, ref, watch } from 'vue'
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
import UpstreamActionMenu from './upstream/UpstreamActionMenu.vue'
import UpstreamCostTrendChart from './upstream/UpstreamCostTrendChart.vue'
import UpstreamKeyRateTrendChart from './upstream/UpstreamKeyRateTrendChart.vue'
import type { Column } from '@/components/common/types'
import { getPersistedPageSize } from '@/composables/usePersistedPageSize'
import { adminAPI } from '@/api/admin'
import upstreamAPI, {
  type UpstreamAuthMode,
  type UpstreamConfig,
  type UpstreamBalanceSnapshot,
  type UpstreamEvent,
  type UpstreamIncident,
  type UpstreamProvider,
  type UpstreamSettings,
  type UpstreamSyncRun,
  type UpstreamSyncResult,
  type UpstreamTrendRange,
  type UpstreamUsageTrend,
  type UpstreamKeyRateCatalogItem,
  type UpstreamKeyRateTrend
} from '@/api/admin/upstreamConfigs'
import { useAppStore } from '@/stores/app'
import type { Proxy } from '@/types'
import {
  parseUpstreamTokenPaste,
  type ParsedTokenCandidate
} from '@/utils/upstreamTokenParser'

type RowAction = 'test' | 'sync'
type OperationsDrawerMode = 'syncRuns' | 'events' | 'trend' | 'rateTrend' | 'settings'
type RateRange = { min: number; max: number } | null

const ALWAYS_VISIBLE_COLUMNS = new Set(['name', 'actions'])
const HIDDEN_COLUMNS_KEY = 'upstream-config-hidden-columns'
const HIDDEN_COLUMNS_VERSION_KEY = 'upstream-config-hidden-columns-version'
const HIDDEN_COLUMNS_CURRENT_VERSION = '1'
const DEFAULT_HIDDEN_COLUMNS = ['rates']

const { t } = useI18n()
const appStore = useAppStore()

const configs = ref<UpstreamConfig[]>([])
const operationConfigs = ref<UpstreamConfig[]>([])
const operationConfigsLoaded = ref(false)
const loading = ref(false)
const saving = ref(false)
const syncingAll = ref(false)
const dialogOpen = ref(false)
const editing = ref<UpstreamConfig | null>(null)
const deletingConfig = ref<UpstreamConfig | null>(null)
const deleteDialogOpen = ref(false)
const tokenAssistantOpen = ref(false)
const busyAction = ref<{ id: number; action: RowAction } | null>(null)
const actionMenu = reactive<{ show: boolean; anchorEl: HTMLElement | null; config: UpstreamConfig | null }>({
  show: false,
  anchorEl: null,
  config: null
})
const search = ref('')
const provider = ref<string>('')
const proxies = ref<Proxy[]>([])
const loadingProxies = ref(false)
const tokenPaste = ref('')
const selectedAccessTokenId = ref('')
const selectedRefreshTokenId = ref('')
const operationsDrawerMode = ref<OperationsDrawerMode | null>(null)
const operationLoading = reactive<Record<OperationsDrawerMode, boolean>>({
  syncRuns: false,
  events: false,
  trend: false,
  rateTrend: false,
  settings: false
})
const selectedOperationsConfigId = ref<number | null>(null)
const syncRuns = ref<UpstreamSyncRun[]>([])
const syncRunsTotal = ref(0)
const selectedSyncRun = ref<UpstreamSyncRun | null>(null)
const events = ref<UpstreamEvent[]>([])
const eventsTotal = ref(0)
const incidents = ref<UpstreamIncident[]>([])
const balanceHistory = ref<UpstreamBalanceSnapshot[]>([])
const balanceHistoryTotal = ref(0)
const trendRange = ref<UpstreamTrendRange>('24h')
const trendRanges: UpstreamTrendRange[] = ['24h', '7d', '30d']
const usageTrend = ref<UpstreamUsageTrend | null>(null)
const rateTrendRange = ref<UpstreamTrendRange>('24h')
const rateTrendKeys = ref<UpstreamKeyRateCatalogItem[]>([])
const selectedRateKeyId = ref<number | null>(null)
const keyRateTrend = ref<UpstreamKeyRateTrend | null>(null)
const upstreamSettings = ref<UpstreamSettings>({ balance_low_threshold_cny: 0, sub2api_not_in_cn_confirmed: false })
const settingsForm = reactive<UpstreamSettings>({ balance_low_threshold_cny: 0, sub2api_not_in_cn_confirmed: false })
const settingsLoaded = ref(false)
const settingsSaving = ref(false)
const operationGeneration: Record<OperationsDrawerMode, number> = {
  syncRuns: 0,
  events: 0,
  trend: 0,
  rateTrend: 0,
  settings: 0
}

const pagination = reactive({
  page: 1,
  page_size: getPersistedPageSize(),
  total: 0
})

const form = reactive({
  name: '',
  provider: 'sub2api' as UpstreamProvider,
  site_url: '',
  api_url: '',
  auth_mode: 'user_login' as UpstreamAuthMode,
  proxy_id: null as number | null,
  email: '',
  username: '',
  password: '',
  cookie: '',
  newapi_access_token: '',
  newapi_user_id: '',
  access_token: '',
  refresh_token: '',
  recharge_rate: 1,
  balance_to_cny_rate: null as number | null
})

let searchTimeout: ReturnType<typeof setTimeout> | null = null

const allColumns = computed<Column[]>(() => [
  { key: 'name', label: t('admin.upstreamConfigs.columns.name') },
  { key: 'provider', label: t('admin.upstreamConfigs.columns.provider') },
  { key: 'urls', label: t('admin.upstreamConfigs.columns.address') },
  { key: 'balance', label: t('admin.upstreamConfigs.columns.balance') },
  { key: 'upstream_concurrency', label: t('admin.upstreamConfigs.columns.upstreamConcurrency') },
  { key: 'rates', label: t('admin.upstreamConfigs.columns.rates') },
  { key: 'auth_mode', label: t('admin.upstreamConfigs.columns.authMode') },
  { key: 'credentials', label: t('admin.upstreamConfigs.columns.credentials') },
  { key: 'last_success_at', label: t('admin.upstreamConfigs.columns.lastSync') },
  { key: 'actions', label: t('admin.upstreamConfigs.columns.actions'), class: 'min-w-[300px]' }
])

const toggleableColumns = computed(() =>
  allColumns.value.filter((column) => !ALWAYS_VISIBLE_COLUMNS.has(column.key))
)
const hiddenColumns = reactive<Set<string>>(new Set())
const showColumnDropdown = ref(false)
const columnDropdownRef = ref<HTMLElement | null>(null)

const getValidHiddenColumnKeys = () =>
  new Set(toggleableColumns.value.map((column) => column.key))

const orderedHiddenColumnKeys = () => {
  const validKeys = getValidHiddenColumnKeys()
  return allColumns.value
    .map((column) => column.key)
    .filter((key) => validKeys.has(key) && hiddenColumns.has(key))
}

const saveColumnsToStorage = () => {
  try {
    localStorage.setItem(HIDDEN_COLUMNS_KEY, JSON.stringify(orderedHiddenColumnKeys()))
    localStorage.setItem(HIDDEN_COLUMNS_VERSION_KEY, HIDDEN_COLUMNS_CURRENT_VERSION)
  } catch (error) {
    console.error('Failed to save upstream config column settings:', error)
  }
}

const loadSavedColumns = () => {
  hiddenColumns.clear()
  const validKeys = getValidHiddenColumnKeys()
  let shouldUseDefaults = false

  try {
    const saved = localStorage.getItem(HIDDEN_COLUMNS_KEY)
    const savedVersion = localStorage.getItem(HIDDEN_COLUMNS_VERSION_KEY)

    if (!saved) {
      shouldUseDefaults = true
    } else {
      const parsed: unknown = JSON.parse(saved)
      if (!Array.isArray(parsed)) {
        shouldUseDefaults = true
      } else {
        parsed
          .filter((key): key is string => typeof key === 'string' && validKeys.has(key))
          .forEach((key) => hiddenColumns.add(key))

        if (savedVersion !== HIDDEN_COLUMNS_CURRENT_VERSION) {
          DEFAULT_HIDDEN_COLUMNS.forEach((key) => {
            if (validKeys.has(key)) hiddenColumns.add(key)
          })
        }
      }
    }
  } catch (error) {
    console.error('Failed to load upstream config column settings:', error)
    shouldUseDefaults = true
  }

  if (shouldUseDefaults) {
    hiddenColumns.clear()
    DEFAULT_HIDDEN_COLUMNS.forEach((key) => {
      if (validKeys.has(key)) hiddenColumns.add(key)
    })
  }

  saveColumnsToStorage()
}

const isColumnVisible = (key: string) => !hiddenColumns.has(key)

const toggleColumn = (key: string) => {
  if (!getValidHiddenColumnKeys().has(key)) return

  if (hiddenColumns.has(key)) {
    hiddenColumns.delete(key)
  } else {
    hiddenColumns.add(key)
  }
  saveColumnsToStorage()
}

const columns = computed<Column[]>(() =>
  allColumns.value.filter(
    (column) => ALWAYS_VISIBLE_COLUMNS.has(column.key) || !hiddenColumns.has(column.key)
  )
)

if (typeof window !== 'undefined') {
  loadSavedColumns()
}

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

const authModeOptions = computed<SelectOption[]>(() => form.provider === 'newapi'
  ? [
      { value: 'user_login', label: t('admin.upstreamConfigs.authModes.userLogin') },
      { value: 'cookie', label: t('admin.upstreamConfigs.authModes.cookie') },
      { value: 'access_token', label: t('admin.upstreamConfigs.authModes.accessToken') }
    ]
  : [
      { value: 'user_login', label: t('admin.upstreamConfigs.authModes.userLogin') },
      { value: 'manual_jwt', label: t('admin.upstreamConfigs.authModes.manualJwt') }
    ])

watch(() => form.provider, (value) => {
  if (value === 'newapi' && !['user_login', 'cookie', 'access_token'].includes(form.auth_mode)) {
    form.auth_mode = 'user_login'
  } else if (value === 'sub2api' && !['user_login', 'manual_jwt'].includes(form.auth_mode)) {
    form.auth_mode = 'user_login'
  }
})

const operationConfigOptions = computed<SelectOption[]>(() =>
  operationConfigs.value.map((item) => ({ value: item.id, label: item.name }))
)

const rateKeyOptions = computed<SelectOption[]>(() =>
  rateTrendKeys.value.map((key) => ({
    value: key.key_id,
    label: `${key.status === 'deleted' || key.deleted_at ? `${t('admin.upstreamConfigs.rateTrend.deletedKey')} ` : ''}${key.name || t('admin.upstreamConfigs.rateTrend.unnamedKey')} · #${key.key_id}${key.remote_key_id ? ` / remote #${key.remote_key_id}` : ''} · ${formatRateValue(key.current_raw_rate)}`
  }))
)

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
  loadSettings(true)
  document.addEventListener('click', handleClickOutside)
})

onUnmounted(() => {
  if (searchTimeout) clearTimeout(searchTimeout)
  document.removeEventListener('click', handleClickOutside)
})

function handleClickOutside(event: MouseEvent) {
  const target = event.target as Node
  if (columnDropdownRef.value && !columnDropdownRef.value.contains(target)) {
    showColumnDropdown.value = false
  }
}

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
    site_url: '',
    api_url: '',
    auth_mode: 'user_login',
    proxy_id: null,
    email: '',
    username: '',
    password: '',
    cookie: '',
    newapi_access_token: '',
    newapi_user_id: '',
    access_token: '',
    refresh_token: '',
    recharge_rate: 1,
    balance_to_cny_rate: null as number | null
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
    site_url: item.site_url,
    api_url: item.api_url ?? '',
    auth_mode: item.auth_mode,
    proxy_id: item.proxy_id ?? null,
    email: '',
    username: '',
    password: '',
    cookie: '',
    newapi_access_token: '',
    newapi_user_id: '',
    access_token: '',
    refresh_token: '',
    recharge_rate: item.recharge_rate || 1,
    balance_to_cny_rate: item.balance_to_cny_rate ?? null
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
    const url = new URL(form.site_url.trim())
    if (!['http:', 'https:'].includes(url.protocol)) return null
    url.pathname = '/login'
    url.search = ''
    url.hash = ''
    return url.toString()
  } catch {
    return null
  }
}

function openUpstreamDashboard(item: UpstreamConfig) {
  const url = buildUpstreamDashboardURL(item)
  if (!url) {
    appStore.showError(t('admin.upstreamConfigs.messages.invalidDashboardUrl'))
    return
  }
  window.open(url, '_blank', 'noopener,noreferrer')
}

function buildUpstreamDashboardURL(item: UpstreamConfig): string | null {
  try {
    const url = new URL((item.site_url || '').trim())
    if (!['http:', 'https:'].includes(url.protocol)) return null
    url.pathname = '/dashboard'
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
  const validationError = validateFormBeforeSave()
  if (validationError) {
    appStore.showError(validationError)
    return
  }
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
    if (form.provider === 'newapi' && form.auth_mode === 'user_login') {
      if (form.username) credentials.newapi_login_username = form.username
      if (form.password) credentials.newapi_login_password = form.password
    }
    if (form.provider === 'newapi' && form.auth_mode === 'cookie') {
      if (form.cookie) credentials.newapi_cookie = form.cookie
      if (form.newapi_user_id) credentials.newapi_user_id = form.newapi_user_id
    }
    if (form.provider === 'newapi' && form.auth_mode === 'access_token') {
      if (form.newapi_access_token) credentials.newapi_access_token = form.newapi_access_token
      if (form.newapi_user_id) credentials.newapi_user_id = form.newapi_user_id
    }

    const payload = {
      name: form.name,
      provider: form.provider,
      site_url: form.site_url,
      api_url: form.api_url.trim() || null,
      clear_api_url: Boolean(editing.value && !form.api_url.trim()),
      auth_mode: form.provider === 'other' ? 'user_login' : form.auth_mode,
      proxy_id: form.proxy_id,
      clear_proxy: Boolean(editing.value && form.proxy_id === null),
      recharge_rate: form.recharge_rate,
      balance_to_cny_rate: finitePositiveNumber(form.balance_to_cny_rate),
      clear_balance_to_cny_rate: Boolean(editing.value && form.balance_to_cny_rate === null),
      credentials
    }

    let savedConfig
    if (editing.value) {
      savedConfig = await upstreamAPI.update(editing.value.id, payload)
    } else {
      savedConfig = await upstreamAPI.create(payload)
    }
    operationConfigsLoaded.value = false
    dialogOpen.value = false
    if (savedConfig?.provider === 'sub2api' || savedConfig?.provider === 'newapi') {
      await syncAfterSave(savedConfig.id)
    } else {
      appStore.showSuccess(editing.value
        ? t('admin.upstreamConfigs.messages.updated')
        : t('admin.upstreamConfigs.messages.created'))
    }
    await loadConfigs()
  } catch (error: any) {
    appStore.showError(apiErrorMessage(error, t('admin.upstreamConfigs.messages.saveFailed')))
  } finally {
    saving.value = false
  }
}

function validateFormBeforeSave(): string {
  if (!Number.isFinite(form.recharge_rate) || form.recharge_rate <= 0 || form.recharge_rate > 100) {
    return t('admin.upstreamConfigs.messages.rechargeRateInvalid')
  }
  if (form.balance_to_cny_rate !== null && finitePositiveNumber(form.balance_to_cny_rate) === null) {
    return t('admin.upstreamConfigs.messages.balanceToCnyRateInvalid')
  }
  if (form.provider !== 'newapi') return ''
  const status = editing.value?.credentials_status || {}
  if (form.auth_mode === 'cookie') {
    if (!form.newapi_user_id && !status.has_newapi_user_id) return t('admin.upstreamConfigs.messages.newapiUserIdRequired')
    if (!form.cookie && !status.has_newapi_cookie) return t('admin.upstreamConfigs.messages.newapiCookieRequired')
    return ''
  }
  if (form.auth_mode === 'access_token') {
    if (!form.newapi_user_id && !status.has_newapi_user_id) return t('admin.upstreamConfigs.messages.newapiUserIdRequired')
    if (!form.newapi_access_token && !status.has_newapi_access_token) return t('admin.upstreamConfigs.messages.newapiAccessTokenRequired')
    return ''
  }
  if (!form.username && !status.has_newapi_login_username) return t('admin.upstreamConfigs.messages.newapiUsernameRequired')
  if (!form.password && !status.has_newapi_login_password) return t('admin.upstreamConfigs.messages.newapiPasswordRequired')
  return ''
}

async function syncAfterSave(id: number) {
  try {
    const res = await upstreamAPI.syncKeys(id)
    const keys = res.key_count ?? res.keys?.length ?? 0
    const accounts = res.updated_account_count ?? 0
    if (res.result?.status === 'partial') {
      appStore.showError(t('admin.upstreamConfigs.messages.savedAndSyncedPartial', { keys, accounts }))
    } else {
      appStore.showSuccess(t('admin.upstreamConfigs.messages.savedAndSynced', { keys, accounts }))
    }
  } catch (error: any) {
    appStore.showError(t('admin.upstreamConfigs.messages.savedButSyncFailed', {
      error: apiErrorMessage(error, t('admin.upstreamConfigs.messages.syncFailed'))
    }))
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
    if (res.result?.status === 'partial') {
      appStore.showError(t('admin.upstreamConfigs.messages.syncPartial', { count }))
    } else {
      appStore.showSuccess(t('admin.upstreamConfigs.messages.syncSuccess', { count }))
    }
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
    const normalizedStatuses = results.map((item) => item.status || (item.success ? 'succeeded' : 'failed'))
    const success = normalizedStatuses.filter((status) => status === 'succeeded').length
    const partial = normalizedStatuses.filter((status) => status === 'partial').length
    const failed = normalizedStatuses.filter((status) => status !== 'succeeded' && status !== 'partial').length
    const keys = results.reduce((sum, item) => sum + (item.key_count || 0), 0)
    if (failed > 0 || partial > 0) {
      appStore.showError(t('admin.upstreamConfigs.messages.syncAllPartial', { success, partial, failed, keys }))
    } else {
      appStore.showSuccess(t('admin.upstreamConfigs.messages.syncAllSuccess', { success, keys }))
    }
    const runId = res.run_id || results.find((item) => item.run_id)?.run_id
    if (runId) {
      await openSyncRunFromBatch(runId, results)
    }
    await loadConfigs()
  } catch (error: any) {
    appStore.showError(apiErrorMessage(error, t('admin.upstreamConfigs.messages.syncAllFailed')))
  } finally {
    syncingAll.value = false
  }
}

const operationsDrawerTitle = computed(() => {
  if (operationsDrawerMode.value === 'syncRuns') return t('admin.upstreamConfigs.operations.syncRunsTitle')
  if (operationsDrawerMode.value === 'events') return t('admin.upstreamConfigs.operations.eventsTitle')
  if (operationsDrawerMode.value === 'trend') return t('admin.upstreamConfigs.operations.trendTitle')
  if (operationsDrawerMode.value === 'rateTrend') return t('admin.upstreamConfigs.operations.rateTrendTitle')
  if (operationsDrawerMode.value === 'settings') return t('admin.upstreamConfigs.operations.settingsTitle')
  return ''
})

const operationsDrawerSubtitle = computed(() => {
  if (operationsDrawerMode.value === 'syncRuns' && selectedSyncRun.value) {
    return t('admin.upstreamConfigs.operations.runSubtitle', { id: selectedSyncRun.value.id })
  }
  if (operationsDrawerMode.value !== 'events' && operationsDrawerMode.value !== 'trend' && operationsDrawerMode.value !== 'rateTrend') return ''
  const config = operationConfigs.value.find((item) => item.id === selectedOperationsConfigId.value)
  return config?.name || ''
})

const operationsDialogWidth = computed<'normal' | 'wide' | 'extra-wide'>(() => {
  if (operationsDrawerMode.value === 'events') return 'wide'
  if (operationsDrawerMode.value === 'settings') return 'normal'
  return 'extra-wide'
})

const trendTotals = computed(() => {
  const points = usageTrend.value?.points || []
  return points.reduce((total, point) => ({
    upstreamCost: total.upstreamCost + point.upstream_cost,
    billedCost: total.billedCost + point.billed_cost,
    grossProfit: total.grossProfit + point.gross_profit,
    unconvertedCost: total.unconvertedCost + point.unconverted_cost
  }), { upstreamCost: 0, billedCost: 0, grossProfit: 0, unconvertedCost: 0 })
})

const trendRequests = computed(() =>
  (usageTrend.value?.points || []).reduce((sum, point) => sum + point.requests, 0).toLocaleString()
)

function ensureSelectedOperationsConfig() {
  if (!operationConfigs.value.some((item) => item.id === selectedOperationsConfigId.value)) {
    selectedOperationsConfigId.value = operationConfigs.value[0]?.id || null
  }
}

async function ensureOperationConfigs() {
  if (operationConfigsLoaded.value) return
  const byId = new Map<number, UpstreamConfig>()
  const pageSize = 200
  let page = 1
  let total = Number.POSITIVE_INFINITY
  while (byId.size < total) {
    const response = await upstreamAPI.list(page, pageSize, {})
    for (const item of response.items || []) byId.set(item.id, item)
    total = response.total || byId.size
    if (!(response.items || []).length || page * pageSize >= total) break
    page += 1
  }
  operationConfigs.value = [...byId.values()]
  operationConfigsLoaded.value = true
  ensureSelectedOperationsConfig()
}

function closeOperationsDrawer() {
  if (operationsDrawerMode.value === 'settings' && settingsSaving.value) return
  for (const mode of Object.keys(operationGeneration) as OperationsDrawerMode[]) {
    operationGeneration[mode] += 1
    operationLoading[mode] = false
  }
  operationsDrawerMode.value = null
  selectedSyncRun.value = null
}

function openActionMenu(config: UpstreamConfig, event: MouseEvent) {
  actionMenu.show = true
  actionMenu.anchorEl = event.currentTarget as HTMLElement
  actionMenu.config = config
}

function closeActionMenu() {
  actionMenu.show = false
  actionMenu.anchorEl = null
  actionMenu.config = null
}

function requestToken(mode: OperationsDrawerMode) {
  const generation = ++operationGeneration[mode]
  operationLoading[mode] = true
  return generation
}

function requestIsCurrent(mode: OperationsDrawerMode, generation: number) {
  return operationsDrawerMode.value === mode && operationGeneration[mode] === generation
}

function clearOperationContent(mode: 'events' | 'trend' | 'rateTrend') {
  if (mode === 'events') {
    events.value = []
    eventsTotal.value = 0
    incidents.value = []
    balanceHistory.value = []
    balanceHistoryTotal.value = 0
    return
  }
  if (mode === 'trend') {
    usageTrend.value = null
    return
  }
  rateTrendKeys.value = []
  selectedRateKeyId.value = null
  keyRateTrend.value = null
}

function handleOperationsConfigChange(mode: 'events' | 'trend' | 'rateTrend') {
  operationGeneration[mode] += 1
  clearOperationContent(mode)
  if (mode === 'events') loadEventsAndIncidents()
  else if (mode === 'trend') loadTrend()
  else loadRateTrendKeys()
}

async function openSyncRuns() {
  operationsDrawerMode.value = 'syncRuns'
  selectedSyncRun.value = null
  await loadSyncRuns()
}

async function loadSyncRuns() {
  const generation = requestToken('syncRuns')
  try {
    const response = await upstreamAPI.listSyncRuns(50, 0)
    if (!requestIsCurrent('syncRuns', generation)) return
    syncRuns.value = response.items
    syncRunsTotal.value = response.total
  } catch (error: any) {
    if (requestIsCurrent('syncRuns', generation)) {
      appStore.showError(apiErrorMessage(error, t('admin.upstreamConfigs.messages.loadSyncRunsFailed')))
    }
  } finally {
    if (requestIsCurrent('syncRuns', generation)) operationLoading.syncRuns = false
  }
}

async function loadMoreSyncRuns() {
  const generation = operationGeneration.syncRuns
  const offset = syncRuns.value.length
  operationLoading.syncRuns = true
  try {
    const response = await upstreamAPI.listSyncRuns(50, offset)
    if (!requestIsCurrent('syncRuns', generation) || syncRuns.value.length !== offset) return
    syncRuns.value.push(...response.items)
    syncRunsTotal.value = response.total
  } finally {
    if (requestIsCurrent('syncRuns', generation)) operationLoading.syncRuns = false
  }
}

async function openSyncRunById(runId: number, silent = false) {
  const generation = requestToken('syncRuns')
  try {
    const run = await upstreamAPI.getSyncRun(runId)
    if (requestIsCurrent('syncRuns', generation)) selectedSyncRun.value = run
  } catch (error: any) {
    if (requestIsCurrent('syncRuns', generation) && !silent) {
      appStore.showError(apiErrorMessage(error, t('admin.upstreamConfigs.messages.loadSyncRunFailed')))
    }
  } finally {
    if (requestIsCurrent('syncRuns', generation)) operationLoading.syncRuns = false
  }
}

async function openSyncRunFromBatch(runId: number, results: UpstreamSyncResult[]) {
  operationsDrawerMode.value = 'syncRuns'
  const generation = requestToken('syncRuns')
  try {
    const run = await upstreamAPI.getSyncRun(runId)
    if (requestIsCurrent('syncRuns', generation)) selectedSyncRun.value = run
  } catch {
    if (requestIsCurrent('syncRuns', generation)) selectedSyncRun.value = syncRunFromBatch(runId, results)
  } finally {
    if (requestIsCurrent('syncRuns', generation)) operationLoading.syncRuns = false
  }
}

function syncRunFromBatch(runId: number, results: UpstreamSyncResult[]): UpstreamSyncRun {
  const normalizedStatuses = results.map((item) => item.status || (item.success ? 'succeeded' : 'failed'))
  const success = normalizedStatuses.filter((status) => status === 'succeeded').length
  const partial = normalizedStatuses.filter((status) => status === 'partial').length
  const failed = results.length - success - partial
  return {
    id: runId,
    trigger: 'manual_batch',
    status: failed || partial ? 'partial' : 'succeeded',
    total_configs: results.length,
    success_configs: success,
    partial_configs: partial,
    failed_configs: failed,
    started_at: new Date().toISOString(),
    finished_at: new Date().toISOString(),
    results: results.map((item, index) => ({
      id: index + 1,
      run_id: runId,
      config_id: item.config_id,
      config_name: item.name,
      provider: item.provider || '',
      status: item.status || (item.success ? 'succeeded' : 'failed'),
      stage: item.stage,
      error_code: item.error_code,
      safe_message: item.error,
      retryable: !!item.retryable,
      remote_key_count: item.key_count,
      persisted_key_count: item.key_count,
      fallback_key_count: item.fallback_key_count || 0,
      unresolved_key_count: item.unresolved_key_count || 0,
      updated_account_count: item.updated_account_count,
      warnings: item.warnings,
      duration_ms: item.duration_ms || 0,
      started_at: new Date().toISOString(),
      finished_at: new Date().toISOString()
    }))
  }
}

async function openEvents() {
  const generation = ++operationGeneration.events
  operationsDrawerMode.value = 'events'
  clearOperationContent('events')
  await ensureOperationConfigs()
  if (!requestIsCurrent('events', generation)) return
  ensureSelectedOperationsConfig()
  await loadEventsAndIncidents()
}

async function loadEventsAndIncidents() {
  if (!selectedOperationsConfigId.value) return
  const configId = Number(selectedOperationsConfigId.value)
  const generation = requestToken('events')
  try {
    const [eventResponse, incidentResponse, balanceResponse] = await Promise.all([
      upstreamAPI.listEvents(configId, 50, 0),
      upstreamAPI.listIncidents(configId, 'open', 50, 0),
      upstreamAPI.listBalanceHistory(configId, 50, 0)
    ])
    if (!requestIsCurrent('events', generation) || Number(selectedOperationsConfigId.value) !== configId) return
    events.value = eventResponse.items
    eventsTotal.value = eventResponse.total
    incidents.value = incidentResponse.items
    balanceHistory.value = balanceResponse.items
    balanceHistoryTotal.value = balanceResponse.total
  } catch (error: any) {
    if (requestIsCurrent('events', generation)) {
      appStore.showError(apiErrorMessage(error, t('admin.upstreamConfigs.messages.loadEventsFailed')))
    }
  } finally {
    if (requestIsCurrent('events', generation)) operationLoading.events = false
  }
}

async function loadMoreEvents() {
  if (!selectedOperationsConfigId.value) return
  const configId = Number(selectedOperationsConfigId.value)
  const generation = operationGeneration.events
  const offset = events.value.length
  operationLoading.events = true
  try {
    const response = await upstreamAPI.listEvents(configId, 50, offset)
    if (!requestIsCurrent('events', generation) || Number(selectedOperationsConfigId.value) !== configId || events.value.length !== offset) return
    events.value.push(...response.items)
    eventsTotal.value = response.total
  } finally {
    if (requestIsCurrent('events', generation)) operationLoading.events = false
  }
}

async function loadMoreBalanceHistory() {
  if (!selectedOperationsConfigId.value) return
  const configId = Number(selectedOperationsConfigId.value)
  const generation = operationGeneration.events
  const offset = balanceHistory.value.length
  operationLoading.events = true
  try {
    const response = await upstreamAPI.listBalanceHistory(configId, 50, offset)
    if (!requestIsCurrent('events', generation) || Number(selectedOperationsConfigId.value) !== configId || balanceHistory.value.length !== offset) return
    balanceHistory.value.push(...response.items)
    balanceHistoryTotal.value = response.total
  } finally {
    if (requestIsCurrent('events', generation)) operationLoading.events = false
  }
}

async function openTrend(config?: UpstreamConfig) {
  const generation = ++operationGeneration.trend
  operationsDrawerMode.value = 'trend'
  clearOperationContent('trend')
  if (config) {
    if (!operationConfigs.value.some((item) => item.id === config.id)) {
      operationConfigs.value = [config, ...operationConfigs.value]
    }
    selectedOperationsConfigId.value = config.id
    await Promise.all([loadTrend(), ensureOperationConfigs()])
    return
  }
  await ensureOperationConfigs()
  if (!requestIsCurrent('trend', generation)) return
  ensureSelectedOperationsConfig()
  await loadTrend()
}

function setTrendRange(range: UpstreamTrendRange) {
  if (trendRange.value === range) return
  trendRange.value = range
  loadTrend()
}

async function loadTrend() {
  if (!selectedOperationsConfigId.value) return
  const configId = Number(selectedOperationsConfigId.value)
  const range = trendRange.value
  const generation = requestToken('trend')
  try {
    const response = await upstreamAPI.getUsageTrend(configId, range)
    if (requestIsCurrent('trend', generation) && Number(selectedOperationsConfigId.value) === configId && trendRange.value === range) usageTrend.value = response
  } catch (error: any) {
    if (requestIsCurrent('trend', generation)) {
      usageTrend.value = null
      appStore.showError(apiErrorMessage(error, t('admin.upstreamConfigs.messages.loadTrendFailed')))
    }
  } finally {
    if (requestIsCurrent('trend', generation)) operationLoading.trend = false
  }
}

async function openRateTrend(config?: UpstreamConfig) {
  const generation = ++operationGeneration.rateTrend
  operationsDrawerMode.value = 'rateTrend'
  clearOperationContent('rateTrend')
  if (config) {
    if (!operationConfigs.value.some((item) => item.id === config.id)) {
      operationConfigs.value = [config, ...operationConfigs.value]
    }
    selectedOperationsConfigId.value = config.id
    await Promise.all([loadRateTrendKeys(), ensureOperationConfigs()])
    return
  }
  await ensureOperationConfigs()
  if (!requestIsCurrent('rateTrend', generation)) return
  ensureSelectedOperationsConfig()
  await loadRateTrendKeys()
}

async function loadRateTrendKeys() {
  if (!selectedOperationsConfigId.value) return
  const configId = Number(selectedOperationsConfigId.value)
  const generation = requestToken('rateTrend')
  try {
    const keys = await upstreamAPI.listKeyRateTrendKeys(configId)
    if (!requestIsCurrent('rateTrend', generation) || Number(selectedOperationsConfigId.value) !== configId) return
    rateTrendKeys.value = keys
    if (!keys.some((key) => key.key_id === selectedRateKeyId.value)) {
      selectedRateKeyId.value = keys[0]?.key_id || null
    }
    if (selectedRateKeyId.value) await loadKeyRateTrend(generation)
  } catch (error: any) {
    if (requestIsCurrent('rateTrend', generation)) {
      rateTrendKeys.value = []
      selectedRateKeyId.value = null
      keyRateTrend.value = null
      appStore.showError(apiErrorMessage(error, t('admin.upstreamConfigs.messages.loadRateTrendFailed')))
    }
  } finally {
    if (requestIsCurrent('rateTrend', generation)) operationLoading.rateTrend = false
  }
}

async function loadKeyRateTrend(existingGeneration?: number) {
  if (!selectedOperationsConfigId.value || !selectedRateKeyId.value) {
    keyRateTrend.value = null
    return
  }
  const configId = Number(selectedOperationsConfigId.value)
  const keyId = Number(selectedRateKeyId.value)
  const range = rateTrendRange.value
  const generation = existingGeneration ?? requestToken('rateTrend')
  try {
    const response = await upstreamAPI.getKeyRateTrend(configId, keyId, range)
    if (requestIsCurrent('rateTrend', generation) && Number(selectedOperationsConfigId.value) === configId && Number(selectedRateKeyId.value) === keyId && rateTrendRange.value === range) {
      keyRateTrend.value = response
    }
  } catch (error: any) {
    if (requestIsCurrent('rateTrend', generation)) {
      keyRateTrend.value = null
      appStore.showError(apiErrorMessage(error, t('admin.upstreamConfigs.messages.loadRateTrendFailed')))
    }
  } finally {
    if (requestIsCurrent('rateTrend', generation)) operationLoading.rateTrend = false
  }
}

function setRateTrendRange(range: UpstreamTrendRange) {
  if (rateTrendRange.value === range) return
  rateTrendRange.value = range
  loadKeyRateTrend()
}

async function openSettings() {
  operationsDrawerMode.value = 'settings'
  settingsLoaded.value = false
  await loadSettings(false)
}

async function loadSettings(silent: boolean) {
  const generation = ++operationGeneration.settings
  if (!silent) operationLoading.settings = true
  try {
    const response = await upstreamAPI.getSettings()
    if (operationGeneration.settings !== generation) return
    if (!silent && operationsDrawerMode.value !== 'settings') return
    upstreamSettings.value = response
    settingsForm.balance_low_threshold_cny = response.balance_low_threshold_cny
    settingsForm.sub2api_not_in_cn_confirmed = response.sub2api_not_in_cn_confirmed
    settingsLoaded.value = true
  } catch (error: any) {
    if (operationGeneration.settings === generation) settingsLoaded.value = false
    if (!silent && requestIsCurrent('settings', generation)) appStore.showError(apiErrorMessage(error, t('admin.upstreamConfigs.messages.loadSettingsFailed')))
  } finally {
    if (!silent && requestIsCurrent('settings', generation)) operationLoading.settings = false
  }
}

async function saveUpstreamSettings() {
  if (!settingsLoaded.value) return
  if (!Number.isFinite(settingsForm.balance_low_threshold_cny) || settingsForm.balance_low_threshold_cny < 0) return
  const generation = operationGeneration.settings
  settingsSaving.value = true
  try {
    const response = await upstreamAPI.updateSettings({
      balance_low_threshold_cny: settingsForm.balance_low_threshold_cny,
      sub2api_not_in_cn_confirmed: settingsForm.sub2api_not_in_cn_confirmed
    })
    if (!requestIsCurrent('settings', generation)) return
    upstreamSettings.value = response
    settingsForm.balance_low_threshold_cny = response.balance_low_threshold_cny
    settingsForm.sub2api_not_in_cn_confirmed = response.sub2api_not_in_cn_confirmed
    appStore.showSuccess(t('admin.upstreamConfigs.messages.settingsSaved'))
  } catch (error: any) {
    if (requestIsCurrent('settings', generation)) {
      appStore.showError(apiErrorMessage(error, t('admin.upstreamConfigs.messages.saveSettingsFailed')))
    }
  } finally {
    settingsSaving.value = false
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
    operationConfigsLoaded.value = false
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
  if (value === 'manual_jwt') return t('admin.upstreamConfigs.authModes.manualJwtShort')
  if (value === 'cookie') return t('admin.upstreamConfigs.authModes.cookieShort')
  if (value === 'access_token') return t('admin.upstreamConfigs.authModes.accessTokenShort')
  return t('admin.upstreamConfigs.authModes.userLoginShort')
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
  if (item.provider === 'newapi') {
    if (item.auth_mode === 'cookie') {
      return [
        { label: t('admin.upstreamConfigs.credentialStatus.userId', { status: credentialStatusLabel(!!status.has_newapi_user_id) }), ok: !!status.has_newapi_user_id },
        { label: t('admin.upstreamConfigs.credentialStatus.cookie', { status: credentialStatusLabel(!!status.has_newapi_cookie) }), ok: !!status.has_newapi_cookie }
      ]
    }
    if (item.auth_mode === 'access_token') {
      return [
        { label: t('admin.upstreamConfigs.credentialStatus.userId', { status: credentialStatusLabel(!!status.has_newapi_user_id) }), ok: !!status.has_newapi_user_id },
        { label: t('admin.upstreamConfigs.credentialStatus.newapiAccessToken', { status: credentialStatusLabel(!!status.has_newapi_access_token) }), ok: !!status.has_newapi_access_token }
      ]
    }
    return [
      { label: t('admin.upstreamConfigs.credentialStatus.username', { status: credentialStatusLabel(!!status.has_newapi_login_username) }), ok: !!status.has_newapi_login_username },
      { label: t('admin.upstreamConfigs.credentialStatus.password', { status: credentialStatusLabel(!!status.has_newapi_login_password) }), ok: !!status.has_newapi_login_password }
    ]
  }
  if (item.auth_mode === 'manual_jwt') {
    return [
      { label: t('admin.upstreamConfigs.credentialStatus.accessToken', { status: credentialStatusLabel(!!status.has_access_token) }), ok: !!status.has_access_token },
      { label: t('admin.upstreamConfigs.credentialStatus.refreshToken', { status: credentialStatusLabel(!!status.has_refresh_token) }), ok: !!status.has_refresh_token }
    ]
  }
  return [
    { label: t('admin.upstreamConfigs.credentialStatus.email', { status: credentialStatusLabel(!!status.has_login_email) }), ok: !!status.has_login_email },
    { label: t('admin.upstreamConfigs.credentialStatus.password', { status: credentialStatusLabel(!!status.has_login_password) }), ok: !!status.has_login_password }
  ]
}

function credentialStatusLabel(ok: boolean) {
  return ok ? t('admin.upstreamConfigs.credentialStatus.configured') : t('admin.upstreamConfigs.credentialStatus.missing')
}

function upstreamBalanceCNY(item: UpstreamConfig): number | null {
  if (item.provider === 'sub2api') {
    return finiteNumberFromExtra(item.extra?.balance_cny)
      ?? finiteNumberFromExtra(item.extra?.sub2api_balance)
  }
  const amount = newAPIAmount(item, 'balance')
  const rate = explicitCNYRate(item)
  if (amount !== null && rate !== null) return amount * rate
  return finiteNumberFromExtra(item.extra?.balance_cny)
}

function upstreamTotalAmountCNY(item: UpstreamConfig): number | null {
  if (item.provider === 'sub2api') {
    return finiteNumberFromExtra(item.extra?.total_recharged_cny)
      ?? finiteNumberFromExtra(item.extra?.sub2api_total_recharged)
  }
  const amount = newAPIAmount(item, 'total')
  const rate = explicitCNYRate(item)
  if (amount !== null && rate !== null) return amount * rate
  return finiteNumberFromExtra(item.extra?.total_recharged_cny)
}

function upstreamBalanceError(item: UpstreamConfig): string {
  if (item.provider === 'newapi') {
    const value = item.extra?.upstream_provider_snapshot_last_error
    return typeof value === 'string' ? value.trim() : ''
  }
  const value = item.extra?.sub2api_balance_last_error
  return typeof value === 'string' ? value.trim() : ''
}

function upstreamBalanceSyncedAt(item: UpstreamConfig): string {
  if (item.provider === 'newapi') {
    const value = upstreamProviderSnapshot(item)?.synced_at
    return typeof value === 'string' ? value : ''
  }
  const value = item.extra?.sub2api_balance_synced_at
  return typeof value === 'string' ? value : ''
}

function upstreamBalanceEmail(item: UpstreamConfig): string {
  if (item.provider === 'newapi') {
    const value = upstreamProviderSnapshot(item)?.email
    return typeof value === 'string' ? value.trim() : ''
  }
  const value = item.extra?.sub2api_user_email
  return typeof value === 'string' ? value.trim() : ''
}

function upstreamProviderSnapshot(item: UpstreamConfig): Record<string, unknown> | null {
  const value = item.extra?.upstream_provider_snapshot
  return value && typeof value === 'object' && !Array.isArray(value)
    ? value as Record<string, unknown>
    : null
}

type UpstreamConcurrencyState = 'limited' | 'providerDefined' | 'unlimited' | 'stale' | 'unsupported' | 'initialFailure'

interface UpstreamConcurrencyDisplay {
  state: UpstreamConcurrencyState
  value: string | null
}

function upstreamConcurrencySnapshot(item: UpstreamConfig): Record<string, unknown> | null {
  const value = item.extra?.upstream_concurrency_snapshot
  return value && typeof value === 'object' && !Array.isArray(value)
    ? value as Record<string, unknown>
    : null
}

function upstreamConcurrencyInteger(value: unknown, preserveString = false): string | null {
  if (typeof value === 'string' && /^\d+$/.test(value)) {
    if (preserveString) return value
    const normalized = value.replace(/^0+(?=\d)/, '')
    return normalized.replace(/\B(?=(\d{3})+(?!\d))/g, ',')
  }
  if (typeof value === 'number' && Number.isSafeInteger(value) && value >= 0) {
    return value.toLocaleString()
  }
  return null
}

function upstreamConcurrencyDisplay(item: UpstreamConfig): UpstreamConcurrencyDisplay {
  const snapshot = upstreamConcurrencySnapshot(item)
  if (!snapshot) return { state: 'unsupported', value: null }

  const status = typeof snapshot.status === 'string' ? snapshot.status.trim().toLowerCase() : ''
  const semantics = typeof snapshot.semantics === 'string' ? snapshot.semantics.trim().toLowerCase() : ''
  if (status === 'stale') {
    const staleValue = semantics === 'provider_defined'
      ? (() => {
          const value = upstreamConcurrencyInteger(snapshot.raw_value, true)
          return value === null ? null : t('admin.upstreamConfigs.concurrency.newapiReported', { count: value })
        })()
      : semantics === 'limited'
        ? upstreamConcurrencyInteger(snapshot.limit)
        : semantics === 'unlimited'
          ? t('admin.upstreamConfigs.concurrency.unlimited')
          : null
    return { state: staleValue === null ? 'initialFailure' : 'stale', value: staleValue }
  }
  if (status === 'unsupported') return { state: 'unsupported', value: null }
  if (status !== 'current') return { state: 'unsupported', value: null }
  if (semantics === 'unlimited') return { state: 'unlimited', value: null }
  if (semantics === 'limited') {
    const value = upstreamConcurrencyInteger(snapshot.limit)
    return value === null
      ? { state: 'unsupported', value: null }
      : { state: 'limited', value }
  }
  if (semantics === 'provider_defined') {
    const value = upstreamConcurrencyInteger(snapshot.raw_value, true)
    return value === null
      ? { state: 'unsupported', value: null }
      : { state: 'providerDefined', value }
  }
  return { state: 'unsupported', value: null }
}

function upstreamConcurrencyLabel(item: UpstreamConfig): string {
  const display = upstreamConcurrencyDisplay(item)
  if (display.state === 'unlimited') return t('admin.upstreamConfigs.concurrency.unlimited')
  if (display.state === 'stale' && display.value !== null) {
    return t('admin.upstreamConfigs.concurrency.stale', { value: display.value })
  }
  if (display.state === 'initialFailure') return t('admin.upstreamConfigs.concurrency.initialFailure')
  if (display.state === 'unsupported' || display.value === null) {
    return t('admin.upstreamConfigs.concurrency.unsupported')
  }
  return display.state === 'providerDefined'
    ? t('admin.upstreamConfigs.concurrency.newapiReported', { count: display.value })
    : t('admin.upstreamConfigs.concurrency.limited', { count: display.value })
}

function upstreamConcurrencyTextClass(item: UpstreamConfig): string {
  const state = upstreamConcurrencyDisplay(item).state
  if (state === 'stale' || state === 'initialFailure') return 'text-amber-600 dark:text-amber-400'
  if (state === 'unsupported') return 'text-gray-400 dark:text-dark-500'
  return 'text-gray-900 dark:text-gray-100'
}

function upstreamConcurrencyTitle(item: UpstreamConfig): string {
  const snapshot = upstreamConcurrencySnapshot(item)
  const status = typeof snapshot?.status === 'string' ? snapshot.status.trim().toLowerCase() : ''
  if (status !== 'stale') return t('admin.upstreamConfigs.concurrency.headerTitle')

  const parts: string[] = []
  const observedAt = typeof snapshot?.observed_at === 'string' ? snapshot.observed_at.trim() : ''
  const lastCheckedAt = typeof snapshot?.last_checked_at === 'string' ? snapshot.last_checked_at.trim() : ''
  if (observedAt) {
    parts.push(t('admin.upstreamConfigs.concurrency.lastObservedAt', { time: formatTime(observedAt) }))
  }
  if (lastCheckedAt) {
    parts.push(t('admin.upstreamConfigs.concurrency.lastCheckedAt', { time: formatTime(lastCheckedAt) }))
  }
  return parts.join('\n') || t('admin.upstreamConfigs.concurrency.headerTitle')
}

function finiteNumberFromExtra(value: unknown): number | null {
  const parsed = typeof value === 'number' ? value : typeof value === 'string' && value.trim() !== '' ? Number(value) : NaN
  return Number.isFinite(parsed) ? parsed : null
}

function finitePositiveNumber(value: unknown): number | null {
  const parsed = finiteNumberFromExtra(value)
  return parsed !== null && parsed > 0 ? parsed : null
}

function formatBalanceAmount(value: number | null): string {
  if (value === null) return '-'
  return value.toLocaleString(undefined, {
    minimumFractionDigits: 2,
    maximumFractionDigits: 4
  })
}

function convertNewAPIQuotaRaw(item: UpstreamConfig, raw: number | null): number | null {
  if (raw === null) return null
  const snapshot = upstreamProviderSnapshot(item)
  const quotaPerUnit = finiteNumberFromExtra(snapshot?.quota_per_unit) ?? 500000
  if (quotaPerUnit <= 0) return raw
  const displayType = typeof snapshot?.quota_display_type === 'string'
    ? snapshot.quota_display_type.toUpperCase()
    : 'USD'
  if (displayType === 'TOKENS') return null
  if (displayType === 'CNY') return raw / quotaPerUnit
  if (displayType === 'CUSTOM') {
    const rate = finiteNumberFromExtra(snapshot?.custom_currency_exchange_rate) ?? 1
    return raw / quotaPerUnit * rate
  }
  if (displayType === 'USD') return raw / quotaPerUnit
  return null
}

function newAPIAmount(item: UpstreamConfig, kind: 'balance' | 'total'): number | null {
  const snapshot = upstreamProviderSnapshot(item)
  if (!snapshot) return null
  const useBaseAmount = finitePositiveNumber(item.balance_to_cny_rate) !== null
  if (kind === 'balance') {
    if (useBaseAmount) {
      return finiteNumberFromExtra(snapshot.base_balance_amount)
        ?? convertNewAPIQuotaRawBase(item, finiteNumberFromExtra(snapshot.remain_quota_raw) ?? finiteNumberFromExtra(snapshot.quota_raw))
    }
    return finiteNumberFromExtra(snapshot.balance_amount)
      ?? convertNewAPIQuotaRaw(item, finiteNumberFromExtra(snapshot.remain_quota) ?? finiteNumberFromExtra(snapshot.quota))
  }
  if (useBaseAmount) {
    return finiteNumberFromExtra(snapshot.base_total_amount)
      ?? convertNewAPIQuotaRawBase(item, finiteNumberFromExtra(snapshot.total_quota_raw) ?? finiteNumberFromExtra(snapshot.total_quota))
  }
  const totalAmount = finiteNumberFromExtra(snapshot.total_amount)
  if (totalAmount !== null) return totalAmount
  const balanceAmount = finiteNumberFromExtra(snapshot.balance_amount)
  const usedAmount = finiteNumberFromExtra(snapshot.used_amount)
  if (balanceAmount !== null && usedAmount !== null) return balanceAmount + usedAmount
  return convertNewAPIQuotaRaw(
    item,
    finiteNumberFromExtra(snapshot.total_quota) ?? finiteNumberFromExtra(snapshot.total_quota_raw)
  )
}

function convertNewAPIQuotaRawBase(item: UpstreamConfig, raw: number | null): number | null {
  if (raw === null) return null
  const quotaPerUnit = finiteNumberFromExtra(upstreamProviderSnapshot(item)?.quota_per_unit) ?? 500000
  return quotaPerUnit > 0 ? raw / quotaPerUnit : null
}

function explicitCNYRate(item: UpstreamConfig): number | null {
  const override = finitePositiveNumber(item.balance_to_cny_rate)
  if (override !== null) return override
  const snapshot = upstreamProviderSnapshot(item)
  const currency = typeof snapshot?.currency === 'string'
    ? snapshot.currency.trim().toUpperCase()
    : typeof snapshot?.quota_display_type === 'string'
      ? snapshot.quota_display_type.trim().toUpperCase()
      : ''
  if (currency === 'CNY') return 1
  if (currency === 'USD') {
    const providerRate = finitePositiveNumber(snapshot?.usd_exchange_rate)
    if (providerRate !== null) return providerRate
  }
  const rateSource = typeof item.extra?.currency_rate_source === 'string'
    ? item.extra.currency_rate_source.trim()
    : ''
  return rateSource !== 'admin_override'
    ? finitePositiveNumber(item.extra?.currency_to_cny_rate)
    : null
}

function formatCNY(value: number | null): string {
  if (value === null) return '-'
  return `¥${formatBalanceAmount(value)}`
}

function isLowBalance(item: UpstreamConfig): boolean {
  const balance = upstreamBalanceCNY(item)
  const threshold = upstreamSettings.value.balance_low_threshold_cny
  return balance !== null && threshold > 0 && balance < threshold
}

function rawRateRange(item: UpstreamConfig): RateRange {
  const values = (item.keys || [])
    .map((key) => finitePositiveNumber(key.rate_multiplier))
    .filter((value): value is number => value !== null)
  if (!values.length) return null
  return { min: Math.min(...values), max: Math.max(...values) }
}

function costRateRange(item: UpstreamConfig): RateRange {
  const effectiveValues = (item.keys || [])
    .map((key) => finitePositiveNumber(key.effective_cost_multiplier))
    .filter((value): value is number => value !== null)
  if (effectiveValues.length) {
    return { min: Math.min(...effectiveValues), max: Math.max(...effectiveValues) }
  }
  const raw = rawRateRange(item)
  const rechargeRate = finitePositiveNumber(item.recharge_rate) || 1
  return raw ? { min: raw.min * rechargeRate, max: raw.max * rechargeRate } : null
}

function rateRangeLabel(range: RateRange): string {
  if (!range) return '-'
  const min = formatRate(range.min)
  const max = formatRate(range.max)
  return min === max ? min : `${min} - ${max}`
}

function formatRate(value: number): string {
  return value.toLocaleString(undefined, { minimumFractionDigits: 0, maximumFractionDigits: 4 })
}

function formatRateValue(value: number | null | undefined): string {
  return value == null || !Number.isFinite(value) ? '-' : formatRate(value)
}

function balanceTitle(item: UpstreamConfig): string {
  const parts: string[] = []
  const email = upstreamBalanceEmail(item)
  const syncedAt = upstreamBalanceSyncedAt(item)
  const error = upstreamBalanceError(item)
  if (item.provider === 'newapi') {
    const snapshot = upstreamProviderSnapshot(item)
    const rawBalance = finiteNumberFromExtra(snapshot?.remain_quota_raw) ?? finiteNumberFromExtra(snapshot?.remain_quota) ?? finiteNumberFromExtra(snapshot?.quota)
    const rawUsed = finiteNumberFromExtra(snapshot?.used_quota_raw) ?? finiteNumberFromExtra(snapshot?.used_quota)
    const rawTotal = finiteNumberFromExtra(snapshot?.total_quota_raw) ?? finiteNumberFromExtra(snapshot?.total_quota)
    if (rawBalance !== null) parts.push(t('admin.upstreamConfigs.balance.rawBalance', { amount: formatBalanceAmount(rawBalance) }))
    if (rawUsed !== null) parts.push(t('admin.upstreamConfigs.balance.rawUsed', { amount: formatBalanceAmount(rawUsed) }))
    if (rawTotal !== null) parts.push(t('admin.upstreamConfigs.balance.rawTotal', { amount: formatBalanceAmount(rawTotal) }))
  }
  if (email) parts.push(t('admin.upstreamConfigs.balance.email', { email }))
  if (syncedAt) parts.push(t('admin.upstreamConfigs.balance.syncedAt', { time: formatTime(syncedAt) }))
  if (error) parts.push(t('admin.upstreamConfigs.balance.error', { error }))
  return parts.join('\n')
}

function statusLabel(status: string): string {
  const key = ['succeeded', 'partial', 'failed', 'open', 'resolved'].includes(status) ? status : 'unknown'
  return t(`admin.upstreamConfigs.operations.status.${key}`)
}

function statusBadgeClass(status: string): string {
  const base = 'inline-flex rounded-full px-2 py-1 text-xs font-medium'
  if (status === 'succeeded' || status === 'resolved') return `${base} bg-emerald-50 text-emerald-700 dark:bg-emerald-900/20 dark:text-emerald-300`
  if (status === 'partial') return `${base} bg-amber-50 text-amber-700 dark:bg-amber-900/20 dark:text-amber-300`
  if (status === 'failed' || status === 'open') return `${base} bg-red-50 text-red-700 dark:bg-red-900/20 dark:text-red-300`
  return `${base} bg-gray-100 text-gray-700 dark:bg-dark-700 dark:text-dark-200`
}

function severityBadgeClass(severity: string): string {
  return statusBadgeClass(severity === 'error' || severity === 'critical' ? 'failed' : severity === 'warning' ? 'partial' : '')
}

function syncTriggerLabel(trigger: string): string {
  const key = ['manual_single', 'manual_batch', 'scheduled'].includes(trigger) ? trigger : 'unknown'
  return t(`admin.upstreamConfigs.operations.trigger.${key}`)
}

function syncRunDuration(run: UpstreamSyncRun): string {
  if (!run.finished_at) return '-'
  const duration = new Date(run.finished_at).getTime() - new Date(run.started_at).getTime()
  if (!Number.isFinite(duration) || duration < 0) return '-'
  return duration < 1000 ? `${duration}ms` : `${(duration / 1000).toFixed(1)}s`
}

function formatIncidentMetric(incident: UpstreamIncident): string {
  if (incident.metric_value === null || incident.metric_value === undefined) return formatTime(incident.last_observed_at)
  if (incident.threshold_value === null || incident.threshold_value === undefined) return String(incident.metric_value)
  return t('admin.upstreamConfigs.operations.incidentMetric', {
    value: incident.metric_value,
    threshold: incident.threshold_value
  })
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
  if (candidate.source === 'raw_refresh') return t('admin.upstreamConfigs.tokenAssistant.sources.rawRefresh')
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
  @apply inline-flex h-12 w-12 flex-col items-center justify-center gap-1 rounded-lg text-[11px] leading-none text-gray-500 transition-colors;
  @apply hover:bg-gray-100 disabled:cursor-not-allowed disabled:opacity-50;
  @apply dark:text-dark-300 dark:hover:bg-dark-700;
}

.drawer-state {
  @apply flex min-h-40 items-center justify-center text-sm text-gray-500 dark:text-dark-400;
}

.operation-row {
  @apply rounded-lg border border-gray-200 bg-white p-4 dark:border-dark-700 dark:bg-dark-800;
}

.metric-block {
  @apply rounded-lg border border-gray-200 bg-gray-50 p-3 dark:border-dark-700 dark:bg-dark-800;
}

.metric-block span {
  @apply block text-xs text-gray-500 dark:text-dark-400;
}

.metric-block strong {
  @apply mt-1 block text-sm font-semibold tabular-nums text-gray-900 dark:text-gray-100;
}

.section-title {
  @apply text-sm font-semibold text-gray-900 dark:text-gray-100;
}
</style>
