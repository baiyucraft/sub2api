import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import UpstreamProtectionStatus from '../UpstreamProtectionStatus.vue'
import locale from '@/i18n/locales/zh/admin/upstreamManagement'
import type { Account, AccountTTFTGuardDegradation, UpstreamProtectionGroupState, UpstreamProtectionSummary, UpstreamTTFTModelObservation } from '@/types'

vi.mock('vue-i18n', async () => ({ ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'), useI18n: () => ({
  t: (key: string, params: Record<string, unknown> = {}) => {
    let value: unknown = locale
    for (const part of key.replace('admin.', '').split('.')) value = (value as Record<string, unknown>)?.[part]
    return typeof value === 'string' ? value.replace(/\{(\w+)\}/g, (_, name: string) => String(params[name] ?? `{${name}}`)) : key
  },
  te: (key: string) => key.includes('low_cache_rate') || key.includes('healthy')
}) }))

const model = (state: UpstreamTTFTModelObservation['state'] = 'normal'): UpstreamTTFTModelObservation => ({
  model: 'gpt-real', state, ewma_ms: 35000, last_ttft_ms: 36000, sample_count: 10,
  last_sample_at: '2026-10-14T01:00:00Z', expires_at: '2026-10-14T01:15:00Z'
})
const group = (id: number, state: UpstreamProtectionGroupState['ttft']['state'] = 'degraded'): UpstreamProtectionGroupState => ({
  group_id: id, group_name: `分组 ${id}`, priority: id === 1 ? 'cache_first' : 'ttft_first', priority_source: 'group',
  ttft: { state, threshold_ms: id === 1 ? 20000 : 40000, min_samples: 5, source: 'group', models: [{ ...model(state), reason: 'ewma', recovery_samples: 1, recovery_samples_required: 3 }] },
  cache: { group_id: id, state: 'protected', rate_percent: 82.6, valid_requests: 42, reason: 'low_cache_rate', recovery_valid_requests: 0, recovery_rate_percent: null, recovery_sessions: 0, recovery_started_at: null, cooldown_until: null, updated_at: null, threshold_percent: 90, recovery_threshold_percent: 95, min_valid_requests: 20, source: 'group' }
})
const protection = (): UpstreamProtectionSummary => ({
  cache: { state: 'protected', rate_percent: 82.6, valid_requests: 42, window_minutes: 60, reason: 'low_cache_rate', recovery_valid_requests: 0, recovery_rate_percent: null, recovery_sessions: 0, recovery_started_at: null, cooldown_until: null, updated_at: null },
  ttft_models: [model()], groups: [group(1), group(2)]
})
const wrappers: ReturnType<typeof mount>[] = []
function render(value: UpstreamProtectionSummary) {
  const wrapper = mount(UpstreamProtectionStatus, {
    props: { account: { id: 1, name: '上游账号', upstream_protection: value } as Account },
    attachTo: document.body,
    global: { stubs: { Icon: true, teleport: true, transition: false } }
  })
  wrappers.push(wrapper)
  return wrapper
}
afterEach(() => { wrappers.forEach(wrapper => wrapper.unmount()); wrappers.length = 0; document.body.innerHTML = '' })

