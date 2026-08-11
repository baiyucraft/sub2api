import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import UpstreamHealthHistory from '../UpstreamHealthHistory.vue'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key, te: () => true })
  }
})

const stubs = {
  HelpTooltip: { template: '<div><slot name="trigger" /><div><slot /></div></div>' }
}

describe('UpstreamHealthHistory', () => {
  it('renders no more than the latest twenty-four observations with state color and height encoding', () => {
    const states = ['healthy', 'degraded', 'suspended', 'observing', 'recovering', 'disabled'] as const
    const observations = Array.from({ length: 27 }, (_, index) => ({
      observed_at: `2026-08-10T01:${String(index).padStart(2, '0')}:00Z`,
      state: states[index % states.length],
      source: index % 2 ? 'traffic' : 'probe',
      result: index % 2 ? '500' : 'success',
      reason: index % 2 ? 'upstream_server_error' : 'probe_succeeded'
    }))
    const wrapper = mount(UpstreamHealthHistory, { props: { observations }, global: { stubs } })

    const bars = wrapper.findAll('[data-observation-state]')
    expect(bars).toHaveLength(24)
    expect(bars[0].attributes('data-observation-state')).toBe(observations[3].state)
    expect(wrapper.text()).toContain('admin.upstreamManagement.health.historySummary')
  })

  it('does not fabricate bars when no observations exist', () => {
    const wrapper = mount(UpstreamHealthHistory, { global: { stubs } })
    expect(wrapper.find('[data-upstream-health-history]').exists()).toBe(false)
    expect(wrapper.get('[data-upstream-health-history-empty]').text()).toContain('admin.upstreamManagement.health.noHistory')
  })
})
