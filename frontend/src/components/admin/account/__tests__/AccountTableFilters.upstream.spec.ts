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
    expect(wrapper.findAll('select')).toHaveLength(5)
    expect(wrapper.text()).toContain('admin.accounts.allPlatforms')
    expect(wrapper.text()).toContain('admin.accounts.allStatus')
    expect(wrapper.text()).toContain('admin.accounts.allGroups')
    expect(wrapper.text()).toContain('admin.accounts.allPreferred')
    expect(wrapper.text()).toContain('admin.accounts.preferredOnly')
    expect(wrapper.text()).toContain('admin.accounts.allQualityFilters')
    expect(wrapper.text()).toContain('1h-A')
    expect(wrapper.text()).toContain('1h-B')
    expect(wrapper.text()).toContain('24h-A')
    expect(wrapper.text()).toContain('24h-B')
    expect(wrapper.text()).not.toContain('admin.accounts.allTypes')
    expect(wrapper.text()).not.toContain('admin.accounts.allPrivacyModes')
    expect(wrapper.text()).not.toContain('admin.upstreamManagement.filters.allConfigs')
    expect(wrapper.text()).not.toContain('admin.upstreamManagement.filters.allKeys')

    const preferredSelect = wrapper.findAllComponents(SelectStub)[3]
    expect(preferredSelect.props('modelValue')).toBe('')
    expect(preferredSelect.props('options')).toEqual([
      { value: '', label: 'admin.accounts.allPreferred' },
      { value: '1', label: 'admin.accounts.preferredOnly' }
    ])

    const qualitySelect = wrapper.findAllComponents(SelectStub)[4]
    expect(qualitySelect.props('modelValue')).toBe('')
    expect(qualitySelect.props('options')).toEqual([
      { value: '', label: 'admin.accounts.allQualityFilters' },
      { value: '1h-A', label: '1h-A' },
      { value: '1h-B', label: '1h-B' },
      { value: '24h-A', label: '24h-A' },
      { value: '24h-B', label: '24h-B' }
    ])
  })

  it('passes preferred=1 together with the selected group', async () => {
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

    const selects = wrapper.findAllComponents(SelectStub)
    await selects[3].vm.$emit('update:modelValue', '1')

    expect(wrapper.emitted('update:filters')?.at(-1)).toEqual([
      { platform: '', status: '', group: '7', preferred: '1' }
    ])
  })

  it('emits the selected quality filter and clears it back to all', async () => {
    const wrapper = mount(AccountTableFilters, {
      props: {
        searchQuery: '',
        filters: { platform: '', status: '', group: '', preferred: '', quality_filter: '1h-A' },
        mode: 'upstream',
      },
      global: {
        stubs: {
          Select: SelectStub,
          SearchInput: { template: '<input />' }
        }
      }
    })

    const qualitySelect = wrapper.findAllComponents(SelectStub)[4]
    expect(qualitySelect.props('modelValue')).toBe('1h-A')

    await qualitySelect.vm.$emit('update:modelValue', '24h-B')
    expect(wrapper.emitted('update:filters')?.at(-1)).toEqual([
      { platform: '', status: '', group: '', preferred: '', quality_filter: '24h-B' }
    ])

    await qualitySelect.vm.$emit('update:modelValue', null)
    expect(wrapper.emitted('update:filters')?.at(-1)).toEqual([
      { platform: '', status: '', group: '', preferred: '', quality_filter: '' }
    ])
  })
})
