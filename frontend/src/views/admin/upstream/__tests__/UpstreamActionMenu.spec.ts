import { describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { mount } from '@vue/test-utils'
import UpstreamActionMenu from '../UpstreamActionMenu.vue'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key })
}))

const ActionMenuStub = defineComponent({
  props: ['show', 'anchorEl', 'width'],
  emits: ['close'],
  template: `
    <div v-if="show" data-test="action-menu" :data-width="width" :data-has-anchor="Boolean(anchorEl)">
      <slot :close="() => $emit('close')" />
    </div>
  `
})

function config(provider = 'sub2api') {
  return {
    id: 7,
    name: 'Main',
    provider,
    site_url: 'https://upstream.example.com',
    auth_mode: 'manual_jwt',
    recharge_rate: 1,
    status: 'active',
    created_at: '',
    updated_at: ''
  }
}

describe('UpstreamActionMenu', () => {
  it('adapts show, anchorEl, and width to common ActionMenu', () => {
    const wrapper = mount(UpstreamActionMenu, {
      props: { show: true, anchorEl: document.body, config: config() as any, width: 'wide' },
      global: { stubs: { ActionMenu: ActionMenuStub, Icon: true } }
    })

    expect(wrapper.get('[data-test="action-menu"]').attributes('data-width')).toBe('wide')
    expect(wrapper.get('[data-test="action-menu"]').attributes('data-has-anchor')).toBe('true')
  })

  it('emits test, dashboard, and delete with the config and closes each action', async () => {
    const item = config()
    const wrapper = mount(UpstreamActionMenu, {
      props: { show: true, anchorEl: document.body, config: item as any },
      global: { stubs: { ActionMenu: ActionMenuStub, Icon: true } }
    })

    for (const [index, event] of ['test', 'dashboard', 'delete'].entries()) {
      await wrapper.findAll('[role="menuitem"]')[index].trigger('click')
      expect(wrapper.emitted(event)?.[0]).toEqual([item])
    }
    expect(wrapper.emitted('close')).toHaveLength(3)
  })

  it('hides the dashboard action for unsupported providers', () => {
    const wrapper = mount(UpstreamActionMenu, {
      props: { show: true, anchorEl: document.body, config: config('other') as any },
      global: { stubs: { ActionMenu: ActionMenuStub, Icon: true } }
    })

    expect(wrapper.findAll('[role="menuitem"]')).toHaveLength(2)
    expect(wrapper.text()).not.toContain('admin.upstreamConfigs.actions.openDashboard')
  })
})
