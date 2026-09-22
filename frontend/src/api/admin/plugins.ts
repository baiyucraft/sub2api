import { apiClient } from '../client'

export interface PluginCapability {
  id: string
  platform: string
  account_type: string
}

export interface PluginRequirements {
  host_service_api?: number
  host_features?: string[]
  sub2api: string
  recommended_sub2api_version?: string
  tested_sub2api_versions?: string[]
  plugin_protocol: number
  transport_api: number
  ui_bridge?: number
  admin_ui?: number
}

export interface PluginManifest {
  schema_version: number
  id: string
  name: string
  version: string
  description?: string
  author?: string
  requires: PluginRequirements
  capabilities: PluginCapability[]
  config_secrets?: string[]
  ui: { type?: 'native' | 'iframe' | 'none'; entrypoint?: string; definition?: string }
}

export interface PluginConfig extends Record<string, unknown> {
  _host_secrets?: Record<string, boolean>
}

export interface PluginCompatibility {
  compatible: boolean
  tested: boolean
  status: 'compatible' | 'untested' | 'incompatible'
  message: string
  current_sub2api_version: string
  required_sub2api_version: string
  recommended_sub2api_version: string
  plugin_protocol: number
  transport_api: number
  ui_bridge: number
  admin_ui: number
}

export interface PluginBinding {
  id: number
  plugin_id: number
  capability: string
  platform: string
  account_type: string
  enabled: boolean
  rollout_percent: number
}

export interface PluginInstallation {
  id: number
  plugin_key: string
  name: string
  version: string
  description: string
  author: string
  manifest: PluginManifest
  binary_sha256: string
  signature_status: 'trusted' | 'unsigned'
  state: 'disabled' | 'starting' | 'enabled' | 'error' | 'incompatible'
  last_error: string
  installed_at: string
  enabled_at?: string
  updated_at: string
  bindings: PluginBinding[]
  compatibility: PluginCompatibility
  runtime_healthy: boolean
  runtime_message: string
}

export interface PluginTestResult {
  success: boolean
  message: string
  latency_ms: number
  status_json?: string
}

export interface PluginStatusResult {
  healthy: boolean
  message: string
  status_json?: string
}

export interface PluginUISession {
  url: string
  bridge_token: string
  ui_bridge_version: number
  expires_at: string
}

export type NativePluginBindingRoot = 'config' | 'resources' | 'status' | 'local' | 'item' | 'secrets'
export type NativePluginConditionOp = 'eq' | 'ne' | 'truthy' | 'falsy' | 'nonempty' | 'empty' | 'in'

export interface NativePluginUICondition {
  op: NativePluginConditionOp
  path: string
  value?: unknown
}

export interface NativePluginUIOption {
  value: string
  label: string
  label_key?: string
}

export interface NativePluginUITableColumn {
  key: string
  label: string
  label_key?: string
  bind?: string
  format?: 'count' | 'boolean' | 'date' | 'duration'
}

export interface NativePluginUITableFilter {
  bind: string
  key: string
  all_value?: string
}

export interface NativePluginUIValidation {
  op: 'required' | 'min_length' | 'max_length' | 'in'
  value?: unknown
  message: string
}

export interface NativePluginUINode {
  type: string
  id?: string
  icon?: string
  title?: string
  description?: string
  title_key?: string
  description_key?: string
  bind?: string
  format?: 'count' | 'boolean' | 'date' | 'duration'
  write?: string
  condition?: NativePluginUICondition
  children?: NativePluginUINode[]
  options?: NativePluginUIOption[]
  options_bind?: string
  columns?: NativePluginUITableColumn[]
  row_actions?: NativePluginUINode[]
  selection_bind?: string
  search_bind?: string
  filter_bind?: string
  filter_key?: string
  filter_options_bind?: string
  filters?: NativePluginUITableFilter[]
  presence_filter_bind?: string
  presence_key?: string
  row_key?: string
  page_size?: number
  sort_key?: string
  sort_desc?: boolean
  action?: string
  payload?: Record<string, string>
  values?: Record<string, unknown>
  validation?: NativePluginUIValidation[]
  apply_result?: 'config'
  confirm?: boolean
  read_only?: boolean
}

export interface NativePluginAdminUI {
  schema_version: number
  title: string
  description: string
  title_key?: string
  description_key?: string
  translations?: Record<string, Record<string, string>>
  poll_interval_seconds: number
  layout: NativePluginUINode[]
}

