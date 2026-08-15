import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { describe, expect, it } from 'vitest'

const componentPath = resolve(dirname(fileURLToPath(import.meta.url)), '../AppSidebar.vue')
const componentSource = readFileSync(componentPath, 'utf8')
const stylePath = resolve(dirname(fileURLToPath(import.meta.url)), '../../../style.css')
const styleSource = readFileSync(stylePath, 'utf8')

describe('AppSidebar custom SVG styles', () => {
  it('does not override uploaded SVG fill or stroke colors', () => {
    expect(componentSource).toContain('.sidebar-svg-icon {')
    expect(componentSource).toContain('color: currentColor;')
    expect(componentSource).toContain('display: block;')
    expect(componentSource).not.toContain('stroke: currentColor;')
    expect(componentSource).not.toContain('fill: none;')
  })
})

describe('AppSidebar scroll position persistence', () => {
  it('binds a template ref to the sidebar nav element', () => {
    expect(componentSource).toContain('ref="sidebarNavRef"')
    expect(componentSource).toContain('sidebar-nav')
  })

  it('declares sidebarNavRef in script setup', () => {
    expect(componentSource).toContain("const sidebarNavRef = ref<HTMLElement | null>(null)")
  })

  it('saves scroll position on beforeUnmount', () => {
    expect(componentSource).toContain('onBeforeUnmount')
    expect(componentSource).toContain('appStore.sidebarScrollTop')
    expect(componentSource).toContain('sidebarNavRef.value.scrollTop')
  })

  it('restores scroll position on mount', () => {
    expect(componentSource).toContain('onMounted')
    expect(componentSource).toContain('appStore.sidebarScrollTop')
    expect(componentSource).toContain('nextTick')
  })
})

describe('AppSidebar header styles', () => {
  it('does not clip the version badge dropdown', () => {
    const sidebarHeaderBlockMatch = styleSource.match(/\.sidebar-header\s*\{[\s\S]*?\n {2}\}/)
    const sidebarBrandBlockMatch = componentSource.match(/\.sidebar-brand\s*\{[\s\S]*?\n\}/)

    expect(sidebarHeaderBlockMatch).not.toBeNull()
    expect(sidebarBrandBlockMatch).not.toBeNull()
    expect(sidebarHeaderBlockMatch?.[0]).not.toContain('@apply overflow-hidden;')
    expect(sidebarBrandBlockMatch?.[0]).not.toContain('overflow: hidden;')
  })
})

describe('AppSidebar upstream navigation', () => {
  const upstreamConfigItem = "{ path: '/admin/upstream-configs', label: t('nav.upstreamConfigs'), icon: UpstreamConfigIcon }"
  const upstreamManagementItem = "{ path: '/admin/upstream-management', label: t('nav.upstreamManagement'), icon: UpstreamManagementIcon }"

  it('renders upstream configuration and management as adjacent top-level entries', () => {
    expect(componentSource).toContain(`${upstreamConfigItem},\n    ${upstreamManagementItem},`)
    expect(componentSource).not.toMatch(/path: '\/admin\/upstream-configs',[\s\S]{0,180}expandOnly: true/)
    expect(componentSource).not.toMatch(/path: '\/admin\/upstream-configs',[\s\S]{0,240}children:/)
  })

  it('uses two dedicated and visually distinct icons', () => {
    const configIcon = componentSource.match(/const UpstreamConfigIcon = \{[\s\S]*?\n\}/)?.[0]
    const managementIcon = componentSource.match(/const UpstreamManagementIcon = \{[\s\S]*?\n\}/)?.[0]

    expect(configIcon).toBeTruthy()
    expect(managementIcon).toBeTruthy()
    expect(configIcon).not.toBe(managementIcon)
    expect(configIcon).toContain("M4.5 4.5h15")
    expect(managementIcon).toContain("M12 9.75a2.25 2.25")
  })

  it('keeps both entries on the normal route-active path', () => {
    expect(componentSource).toContain("'sidebar-link-active': isActive(item.path)")
    expect(componentSource).toContain(upstreamConfigItem)
    expect(componentSource).toContain(upstreamManagementItem)
  })
})
