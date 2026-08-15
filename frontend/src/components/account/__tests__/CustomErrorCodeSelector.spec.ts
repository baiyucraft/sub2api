import { mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import CustomErrorCodeSelector from '../CustomErrorCodeSelector.vue'

const stubs = { Icon: true }

describe('CustomErrorCodeSelector', () => {
  beforeEach(() => {
    vi.stubGlobal('confirm', vi.fn(() => true))
  })

  it('adds, deduplicates and removes custom HTTP codes', async () => {
    const wrapper = mount(CustomErrorCodeSelector, {
      props: {
        enabled: true,
        modelValue: [503],
        title: 'Probe codes'
      },
      global: { stubs }
    })

    await wrapper.find('input[type="number"]').setValue(404)
    await wrapper.findAll('button').find(button => button.attributes('aria-label') === 'Add error code')!.trigger('click')
    expect(wrapper.emitted('update:modelValue')?.slice(-1)[0]?.[0]).toEqual([404, 503])

    await wrapper.setProps({ modelValue: [404, 503] })
    await wrapper.find('input[type="number"]').setValue(404)
    await wrapper.findAll('button').find(button => button.attributes('aria-label') === 'Add error code')!.trigger('click')
    expect(wrapper.emitted('info')).toHaveLength(1)

    await wrapper.find('button[aria-label="Remove error code 503"]').trigger('click')
    expect(wrapper.emitted('update:modelValue')?.slice(-1)[0]?.[0]).toEqual([404])
  })

  it('requires confirmation before adding capacity codes', async () => {
    const confirm = vi.fn(() => false)
    vi.stubGlobal('confirm', confirm)
    const wrapper = mount(CustomErrorCodeSelector, {
      props: {
        enabled: true,
        modelValue: [],
        title: 'Probe codes',
        confirm429Message: 'confirm 429'
      },
      global: { stubs }
    })

    await wrapper.findAll('button').find(button => button.text().includes('429'))!.trigger('click')
    expect(confirm).toHaveBeenCalledWith('confirm 429')
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
  })
})
