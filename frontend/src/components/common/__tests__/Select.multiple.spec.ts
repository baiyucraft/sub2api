import { afterEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

import Select from '../Select.vue'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key })
  }
})

const options = Array.from({ length: 6 }, (_, index) => ({
  value: index + 1,
  label: `Group ${index + 1}`
}))

function mountSelect(modelValue: number[] | number | null, multiple = true) {
  return mount(Select, {
    props: {
      modelValue,
      options,
      multiple,
      searchable: 'auto'
    },
    global: {
      stubs: {
        Icon: true,
        Teleport: true
      }
    }
  })
}

afterEach(() => {
  document.body.innerHTML = ''
})

describe('Select multiple mode', () => {
  it('toggles several values without closing the dropdown', async () => {
    const wrapper = mountSelect([])
    await wrapper.get('.select-trigger').trigger('click')

    const firstOption = wrapper.findAll('.select-option')[0]
    await firstOption.trigger('click')
    expect(wrapper.emitted('update:modelValue')?.[0]).toEqual([[1]])
    expect(wrapper.find('.select-dropdown-portal').exists()).toBe(true)

    await wrapper.setProps({ modelValue: [1] })
    await wrapper.findAll('.select-option')[1].trigger('click')
    expect(wrapper.emitted('update:modelValue')?.[1]).toEqual([[1, 2]])

    await wrapper.setProps({ modelValue: [1, 2] })
    await wrapper.findAll('.select-option')[0].trigger('click')
    expect(wrapper.emitted('update:modelValue')?.[2]).toEqual([[2]])
  })

  it('keeps search and keyboard close behavior for long lists', async () => {
    const wrapper = mountSelect([])
    await wrapper.get('.select-trigger').trigger('click')
    await wrapper.get('.select-search-input').setValue('Group 6')

    expect(wrapper.findAll('.select-option')).toHaveLength(1)
    expect(wrapper.get('.select-option').text()).toContain('Group 6')
    await wrapper.get('.select-dropdown-portal').trigger('keydown', { key: 'Escape' })
    expect(wrapper.find('.select-dropdown-portal').exists()).toBe(false)
  })

  it('preserves single-select close behavior', async () => {
    const wrapper = mountSelect(null, false)
    await wrapper.get('.select-trigger').trigger('click')
    await wrapper.findAll('.select-option')[0].trigger('click')

    expect(wrapper.emitted('update:modelValue')?.[0]).toEqual([1])
    expect(wrapper.find('.select-dropdown-portal').exists()).toBe(false)
  })
})
