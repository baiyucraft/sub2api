import { beforeEach, describe, expect, it, vi } from 'vitest'
import plugins from '../plugins'

const { get, post, put } = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn(), put: vi.fn() }))
vi.mock('../../client', () => ({ apiClient: { get, post, put } }))

describe('plugin resources, actions and upgrade API', () => {
  beforeEach(() => { vi.clearAllMocks(); get.mockResolvedValue({ data: {} }); post.mockResolvedValue({ data: {} }); put.mockResolvedValue({ data: {} }) })
  it('updates only explicitly changed secrets through the trusted endpoint', async () => {
    put.mockResolvedValue({ data: { endpoint: '', _host_secrets: { endpoint: true, token: false } } })
    const result = await plugins.saveSecrets(7, { endpoint: 'PRIVATE', token: '' })
    expect(put).toHaveBeenCalledWith('/admin/plugins/7/config/secrets', { values: { endpoint: 'PRIVATE', token: '' } })
    expect(result).toEqual({ endpoint: '', _host_secrets: { endpoint: true, token: false } })
  })
  it('uses the plugin-scoped metadata endpoint', async () => {
    await plugins.resources(7)
    expect(get).toHaveBeenCalledWith('/admin/plugins/7/resources')
  })
  it('sends the exact action envelope', async () => {
    const request = { action_id: 'request-1', name: 'harvest', payload: { account_id: 123, model: 'gpt-6-astra' } }
    await plugins.action(7, request)
    expect(post).toHaveBeenCalledWith('/admin/plugins/7/actions', request)
  })
  it('uploads upgrades with the plugin multipart field', async () => {
    const file = new File(['package'], 'state.s2plugin')
    await plugins.upgrade(7, file)
    expect(post.mock.calls[0][0]).toBe('/admin/plugins/7/upgrade')
    expect((post.mock.calls[0][1] as FormData).get('plugin')).toBe(file)
    expect(post.mock.calls[0][2].timeout).toBe(120000)
  })
})
