import { describe, expect, it } from 'vitest'
import { isSecretsEditRequest, parsePluginAction, pluginSecretFields, preparePluginConfig, projectPluginConfig, projectPluginResources, projectSecretFlags } from '../pluginBridge'
import type { PluginManifest } from '@/api/admin/plugins'

describe('plugin bridge resource and action boundary', () => {
  it('projects declared secrets for both load and save, retaining legacy config behavior', () => {
    const input = { enabled: true, endpoint: 'PRIVATE', _host_secrets: { endpoint: true, token: 'PRIVATE', unexpected: 'PRIVATE' } }
    expect(projectPluginConfig(input, ['endpoint', 'token'])).toEqual({ enabled: true, endpoint: '', token: '', _host_secrets: { endpoint: true, token: false } })
    expect(preparePluginConfig(input, ['endpoint', 'token'])).toEqual({ enabled: true, endpoint: '', token: '' })
    expect(projectPluginConfig({ legacy: 'value' }, [])).toEqual({ legacy: 'value' })
    expect(input.endpoint).toBe('PRIVATE')
    expect(projectSecretFlags({ endpoint: 'PRIVATE', token: 1 }, ['endpoint', 'token'])).toEqual({ endpoint: false, token: false })
  })

  it('accepts only declared safe fields and a parameter-free secrets edit request', () => {
    const manifest = { config_secrets: ['endpoint', 'endpoint', '_host_secrets', '__proto__', 'constructor', 'prototype'] } as PluginManifest
    expect(pluginSecretFields(manifest)).toEqual(['endpoint'])
    expect(pluginSecretFields({} as PluginManifest)).toEqual([])
    const envelope = { source: 'sub2api-plugin-ui', bridge_token: 'token', type: 'plugin.secrets.edit', request_id: '1' }
    expect(isSecretsEditRequest(envelope)).toBe(true)
    for (const field of ['value', 'values', 'config', 'payload', 'field']) expect(isSecretsEditRequest({ ...envelope, [field]: {} })).toBe(false)
  })
  it('discards unexpected metadata and rejects invalid identifiers', () => {
    expect(projectPluginResources({
      accounts: [{ id: -1, name: 'invalid' }, { id: 3, name: 'valid', group_ids: [1, -2, '3'], extra: { secret: 'S' } }],
      proxies: [{ id: 9, host: 'proxy.local', port: 99999, username: 'S', password: 'S' }],
      admin_token: 'S',
    })).toEqual({
      accounts: [{ id: 3, name: 'valid', group_ids: [1], platform: '', account_type: '', business_egress_configured: false }], groups: [],
      proxies: [{ id: 9, name: '', protocol: '', host: 'proxy.local', port: 0 }],
    })
  })

  it('bounds UTF-8 action payloads without restricting generic action names to STATE', () => {
    expect(parsePluginAction({ action_id: 'id-1', name: 'other_plugin.refresh', payload: { enabled: true } })).toEqual({ action_id: 'id-1', name: 'other_plugin.refresh', payload: { enabled: true } })
    expect(parsePluginAction({ action_id: 'id', name: 'harvest', payload: { data: '汉'.repeat(100000) } })).toBeNull()
    expect(parsePluginAction({ action_id: 'id', name: 'harvest', payload: [] })).toBeNull()
    const circular: Record<string, unknown> = {}
    circular.self = circular
    expect(parsePluginAction({ action_id: 'id', name: 'harvest', payload: circular })).toBeNull()
  })
})
