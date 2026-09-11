import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import AccountGroupsCell from '../AccountGroupsCell.vue'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) => {
        if (key === 'admin.accounts.groupCountTotal') return `Groups: ${params?.count ?? 0}`
        return key
      }
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
        template: '<svg data-testid="preferred-icon" />'
      }
    }
  }
})

describe('AccountGroupsCell preferred account pool', () => {
  it('keeps existing calls read-only when preferred props are omitted', () => {
    const wrapper = mountCell({ maxDisplay: 3 })

    expect(wrapper.findAll('[data-group-id]')).toHaveLength(0)
    expect(wrapper.findAll('button')).toHaveLength(1)
    expect(wrapper.text()).toContain('+2')
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
  })

  it('toggles each visible group independently and emits the next state', async () => {
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

  it('supports independent preferred toggles inside the +N popover', async () => {
    const wrapper = mountCell({
      accountId: 42,
      preferredGroupIds: [4],
      interactive: true,
      maxDisplay: 3
    })

    await wrapper.find('button:not([data-group-id])').trigger('click')

    const popoverButtons = document.querySelectorAll<HTMLButtonElement>(
      '[data-testid="account-groups-popover"] button[data-group-id]'
    )
    expect(popoverButtons).toHaveLength(4)

    popoverButtons[3].click()
    await wrapper.vm.$nextTick()

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
