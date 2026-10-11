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
  id: 42, name: '测试上游', provider: 'sub2api', site_url: 'https://upstream.example',
  enabled: true, config_status: 'active', overall_status: 'operational',
  requests: 13260, completed_requests: 13210, failed_requests: 50, success_rate: 0.996,
  error_429: 4, error_5xx: 46, timeouts: 0, auth_config_errors: 0,
  account_count: 8, schedulable_account_count: 7, temp_unschedulable_count: 0,
  revenue: 425.256, upstream_cost: 220.96, estimated_gross_profit: 204.296,
  estimated_gross_profit_rate: 204.296 / 425.256,
  balance_available: true, balance_cny: 67.63, balance_low: false, open_incident_count: 0,
  probe: { samples: 0, healthy_samples: 0, confidence_samples: 0 }, data_quality: 'sufficient', ...overrides
})

let wrapper: VueWrapper | undefined
async function render(item: UpstreamDashboardCard, detailOverrides: Record<string, unknown> = {}) {
  dashboard.mockResolvedValue({ items: [item] })
  detail.mockResolvedValue({ ...item, traffic: { models: [] }, recent_errors: [], recent_incidents: [], recent_rate_changes: [], ...detailOverrides })
  wrapper = mount(View, { global: { stubs: {
    AppLayout: { template: '<main><slot /></main>' },
    BaseDialog: { props: ['show'], template: '<section v-if="show" role="dialog"><slot /></section>' },
    Select: true, Icon: true
  } } })
  await flushPromises()
  return wrapper
}

function expectFinancials(view: VueWrapper, scope: 'card' | 'detail', values: string[]) {
  const group = view.get(`[data-test="${scope}-financials"]`)
  const labels = ['渠道收入', '上游消费', '估算毛利', '毛利率']
  const metrics = ['revenue', 'cost', 'profit', 'margin']
  metrics.forEach((metric, index) => {
    const item = group.get(`[data-test="financial-${metric}"]`)
    expect(item.get('span').text()).toBe(labels[index])
    expect(item.get('strong').text()).toBe(values[index])
  })
}

beforeEach(() => vi.clearAllMocks())
afterEach(() => { wrapper?.unmount(); wrapper = undefined })

describe('upstream financial metrics', () => {
  it('shows income, upstream spending, gross profit and margin separately using the balance amount format', async () => {
    const view = await render(card())
    expectFinancials(view, 'card', ['¥425.26', '¥220.96', '¥204.30', '48.0%'])
    expect(view.get('.ops-value').text()).toContain('¥67.63')
    expect(view.get('.card-footer').text()).toContain('7/8')
    expect(view.get('.card-footer').text()).not.toContain('48.0%')
    expect(view.get('.success-metric').text()).toContain('99.6%')
    expect(view.get('.request-metric').text()).toContain('13,260')
  })

  it('shows the same four amounts and accounting explanation in details for the selected window', async () => {
    const view = await render(card())
    const weekTab = view.findAll('.window-tab').find(tab => tab.text() === '7 天')!
    await weekTab.trigger('click')
    await flushPromises()
    await view.get('.dashboard-card').trigger('click')
    await flushPromises()
    expect(detail).toHaveBeenCalledWith(42, '7d')
    expectFinancials(view, 'detail', ['¥425.26', '¥220.96', '¥204.30', '48.0%'])
    expect(view.get('[role="dialog"]').text()).toContain('收入按用户实际计费累计；消费沿用余额记账口径；毛利率 = 毛利 ÷ 收入。')
  })

  it('keeps negative gross profit amounts and margin visible in both views', async () => {
    const view = await render(card({ revenue: 20, upstream_cost: 25.25, estimated_gross_profit: -5.25, estimated_gross_profit_rate: -0.2625 }))
    expectFinancials(view, 'card', ['¥20.00', '¥25.25', '¥-5.25', '-26.3%'])
    expect(view.get('[data-test="financial-profit"] strong').classes()).toContain('profit-value-negative')
    await view.get('.dashboard-card').trigger('click')
    await flushPromises()
    expectFinancials(view, 'detail', ['¥20.00', '¥25.25', '¥-5.25', '-26.3%'])
  })

  it.each([
    ['missing', { revenue: null, upstream_cost: null, estimated_gross_profit: null, estimated_gross_profit_rate: null }],
    ['non-finite', { revenue: NaN, upstream_cost: Infinity, estimated_gross_profit: -Infinity, estimated_gross_profit_rate: Infinity }]
  ])('shows unavailable %s values without inventing zero income or profit', async (_, overrides) => {
    const view = await render(card(overrides))
    expectFinancials(view, 'card', ['-', '-', '-', '-'])
    await view.get('.dashboard-card').trigger('click')
    await flushPromises()
    expectFinancials(view, 'detail', ['-', '-', '-', '-'])
  })

  it('shows zero income as an amount but does not claim a margin without a positive denominator', async () => {
    const view = await render(card({ revenue: 0, upstream_cost: 2, estimated_gross_profit: -2, estimated_gross_profit_rate: 1 }))
    expectFinancials(view, 'card', ['¥0.00', '¥2.00', '¥-2.00', '-'])
    await view.get('.dashboard-card').trigger('click')
    await flushPromises()
    expectFinancials(view, 'detail', ['¥0.00', '¥2.00', '¥-2.00', '-'])
  })

  it('honors unavailable profit evidence in details without hiding known income and spending', async () => {
    const view = await render(card(), { profit_unavailable: true })
    await view.get('.dashboard-card').trigger('click')
    await flushPromises()
    expectFinancials(view, 'detail', ['¥425.26', '¥220.96', '-', '-'])
    expect(view.get('[role="dialog"]').text()).toContain('估算不可用')
  })
})