export interface PluginResources {
  accounts: Array<{
    id: number
    name: string
    platform: string
    account_type: string
    group_ids: number[]
    business_egress_configured: boolean
  }>
  groups: Array<{ id: number; name: string }>
  proxies: Array<{ id: number; name: string; protocol: string; host: string; port: number }>
}

export interface PluginActionRequest {
  action_id: string
  name: string
  payload: Record<string, unknown>
}

export interface PluginActionResult {
  action_id: string
  accepted: boolean
  status: string
  result?: unknown
}

export async function list(): Promise<PluginInstallation[]> {
  const { data } = await apiClient.get<PluginInstallation[]>('/admin/plugins')
  return data
}

export async function upload(file: File): Promise<PluginInstallation> {
  const form = new FormData()
  form.append('plugin', file)
  const { data } = await apiClient.post<PluginInstallation>('/admin/plugins/upload', form, {
    headers: { 'Content-Type': 'multipart/form-data' },
    timeout: 120000
  })
  return data
}

export async function upgrade(id: number, file: File): Promise<PluginInstallation> {
  const form = new FormData()
  form.append('plugin', file)
  const { data } = await apiClient.post<PluginInstallation>(`/admin/plugins/${id}/upgrade`, form, {
    headers: { 'Content-Type': 'multipart/form-data' },
    timeout: 120000
  })
  return data
}

export async function resources(id: number): Promise<PluginResources> {
  const { data } = await apiClient.get<PluginResources>(`/admin/plugins/${id}/resources`)
  return data
}

export async function action(id: number, request: PluginActionRequest): Promise<PluginActionResult> {
  const { data } = await apiClient.post<PluginActionResult>(`/admin/plugins/${id}/actions`, request)
  return data
}

export async function enable(
  id: number,
  rolloutPercent: number,
  acceptUntested: boolean
): Promise<PluginInstallation> {
  const { data } = await apiClient.post<PluginInstallation>(`/admin/plugins/${id}/enable`, {
    rollout_percent: rolloutPercent,
    accept_untested: acceptUntested
  })
  return data
}

export async function disable(id: number): Promise<PluginInstallation> {
  const { data } = await apiClient.post<PluginInstallation>(`/admin/plugins/${id}/disable`)
  return data
}

export async function remove(id: number): Promise<void> {
  await apiClient.delete(`/admin/plugins/${id}`)
}

export async function getConfig(id: number): Promise<PluginConfig> {
  const { data } = await apiClient.get<PluginConfig>(`/admin/plugins/${id}/config`)
  return data
}

export async function saveConfig(
  id: number,
  config: Record<string, unknown>
): Promise<PluginConfig> {
  const { data } = await apiClient.put<PluginConfig>(`/admin/plugins/${id}/config`, config)
  return data
}

export async function saveSecrets(id: number, values: Record<string, string>): Promise<PluginConfig> {
  const { data } = await apiClient.put<PluginConfig>(`/admin/plugins/${id}/config/secrets`, { values })
  return data
}

export async function test(id: number): Promise<PluginTestResult> {
  const { data } = await apiClient.post<PluginTestResult>(`/admin/plugins/${id}/test`)
  return data
}

export async function status(id: number): Promise<PluginStatusResult> {
  const { data } = await apiClient.get<PluginStatusResult>(`/admin/plugins/${id}/status`)
  return data
}

export async function createUISession(id: number): Promise<PluginUISession> {
  const { data } = await apiClient.post<PluginUISession>(`/admin/plugins/${id}/ui-session`)
  return data
}

export async function byKey(pluginKey: string): Promise<PluginInstallation> {
  const { data } = await apiClient.get<PluginInstallation>(`/admin/plugins/by-key/${encodeURIComponent(pluginKey)}`)
  return data
}

export async function adminUI(id: number): Promise<NativePluginAdminUI> {
  const { data } = await apiClient.get<NativePluginAdminUI>(`/admin/plugins/${id}/admin-ui`)
  return data
}

export default {
  list,
  upload,
  upgrade,
  resources,
  action,
  enable,
  disable,
  remove,
  getConfig,
  saveConfig,
  saveSecrets,
  test,
  status,
  createUISession,
  byKey,
  adminUI
}
