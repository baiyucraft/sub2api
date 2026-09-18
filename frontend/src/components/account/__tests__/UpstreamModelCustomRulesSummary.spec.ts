import { defineComponent } from 'vue'
import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import UpstreamModelCustomRulesSummary from '../UpstreamModelCustomRulesSummary.vue'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

const IconStub = defineComponent({ template: '<span />' })

describe('UpstreamModelCustomRulesSummary', () => {
  it('emits remove for every account-level rule and marks unavailable mappings', async () => {
    const wrapper = mount(UpstreamModelCustomRulesSummary, {
      props: {
        autoMapping: { 'gpt-a': 'gpt-a' },
        rules: [
          { source: 'gpt-a', action: 'deny' },
          { source: 'custom-model', action: 'allow' },
          { source: 'public', action: 'map', target: 'missing' }
        ]
      },
      global: { stubs: { Icon: IconStub } }
    })

    expect(wrapper.findAll('[data-test="upstream-model-custom-rule"]')).toHaveLength(3)
    expect(wrapper.find('[data-test="upstream-model-custom-rule-waiting"]').exists()).toBe(true)
    const removeButtons = wrapper.findAll('[data-test="remove-upstream-model-custom-rule"]')
    await removeButtons[0].trigger('click')
    await removeButtons[1].trigger('click')
    await removeButtons[2].trigger('click')
    expect(wrapper.emitted('remove')).toEqual([['gpt-a'], ['custom-model'], ['public']])
  })

  it('shows the empty state when the account has no custom rules', () => {
    const wrapper = mount(UpstreamModelCustomRulesSummary, {
      props: { rules: [] },
      global: { stubs: { Icon: IconStub } }
    })
    expect(wrapper.find('[data-test="upstream-model-custom-rules-empty"]').exists()).toBe(true)
  })
})
