import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

import UpstreamKeySelector from '../UpstreamKeySelector.vue'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) =>
        params ? `${key}:${JSON.stringify(params)}` : key
    })
  }
})

describe('UpstreamKeySelector', () => {
  it('prioritizes key name, group, platform and rate over the key suffix', async () => {
    const wrapper = mount(UpstreamKeySelector, {
      props: {
        modelValue: 20,
        keys: [
          {
            id: 20,
            upstream_config_id: 10,
            name: '聪明-plus',
            key_status: { has_key: true, suffix: 'abc123' },
            remote_key_id: 5019,
            upstream_group_id: 2,
            upstream_group_name: 'plus',
            platform: 'openai',
            rate_multiplier: 0.06,
            status: 'active',
            last_seen_at: '2026-07-08T06:00:00Z',
            created_at: '2026-07-08T05:00:00Z',
            updated_at: '2026-07-08T06:00:00Z'
          }
        ]
      },
      global: {
        stubs: {
          Icon: true
        }
      }
    })

    expect(wrapper.text()).toContain('聪明-plus')
    expect(wrapper.text()).toContain('plus')
    expect(wrapper.text()).toContain('0.06')
    expect(wrapper.text()).toContain('abc123')

    await wrapper.get('button').trigger('click')

    const optionText = wrapper.text()
    expect(optionText).toContain('openai')
    expect(optionText.indexOf('聪明-plus')).toBeLessThan(optionText.indexOf('abc123'))
    expect(optionText).toContain('5019')
  })

  it('uses the upstream id as the fallback title instead of a suffix-only key name', async () => {
    const wrapper = mount(UpstreamKeySelector, {
      props: {
        modelValue: null,
        keys: [
          {
            id: 21,
            upstream_config_id: 10,
            name: '...1b0fbc',
            key_status: { has_key: true, suffix: '1b0fbc' },
            remote_key_id: 1440,
            upstream_group_name: '',
            platform: 'openai',
            rate_multiplier: 0.12,
            status: 'active',
            last_seen_at: '2026-07-08T06:00:00Z',
            created_at: '2026-07-08T05:00:00Z',
            updated_at: '2026-07-08T06:00:00Z'
          }
        ]
      },
      global: {
        stubs: {
          Icon: true
        }
      }
    })

    await wrapper.get('button').trigger('click')

    const optionText = wrapper.text()
    expect(optionText).toContain('admin.accounts.upstreamKeySelector.unnamedRemote:{"id":1440}')
    expect(optionText.indexOf('unnamedRemote')).toBeLessThan(optionText.indexOf('1b0fbc'))
  })

  it('shows upstream key name and group name in the selected summary', () => {
    const wrapper = mount(UpstreamKeySelector, {
      props: {
        modelValue: 1,
        keys: [
          {
            id: 1,
            upstream_config_id: 10,
            name: 'free',
            key_status: { has_key: true, suffix: '276d83' },
            remote_key_id: 11917,
            upstream_group_id: 44,
            upstream_group_name: 'ChatGPT-Plus【高并发-特惠通道】',
            platform: 'openai',
            rate_multiplier: 0.03,
            status: 'active',
            last_seen_at: '2026-07-08T06:00:00Z',
            created_at: '2026-07-08T05:00:00Z',
            updated_at: '2026-07-08T06:00:00Z'
          }
        ]
      },
      global: {
        stubs: {
          Icon: true
        }
      }
    })

    const text = wrapper.text()
    expect(text).toContain('admin.accounts.upstreamKeySelector.selectedTitle:{"name":"free"}')
    expect(text).toContain('ChatGPT-Plus【高并发-特惠通道】')
    expect(text).toContain('0.03')
    expect(text).toContain('276d83')
    expect(text).not.toContain(' / openai / ')
  })

  it('shows dedicated upstream rate override when available', () => {
    const wrapper = mount(UpstreamKeySelector, {
      props: {
        modelValue: 1,
        keys: [
          {
            id: 1,
            upstream_config_id: 10,
            name: 'plus',
            key_status: { has_key: true, suffix: '276d83' },
            remote_key_id: 11917,
            upstream_group_id: 44,
            upstream_group_name: 'Plus',
            platform: 'openai',
            rate_multiplier: 0.06,
            status: 'active',
            extra: {
              default_rate_multiplier: 0.1,
              dedicated_rate_multiplier: 0.06,
              has_dedicated_rate_multiplier: true
            },
            last_seen_at: '2026-07-08T06:00:00Z',
            created_at: '2026-07-08T05:00:00Z',
            updated_at: '2026-07-08T06:00:00Z'
          }
        ]
      },
      global: {
        stubs: {
          Icon: true
        }
      }
    })

    expect(wrapper.text()).toContain('admin.accounts.upstreamKeySelector.rateOverride:{"defaultRate":"0.1","dedicatedRate":"0.06"}')
  })

  it('labels a dedicated rate even when the default rate is unavailable', () => {
    const wrapper = mount(UpstreamKeySelector, {
      props: {
        modelValue: 1,
        keys: [
          {
            id: 1,
            upstream_config_id: 10,
            name: 'plus',
            key_status: { has_key: true, suffix: '276d83' },
            remote_key_id: 11917,
            upstream_group_id: 44,
            upstream_group_name: 'Plus',
            platform: 'openai',
            rate_multiplier: 0.06,
            status: 'active',
            extra: {
              dedicated_rate_multiplier: 0.06,
              has_dedicated_rate_multiplier: true
            },
            last_seen_at: '2026-07-08T06:00:00Z',
            created_at: '2026-07-08T05:00:00Z',
            updated_at: '2026-07-08T06:00:00Z'
          }
        ]
      },
      global: {
        stubs: {
          Icon: true
        }
      }
    })

    expect(wrapper.text()).toContain('admin.accounts.upstreamKeySelector.rateDedicated:{"dedicatedRate":"0.06"}')
  })

  it('falls back to group id and allows searching by that id', async () => {
    const wrapper = mount(UpstreamKeySelector, {
      props: {
        modelValue: null,
        keys: [
          {
            id: 1,
            upstream_config_id: 10,
            name: 'plus',
            key_status: { has_key: true, suffix: '77db38' },
            remote_key_id: 10046,
            upstream_group_id: 33,
            upstream_group_name: '',
            platform: 'openai',
            rate_multiplier: 0.05,
            status: 'active',
            last_seen_at: '2026-07-08T06:00:00Z',
            created_at: '2026-07-08T05:00:00Z',
            updated_at: '2026-07-08T06:00:00Z'
          }
        ]
      },
      global: {
        stubs: {
          Icon: true
        }
      }
    })

    await wrapper.get('button').trigger('click')

    expect(wrapper.text()).toContain('admin.accounts.upstreamKeySelector.groupId:{"id":33}')

    await wrapper.get('input').setValue('33')
    expect(wrapper.text()).toContain('plus')
    expect(wrapper.text()).toContain('77db38')
  })

  it('warns for an existing stale binding and refuses to select stale keys', async () => {
    const wrapper = mount(UpstreamKeySelector, {
      props: {
        modelValue: 9,
        keys: [{
          id: 9,
          upstream_config_id: 5,
          name: '特惠',
          key_status: { has_key: true, suffix: 'abc123' },
          remote_key_id: 2144,
          upstream_group_name: '专享福利',
          platform: 'openai',
          rate_multiplier: 0.04,
          status: 'stale',
          missing_count: 4,
          missing_since: '2026-07-11T09:31:35Z',
          last_seen_at: '2026-07-11T09:31:35Z',
          created_at: '2026-07-10T00:00:00Z',
          updated_at: '2026-07-12T00:00:00Z'
        }]
      },
      global: { stubs: { Icon: true } }
    })

    expect(wrapper.get('[data-test="stale-key-warning"]').text()).toContain('staleWarning')
    await wrapper.get('button').trigger('click')
    const option = wrapper.findAll('button').find((button) => button.attributes('disabled') !== undefined)
    expect(option).toBeTruthy()
    await option!.trigger('click')
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
  })
})
