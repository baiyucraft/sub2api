import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import MonitorHero from '@/components/user/monitor/MonitorHero.vue'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => ({
      'channelStatus.range.24h': '24 小时',
      'channelStatus.range.7d': '7 天',
      'channelStatus.range.15d': '15 天',
      'channelStatus.range.30d': '30 天',
      'channelStatus.rangeLabel': '统计范围',
      'channelStatus.overall.operational': '正常',
    }[key] || key),
  }),
}))

describe('MonitorHero', () => {
  it('renders one four-option range selector with 24h selected by default', async () => {
    const wrapper = mount(MonitorHero, {
      props: {
        overallStatus: 'operational',
        intervalSeconds: 60,
        range: '24h',
        loading: false,
      },
      global: {
        stubs: {
          Icon: true,
          AutoRefreshButton: true,
        },
      },
    })

    expect(wrapper.get('.channel-status-v1-hero').classes()).toContain('top-16')
    expect(wrapper.get('.channel-status-v1-hero').classes()).toContain('z-20')

    const tablists = wrapper.findAll('[role="tablist"]')
    expect(tablists).toHaveLength(1)
    expect(tablists[0].attributes('aria-label')).toBe('统计范围')
    const tabs = tablists[0].findAll('[role="tab"]')
    expect(tabs).toHaveLength(4)
    expect(tabs.map(tab => tab.text())).toEqual(['24 小时', '7 天', '15 天', '30 天'])
    expect(tabs[0].attributes('aria-selected')).toBe('true')

    await tabs[2].trigger('click')
    expect(wrapper.emitted('update:range')).toEqual([['15d']])
  })

  it('renders platform navigation and emits the selected platform', async () => {
    const wrapper = mount(MonitorHero, {
      props: {
        overallStatus: 'operational',
        intervalSeconds: 60,
        range: '24h',
        loading: false,
        platforms: [
          { value: 'openai', label: 'OpenAI' },
          { value: 'anthropic', label: 'Anthropic' },
        ],
        activePlatform: 'openai',
      },
      global: {
        stubs: {
          Icon: true,
          AutoRefreshButton: true,
        },
      },
    })

    const buttons = wrapper.findAll('nav button')
    expect(buttons).toHaveLength(2)
    expect(buttons[0].attributes('aria-current')).toBe('location')
    expect(buttons[0].find('svg').exists()).toBe(true)
    expect(buttons[1].find('svg').exists()).toBe(true)
    expect(buttons[0].classes()).toContain('text-white')
    expect(buttons[1].classes()).not.toContain('text-white')
    expect(buttons[0].attributes('style')).toContain('background-color: rgb(34, 197, 94)')
    await buttons[1].trigger('click')
    expect(wrapper.emitted('navigate-platform')).toEqual([['anthropic']])
  })
})
