import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const { listGroups } = vi.hoisted(() => ({ listGroups: vi.fn() }))

vi.mock('@/api/admin', () => ({
  adminAPI: { proxyIpGroups: { list: listGroups } },
}))

vi.mock('vue-i18n', async () => ({
  ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'),
  useI18n: () => ({
    t: (key: string, params?: Record<string, unknown>) => params ? `${key}:${JSON.stringify(params)}` : key,
  }),
}))

import ProxyBindingSelector from '../ProxyBindingSelector.vue'

const ProxySelectorStub = defineComponent({
  name: 'ProxySelector',
  emits: ['update:modelValue'],
  template: '<button data-testid="choose-proxy" @click="$emit(\'update:modelValue\', 5)">proxy</button>',
})

const SelectStub = defineComponent({
  name: 'SelectStub',
  props: { options: { type: Array, default: () => [] } },
  emits: ['update:modelValue'],
  template: '<button data-testid="choose-group" @click="$emit(\'update:modelValue\', 9)">group</button>',
})

const mountSelector = (proxyId: number | null, proxyIpGroupId: number | null) => mount(ProxyBindingSelector, {
  props: { proxyId, proxyIpGroupId, proxies: [] },
  global: { stubs: { ProxySelector: ProxySelectorStub, Select: SelectStub } },
})

describe('ProxyBindingSelector', () => {
  beforeEach(() => {
    listGroups.mockReset().mockResolvedValue([
      { id: 9, name: 'pool', member_count: 2, per_ip_concurrency: 4 },
    ])
  })

  it('switches from a single proxy to a proxy group without keeping both bindings', async () => {
    const wrapper = mountSelector(5, null)
    await flushPromises()

    await wrapper.get('[data-testid="proxy-binding-mode-group"]').trigger('click')
    expect(wrapper.emitted('update:proxyId')?.at(-1)).toEqual([null])

    await wrapper.get('[data-testid="proxy-ip-group-select"]').trigger('click')
    expect(wrapper.emitted('update:proxyIpGroupId')?.at(-1)).toEqual([9])
    expect(wrapper.emitted('update:proxyId')?.at(-1)).toEqual([null])
  })

  it('switches from a proxy group to a single proxy and clears the group', async () => {
    const wrapper = mountSelector(null, 9)
    await flushPromises()

    await wrapper.get('[data-testid="proxy-binding-mode-proxy"]').trigger('click')
    expect(wrapper.emitted('update:proxyIpGroupId')?.at(-1)).toEqual([null])

    await wrapper.get('[data-testid="choose-proxy"]').trigger('click')
    expect(wrapper.emitted('update:proxyId')?.at(-1)).toEqual([5])
  })

  it('derives the option member count when the response only contains proxy ids', async () => {
    listGroups.mockResolvedValue([
      { id: 9, name: 'pool', proxy_ids: [2, 3, 4], per_ip_concurrency: 10 },
    ])
    const wrapper = mountSelector(null, null)
    await flushPromises()
    await wrapper.get('[data-testid="proxy-binding-mode-group"]').trigger('click')

    const options = wrapper.getComponent(SelectStub).props('options') as Array<{ label: string }>
    expect(options[0]?.label).toContain('"count":3')
    expect(options[0]?.label).toContain('"limit":10')
  })
})
