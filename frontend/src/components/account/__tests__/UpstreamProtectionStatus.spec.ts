import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import UpstreamProtectionStatus from '../UpstreamProtectionStatus.vue'
import locale from '@/i18n/locales/zh/admin/upstreamManagement'
import enLocale from '@/i18n/locales/en/admin/upstreamManagement'
import type { Account, AccountTTFTGuardDegradation, UpstreamProtectionGroupState, UpstreamProtectionSummary, UpstreamTTFTModelObservation } from '@/types'

vi.mock('vue-i18n', async () => ({ ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'), useI18n: () => ({
  t: (key: string, params: Record<string, unknown> = {}) => {
    let value: unknown = locale
    for (const part of key.replace('admin.', '').split('.')) value = (value as Record<string, unknown>)?.[part]
    return typeof value === 'string' ? value.replace(/\{(\w+)\}/g, (_, name: string) => String(params[name] ?? `{${name}}`)) : key
  },
  te: (key: string) => { let value: unknown = locale; for (const part of key.replace('admin.', '').split('.')) value = (value as Record<string, unknown>)?.[part]; return typeof value === 'string' }
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
    expect(wrapper.get('[data-test="protection-cache-summary"]').text()).toBe('缓存 未知 · 样本不足')
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
    expect(wrapper.get('[data-test="protection-ttft-summary"]').text()).toContain('样本不足')
    expect(wrapper.get('[data-test="protection-ttft-summary"]').text()).not.toContain('正常')
    expect(wrapper.get('[data-test="protection-cache-summary"]').text()).toContain('未知')
    await wrapper.get('[data-test="protection-ttft-summary"]').trigger('click')
    expect(wrapper.get('[data-test="upstream-protection-details"]').text()).toContain('暂无有效 TTFT 观测')
  })

  it.each(['insufficient', 'expired'] as const)('shows insufficient samples across TTFT summaries and details (%s)', async state => {
    const value = protection()
    value.ttft_models = [model(state)]
    value.groups = [group(1, state), group(2, 'normal')]
    const wrapper = render(value)
    expect(wrapper.get('[data-test="protection-ttft-summary"]').text()).toBe('TTFT · 样本不足')
    await wrapper.get('[data-test="protection-ttft-summary"]').trigger('click')
    expect(wrapper.get('[data-test="protection-ttft-detail"] .state-badge').text()).toBe('样本不足')
    const details = wrapper.get('[data-protection-group="1"] [data-test="protection-group-ttft"]')
    expect(details.get('.state-badge').text()).toBe('样本不足')
    expect(details.get('.rounded-lg .font-medium + span').text()).toBe('样本不足')
    expect(wrapper.text()).not.toContain('数据过期')
    expect(wrapper.text()).not.toContain('恢复进度')
  })

  it.each(['insufficient', 'protected', 'normal'] as const)('shows the primary three-state contract (%s)', async state => {
    const value = protection()
    value.groups = [group(1, state)]
    value.groups[0].ttft.display_state = state
    value.groups[0].ttft.models[0].state = state
    const wrapper = render(value)
    await wrapper.get('[data-test="protection-ttft-summary"]').trigger('click')
    const details = wrapper.get('[data-protection-group="1"] [data-test="protection-group-ttft"]')
    expect(details.get('.state-badge').text()).toBe(locale.upstreamManagement.protection.state[state])
    expect(details.get('[data-test="protection-model-policy"] .font-medium + span').text()).toBe(locale.upstreamManagement.protection.state[state])
    expect(details.text()).not.toContain('渐出保护')
    expect(details.text()).not.toContain('恢复中')
  })

  it('uses the current display state instead of retained legacy execution labels', async () => {
    const value = protection()
    value.groups = [group(1, 'draining')]
    value.groups[0].ttft.display_state = 'normal'
    value.groups[0].ttft.models[0].display_state = 'normal'
    value.groups[0].cache.display_state = 'normal'
    const wrapper = render(value)
    expect(wrapper.get('[data-test="protection-ttft-summary"]').text()).toBe('TTFT · 正常')
    expect(wrapper.get('[data-test="protection-cache-summary"]').text()).toBe('缓存 82.6% · 正常')
    await wrapper.get('[data-test="protection-ttft-summary"]').trigger('click')
    expect(wrapper.find('[data-test="protection-recovery-evidence"]').exists()).toBe(false)
    expect(wrapper.find('[data-test="protection-routing-hint"]').exists()).toBe(false)
  })

  it('separates the historical trigger from current quality and recovery contributions', async () => {
    const value = protection()
    value.groups = [group(1, 'protected')]
    Object.assign(value.groups[0].ttft, { threshold_ms: 75000, recovery_threshold_ms: 45000 })
    Object.assign(value.groups[0].ttft.models[0], {
      reason: 'critical_sample', trigger_ttft_ms: 230000, trigger_threshold_ms: 75000,
      triggered_at: '2026-10-14T01:00:00Z', bad_evidence_expires_at: '2026-10-14T01:15:00Z',
      ewma_ms: 12000, last_ttft_ms: 5000, recovery_samples: 2, recovery_samples_required: 3,
      recovery_session_count: 2, recovery_sessions_required: 2, recovery_ordinary_samples: 1,
      recovery_trial_samples: 1, recovery_reason: 'recovery_samples_insufficient',
      runtime_id: 'runtime-1', algorithm_version: 'ttft-window-v2', policy_generation: 8, revalidation_epoch: 2
    })
    const wrapper = render(value)
    await wrapper.get('[data-test="protection-ttft-summary"]').trigger('click')
    const details = wrapper.get('[data-protection-group="1"] [data-test="protection-group-ttft"]')
    expect(details.text()).toContain('首字阈值 75.0s · 恢复目标 ≤ 45.0s')
    expect(details.get('[data-test="protection-trigger-evidence"]').text()).toContain('当次 TTFT 230.0s · 当时阈值 75.0s')
    expect(details.text()).toContain('当前质量：平滑 12.0s · 最近 5.0s · 10 个样本')
    expect(details.get('[data-test="protection-recovery-evidence"]').text()).toContain('恢复进度 2/3')
    expect(details.text()).toContain('恢复会话 2/2')
    expect(details.text()).toContain('恢复贡献：普通请求 1 · 试探请求 1')
    expect(details.text()).toContain('等待更多合格恢复样本')
    expect(details.get('[data-test="protection-runtime"]').text()).toContain('ttft-window-v2')
    expect(details.get('[data-test="protection-routing-hint"]').text()).toContain('不等于保护账号全部流量的上限')
  })

  it('keeps recovery gaps visible after bad evidence expires, then hides them after revalidation', async () => {
    const value = protection()
    value.groups = [group(1, 'insufficient')]
    Object.assign(value.groups[0].ttft.models[0], {
      recovery_samples: 3, recovery_samples_required: 3,
      recovery_session_count: 1, recovery_sessions_required: 2,
      recovery_ordinary_samples: 3, recovery_trial_samples: 0,
      recovery_reason: 'recovery_sessions_insufficient', revalidation_epoch: 2
    })
    const wrapper = render(value)
    await wrapper.get('[data-test="protection-ttft-summary"]').trigger('click')
    const details = wrapper.get('[data-test="protection-model-policy"]')
    expect(details.get('[data-test="protection-recovery-evidence"]').text()).toContain('恢复进度 3/3')
    expect(details.get('[data-test="protection-recovery-evidence"]').text()).toContain('恢复会话 1/2')
    expect(details.get('[data-test="protection-recovery-evidence"]').text()).toContain('普通请求 3 · 试探请求 0')

    const recovered = structuredClone(value)
    Object.assign(recovered.groups[0].ttft.models[0], { state: 'normal', display_state: 'normal', recovery_reason: '', revalidation_epoch: 0 })
    recovered.groups[0].ttft.state = 'normal'
    recovered.groups[0].ttft.display_state = 'normal'
    await wrapper.setProps({ account: { id: 1, name: '上游账号', upstream_protection: recovered } as Account })
    expect(wrapper.find('[data-test="protection-recovery-evidence"]').exists()).toBe(false)
  })

  it('shows protocol-specific execution paths and does not equate send attempts with receipt', async () => {
    const value = protection()
    value.groups = [group(1, 'protected')]
    value.groups[0].ttft.models[0].executions = [
      { protocol: 'http_sse', runtime_id: 'runtime-1', selection_path: 'capacity_fallback', fallback_reason: 'preferred_capacity_unavailable', send_attempted: true, completed: true, succeeded: false, trial: false, recovery_counted: false, recovery_reason: 'failure', observed_at: '2026-10-14T01:02:00Z', window_started_at: '2026-10-14T01:00:00Z', sent_count: 8, completed_count: 7, success_count: 6, trial_count: 1 },
      { protocol: 'ws_v2', runtime_id: 'runtime-2', selection_path: 'trial', send_attempted: true, completed: true, succeeded: true, trial: true, recovery_counted: true, recovery_reason: 'counted', observed_at: '2026-10-14T01:03:00Z', window_started_at: '2026-10-14T01:00:00Z', sent_count: 2, completed_count: 2, success_count: 2, trial_count: 2 }
    ]
    const wrapper = render(value)
    await wrapper.get('[data-test="protection-ttft-summary"]').trigger('click')
    const executions = wrapper.findAll('[data-test="protection-execution"]')
    expect(executions).toHaveLength(2)
    expect(executions[0].text()).toContain('最近执行 · http_sse')
    expect(executions[0].text()).toContain('选择路径: 可用容量回退')
    expect(executions[0].text()).toContain('更优候选无可用容量')
    expect(executions[0].text()).toContain('尝试发送 是 · 执行结果 未成功 · 恢复试探 否')
    expect(executions[0].text()).toContain('尝试发送 8 · 已完成 7 · 成功 6 · 试探 1')
    expect(executions[0].text()).toContain('不能证明上游已收到或处理')
    expect(executions[0].get('[data-test="protection-execution-recovery"]').text()).toContain('本次计入恢复 否')
    expect(executions[0].get('[data-test="protection-execution-recovery"]').text()).toContain('请求未成功完成')
    expect(executions[1].text()).toContain('最近执行 · ws_v2')
    expect(executions[1].text()).toContain('runtime-2')
    expect(executions[1].get('[data-test="protection-execution-recovery"]').text()).toContain('本次计入恢复 是')
  })

  it.each(Object.keys(locale.upstreamManagement.protection.recoveryBlockReasons) as Array<keyof typeof locale.upstreamManagement.protection.recoveryBlockReasons>)('explains why a recent request did not advance recovery (%s)', async reason => {
    const value = protection()
    value.groups = [group(1, 'protected')]
    Object.assign(value.groups[0].ttft.models[0], { last_recovery_block_reason: reason, last_recovery_block_at: '2026-10-14T01:02:00Z' })
    const wrapper = render(value)
    await wrapper.get('[data-test="protection-ttft-summary"]').trigger('click')
    const block = wrapper.get('[data-test="protection-recovery-block"]')
    expect(block.text()).toContain(locale.upstreamManagement.protection.recoveryBlockReasons[reason])
    expect(block.text()).toContain('发生时间:')
    expect(block.text()).not.toContain('—')
    expect(enLocale.upstreamManagement.protection.recoveryBlockReasons[reason]).toBeTruthy()
    expect(wrapper.get('[data-test="protection-recovery-evidence"]').text()).toContain('恢复进度 1/3')

    const recovered = structuredClone(value)
    Object.assign(recovered.groups[0].ttft.models[0], { state: 'normal', last_recovery_block_reason: '', last_recovery_block_at: null })
    recovered.groups[0].ttft.state = 'normal'
    await wrapper.setProps({ account: { id: 1, name: '上游账号', upstream_protection: recovered } as Account })
    expect(wrapper.find('[data-test="protection-recovery-block"]').exists()).toBe(false)
  })

  it('retains unknown recovery reasons and omits invalid timestamps', async () => {
    const value = protection()
    value.groups = [group(1, 'protected')]
    Object.assign(value.groups[0].ttft.models[0], { last_recovery_block_reason: 'future_reason', last_recovery_block_at: '0001-01-01T00:00:00Z' })
    const wrapper = render(value)
    await wrapper.get('[data-test="protection-ttft-summary"]').trigger('click')
    const block = wrapper.get('[data-test="protection-recovery-block"]')
    expect(block.text()).toContain('future_reason')
    expect(block.text()).not.toContain('发生时间')
  })

  it.each([false, undefined] as const)('distinguishes pending or unknown execution from terminal failure (%s)', async completed => {
    const value = protection()
    value.groups = [group(1, 'protected')]
    value.groups[0].ttft.models[0].executions = [{ protocol: 'openai', runtime_id: 'runtime-1', selection_path: 'normal', send_attempted: true, completed, succeeded: false, trial: false, observed_at: '2026-10-14T01:02:00Z', window_started_at: '2026-10-14T01:00:00Z', sent_count: 1, completed_count: 0, success_count: 0, trial_count: 0 }]
    const wrapper = render(value)
    await wrapper.get('[data-test="protection-ttft-summary"]').trigger('click')
    const execution = wrapper.get('[data-test="protection-execution"]').text()
    expect(execution).toContain(completed === false ? '执行结果 进行中' : '执行结果 终态未知')
    expect(execution).not.toContain('未成功')
    expect(wrapper.find('[data-test="protection-execution-recovery"]').exists()).toBe(false)
  })

  it.each(['counted', 'pending_completion', 'not_required', 'observation_rejected', 'awaiting_second_session'] as const)('uses the server recovery verdict independently of execution success (%s)', async reason => {
    const value = protection()
    value.groups = [group(1, 'protected')]
    value.groups[0].ttft.models[0].executions = [{ protocol: 'http_sse', runtime_id: 'runtime-1', selection_path: 'normal', send_attempted: true, completed: true, succeeded: true, trial: false, recovery_counted: reason === 'counted', recovery_reason: reason, observed_at: '2026-10-14T01:02:00Z', window_started_at: '2026-10-14T01:00:00Z', sent_count: 1, completed_count: 1, success_count: 1, trial_count: 0 }]
    const wrapper = render(value)
    await wrapper.get('[data-test="protection-ttft-summary"]').trigger('click')
    const recovery = wrapper.get('[data-test="protection-execution-recovery"]')
    expect(recovery.text()).toContain(`本次计入恢复 ${reason === 'counted' ? '是' : '否'}`)
    expect(recovery.text()).toContain(locale.upstreamManagement.protection.recoveryBlockReasons[reason])
    const legacy = structuredClone(value)
    delete legacy.groups[0].ttft.models[0].executions![0].recovery_counted
    await wrapper.setProps({ account: { id: 1, name: '上游账号', upstream_protection: legacy } as Account })
    expect(wrapper.find('[data-test="protection-execution-recovery"]').exists()).toBe(false)
  })

  it('does not invent historical trigger evidence or missing recovery counters for legacy data', async () => {
    const value = protection()
    value.groups = [group(1)]
    Object.assign(value.groups[0].ttft.models[0], { trigger_ttft_ms: 0, trigger_threshold_ms: 0, triggered_at: '0001-01-01T00:00:00Z' })
    const wrapper = render(value)
    await wrapper.get('[data-test="protection-ttft-summary"]').trigger('click')
    const details = wrapper.get('[data-test="protection-model-policy"]')
    expect(details.get('[data-test="protection-trigger-evidence"]').text()).toBe('触发证据暂无可核验的触发快照')
    expect(details.text()).toContain('恢复会话 —/—')
    expect(details.text()).toContain('普通请求 — · 试探请求 —')
    expect(details.text()).not.toContain('0001')
    expect(details.find('[data-test="protection-runtime"]').exists()).toBe(false)
    expect(details.find('[data-test="protection-recovery-block"]').exists()).toBe(false)
  })

  it('keeps legacy expired TTFT labels compatible in both languages', () => {
    expect(locale.upstreamManagement.protection.state.expired).toBe('样本不足')
    expect(enLocale.upstreamManagement.protection.state.expired).toBe('Insufficient samples')
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
    expect(wrapper.get('[data-test="protection-ttft-summary"]').text()).toContain('1模型保护')
    expect(wrapper.get('[data-test="protection-ttft-summary"]').text()).not.toContain('未见降级')
    await wrapper.get('[data-test="protection-ttft-summary"]').trigger('click')
    expect(wrapper.get('[data-test="upstream-protection-details"]').text()).toContain('已有旧格式 TTFT 降级记录')
  })
})
