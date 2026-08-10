import { defineComponent } from 'vue'
import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import AccountTableFilters from '../AccountTableFilters.vue'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key })
}))

const SelectStub = defineComponent({
  props: ['modelValue', 'options', 'disabled'],
  template: '<select :disabled="disabled"><option v-for="option in options" :key="String(option.value)">{{ option.label }}</option></select>'
})

describe('AccountTableFilters upstream mode', () => {
  it('renders discoverable config and key choices without exposing key secrets', () => {
    const wrapper = mount(AccountTableFilters, {
      props: {
        searchQuery: '',
        filters: { platform: '', type: '', status: '', privacy_mode: '', group: '', upstream_config_id: '', upstream_key_id: '' },
        mode: 'upstream',
        upstreamConfigs: [{ id: 7, name: 'Primary', provider: 'sub2api', site_url: '', auth_mode: 'access_token', recharge_rate: 1, scheduling_enabled: true, status: 'active', created_at: '', updated_at: '' }],
        upstreamKeys: [{ id: 9, upstream_config_id: 7, name: 'Key A', key_status: { has_key: true, suffix: '7890' }, platform: 'openai', status: 'active', created_at: '', updated_at: '' }]
      },
      global: {
        stubs: {
          Select: SelectStub,
          SearchInput: { template: '<input />' }
        }
      }
    })

    expect(wrapper.text()).toContain('Primary (#7)')
    expect(wrapper.text()).toContain('Key A · …7890')
    expect(wrapper.text()).not.toContain('sk-')
  })
})
