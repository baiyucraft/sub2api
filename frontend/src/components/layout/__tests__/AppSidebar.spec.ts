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
  it('renders one expandable upstream group with dashboard, channels and accounts', () => {
    expect(componentSource).toContain("path: '/admin/upstream'")
    expect(componentSource).toContain("path: '/admin/upstream/dashboard'")
    expect(componentSource).toContain("path: '/admin/upstream/channels'")
    expect(componentSource).toContain("path: '/admin/upstream/accounts'")
    expect(componentSource).toContain('expandOnly: true')
    expect(componentSource).toContain('alwaysExpanded: true')
    expect(componentSource).toContain('item.alwaysExpanded === true')
    expect(componentSource).toContain("label: t('nav.upstreamDashboard'), icon: UpstreamDashboardIcon")
    expect(componentSource).toContain("label: t('nav.upstreamChannels'), icon: UpstreamChannelsIcon")
    expect(componentSource).toContain("label: t('nav.upstreamAccounts'), icon: UpstreamAccountsIcon")
    expect(componentSource).toContain("h(Icon, { name: 'chartBar' })")
    expect(componentSource).toContain("h(Icon, { name: 'database' })")
    expect(componentSource).toContain("h(Icon, { name: 'userCircle' })")
    expect(componentSource).not.toContain("path: '/admin/upstream-configs'")
    expect(componentSource).not.toContain("path: '/admin/upstream-management'")
  })

  it('keeps child entries on the route-active path', () => {
    expect(componentSource).toContain("'sidebar-link-active': isActive(item.path)")
    expect(componentSource).toContain("'sidebar-link-active': route.path === child.path")
  })
})

describe('AppSidebar recharge store navigation', () => {
  const purchaseItem = "{ path: '/purchase', label: t('nav.buySubscription'), icon: RechargeSubscriptionIcon, hideInSimpleMode: true, featureFlag: flagPayment }"
  const storeItem = "{ path: '/recharge-store', label: t('nav.rechargeStore'), icon: RechargeStoreIcon }"
  const ordersItem = "{ path: '/orders', label: t('nav.myOrders'), icon: OrderListIcon, hideInSimpleMode: true, featureFlag: flagPayment }"

  it('keeps the fixed store between purchase and orders without the native payment flag', () => {
    expect(componentSource).toContain(`${purchaseItem},\n    ${storeItem},\n    ${ordersItem},`)
    expect(storeItem).not.toContain('featureFlag')
    expect(storeItem).not.toContain('hideInSimpleMode')
  })
})
