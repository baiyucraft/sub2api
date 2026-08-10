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
  it('keeps the shared search and useful filters without ordinary or exact upstream selectors', () => {
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
    expect(wrapper.findAll('select')).toHaveLength(3)
    expect(wrapper.text()).toContain('admin.accounts.allPlatforms')
    expect(wrapper.text()).toContain('admin.accounts.allStatus')
    expect(wrapper.text()).toContain('admin.accounts.allGroups')
    expect(wrapper.text()).not.toContain('admin.accounts.allTypes')
    expect(wrapper.text()).not.toContain('admin.accounts.allPrivacyModes')
    expect(wrapper.text()).not.toContain('admin.upstreamManagement.filters.allConfigs')
    expect(wrapper.text()).not.toContain('admin.upstreamManagement.filters.allKeys')
  })
})
