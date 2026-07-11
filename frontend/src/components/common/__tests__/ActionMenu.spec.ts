import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { h } from 'vue'
import ActionMenu from '../ActionMenu.vue'

const rect = (values: Partial<DOMRect>): DOMRect => ({
  x: 0,
  y: 0,
  top: 0,
  right: 0,
  bottom: 0,
  left: 0,
  width: 0,
  height: 0,
  toJSON: () => ({}),
  ...values
})

const createAnchor = (bounds = rect({ top: 100, right: 300, bottom: 120, left: 260, width: 40, height: 20 })) => {
  const anchor = document.createElement('button')
  anchor.getBoundingClientRect = vi.fn(() => bounds)
  document.body.appendChild(anchor)
  return anchor
}

const mountMenu = (anchorEl: HTMLElement, show = true) => mount(ActionMenu, {
  props: { show, anchorEl, width: 'normal' },
  slots: {
    default: `<button role="menuitem">First</button><button role="menuitem">Second</button><button role="menuitem">Third</button>`
  },
  attachTo: document.body
})

describe('ActionMenu', () => {
  beforeEach(() => {
    Object.defineProperty(window, 'innerWidth', { configurable: true, value: 800 })
    Object.defineProperty(window, 'innerHeight', { configurable: true, value: 600 })
  })

  afterEach(() => {
    document.body.innerHTML = ''
    vi.restoreAllMocks()
  })

  it('Teleport 到 body，并按实际尺寸在锚点下方右对齐', async () => {
    const anchor = createAnchor()
    const wrapper = mountMenu(anchor)
    await flushPromises()

    const menu = document.body.querySelector<HTMLElement>('[role="menu"]')!
    menu.getBoundingClientRect = vi.fn(() => rect({ width: 192, height: 120 }))
    window.dispatchEvent(new Event('resize'))
    await flushPromises()

    expect(menu.style.left).toBe('108px')
    expect(menu.style.top).toBe('124px')
    expect(menu.style.visibility).toBe('visible')
    expect(document.body.querySelector('[data-testid="action-menu-backdrop"]')).not.toBeNull()
    wrapper.unmount()
  })

  it('空间不足时自动上翻、左移，并保持 8px 视口边界', async () => {
    const anchor = createAnchor(rect({ top: 550, right: 70, bottom: 570, left: 30, width: 40, height: 20 }))
    const wrapper = mountMenu(anchor)
    await flushPromises()

    const menu = document.body.querySelector<HTMLElement>('[role="menu"]')!
    menu.getBoundingClientRect = vi.fn(() => rect({ width: 192, height: 160 }))
    window.dispatchEvent(new Event('resize'))
    await flushPromises()

    expect(menu.style.left).toBe('8px')
    expect(menu.style.top).toBe('386px')
    wrapper.unmount()
  })

  it('scroll capture 和 resize 时重新读取锚点位置', async () => {
    const anchor = createAnchor()
    const wrapper = mountMenu(anchor)
    await flushPromises()
    const menu = document.body.querySelector<HTMLElement>('[role="menu"]')!
    menu.getBoundingClientRect = vi.fn(() => rect({ width: 192, height: 120 }))

    window.dispatchEvent(new Event('scroll'))
    window.dispatchEvent(new Event('resize'))
    await flushPromises()

    expect(anchor.getBoundingClientRect).toHaveBeenCalledTimes(3)
    wrapper.unmount()
  })

  it('聚焦首项，支持循环方向键、Home、End 和 Escape', async () => {
    const anchor = createAnchor()
    anchor.focus()
    const wrapper = mountMenu(anchor)
    await flushPromises()
    const menu = document.body.querySelector<HTMLElement>('[role="menu"]')!
    const items = Array.from(document.body.querySelectorAll<HTMLElement>('[role="menuitem"]'))

    expect(document.activeElement).toBe(items[0])
    expect(items.map(item => item.getAttribute('tabindex'))).toEqual(['0', '-1', '-1'])
    items[0].dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowUp', bubbles: true }))
    expect(document.activeElement).toBe(items[2])
    expect(items.map(item => item.getAttribute('tabindex'))).toEqual(['-1', '-1', '0'])
    items[2].dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true }))
    expect(document.activeElement).toBe(items[0])
    menu.dispatchEvent(new KeyboardEvent('keydown', { key: 'End', bubbles: true }))
    expect(document.activeElement).toBe(items[2])
    menu.dispatchEvent(new KeyboardEvent('keydown', { key: 'Home', bubbles: true }))
    expect(document.activeElement).toBe(items[0])
    menu.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    expect(wrapper.emitted('close')).toHaveLength(1)
    wrapper.unmount()
  })

  it('Tab 关闭菜单并恢复锚点焦点', async () => {
    const anchor = createAnchor()
    anchor.focus()
    const wrapper = mountMenu(anchor)
    await flushPromises()

    document.body.querySelector<HTMLElement>('[role="menuitem"]')!
      .dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', bubbles: true }))
    expect(wrapper.emitted('close')).toHaveLength(1)

    await wrapper.setProps({ show: false })
    await flushPromises()
    expect(document.activeElement).toBe(anchor)
    wrapper.unmount()
  })

  it('backdrop 和 slot close 可关闭，关闭后恢复焦点', async () => {
    const anchor = createAnchor()
    anchor.focus()
    const wrapper = mount(ActionMenu, {
      props: { show: true, anchorEl: anchor, width: 'normal' },
      slots: { default: ({ close }: { close: () => void }) => mountMenuSlot(close) },
      attachTo: document.body
    })
    await flushPromises()

    document.body.querySelector<HTMLElement>('[data-slot-close]')!.click()
    document.body.querySelector<HTMLElement>('[data-testid="action-menu-backdrop"]')!.click()
    expect(wrapper.emitted('close')).toHaveLength(2)

    await wrapper.setProps({ show: false })
    await flushPromises()
    expect(document.activeElement).toBe(anchor)
    wrapper.unmount()
  })

  it('锚点卸载时请求关闭', async () => {
    const anchor = createAnchor()
    const wrapper = mountMenu(anchor)
    await flushPromises()

    anchor.remove()
    await flushPromises()

    expect(wrapper.emitted('close')).toHaveLength(1)
    wrapper.unmount()
  })

  it('新弹窗接管焦点时不把焦点抢回背景锚点', async () => {
    const anchor = createAnchor()
    anchor.focus()
    const wrapper = mountMenu(anchor)
    await flushPromises()

    const dialog = document.createElement('div')
    dialog.setAttribute('role', 'dialog')
    dialog.setAttribute('aria-modal', 'true')
    const input = document.createElement('input')
    dialog.appendChild(input)
    document.body.appendChild(dialog)
    input.focus()

    await wrapper.setProps({ show: false })
    await flushPromises()
    expect(document.activeElement).toBe(input)
    wrapper.unmount()
  })
})

function mountMenuSlot(close: () => void) {
  return h('button', { role: 'menuitem', 'data-slot-close': '', onClick: close }, 'Close')
}
