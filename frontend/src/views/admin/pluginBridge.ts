import type { PluginActionRequest, PluginInstallation, PluginManifest, PluginResources } from '@/api/admin/plugins'

function record(value: unknown): Record<string, unknown> {
  return value && typeof value === 'object' && !Array.isArray(value)
    ? value as Record<string, unknown> : {}
}

function entries(value: unknown): Record<string, unknown>[] {
  return Array.isArray(value) ? value.map(record) : []
}

const text = (value: unknown) => typeof value === 'string' ? value.slice(0, 256) : ''
const validID = (value: unknown): value is number => Number.isSafeInteger(value) && Number(value) > 0

export function pluginSecretFields(manifest: PluginManifest): string[] {
  return Array.isArray(manifest.config_secrets)
    ? [...new Set(manifest.config_secrets.filter(key => typeof key === 'string' && key.length > 0 &&
      !['_host_secrets', '__proto__', 'constructor', 'prototype'].includes(key)))] : []
}

export function projectSecretFlags(input: unknown, fields: string[]): Record<string, boolean> {
  const source = record(input)
  return Object.fromEntries(fields.map(key => [key, Object.prototype.hasOwnProperty.call(source, key) && source[key] === true]))
}

export function projectPluginConfig(input: unknown, fields: string[]): Record<string, unknown> {
  const config = { ...record(input) }
  if (fields.length) {
    for (const key of fields) config[key] = ''
    config._host_secrets = projectSecretFlags(config._host_secrets, fields)
  }
  return config
}

export function preparePluginConfig(input: unknown, fields: string[]): Record<string, unknown> {
  const config = projectPluginConfig(input, fields)
  delete config._host_secrets
  return config
}

export function isSecretsEditRequest(input: unknown): boolean {
  const request = record(input)
  return Object.keys(request).every(key => ['source', 'bridge_token', 'type', 'request_id'].includes(key))
}

// Never forward raw Account/Proxy DTOs (credentials and account.extra are private).
export function projectPluginResources(input: unknown): PluginResources {
  const source = record(input)
  return {
    accounts: entries(source.accounts).filter(a => validID(a.id)).map(a => ({
      id: a.id as number,
      name: text(a.name),
      platform: text(a.platform),
      account_type: text(a.account_type),
      group_ids: Array.isArray(a.group_ids) ? a.group_ids.filter(validID) : [],
      business_egress_configured: a.business_egress_configured === true,
    })),
    groups: entries(source.groups).filter(g => validID(g.id)).map(g => ({ id: g.id as number, name: text(g.name) })),
    proxies: entries(source.proxies).filter(p => validID(p.id)).map(p => ({
      id: p.id as number, name: text(p.name), protocol: text(p.protocol), host: text(p.host),
      port: Number.isInteger(p.port) && Number(p.port) > 0 && Number(p.port) <= 65535 ? Number(p.port) : 0,
    })),
  }
}

export function pluginRunning(plugin: PluginInstallation): boolean {
  return plugin.state === 'enabled' && plugin.runtime_healthy
}

export function parsePluginAction(input: unknown): PluginActionRequest | null {
  const value = record(input)
  if (typeof value.action_id !== 'string' || !/^[a-zA-Z0-9_.:-]{1,128}$/.test(value.action_id) ||
      typeof value.name !== 'string' || !/^[a-zA-Z0-9_.-]{1,128}$/.test(value.name) ||
      !value.payload || typeof value.payload !== 'object' || Array.isArray(value.payload)) return null
  try {
    const json = JSON.stringify(value.payload)
    if (new TextEncoder().encode(json).length > 256 * 1024) return null
    return { action_id: value.action_id, name: value.name, payload: JSON.parse(json) }
  } catch { return null }
}
