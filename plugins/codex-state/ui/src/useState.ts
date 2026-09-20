import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { createBridge, type Bridge } from './bridge'
import { defaultAccount, emptyModelStatus, models, normalizeConfig, normalizeResources, normalizeSecretFlags, normalizeStatus, record,
  type AccountResource, type Config, type Model, type Resources, type Status } from './contracts'
import { en, zh, type MessageKey } from './i18n'

export function useState(bridge: Bridge = createBridge()) {
  const language = ref<'en' | 'zh'>(navigator.language.startsWith('zh') ? 'zh' : 'en')
  const t = (key: MessageKey) => (language.value === 'zh' ? zh : en)[key]
  const draft = reactive<Config>(normalizeConfig({}))
  const hostSecrets = reactive(normalizeSecretFlags({}))
  const editingSecrets = ref(false)
  const needsInitialSave = ref(false)
  const savedSnapshot = ref(JSON.stringify(draft))
  const resources = ref<Resources>({ accounts: [], groups: [] })
  const status = ref<Status>({ running: false, accounts: [], logs: [] })
  const loaded = ref(false)
  const resourcesLoaded = ref(false)
  const loading = ref(false)
  const saving = ref(false)
  const refreshing = ref(false)
  const errors = reactive<{ config: MessageKey | ''; resources: MessageKey | ''; status: MessageKey | ''; operation: MessageKey | '' }>({ config: '', resources: '', status: '', operation: '' })
  const notice = ref<MessageKey | ''>('')
  const busy = reactive(new Set<string>())
  const query = ref('')
  const group = ref('all')
  const configuredOnly = ref(false)
  const tab = ref<'accounts' | 'logs'>('accounts')
  const clock = ref(Date.now())
  const observedAt = ref(Date.now())
  let disposed = false
  let timer: ReturnType<typeof setInterval> | undefined
  const uncertainActions = new Map<string, string>()
  const dirty = computed(() => needsInitialSave.value || JSON.stringify(draft) !== savedSnapshot.value)
  const editable = computed(() => loaded.value && !saving.value && !editingSecrets.value)
  const accounts = computed(() => {
    const known = new Set(resources.value.accounts.map(a => a.id))
    const missing: AccountResource[] = draft.accounts.filter(a => !known.has(a.account_id)).map(a => ({
      id: a.account_id, name: `#${a.account_id}`, platform: 'openai', account_type: '', group_ids: [], business_egress_configured: false,
    }))
    return [...resources.value.accounts, ...missing].filter(a => {
      const match = `${a.name} ${a.id}`.toLowerCase().includes(query.value.toLowerCase())
      const groupMatch = group.value === 'all' || (group.value === 'none' ? !a.group_ids.length : a.group_ids.includes(Number(group.value)))
      return match && groupMatch && (!configuredOnly.value || draft.accounts.some(c => c.account_id === a.id))
    })
  })
  const accountConfig = (id: number) => draft.accounts.find(a => a.account_id === id) || defaultAccount(id)
  const modelStatus = (id: number, model: Model) => status.value.accounts.find(a => a.account_id === id)?.models[model] || emptyModelStatus()
  function editModel(id: number, model: Model, field: 'enabled' | 'ticket_plan', value: boolean | string) {
    if (!editable.value) return
    let account = draft.accounts.find(a => a.account_id === id)
    if (!account) { account = defaultAccount(id); draft.accounts.push(account) }
    if (field === 'enabled') account.models[model].enabled = value === true
    else account.models[model].ticket_plan = value === 'team' ? 'team' : 'pro'
    notice.value = ''
  }
  async function refresh() {
    if (refreshing.value || disposed) return
    refreshing.value = true
    try {
      const response = await bridge.request('plugin.status')
      if (disposed) return
      status.value = normalizeStatus(response.result)
      observedAt.value = Date.now()
      errors.status = ''
    } catch {
      if (!disposed) { status.value = { running: false, accounts: [], logs: [] }; errors.status = 'statusFailed' }
    } finally { refreshing.value = false }
  }
  async function load() {
    if (loading.value || disposed) return
    loading.value = true
    await Promise.allSettled([
      (async () => {
        if (loaded.value) return
        try {
          const response = await bridge.request('config.load')
          if (disposed) return
          const config = record(response.config)
          if (config.version !== undefined && config.version !== 1) throw new Error('unsupported_version')
          Object.assign(draft, normalizeConfig(config))
          Object.assign(hostSecrets, normalizeSecretFlags(config._host_secrets))
          needsInitialSave.value = config.version === undefined
          savedSnapshot.value = JSON.stringify(draft)
          if (typeof record(response.host).locale === 'string') language.value = String(record(response.host).locale).startsWith('zh') ? 'zh' : 'en'
          loaded.value = true
          errors.config = ''
        } catch { if (!disposed) errors.config = 'loadFailed' }
      })(),
      (async () => {
        try {
          const response = await bridge.request('plugin.resources')
          if (disposed) return
          resources.value = normalizeResources(response.resources)
          resourcesLoaded.value = true
          errors.resources = ''
        } catch { if (!disposed) { errors.resources = 'resourcesFailed'; resourcesLoaded.value = false } }
      })(),
      refresh(),
    ])
    loading.value = false
  }
  async function save() {
    if (!editable.value || !dirty.value) return
    saving.value = true
    errors.operation = ''
    notice.value = ''
    const snapshot = normalizeConfig(draft)
    try {
      const response = await bridge.request('config.save', { config: snapshot })
      if (disposed) return
      Object.assign(hostSecrets, normalizeSecretFlags(record(response.config)._host_secrets))
      needsInitialSave.value = false
      savedSnapshot.value = JSON.stringify(snapshot)
      notice.value = 'saved'
      await refresh()
    } catch { if (!disposed) errors.operation = 'saveFailed' }
    finally { saving.value = false }
  }
  async function editSecrets() {
    if (!editable.value || needsInitialSave.value) return
    editingSecrets.value = true
    errors.operation = ''
    try {
      const response = await bridge.request('plugin.secrets.edit')
      if (disposed) return
      Object.assign(hostSecrets, normalizeSecretFlags(record(response.result).configured))
      await refresh()
    } catch (error) {
      if (disposed) return
      if (!(error instanceof Error && error.message === 'bridge_cancelled')) errors.operation = 'secretsFailed'
      // A failed save may have committed before the response was lost. Reload
      // only safe flags; never replace the user's ordinary configuration draft.
      try {
        const response = await bridge.request('config.load')
        if (!disposed) Object.assign(hostSecrets, normalizeSecretFlags(record(response.config)._host_secrets))
      } catch {
        if (!disposed) { Object.assign(hostSecrets, normalizeSecretFlags({})); errors.operation = 'secretsFailed' }
      }
    } finally { editingSecrets.value = false }
  }
  const actionKey = (id: number, model: Model) => `${id}:${model}`
  function canHarvest(account: AccountResource, model: Model) {
    const current = modelStatus(account.id, model)
    return loaded.value && resourcesLoaded.value && !dirty.value && !saving.value && !editingSecrets.value && status.value.running && draft.enabled &&
      account.business_egress_configured && hostSecrets.harvest_proxy_url && accountConfig(account.id).models[model].enabled &&
      !busy.has(actionKey(account.id, model)) && !current.refreshing && current.state !== 'harvesting' && current.state !== 'queued' &&
      (!current.cooldown_until || Date.parse(current.cooldown_until) <= clock.value)
  }
  function canCancel(account: AccountResource, model: Model) {
    const current = modelStatus(account.id, model)
    return status.value.running && !busy.has(actionKey(account.id, model)) && (current.refreshing || current.state === 'harvesting' || current.state === 'queued')
  }
  async function action(account: AccountResource, model: Model, name: 'harvest' | 'cancel') {
    if (name === 'harvest' ? !canHarvest(account, model) : !canCancel(account, model)) return
    const key = actionKey(account.id, model)
    const operationKey = `${key}:${name}`
    const actionID = uncertainActions.get(operationKey) || `state-${crypto.randomUUID()}`
    uncertainActions.set(operationKey, actionID)
    busy.add(key)
    errors.operation = ''
    notice.value = ''
    try {
      const response = await bridge.request('plugin.action', {
        action_id: actionID,
        name, payload: { account_id: account.id, model },
      })
      if (disposed) return
      if (record(response.result).accepted !== true) throw new Error('not_accepted')
      uncertainActions.delete(operationKey)
      notice.value = 'actionAccepted'
      await refresh()
    } catch { if (!disposed) errors.operation = 'actionFailed' }
    finally { busy.delete(key) }
  }
  function removeMissing(id: number) {
    if (!editable.value || !resourcesLoaded.value || resources.value.accounts.some(a => a.id === id)) return
    draft.accounts = draft.accounts.filter(a => a.account_id !== id)
  }
  function remaining(slot: { expires_at: string; remaining_seconds: number } | null) {
    if (!slot) return t('none')
    const seconds = slot.expires_at ? (Date.parse(slot.expires_at) - clock.value) / 1000 : slot.remaining_seconds - (clock.value - observedAt.value) / 1000
    const n = Math.max(0, Math.floor(seconds))
    return `${Math.floor(n / 60)}m ${String(n % 60).padStart(2, '0')}s`
  }
  function formatDate(value: string) {
    return value ? new Date(value).toLocaleString(language.value === 'zh' ? 'zh-CN' : 'en-US') : t('none')
  }
  onMounted(() => {
    bridge.notify('sub2api.plugin.ready')
    bridge.notify('ui.resize', { height: 900 })
    void load()
    timer = setInterval(() => { clock.value = Date.now(); if (!saving.value && !busy.size) void refresh() }, 4000)
  })
  onBeforeUnmount(() => { disposed = true; clearInterval(timer); bridge.dispose() })
  return { language, t, models, draft, hostSecrets, editingSecrets, editSecrets, needsInitialSave, resources, status, loaded, resourcesLoaded, loading, saving, refreshing, errors, notice,
    busy, query, group, configuredOnly, tab, dirty, editable, accounts, accountConfig, modelStatus, editModel, refresh, load,
    save, canHarvest, canCancel, action, removeMissing, remaining, formatDate }
}
