import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'

import PlatformBadge from '../PlatformBadge.vue'
import PlatformIcon from '../PlatformIcon.vue'

describe('PlatformBadge', () => {
  it('combines the shared platform icon, label, and color classes', () => {
    const wrapper = mount(PlatformBadge, {
      props: { platform: 'openai' }
    })

    expect(wrapper.getComponent(PlatformIcon).props()).toMatchObject({
      platform: 'openai',
      size: 'xs'
    })
    expect(wrapper.text()).toBe('OpenAI')
    expect(wrapper.classes()).toContain('bg-green-500/10')
    expect(wrapper.attributes('data-platform')).toBe('openai')
  })

  it('supports a caller-provided display label', () => {
    const wrapper = mount(PlatformBadge, {
      props: { platform: 'anthropic', label: 'Claude' }
    })

    expect(wrapper.text()).toBe('Claude')
    expect(wrapper.classes()).toContain('text-orange-600')
  })
})
