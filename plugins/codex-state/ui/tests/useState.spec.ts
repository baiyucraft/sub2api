import { defineComponent, h } from 'vue'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { useState } from '../src/useState'
import { defaultAccount, normalizeConfig, type Model } from '../src/contracts'

const wrappers: VueWrapper[] = []
let state: ReturnType<typeof useState>
const astra: Model = 'gpt-6-astra'
const directory = { accounts: [
  { id: 123, name: 'Primary', platform: 'openai', account_type: 'oauth', group_ids: [9], business_egress_configured: true },
  { id: 124, name: 'Secondary', platform: 'openai', account_type: 'setup-token', group_ids: [10], business_egress_configured: false },
], groups: [{ id: 9, name: 'Pro' }, { id: 10, name: 'Team' }] }

function harness(enabled = false, running = true, initial = false) {
  const config = normalizeConfig({ enabled, accounts: [defaultAccount(123)] })
  config.accounts[0].models[astra].enabled = enabled
  const configured = { harvest_proxy_url: !initial, dial_proxy_url: false }
  const runtime = { running, accounts: [{ account_id: 123, models: { [astra]: { state: 'waiting', refreshing: false } } }], logs: [] }
  const request = vi.fn(async (type: string, payload?: Record<string, unknown>): Promise<Record<string, unknown>> => {
    if (type === 'config.load') return { config: initial ? {} : { ...config, harvest_proxy_url: 'STALE_SECRET', _host_secrets: configured }, host: { locale: 'zh' } }
    if (type === 'plugin.resources') return { resources: directory }
    if (type === 'plugin.status') return { result: { healthy: running, status_json: JSON.stringify(runtime) } }
    if (type === 'config.save') return { config: { ...(payload?.config as object), _host_secrets: configured } }
    if (type === 'plugin.secrets.edit') return { result: { configured } }
    if (type === 'plugin.action') return { result: { accepted: true, action_id: payload?.action_id, status: 'queued' } }
    throw new Error('unexpected')
  })
  const bridge = { request, notify: vi.fn(), dispose: vi.fn() }
  const wrapper = mount(defineComponent({ setup() { state = useState(bridge); return () => h('div') } }))
  wrappers.push(wrapper)
  return { bridge, request, runtime, wrapper, configured }
}

beforeEach(() => vi.useFakeTimers())
afterEach(() => { for (const wrapper of wrappers.splice(0)) wrapper.unmount(); vi.useRealTimers() })

