import { defineComponent } from 'vue'
import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import AccountTableFilters from '../AccountTableFilters.vue'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key })
}))

const SelectStub = defineComponent({
  props: ['modelValue', 'options', 'disabled'],
  template: '<select :disabled="disabled"><option v-for="option in options" :key="String(option.value)" :value="option.value">{{ option.label }}</option></select>'
})

describe('AccountTableFilters upstream mode', () => {
  it('shows the preferred-account filter only in upstream mode and defaults it to all', () => {
    const wrapper = mount(AccountTableFilters, {
      props: {
        searchQuery: '',
        filters: { platform: '', type: '', status: '', privacy_mode: '', group: '', upstream_config_id: '', upstream_key_id: '' },
        mode: 'upstream',
      },
      global: {
        stubs: {
          Select: SelectStub,
          SearchInput: { template: '<input />' }
        }
      }
    })

    expect(wrapper.find('input').exists()).toBe(true)
    expect(wrapper.findAll('select')).toHaveLength(4)
    expect(wrapper.text()).toContain('admin.accounts.allPlatforms')
    expect(wrapper.text()).toContain('admin.accounts.allStatus')
    expect(wrapper.text()).toContain('admin.accounts.allGroups')
    expect(wrapper.text()).toContain('admin.accounts.allPreferred')
    expect(wrapper.text()).toContain('admin.accounts.preferredOnly')
    const preferredSelect = wrapper.findAllComponents(SelectStub)[3]
    expect(preferredSelect.props('modelValue')).toBe('')
    expect(preferredSelect.props('options')).toEqual([
      { value: '', label: 'admin.accounts.allPreferred' },
      { value: '1', label: 'admin.accounts.preferredOnly' }
    ])

    expect(wrapper.text()).not.toContain('admin.accounts.allTypes')
    expect(wrapper.text()).not.toContain('admin.accounts.allPrivacyModes')
    expect(wrapper.text()).not.toContain('admin.upstreamManagement.filters.allConfigs')
    expect(wrapper.text()).not.toContain('admin.upstreamManagement.filters.allKeys')
  })

  it('emits the preferred filter together with the selected group', async () => {
    const wrapper = mount(AccountTableFilters, {
      props: {
        searchQuery: '',
        filters: { platform: '', status: '', group: '7', preferred: '' },
        mode: 'upstream',
      },
      global: {
        stubs: {
          Select: SelectStub,
          SearchInput: { template: '<input />' }
        }
      }
    })

    const preferredSelect = wrapper.findAllComponents(SelectStub)[3]
    await preferredSelect.vm.$emit('update:modelValue', '1')

    expect(wrapper.emitted('update:filters')?.at(-1)).toEqual([
      { platform: '', status: '', group: '7', preferred: '1' }
    ])
  })

})
