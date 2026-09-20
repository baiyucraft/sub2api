<script setup lang="ts">
import { Activity, Search, RefreshCw, Save, Play, Square, ShieldCheck, AlertTriangle, Trash2, Radio, KeyRound } from 'lucide-vue-next'
import { useState } from './useState'
import type { MessageKey } from './i18n'

const { language, t, models, draft, hostSecrets, editingSecrets, editSecrets, needsInitialSave, resources, status, resourcesLoaded, loading, saving, refreshing, errors, notice,
  query, group, configuredOnly, tab, dirty, editable, accounts, accountConfig, modelStatus, editModel, refresh, load,
  save, canHarvest, canCancel, action, removeMissing, remaining, formatDate } = useState()
const shortModel = (model: string) => model.split('-').at(-1)!.replace(/^./, c => c.toUpperCase())
const stateLabel = (state: MessageKey) => t(state === 'ready' ? 'stateReady' : state === 'cooldown' ? 'stateCooldown' : state)
</script>

<template>
  <main>
    <header class="toolbar">
      <div class="identity"><Activity :size="22" aria-hidden="true" /><h1>{{ t('title') }}</h1><span class="badge" :class="{ live: status.running }"><span class="dot" />{{ t(status.running ? 'running' : 'stopped') }}</span></div>
      <div class="toolbar-actions">
        <select v-model="language" :aria-label="t('language')"><option value="zh">中文</option><option value="en">English</option></select>
        <button class="icon-button" :title="t('refresh')" :aria-label="t('refresh')" :disabled="refreshing" @click="refresh"><RefreshCw :size="17" :class="{ spin: refreshing }" /></button>
        <button class="primary" :disabled="!editable || !dirty" @click="save"><Save :size="16" />{{ t(saving ? 'saving' : 'save') }}</button>
      </div>
    </header>
    <div class="feedback" aria-live="polite">
      <span v-if="dirty" class="unsaved">{{ t('unsaved') }}</span><span v-else-if="notice" class="success">{{ t(notice) }}</span>
      <p v-for="(error, key) in errors" v-show="error" :key="key" role="alert">{{ error ? t(error) : '' }}</p>
      <button v-if="errors.config || errors.resources" :disabled="loading" @click="load">{{ t('retry') }}</button>
    </div>
    <section class="configuration" :aria-label="t('settings')">
      <label class="master"><input v-model="draft.enabled" data-testid="enabled" type="checkbox" role="switch" :disabled="!editable" /><span>{{ t('enablePlugin') }}</span></label>
      <div class="field"><span>{{ t('harvestProxy') }}</span><strong data-testid="harvest-proxy-status">{{ t(hostSecrets.harvest_proxy_url ? 'secretConfigured' : 'secretNotConfigured') }}</strong></div>
      <div class="field"><span>{{ t('dialProxy') }}</span><strong data-testid="dial-proxy-status">{{ t(hostSecrets.dial_proxy_url ? 'secretConfigured' : 'secretNotConfigured') }}</strong></div>
      <div class="secrets-edit"><button data-testid="edit-secrets" :disabled="!editable || needsInitialSave" @click="editSecrets"><KeyRound :size="15" />{{ t(editingSecrets ? 'secretsEditing' : 'editSecrets') }}</button><span v-if="needsInitialSave" class="muted" role="status">{{ t('saveInitialConfig') }}</span></div>
    </section>
    <nav class="tabs" aria-label="Codex STATE">
      <button :aria-current="tab === 'accounts' ? 'page' : undefined" @click="tab = 'accounts'"><ShieldCheck :size="16" />{{ t('accounts') }}<span>{{ resources.accounts.length }}</span></button>
      <button :aria-current="tab === 'logs' ? 'page' : undefined" @click="tab = 'logs'"><Radio :size="16" />{{ t('logs') }}<span>{{ status.logs.length }}</span></button>
    </nav>
    <section v-show="tab === 'accounts'">
      <div class="filters">
        <label class="search"><Search :size="16" /><input v-model="query" type="search" :placeholder="t('search')" :aria-label="t('search')" /></label>
        <select v-model="group" :aria-label="t('group')"><option value="all">{{ t('allGroups') }}</option><option value="none">{{ t('ungrouped') }}</option><option v-for="g in resources.groups" :key="g.id" :value="String(g.id)">{{ g.name }}</option></select>
        <label class="check"><input v-model="configuredOnly" type="checkbox" />{{ t('configuredOnly') }}</label>
      </div>
      <p v-if="loading" class="empty">{{ t('loading') }}</p>
      <p v-else-if="!accounts.length" class="empty">{{ t('noAccounts') }}</p>
      <article v-for="account in accounts" :key="account.id" class="account" :data-account="account.id">
        <header class="account-header"><div><h2>{{ account.name }}</h2><span class="muted mono">#{{ account.id }}</span><span v-for="g in resources.groups.filter(g => account.group_ids.includes(g.id))" :key="g.id" class="group">{{ g.name }}</span></div>
          <span v-if="account.business_egress_configured" class="egress"><ShieldCheck :size="14" />{{ t('egress') }}</span>
          <span v-else class="warning"><AlertTriangle :size="14" />{{ t(account.account_type ? 'noEgress' : 'unavailableAccount') }}</span>
          <button v-if="resourcesLoaded && !account.account_type" class="icon-button" :title="t('remove')" :aria-label="t('remove')" :disabled="!editable" @click="removeMissing(account.id)"><Trash2 :size="16" /></button>
        </header>
        <div class="models">
          <section v-for="model in models" :key="model" class="model" :data-model="model">
            <div class="model-heading"><label class="check"><input type="checkbox" role="switch" :aria-label="`${shortModel(model)} ${t('enabled')}`" :checked="accountConfig(account.id).models[model].enabled" :disabled="!editable || !account.account_type" @change="editModel(account.id, model, 'enabled', ($event.target as HTMLInputElement).checked)" /><h3>{{ shortModel(model) }}</h3></label>
              <select :aria-label="`${shortModel(model)} ${t('plan')}`" :value="accountConfig(account.id).models[model].ticket_plan" :disabled="!editable" @change="editModel(account.id, model, 'ticket_plan', ($event.target as HTMLSelectElement).value)"><option value="pro">Pro</option><option value="team">Team</option></select>
            </div>
            <p class="model-state" :class="{ success: modelStatus(account.id, model).state === 'ready', warning: modelStatus(account.id, model).state === 'error' }">{{ stateLabel(modelStatus(account.id, model).state) }}</p>
            <dl class="metrics">
              <dt>{{ t('active') }}</dt><dd :title="formatDate(modelStatus(account.id, model).active?.expires_at || '')">{{ remaining(modelStatus(account.id, model).active) }}<span v-if="modelStatus(account.id, model).active && !modelStatus(account.id, model).active?.usable" class="diagnostic"> · {{ t('unusable') }}</span></dd>
              <dt>{{ t('ready') }}</dt><dd :title="formatDate(modelStatus(account.id, model).ready?.expires_at || '')">{{ remaining(modelStatus(account.id, model).ready) }}<span v-if="modelStatus(account.id, model).ready && !modelStatus(account.id, model).ready?.usable" class="diagnostic"> · {{ t('unusable') }}</span></dd>
              <dt>{{ t('strikes') }}</dt><dd>{{ modelStatus(account.id, model).strikes }}</dd>
              <dt>{{ t('attempts') }}</dt><dd>{{ modelStatus(account.id, model).attempts }}</dd>
            </dl>
            <p v-if="modelStatus(account.id, model).cooldown_until" class="detail">{{ t('cooldown') }}: {{ formatDate(modelStatus(account.id, model).cooldown_until) }}</p>
            <p v-if="modelStatus(account.id, model).last_error" class="diagnostic">{{ t(modelStatus(account.id, model).last_error as MessageKey) }}</p>
            <div class="model-actions"><button :disabled="!canHarvest(account, model)" :title="canHarvest(account, model) ? t('harvest') : t('disabledReason')" @click="action(account, model, 'harvest')"><Play :size="14" />{{ t('harvest') }}</button><button :disabled="!canCancel(account, model)" @click="action(account, model, 'cancel')"><Square :size="13" />{{ t('cancel') }}</button></div>
          </section>
        </div>
      </article>
    </section>
    <section v-show="tab === 'logs'" class="logs" :aria-label="t('logs')">
      <p v-if="!status.logs.length" class="empty">{{ t('emptyLogs') }}</p>
      <ol v-else><li v-for="(log, index) in status.logs" :key="index"><time>{{ formatDate(log.at) }}</time><span class="mono">{{ log.account_id ? `#${log.account_id}` : '' }} {{ log.model ? shortModel(log.model) : '' }}</span><span :class="{ diagnostic: log.level === 'error' }">{{ log.message ? t(log.message as MessageKey) : t('unknown') }}</span></li></ol>
    </section>
  </main>
</template>
