import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import OrderedProxySelector from '@/components/account/OrderedProxySelector.vue'
import Select from '@/components/common/Select.vue'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key })
}))

const proxies = [
  { id: 11, name: 'Hong Kong', host: '127.0.0.1', port: 8080, status: 'active' },
  { id: 12, name: 'Tokyo', host: '127.0.0.2', port: 8080, status: 'active' }
] as never[]

describe('OrderedProxySelector', () => {
  it('adds each available proxy once', async () => {
    const wrapper = mount(OrderedProxySelector, { props: { modelValue: [], proxies } })
    const add = wrapper.findAll('button').find((button) => button.text() === 'admin.accounts.proxyBindingAdd')!
    await add.trigger('click')
    expect(wrapper.emitted('update:modelValue')?.at(-1)).toEqual([[11]])

    await wrapper.setProps({ modelValue: [11] })
    await add.trigger('click')
    expect(wrapper.emitted('update:modelValue')?.at(-1)).toEqual([[11, 12]])
  })

  it('moves the preferred proxy order without duplicating bindings', async () => {
    const wrapper = mount(OrderedProxySelector, { props: { modelValue: [11, 12], proxies } })
    const moveUpButtons = wrapper.findAll('button[aria-label="admin.accounts.proxyBindingMoveUp"]')
    await moveUpButtons[1]!.trigger('click')
    expect(wrapper.emitted('update:modelValue')?.at(-1)).toEqual([[12, 11]])
  })

  it('does not offer inactive or expired proxies for new bindings', async () => {
    const unavailable = [
      { id: 20, name: 'Inactive', status: 'inactive', expires_at: null },
      { id: 21, name: 'Expired by status', status: 'expired', expires_at: null },
      { id: 22, name: 'Expired by time', status: 'active', expires_at: '2000-01-01T00:00:00Z' },
      { id: 23, name: 'Available', status: 'active', expires_at: null }
    ] as never[]
    const wrapper = mount(OrderedProxySelector, { props: { modelValue: [], proxies: unavailable } })
    const add = wrapper.findAll('button').find((button) => button.text() === 'admin.accounts.proxyBindingAdd')!
    await add.trigger('click')
    expect(wrapper.emitted('update:modelValue')?.at(-1)).toEqual([[23]])
  })

  it('keeps an unavailable existing binding visible but removes it from other slots', () => {
    const mixed = [
      { id: 20, name: 'Inactive', status: 'inactive', expires_at: null },
      { id: 23, name: 'Available', status: 'active', expires_at: null }
    ] as never[]
    const wrapper = mount(OrderedProxySelector, { props: { modelValue: [20, 23], proxies: mixed } })
    const selects = wrapper.findAllComponents(Select)
    expect(selects[0]!.props('options')).toContainEqual(expect.objectContaining({ value: 20, disabled: false }))
    expect(selects[1]!.props('options')).not.toContainEqual(expect.objectContaining({ value: 20 }))
    expect(wrapper.text()).toContain('Inactive · admin.accounts.proxyBindingInactive')
  })

  it('blocks every mutation when disabled and exposes the disabled state', async () => {
    const wrapper = mount(OrderedProxySelector, { props: { modelValue: [11, 12], proxies, disabled: true } })
    expect(wrapper.attributes('aria-disabled')).toBe('true')
    expect(wrapper.findAll('button').every((button) => button.attributes('disabled') !== undefined)).toBe(true)
    await wrapper.find('button[aria-label="admin.accounts.proxyBindingRemove"]').trigger('click')
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
  })
})