describe('plugin form and lifecycle', () => {
  it('loads and polls without saving, testing or starting collection', async () => {
    const { request } = harness()
    await flushPromises()
    expect(state.draft.enabled).toBe(false)
    expect(state.accountConfig(123).models[astra].enabled).toBe(false)
    expect(state.accountConfig(124).models[astra].enabled).toBe(false)
    expect(state.dirty.value).toBe(false)
    await vi.advanceTimersByTimeAsync(12000)
    expect([...new Set(request.mock.calls.map(c => c[0]))].sort()).toEqual(['config.load', 'plugin.resources', 'plugin.status'])
    expect(state.canHarvest(directory.accounts[0], astra)).toBe(false)
  })

  it('preserves configuration drafts across status polls, reload and filtering', async () => {
    const { request } = harness()
    await flushPromises()
    state.draft.enabled = true
    state.editModel(123, astra, 'ticket_plan', 'team')
    state.editModel(124, astra, 'enabled', true)
    await state.refresh()
    await state.load()
    state.group.value = '9'
    expect(state.accounts.value.map(a => a.id)).toEqual([123])
    state.group.value = '10'
    expect(state.accounts.value.map(a => a.id)).toEqual([124])
    expect(state.accountConfig(123).models[astra].ticket_plan).toBe('team')
    expect(state.accountConfig(124).models[astra].enabled).toBe(true)
    expect(state.draft.harvest_proxy_url).toBe('')
    expect(state.draft.enabled).toBe(true)
    expect(request.mock.calls.filter(c => c[0] === 'config.load')).toHaveLength(1)
    expect(state.dirty.value).toBe(true)
  })

  it('keeps the catalog readable when the plugin is not running but blocks actions', async () => {
    const { request } = harness(true, false)
    await flushPromises()
    expect(state.accounts.value).toHaveLength(2)
    expect(state.canHarvest(directory.accounts[0], astra)).toBe(false)
    await state.action(directory.accounts[0], astra, 'harvest')
    expect(request.mock.calls.some(c => c[0] === 'plugin.action')).toBe(false)
  })

  it('saves exact config and preserves edits made while the save is in flight', async () => {
    const { request } = harness()
    await flushPromises()
    state.draft.enabled = true
    state.editModel(123, astra, 'enabled', true)
    let complete!: (data: Record<string, unknown>) => void
    request.mockImplementationOnce(() => new Promise(resolve => { complete = resolve }))
    const saving = state.save()
    state.draft.accounts[0].models[astra].ticket_plan = 'team'
    complete({ config: {} })
    await saving
    expect(state.draft.accounts[0].models[astra].ticket_plan).toBe('team')
    expect(state.dirty.value).toBe(true)
    const call = request.mock.calls.find(c => c[0] === 'config.save')!
    expect(Object.keys(call[1]!.config as object).sort()).toEqual(['accounts', 'dial_proxy_url', 'enabled', 'harvest_proxy_url', 'version'])
    expect(JSON.stringify(state.status.value)).not.toContain('SECRET')
  })

  it('requires saved enabled settings and safe egress, sends only the target payload', async () => {
    const { request, runtime } = harness(true)
    await flushPromises()
    expect(state.canHarvest(directory.accounts[0], astra)).toBe(true)
    expect(state.canHarvest(directory.accounts[1], astra)).toBe(false)
    await state.action(directory.accounts[0], astra, 'harvest')
    const call = request.mock.calls.find(c => c[0] === 'plugin.action')!
    expect(call[1]).toEqual({ action_id: expect.stringMatching(/^state-/), name: 'harvest', payload: { account_id: 123, model: astra } })
    expect(JSON.stringify(call)).not.toContain('SECRET')
    runtime.accounts[0].models[astra].state = 'harvesting'
    runtime.accounts[0].models[astra].refreshing = true
    await state.refresh()
    expect(state.canCancel(directory.accounts[0], astra)).toBe(true)
    state.editModel(123, astra, 'ticket_plan', 'team')
    expect(state.canHarvest(directory.accounts[0], astra)).toBe(false)
    await state.action(directory.accounts[0], astra, 'cancel')
    expect(request.mock.calls.filter(c => c[0] === 'plugin.action').at(-1)![1]?.name).toBe('cancel')
  })

  it('reuses an action id after uncertain transport failure and never retains exception text', async () => {
    const { request } = harness(true)
    await flushPromises()
    request.mockRejectedValueOnce(new Error('SECRET proxy credentials'))
    await state.action(directory.accounts[0], astra, 'harvest')
    expect(state.errors.operation).toBe('actionFailed')
    await state.action(directory.accounts[0], astra, 'harvest')
    const calls = request.mock.calls.filter(c => c[0] === 'plugin.action')
    expect(calls).toHaveLength(2)
    expect(calls[0][1]?.action_id).toBe(calls[1][1]?.action_id)
    expect(JSON.stringify(state.errors)).not.toContain('SECRET')
  })

  it('allows cancellation of queued work and prevents duplicate harvests', async () => {
    const { request, runtime } = harness(true)
    runtime.accounts[0].models[astra].state = 'queued'
    runtime.accounts[0].models[astra].refreshing = true
    await flushPromises()
    await state.refresh()
    expect(state.modelStatus(123, astra).state).toBe('queued')
    expect(state.canHarvest(directory.accounts[0], astra)).toBe(false)
    expect(state.canCancel(directory.accounts[0], astra)).toBe(true)
    await state.action(directory.accounts[0], astra, 'cancel')
    expect(request.mock.calls.find(c => c[0] === 'plugin.action')![1]?.name).toBe('cancel')
  })

  it('clears polling on unmount', async () => {
    const { request, wrapper, bridge } = harness()
    await flushPromises()
    expect(request.mock.calls.some(c => c[0] === 'config.save')).toBe(false)
    wrapper.unmount()
    const calls = request.mock.calls.length
    await vi.advanceTimersByTimeAsync(10000)
    expect(request).toHaveBeenCalledTimes(calls)
    expect(bridge.dispose).toHaveBeenCalled()
  })

  it('never retains raw config secrets and sends only a parameter-free edit request', async () => {
    const { request, configured } = harness(true)
    await flushPromises()
    expect(state.draft.harvest_proxy_url).toBe('')
    expect(JSON.stringify(state.draft)).not.toContain('SECRET')
    expect(state.hostSecrets.harvest_proxy_url).toBe(true)
    expect(state.canHarvest(directory.accounts[0], astra)).toBe(true)
    configured.harvest_proxy_url = false
    await state.editSecrets()
    expect(request.mock.calls.find(c => c[0] === 'plugin.secrets.edit')).toEqual(['plugin.secrets.edit'])
    expect(state.hostSecrets.harvest_proxy_url).toBe(false)
    expect(state.canHarvest(directory.accounts[0], astra)).toBe(false)
    expect(state.dirty.value).toBe(false)
    state.editModel(123, astra, 'ticket_plan', 'team')
    await state.save()
    const saved = request.mock.calls.find(c => c[0] === 'config.save')![1]!.config
    expect(saved).toMatchObject({ harvest_proxy_url: '', dial_proxy_url: '' })
    expect(saved).not.toHaveProperty('_host_secrets')
    expect(JSON.stringify(saved)).not.toContain('SECRET')
  })

  it('preserves drafts while editing secrets, projects only flags and blocks overlapping saves', async () => {
    const { request } = harness()
    await flushPromises()
    state.editModel(123, astra, 'ticket_plan', 'team')
    let complete!: (response: Record<string, unknown>) => void
    request.mockImplementationOnce(() => new Promise(resolve => { complete = resolve }))
    const editing = state.editSecrets()
    await state.save()
    expect(request.mock.calls.some(c => c[0] === 'config.save')).toBe(false)
    complete({ result: { configured: { harvest_proxy_url: false, dial_proxy_url: true, extra: 'SECRET' }, values: 'SECRET' } })
    await editing
    expect(state.accountConfig(123).models[astra].ticket_plan).toBe('team')
    expect(state.dirty.value).toBe(true)
    expect(state.hostSecrets).toEqual({ harvest_proxy_url: false, dial_proxy_url: true })
    expect(state.editingSecrets.value).toBe(false)
  })

  it('keeps flags on cancel and never retains sensitive editor errors', async () => {
    const { request } = harness()
    await flushPromises()
    request.mockRejectedValueOnce(new Error('bridge_cancelled'))
    await state.editSecrets()
    expect(state.errors.operation).toBe('')
    expect(state.hostSecrets.harvest_proxy_url).toBe(true)
    request.mockRejectedValueOnce(new Error('PRIVATE proxy URL'))
    await state.editSecrets()
    expect(state.errors.operation).toBe('secretsFailed')
    expect(JSON.stringify(state.errors)).not.toContain('PRIVATE')
  })

  it('requires an explicit all-off initial save before secret editing without load side effects', async () => {
    const { request } = harness(false, false, true)
    await flushPromises()
    expect(state.needsInitialSave.value).toBe(true)
    expect(state.dirty.value).toBe(true)
    expect(state.draft.enabled).toBe(false)
    expect(state.draft.accounts).toEqual([])
    await state.editSecrets()
    expect(request.mock.calls.some(c => ['config.save', 'plugin.secrets.edit'].includes(c[0]))).toBe(false)
    await state.save()
    expect(request.mock.calls.find(c => c[0] === 'config.save')![1]).toEqual({ config: { version: 1, enabled: false, harvest_proxy_url: '', dial_proxy_url: '', accounts: [] } })
    expect(state.needsInitialSave.value).toBe(false)
    expect(state.dirty.value).toBe(false)
    await state.editSecrets()
    expect(request.mock.calls.some(c => c[0] === 'plugin.secrets.edit')).toBe(true)
  })

  it('fails closed when the outcome of a secret save and the metadata refresh are both unavailable', async () => {
    const { request } = harness(true)
    await flushPromises()
    expect(state.canHarvest(directory.accounts[0], astra)).toBe(true)
    request.mockRejectedValueOnce(new Error('PRIVATE uncertain save')).mockRejectedValueOnce(new Error('PRIVATE read failure'))
    await state.editSecrets()
    expect(state.hostSecrets).toEqual({ harvest_proxy_url: false, dial_proxy_url: false })
    expect(state.canHarvest(directory.accounts[0], astra)).toBe(false)
    expect(state.errors.operation).toBe('secretsFailed')
    expect(JSON.stringify(state.draft)).not.toContain('PRIVATE')
  })

  it('keeps secret editing locked when initial configuration save fails', async () => {
    const { request } = harness(false, false, true)
    await flushPromises()
    request.mockRejectedValueOnce(new Error('PRIVATE failure'))
    await state.save()
    expect(state.needsInitialSave.value).toBe(true)
    await state.editSecrets()
    expect(request.mock.calls.some(c => c[0] === 'plugin.secrets.edit')).toBe(false)
  })
})
