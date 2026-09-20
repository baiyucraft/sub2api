import { mount, flushPromises, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import App from '../src/App.vue'

const { request } = vi.hoisted(() => ({ request: vi.fn() }))
vi.mock('../src/bridge', () => ({ createBridge: () => ({ request, notify: vi.fn(), dispose: vi.fn() }) }))
const wrappers: VueWrapper[] = []
beforeEach(() => {
  vi.useFakeTimers()
  request.mockReset()
  request.mockImplementation(async (type: string) => {
    if (type === 'config.load') return { config: { version: 1, harvest_proxy_url: 'PRIVATE', dial_proxy_url: 'SECRET', _host_secrets: { harvest_proxy_url: true, dial_proxy_url: false } }, host: { locale: 'en' } }
    if (type === 'plugin.secrets.edit') return { result: { configured: { harvest_proxy_url: false, dial_proxy_url: true } } }
    if (type === 'plugin.resources') return { resources: { accounts: [{ id: 123, name: 'Example OAuth', platform: 'openai', account_type: 'oauth', group_ids: [1], business_egress_configured: true }], groups: [{ id: 1, name: 'Production' }] } }
    if (type === 'plugin.status') return { result: { healthy: true, status_json: JSON.stringify({ running: true,
      accounts: [{ account_id: 123, models: { 'gpt-6-astra': { state: 'ready', active: { remaining_seconds: 3600, usable: true }, last_error: 'http://user:PRIVATE@host Authorization: Bearer SECRET' } } }],
      logs: [{ code: 'harvest_persisted', account_id: 123, model: 'gpt-6-astra', at: '2026-09-20T15:00:00Z' }, { message: 'PRIVATE SECRET' }],
    }) } }
    return {}
  })
})
afterEach(() => { for (const wrapper of wrappers.splice(0)) wrapper.unmount(); vi.useRealTimers() })

describe('standalone plugin page', () => {
  it('renders disabled switches and configured flags, with no credential inputs or values', async () => {
    const wrapper = mount(App)
    wrappers.push(wrapper)
    await flushPromises()
    expect(wrapper.findAll('[data-model]')).toHaveLength(3)
    expect(wrapper.findAll<HTMLInputElement>('input[role="switch"]').every(input => !input.element.checked)).toBe(true)
    expect(wrapper.findAll('input[type="password"]')).toHaveLength(0)
    expect(wrapper.get('[data-testid="harvest-proxy-status"]').text()).toBe('Configured')
    expect(wrapper.get('[data-testid="dial-proxy-status"]').text()).toBe('Not configured')
    expect(wrapper.text()).toContain('60m 00s')
    expect(wrapper.text()).toContain('Diagnostic details withheld')
    expect(wrapper.html()).not.toMatch(/PRIVATE|SECRET|Authorization/)
  })

  it('keeps unsaved plan edits when changing language, tabs or filters', async () => {
    const wrapper = mount(App)
    wrappers.push(wrapper)
    await flushPromises()
    await wrapper.get('[aria-label="Astra Plan"]').setValue('team')
    await wrapper.get('[aria-label="Language"]').setValue('zh')
    await wrapper.get('[aria-label="搜索账号"]').setValue('no-match')
    expect(wrapper.find('[data-account]').exists()).toBe(false)
    await wrapper.get('[aria-label="搜索账号"]').setValue('Example')
    expect((wrapper.get('[aria-label="Astra 套餐"]').element as HTMLSelectElement).value).toBe('team')
    expect(wrapper.get('[data-testid="harvest-proxy-status"]').text()).toBe('已配置')
    await wrapper.findAll('.tabs button')[1].trigger('click')
    expect(wrapper.text()).toContain('已保存验证通过的票据')
    expect(wrapper.text()).not.toContain('PRIVATE')
    expect(wrapper.text()).not.toContain('SECRET')
    expect(request.mock.calls.every(call => !['config.save', 'plugin.action', 'config.test'].includes(call[0]))).toBe(true)
  })

  it('opens only the trusted host editor and updates flags after explicit completion', async () => {
    const wrapper = mount(App)
    wrappers.push(wrapper)
    await flushPromises()
    await wrapper.get('[data-testid="edit-secrets"]').trigger('click')
    await flushPromises()
    expect(request.mock.calls.find(call => call[0] === 'plugin.secrets.edit')).toEqual(['plugin.secrets.edit'])
    expect(wrapper.get('[data-testid="harvest-proxy-status"]').text()).toBe('Not configured')
    expect(wrapper.get('[data-testid="dial-proxy-status"]').text()).toBe('Configured')
    expect(wrapper.html()).not.toMatch(/PRIVATE|SECRET|type="password"/)
  })

  it('shows a concise initial-save reason in both languages without auto-saving', async () => {
    const implementation = request.getMockImplementation()!
    request.mockImplementation(async (type: string) => type === 'config.load' ? { config: {}, host: { locale: 'en' } } : implementation(type))
    const wrapper = mount(App)
    wrappers.push(wrapper)
    await flushPromises()
    expect(wrapper.text()).toContain('Save initial configuration first.')
    expect(wrapper.get('[data-testid="edit-secrets"]').attributes('disabled')).toBeDefined()
    expect(wrapper.findAll('button').find(button => button.text().includes('Save configuration'))!.attributes('disabled')).toBeUndefined()
    await wrapper.get('[aria-label="Language"]').setValue('zh')
    expect(wrapper.text()).toContain('请先保存初始配置。')
    expect(request.mock.calls.every(call => !['config.save', 'plugin.secrets.edit'].includes(call[0]))).toBe(true)
  })
})
