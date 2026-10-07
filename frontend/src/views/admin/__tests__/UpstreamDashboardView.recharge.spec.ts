import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import type { UpstreamDashboardCard } from '@/api/admin/upstreamConfigs'
import zh from '@/i18n/locales/zh/admin/upstreamDashboard'
import View from '../UpstreamDashboardView.vue'

const { dashboard, detail, push } = vi.hoisted(() => ({ dashboard: vi.fn(), detail: vi.fn(), push: vi.fn() }))
vi.mock('@/api/admin/upstreamConfigs', () => ({ getDashboard: dashboard, getDashboardDetail: detail }))
vi.mock('@/components/layout/AppLayout.vue', () => ({ default: { template: '<main><slot /></main>' } }))
vi.mock('vue-router', () => ({ useRouter: () => ({ push }) }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({
  t: (key: string, values: Record<string, string | number> = {}) => {
    let result: unknown = zh
    for (const part of key.replace(/^admin\./, '').split('.')) result = (result as Record<string, unknown>)?.[part]
    return typeof result === 'string' ? result.replace(/\{(\w+)\}/g, (_, name: string) => String(values[name] ?? '')) : key
  }
}) }))

const card = (overrides: Partial<UpstreamDashboardCard> = {}): UpstreamDashboardCard => ({
  id: 42, name: '测试上游', provider: 'lcodex', site_url: 'https://upstream.example',
  enabled: true, config_status: 'active', overall_status: 'operational',
  requests: 10, completed_requests: 10, failed_requests: 0, success_rate: 1,
  error_429: 0, error_5xx: 0, timeouts: 0, auth_config_errors: 0,
  account_count: 1, schedulable_account_count: 1, temp_unschedulable_count: 0,
  balance_available: true, balance_cny: 50, balance_low: false, open_incident_count: 0,
  probe: { samples: 0, healthy_samples: 0, confidence_samples: 0 }, data_quality: 'sufficient', ...overrides
})

let wrapper: VueWrapper | undefined
async function render(item: UpstreamDashboardCard) {
  dashboard.mockResolvedValue({ items: [item] })
  detail.mockResolvedValue({ ...item, traffic: { models: [] }, recent_errors: [], recent_incidents: [], recent_rate_changes: [] })
  wrapper = mount(View, { global: { stubs: {
    AppLayout: { template: '<main><slot /></main>' },
    BaseDialog: { props: ['show'], template: '<section v-if="show" role="dialog"><slot /></section>' },
    Select: true, Icon: true
  } } })
  await flushPromises()
  return wrapper
}

beforeEach(() => vi.clearAllMocks())
afterEach(() => { wrapper?.unmount(); wrapper = undefined })

describe('upstream recharge actions', () => {
  it('offers a separate new-tab link beside the balance', async () => {
    const view = await render(card())
    const link = view.get('[data-test="card-recharge-link"]')
    expect(link.text()).toBe('去充值')
    expect(link.attributes('href')).toBe('https://upstream.example/purchase')
    expect(link.attributes('target')).toBe('_blank')
    expect(link.attributes('rel')).toBe('noopener noreferrer')
    expect(link.attributes('aria-label')).toContain('测试上游')
    expect(link.classes()).not.toContain('recharge-link-alert')
    await link.trigger('click')
    expect(detail).not.toHaveBeenCalled()
    expect(view.find('[role="dialog"]').exists()).toBe(false)
  })

  it('highlights recharge only when the channel reports low balance', async () => {
    const view = await render(card({ balance_low: true }))
    expect(view.get('[data-test="card-recharge-link"]').classes()).toContain('recharge-link-alert')
    expect(view.text()).toContain('余额不足，请及时充值')
  })

  it('still offers recharge when the balance snapshot is unavailable', async () => {
    const view = await render(card({ balance_available: false, balance_cny: null }))
    expect(view.get('[data-test="card-recharge-link"]').attributes('href')).toBe('https://upstream.example/purchase')
    expect(view.get('[data-test="card-recharge-link"]').classes()).not.toContain('recharge-link-alert')
  })

  it.each(['Enter', ' '])('does not open detail or cancel native link behavior for %s', async key => {
    const view = await render(card())
    const event = new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true })
    view.get('[data-test="card-recharge-link"]').element.dispatchEvent(event)
    await flushPromises()
    expect(event.defaultPrevented).toBe(false)
    expect(detail).not.toHaveBeenCalled()
  })

  it('retains ordinary card interaction and shows the same link in details', async () => {
    const view = await render(card({ balance_low: true }))
    await view.get('.dashboard-card').trigger('click')
    await flushPromises()
    expect(detail).toHaveBeenCalledWith(42, '24h')
    const link = view.get('[data-test="detail-recharge-link"]')
    expect(link.attributes('href')).toBe('https://upstream.example/purchase')
    expect(link.classes()).toContain('recharge-link-alert')
    expect(link.attributes('target')).toBe('_blank')
  })

  it('does not expose an unsafe URL from the channel as a clickable link', async () => {
    const view = await render(card({ site_url: 'javascript:alert(1)' }))
    expect(view.find('[data-test="card-recharge-link"]').exists()).toBe(false)
    await view.get('.dashboard-card').trigger('click')
    await flushPromises()
    expect(view.find('[data-test="detail-recharge-link"]').exists()).toBe(false)
  })

  it('offers an honest site fallback when the provider has no known payment route', async () => {
    const view = await render(card({ provider: 'other', site_url: 'https://upstream.example/site/' }))
    const link = view.get('[data-test="card-recharge-link"]')
    expect(link.text()).toBe('访问站点')
    expect(link.attributes('href')).toBe('https://upstream.example/site/')
  })
})
