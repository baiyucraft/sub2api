import { afterEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { nextTick } from 'vue'
import HelpTooltip from '@/components/common/HelpTooltip.vue'

function getTooltipElement(): HTMLDivElement {
  const tooltip = document.body.querySelector('[role="tooltip"]')
  if (!(tooltip instanceof HTMLDivElement)) {
    throw new Error('tooltip element not found')
  }
  return tooltip
}

describe('HelpTooltip', () => {
  afterEach(() => {
    document.body.innerHTML = ''
  })

  it('keeps the existing hover interaction by default', async () => {
    const wrapper = mount(HelpTooltip, {
      attachTo: document.body,
      props: {
        content: 'hover details',
      },
    })

    const trigger = wrapper.get('.group')
    expect(document.body.querySelector('[role="tooltip"]')).toBeNull()

    await trigger.trigger('mouseenter')
    await nextTick()
    expect(getTooltipElement().textContent).toContain('hover details')

    await trigger.trigger('mouseleave')
    await nextTick()
    expect(document.body.querySelector('[role="tooltip"]')).toBeNull()

    wrapper.unmount()
  })

  it('supports click-to-toggle details and closes on outside click', async () => {
    const wrapper = mount(HelpTooltip, {
      attachTo: document.body,
      props: {
        content: 'click details',
        trigger: 'click',
      },
    })

    const trigger = wrapper.get('.group')
    expect(document.body.querySelector('[role="tooltip"]')).toBeNull()

    await trigger.trigger('click')
    await nextTick()
    const tooltip = getTooltipElement()
    expect(tooltip.textContent).toContain('click details')

    const closeButton = tooltip.querySelector('button[aria-label="Close"]')
    if (!(closeButton instanceof HTMLButtonElement)) {
      throw new Error('close button not found')
    }
    closeButton.click()
    await nextTick()
    expect(document.body.querySelector('[role="tooltip"]')).toBeNull()

    await trigger.trigger('click')
    await nextTick()
    expect(getTooltipElement().textContent).toContain('click details')

    document.body.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await nextTick()
    expect(document.body.querySelector('[role="tooltip"]')).toBeNull()

    wrapper.unmount()
  })

  it('only attaches viewport and document listeners while open', async () => {
    const documentAddSpy = vi.spyOn(document, 'addEventListener')
    const documentRemoveSpy = vi.spyOn(document, 'removeEventListener')
    const windowAddSpy = vi.spyOn(window, 'addEventListener')
    const windowRemoveSpy = vi.spyOn(window, 'removeEventListener')
    const wrapper = mount(HelpTooltip, { attachTo: document.body, props: { content: 'details' } })

    expect(documentAddSpy.mock.calls.some(([type]) => type === 'click' || type === 'keydown')).toBe(false)
    expect(windowAddSpy.mock.calls.some(([type]) => type === 'resize' || type === 'scroll')).toBe(false)

    await wrapper.get('.group').trigger('mouseenter')
    await nextTick()
    expect(documentAddSpy.mock.calls.some(([type]) => type === 'click')).toBe(true)
    expect(documentAddSpy.mock.calls.some(([type]) => type === 'keydown')).toBe(true)
    expect(windowAddSpy.mock.calls.some(([type]) => type === 'resize')).toBe(true)
    expect(windowAddSpy.mock.calls.some(([type]) => type === 'scroll')).toBe(true)

    await wrapper.get('.group').trigger('mouseleave')
    await nextTick()
    expect(documentRemoveSpy.mock.calls.some(([type]) => type === 'click')).toBe(true)
    expect(documentRemoveSpy.mock.calls.some(([type]) => type === 'keydown')).toBe(true)
    expect(windowRemoveSpy.mock.calls.some(([type]) => type === 'resize')).toBe(true)
    expect(windowRemoveSpy.mock.calls.some(([type]) => type === 'scroll')).toBe(true)

    wrapper.unmount()
  })
})
