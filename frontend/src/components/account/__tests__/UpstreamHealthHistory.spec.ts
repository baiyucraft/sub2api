import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import UpstreamHealthHistory from '../UpstreamHealthHistory.vue'
import HelpTooltip from '@/components/common/HelpTooltip.vue'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key, te: () => true })
  }
})

const stubs = {
  HelpTooltip: {
    props: ['triggerClass'],
    template: '<div data-test="health-history-tooltip" :class="triggerClass"><slot name="trigger" /><div><slot /></div></div>'
  }
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
    const chart = wrapper.get('[data-upstream-health-bars]')
    expect(chart.classes()).toContain('grid')
    expect(chart.classes()).toContain('w-full')
    expect(chart.attributes('style')).toContain('repeat(24, minmax(0, 1fr))')
    const history = wrapper.get('[data-upstream-health-history]')
    expect(history.classes()).toContain('w-full')
    expect(history.classes()).toContain('min-w-[150px]')
    expect(history.classes()).toContain('max-w-[210px]')
    const tooltipTrigger = wrapper.get('[data-test="health-history-tooltip"]')
    expect(tooltipTrigger.classes()).toContain('!flex')
    expect(tooltipTrigger.classes()).toContain('w-full')
    expect(wrapper.text()).toContain('admin.upstreamManagement.health.historySummary')
    expect(wrapper.findAll('[data-test="health-history-tooltip"]')).toHaveLength(1)
    expect(wrapper.findAllComponents(HelpTooltip)).toHaveLength(1)
  })

  it('does not fabricate bars when no observations exist', () => {
    const wrapper = mount(UpstreamHealthHistory, { global: { stubs } })
    expect(wrapper.find('[data-upstream-health-history]').exists()).toBe(false)
    expect(wrapper.get('[data-upstream-health-history-empty]').text()).toContain('admin.upstreamManagement.health.noHistory')
  })
})
