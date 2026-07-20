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
})
