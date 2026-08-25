import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

const viewSource = readFileSync(
  resolve(dirname(fileURLToPath(import.meta.url)), '../RechargeStoreView.vue'),
  'utf8',
)
const sidebarSource = readFileSync(
  resolve(dirname(fileURLToPath(import.meta.url)), '../../../components/layout/AppSidebar.vue'),
  'utf8',
)
const routerSource = readFileSync(
  resolve(dirname(fileURLToPath(import.meta.url)), '../../../router/index.ts'),
  'utf8',
)

describe('RechargeStoreView', () => {
  it('embeds the fixed store URL without forwarding application context', () => {
    expect(viewSource).toContain("const RECHARGE_STORE_URL = 'https://catfk.com/shop/baiyuapi'")
    expect(viewSource).toContain(':src="RECHARGE_STORE_URL"')
    expect(viewSource).toContain(':href="RECHARGE_STORE_URL"')
    expect(viewSource).toContain('referrerpolicy="no-referrer"')
    expect(viewSource).not.toContain('buildEmbeddedUrl')
    expect(viewSource).not.toContain('authStore.token')
    expect(viewSource).not.toContain('authStore.user')
  })

  it('keeps the external navigation safe', () => {
    expect(viewSource).toContain('target="_blank"')
    expect(viewSource).toContain('rel="noopener noreferrer"')
  })

  it('provides loading and slow-load recovery feedback without rewriting the external page', () => {
    expect(viewSource).toContain("frameLoadState = ref<FrameLoadState>('loading')")
    expect(viewSource).toContain('@load="handleFrameLoad"')
    expect(viewSource).toContain('@error="handleFrameError"')
    expect(viewSource).toContain("frameLoadState.value = 'slow'")
    expect(viewSource).toContain("t('rechargeStore.loadSlow')")
    expect(viewSource).toContain("t('rechargeStore.loadFailed')")
    expect(viewSource).toContain('prefers-reduced-motion: reduce')
  })

  it('keeps a taller, touch-friendly mobile viewport and recommends a new window', () => {
    expect(viewSource).toContain("t('rechargeStore.mobileHint')")
    expect(viewSource).toContain('min-height: 720px')
    expect(viewSource).toContain('overscroll-behavior: contain')
    expect(viewSource).toContain('w-full justify-center')
  })

  it('registers an authenticated route without the native payment feature gate', () => {
    const routeBlock = routerSource.match(/path: '\/recharge-store',[\s\S]*?\n {2}\},/)?.[0]

    expect(routeBlock).toBeTruthy()
    expect(routeBlock).toContain('requiresAuth: true')
    expect(routeBlock).toContain('requiresAdmin: false')
    expect(routeBlock).not.toContain('requiresPayment')
  })

  it('places the store between native purchase and orders without a payment flag', () => {
    const purchaseItem = "{ path: '/purchase', label: t('nav.buySubscription'), icon: RechargeSubscriptionIcon, hideInSimpleMode: true, featureFlag: flagPayment }"
    const storeItem = "{ path: '/recharge-store', label: t('nav.rechargeStore'), icon: RechargeStoreIcon }"
    const ordersItem = "{ path: '/orders', label: t('nav.myOrders'), icon: OrderListIcon, hideInSimpleMode: true, featureFlag: flagPayment }"

    expect(sidebarSource).toContain(`${purchaseItem},\n    ${storeItem},\n    ${ordersItem},`)
    expect(storeItem).not.toContain('hideInSimpleMode')
  })
})
