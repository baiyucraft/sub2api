import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import AccountGroupsCell from '../AccountGroupsCell.vue'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key
    })
  }
})

const groups = [
  { id: 1, name: 'OpenAI', platform: 'openai' },
  { id: 2, name: 'Claude', platform: 'anthropic' },
  { id: 3, name: 'Gemini', platform: 'gemini' },
  { id: 4, name: 'Grok', platform: 'grok' }
] as any

const mountCell = (props: Record<string, unknown> = {}) => mount(AccountGroupsCell, {
  props: {
    groups,
    ...props
  },
  global: {
    stubs: {
      GroupBadge: {
        props: ['name'],
        template: '<span class="group-badge">{{ name }}</span>'
      },
      Icon: {
        props: ['filled'],
        template: `<svg data-testid="preferred-icon" :data-filled="filled ? 'true' : 'false'" />`
      }
    }
  }
})

describe('AccountGroupsCell preferred account pool', () => {
  it('keeps long group names constrained and exposes the full name through the title', () => {
    const wrapper = mountCell({
      groups: [{ id: 1, name: 'gpt-mixed-stable-with-a-very-long-group-name', platform: 'openai' }] as any
    })

    expect(wrapper.find('.account-groups-cell').classes()).toEqual(
      expect.arrayContaining(['w-full', 'max-w-full', 'min-w-0'])
    )
    expect(wrapper.find('.group-badge').attributes('title')).toBe(
      'gpt-mixed-stable-with-a-very-long-group-name'
    )
  })

  it('renders every group and lets the row grow naturally', () => {
    const wrapper = mountCell({
      groups: [
        { id: 1, name: 'gpt-low-price-with-a-very-long-group-name', platform: 'openai' },
        { id: 2, name: 'gpt-mixed-with-a-very-long-group-name', platform: 'openai' },
        { id: 3, name: 'gpt-pro-with-a-very-long-group-name', platform: 'openai' }
      ] as any
    })

    const container = wrapper.get('[data-testid="account-groups-list"]')
    expect(container.classes()).toContain('flex-wrap')
    expect(container.classes()).not.toContain('max-h-14')
    expect(container.classes()).not.toContain('overflow-hidden')
    expect(wrapper.findAll('.group-badge')).toHaveLength(3)
    expect(wrapper.text()).not.toMatch(/\+\d+/)
    expect(wrapper.find('[data-testid="account-groups-popover"]').exists()).toBe(false)
  })

  it('keeps existing calls read-only when preferred props are omitted', () => {
    const wrapper = mountCell()

    expect(wrapper.findAll('[data-group-id]')).toHaveLength(0)
    expect(wrapper.findAll('button')).toHaveLength(0)
    expect(wrapper.findAll('.group-badge')).toHaveLength(4)
  })

  it('shows group-level preferred state without making the default display interactive', () => {
    const wrapper = mountCell({
      accountId: 42,
      preferredGroupIds: [1, 3]
    })

    const indicators = wrapper.findAll('[role="img"]')
    expect(indicators).toHaveLength(4)
    expect(indicators[0].attributes('aria-pressed')).toBe('true')
    expect(indicators[1].attributes('aria-pressed')).toBe('false')
    expect(indicators[2].attributes('aria-pressed')).toBe('true')
    expect(indicators[3].attributes('aria-pressed')).toBe('false')
    expect(wrapper.findAll('button[data-group-id]')).toHaveLength(0)
    const icons = wrapper.findAll('[data-testid="preferred-icon"]')
    expect(icons[0].attributes('data-filled')).toBe('true')
    expect(icons[1].attributes('data-filled')).toBe('false')
    expect(icons[2].attributes('data-filled')).toBe('true')
    expect(icons[3].attributes('data-filled')).toBe('false')
  })

  it('toggles each group independently and emits the next state', async () => {
    const wrapper = mountCell({
      accountId: 42,
      preferredGroupIds: [1],
      interactive: true
    })

    const buttons = wrapper.findAll('button[data-group-id]')
    expect(buttons).toHaveLength(4)

    await buttons[0].trigger('click')
    await buttons[1].trigger('click')

    expect(wrapper.emitted('toggle-preferred')).toEqual([
      [{ groupId: 1, preferred: false }],
      [{ groupId: 2, preferred: true }]
    ])
  })

  it('supports toggling the last group inline', async () => {
    const wrapper = mountCell({
      accountId: 42,
      preferredGroupIds: [4],
      interactive: true
    })

    const lastGroup = wrapper.get('button[data-group-id="4"]')
    expect(lastGroup.find('[data-testid="preferred-icon"]').attributes('data-filled')).toBe('true')

    await lastGroup.trigger('click')

    expect(wrapper.emitted('toggle-preferred')).toEqual([[{ groupId: 4, preferred: false }]])
  })

  it('does not expose toggle controls without an account id', () => {
    const wrapper = mountCell({
      preferredGroupIds: [1],
      interactive: true
    })

    expect(wrapper.findAll('button[data-group-id]')).toHaveLength(0)
    expect(wrapper.findAll('[role="img"]')).toHaveLength(4)
  })
})
