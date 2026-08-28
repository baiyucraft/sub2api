import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import MonitorCardGrid from '@/components/user/monitor/MonitorCardGrid.vue'

vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key }),
  }
})

describe('MonitorCardGrid', () => {
  it('passes the selected range and list availability directly to each card', () => {
    const wrapper = mount(MonitorCardGrid, {
      props: {
        range: '15d',
        countdownSeconds: 30,
        loading: false,
        groups: [{
          provider: 'openai',
          items: [{
            id: 4,
            name: 'cc-max',
            provider: 'openai',
            group_name: 'cc-max',
            primary_model: 'gpt-4o',
            primary_status: 'operational',
            primary_latency_ms: 120,
            primary_ping_latency_ms: 30,
            availability: 98.5,
            availability_7d: 97,
            extra_models: [],
            timeline: [],
            show_group_rate: false,
          }],
        }],
      },
      global: {
        stubs: {
          EmptyState: true,
          MonitorCard: {
            name: 'MonitorCard',
            props: ['item', 'range', 'availabilityValue', 'countdownSeconds'],
            template: '<div data-test="monitor-card" />',
          },
        },
      },
    })

    const card = wrapper.getComponent({ name: 'MonitorCard' })
    expect(card.props('range')).toBe('15d')
    expect(card.props('availabilityValue')).toBe(98.5)
    expect(wrapper.find('h2').text()).toBe('monitorCommon.providers.openai')
  })

  it('renders each supplied platform as its own section', () => {
    const wrapper = mount(MonitorCardGrid, {
      props: {
        range: '24h',
        countdownSeconds: 30,
        loading: false,
        groups: [
          { provider: 'openai', items: [] },
          { provider: 'anthropic', items: [] },
        ],
      },
      global: {
        stubs: {
          EmptyState: true,
          MonitorCard: true,
        },
      },
    })

    const headings = wrapper.findAll('h2')
    expect(headings).toHaveLength(2)
    expect(headings[0].attributes('id')).toBe('monitor-platform-openai')
    expect(headings[1].attributes('id')).toBe('monitor-platform-anthropic')
  })
})