describe('UpstreamProtectionStatus', () => {
  it('shows an excluded account scope over retained facts and historical protection progress', async () => {
    const value = protection()
    value.platform_selected = false
    value.groups[0].cache.state = 'recovering'
    value.groups[0].cache.recovery_started_at = '2026-10-14T01:00:00Z'
    const wrapper = render(value)
    expect(wrapper.get('[data-test="protection-platform-summary"]').text()).toBe('调度保护 · 未纳入平台范围')
    expect(wrapper.find('[data-test="protection-cache-summary"]').exists()).toBe(false)
    expect(wrapper.find('[data-test="protection-ttft-summary"]').exists()).toBe(false)
    await wrapper.get('[data-test="protection-platform-summary"]').trigger('click')
    expect(wrapper.get('[data-test="protection-platform-excluded"]').text()).toContain('历史共享观测保留')
    expect(wrapper.get('[data-test="protection-cache-metrics"]').text()).toContain('82.6%')
    expect(wrapper.get('[data-test="protection-ttft-detail"]').text()).toContain('共享观测')
    expect(wrapper.get('[data-test="upstream-protection-details"]').text()).not.toContain('恢复进度')
    expect(wrapper.get('[data-test="upstream-protection-details"]').text()).not.toContain('渐出保护')
  })

  it('does not count excluded groups as protected and updates scope in an open detail view', async () => {
    const value = protection()
    value.platform_selected = true
    value.groups[0].platform_selected = false
    const wrapper = render(value)
    expect(wrapper.get('[data-test="protection-ttft-summary"]').text()).toContain('1模型／1组保护')
    await wrapper.get('[data-test="protection-ttft-summary"]').trigger('click')
    expect(wrapper.get('[data-protection-group="1"]').text()).toContain('未纳入平台范围')
    value.platform_selected = false
    await wrapper.setProps({ account: { id: 1, name: '上游账号', upstream_protection: structuredClone(value) } as Account })
    expect(wrapper.get('[data-test="protection-platform-summary"]').text()).toContain('未纳入平台范围')
    expect(wrapper.get('[data-test="upstream-protection-details"]').text()).not.toContain('恢复进度')
  })

  it('does not let an excluded group\'s historical protection override an active group\'s normal summary', () => {
    const value = protection()
    value.platform_selected = true
    value.groups[0].platform_selected = false
    value.groups[1].ttft.state = 'normal'
    value.groups[1].ttft.models[0].state = 'normal'
    value.groups[1].cache.state = 'normal'
    const wrapper = render(value)
    expect(wrapper.get('[data-test="protection-ttft-summary"]').text()).toBe('TTFT · 正常')
    expect(wrapper.get('[data-test="protection-cache-summary"]').text()).toBe('缓存 82.6% · 正常')
  })

  it('uses two compact summaries and one detail view for shared metrics and independent groups', async () => {
    const value = protection()
    const wrapper = render(value)
    expect(wrapper.get('[data-test="protection-cache-summary"]').text()).toBe('缓存 82.6% · 2组保护')
    expect(wrapper.get('[data-test="protection-ttft-summary"]').text()).toBe('TTFT · 1模型／2组保护')
    await wrapper.get('[data-test="protection-cache-summary"]').trigger('click')
    expect(wrapper.findAll('[data-test="upstream-protection-details"]')).toHaveLength(1)
    expect(wrapper.findAll('[data-test="protection-ttft-detail"]')).toHaveLength(1)
    expect(wrapper.get('[data-protection-group="1"]').text()).toContain('缓存优先')
    expect(wrapper.get('[data-protection-group="2"]').text()).toContain('TTFT 优先')
    expect(wrapper.get('[data-protection-group="1"]').text()).toContain('20.0s')
    expect(wrapper.get('[data-protection-group="2"]').text()).toContain('40.0s')
  })

  it('updates open details and removes protected counts after the latest row recovers', async () => {
    const wrapper = render(protection())
    await wrapper.get('[data-test="protection-ttft-summary"]').trigger('click')
    const value = protection()
    value.cache!.rate_percent = 96.2
    value.cache!.state = 'normal'
    value.groups.forEach(item => { item.ttft.state = 'normal'; item.ttft.models[0].state = 'normal'; item.cache.state = 'normal' })
    await wrapper.setProps({ account: { id: 1, name: '上游账号', upstream_protection: value } as Account })
    expect(wrapper.get('[data-test="protection-ttft-summary"]').text()).toBe('TTFT · 正常')
    expect(wrapper.get('[data-test="protection-cache-summary"]').text()).toBe('缓存 96.2% · 正常')
    expect(wrapper.get('[data-test="upstream-protection-details"]').text()).toContain('96.2%')
    expect(wrapper.get('[data-test="upstream-protection-details"]').text()).not.toContain('恢复进度')
  })

  it('shows the last usable cache snapshot until the API reports it unavailable', async () => {
    const value = protection()
    value.cache!.updated_at = '2026-10-16T01:00:00Z'
    const wrapper = render(value)
    await wrapper.get('[data-test="protection-cache-summary"]').trigger('click')

    // A delayed refresh within the tolerance window keeps the server's last
    // successful metrics. The component does not add another expiry window.
    await wrapper.setProps({ account: { id: 1, name: '上游账号', upstream_protection: structuredClone(value) } as Account })
    expect(wrapper.get('[data-test="protection-cache-summary"]').text()).toBe('缓存 82.6% · 2组保护')
    const metrics = wrapper.get('[data-test="protection-cache-metrics"]').text()
    expect(metrics).toContain('82.6%')
    expect(metrics).toContain('42')
    expect(metrics).not.toContain('已过期')

    const unavailable = structuredClone(value)
    Object.assign(unavailable.cache!, { state: 'unavailable', reason: 'snapshot_unavailable', rate_percent: null, valid_requests: 0, updated_at: null })
    unavailable.groups.forEach(item => Object.assign(item.cache, { state: 'unavailable', reason: 'snapshot_unavailable', rate_percent: null, valid_requests: 0, updated_at: null }))
    await wrapper.setProps({ account: { id: 1, name: '上游账号', upstream_protection: unavailable } as Account })
    expect(wrapper.get('[data-test="protection-cache-summary"]').text()).toBe('缓存 未知 · 暂不可用')
    expect(wrapper.get('[data-test="protection-cache-metrics"]').text()).not.toContain('82.6%')
    expect(wrapper.get('[data-test="protection-cache-metrics"]').text()).toContain('未知')
  })

  it.each(['insufficient', 'expired', 'unavailable', 'disabled'] as const)('does not infer normal from an empty model list (%s)', async state => {
    const value = protection()
    value.ttft_models = []
    value.groups = [group(1, state)]
    value.groups[0].ttft.models = []
    value.cache!.rate_percent = null
    const wrapper = render(value)
    expect(wrapper.get('[data-test="protection-ttft-summary"]').text()).toContain(locale.upstreamManagement.protection.state[state])
    expect(wrapper.get('[data-test="protection-ttft-summary"]').text()).not.toContain('正常')
    expect(wrapper.get('[data-test="protection-cache-summary"]').text()).toContain('未知')
    await wrapper.get('[data-test="protection-ttft-summary"]').trigger('click')
    expect(wrapper.get('[data-test="upstream-protection-details"]').text()).toContain('暂无有效 TTFT 观测')
  })

  it('keeps keyboard focus in the dialog and returns it to the selected summary after Escape', async () => {
    const wrapper = render(protection())
    const trigger = wrapper.get('[data-test="protection-ttft-summary"]')
    ;(trigger.element as HTMLButtonElement).focus()
    await trigger.trigger('click')
    await flushPromises()
    const first = wrapper.get('button[aria-label="Close modal"]').element as HTMLButtonElement
    const last = wrapper.get('[data-test="protection-close"]').element as HTMLButtonElement
    expect(document.activeElement).toBe(first)
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', shiftKey: true, cancelable: true }))
    expect(document.activeElement).toBe(last)
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', cancelable: true }))
    expect(document.activeElement).toBe(first)
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    await flushPromises()
    expect(wrapper.find('[data-test="upstream-protection-details"]').exists()).toBe(false)
    expect(document.activeElement).toBe(trigger.element)
  })

  it('shows neutral shared observations and does not invent unavailable configuration values', async () => {
    const value = protection()
    value.ttft_models = [model('observed')]
    value.groups[0].priority_source = 'unavailable'
    value.groups[0].ttft.source = 'unavailable'
    value.groups[0].cache.source = 'unavailable'
    const wrapper = render(value)
    expect(wrapper.get('[data-test="protection-ttft-summary"]').attributes('aria-label')).toContain('1模型／2组保护')
    await wrapper.get('[data-test="protection-ttft-summary"]').trigger('click')
    expect(wrapper.get('[data-test="protection-ttft-detail"]').text()).toContain('共享观测')
    expect(wrapper.get('[data-test="protection-ttft-detail"]').text()).not.toContain('正常')
    expect(wrapper.get('[data-protection-group="1"]').text()).toContain('优先策略暂不可用')
    expect(wrapper.get('[data-protection-group="1"]').text()).not.toContain('首字阈值')
    expect(wrapper.get('[data-protection-group="1"]').text()).not.toContain('低于 90%')
  })

  it('does not conceal a known legacy degradation when the unified DTO is unavailable', async () => {
    const wrapper = render(protection())
    const legacy = { model: 'gpt-real', reason: 'ewma', threshold_ms: 20000, last_ttft_ms: 36000, ewma_ms: 35000, sample_count: 5, recovery_samples: 0, recovery_samples_required: 3, degraded_at: '2026-10-14T01:00:00Z', last_sample_at: '2026-10-14T01:00:00Z', expires_at: '2026-10-14T01:15:00Z' } as AccountTTFTGuardDegradation
    await wrapper.setProps({ account: { id: 1, name: '上游账号', ttft_guard_degradations: [legacy] } as Account })
    expect(wrapper.get('[data-test="protection-ttft-summary"]').text()).toContain('1模型渐出保护')
    expect(wrapper.get('[data-test="protection-ttft-summary"]').text()).not.toContain('未见降级')
    await wrapper.get('[data-test="protection-ttft-summary"]').trigger('click')
    expect(wrapper.get('[data-test="upstream-protection-details"]').text()).toContain('已有旧格式 TTFT 降级记录')
  })
})
