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
    vi.restoreAllMocks()
  })

  it('clamps mobile edges, flips below the trigger and ignores document scroll offsets', async () => {
    vi.spyOn(window, 'innerWidth', 'get').mockReturnValue(390)
    vi.spyOn(window, 'innerHeight', 'get').mockReturnValue(844)
    vi.spyOn(window, 'scrollY', 'get').mockReturnValue(1000)
    vi.spyOn(window, 'scrollX', 'get').mockReturnValue(50)
    vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function () {
      return (this.getAttribute('role') === 'tooltip'
        ? { top: 0, bottom: 500, left: 0, right: 358, width: 358, height: 500 }
        : { top: 50, bottom: 74, left: 360, right: 385, width: 25, height: 24 }) as DOMRect
    })
    const wrapper = mount(HelpTooltip, { attachTo: document.body, props: { trigger: 'click', content: 'mobile distribution details' } })
    await wrapper.get('.group').trigger('click')
    await nextTick()
    expect(getTooltipElement().dataset.placement).toBe('bottom')
    expect(getTooltipElement().style.top).toBe('82px')
    expect(getTooltipElement().style.left).toBe('195px')
    expect(getTooltipElement().style.maxWidth).toBe('calc(100vw - 32px)')
    wrapper.unmount()
  })

  it('keeps a top placement when there is room and clamps the left edge', async () => {
    vi.spyOn(window, 'innerWidth', 'get').mockReturnValue(1440)
    vi.spyOn(window, 'innerHeight', 'get').mockReturnValue(900)
    vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function () {
      return (this.getAttribute('role') === 'tooltip'
        ? { top: 0, bottom: 240, left: 0, right: 384, width: 384, height: 240 }
        : { top: 500, bottom: 524, left: 10, right: 50, width: 40, height: 24 }) as DOMRect
    })
    const wrapper = mount(HelpTooltip, { attachTo: document.body, props: { content: 'desktop details' } })
    await wrapper.get('.group').trigger('mouseenter')
    await nextTick()
    expect(getTooltipElement().dataset.placement).toBe('top')
    expect(getTooltipElement().style.top).toBe('252px')
    expect(getTooltipElement().style.left).toBe('208px')
    wrapper.unmount()
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

  it('keeps a hover tooltip open while the pointer moves between the trigger and the tooltip', async () => {
    const wrapper = mount(HelpTooltip, {
      attachTo: document.body,
      props: {
        content: 'copyable details',
      },
    })

    const trigger = wrapper.get('.group')
    expect(document.body.querySelector('[role="tooltip"]')).toBeNull()

    await trigger.trigger('mouseenter')
    await nextTick()
    const tooltip = getTooltipElement()
    expect(tooltip.textContent).toContain('copyable details')

    await trigger.trigger('mouseleave', { relatedTarget: tooltip })
    await nextTick()
    expect(getTooltipElement()).toBe(tooltip)

    tooltip.dispatchEvent(new MouseEvent('mouseleave', { relatedTarget: trigger.element }))
    await nextTick()
    expect(getTooltipElement()).toBe(tooltip)

    tooltip.dispatchEvent(new MouseEvent('mouseleave', { relatedTarget: null }))
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
