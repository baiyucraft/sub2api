import { describe, expect, it } from 'vitest'
import type { Proxy, ProxyIPGroupVirtual } from '@/types'
import { filterRealProxies, isProxyIPGroupVirtual } from '../proxy'

const realProxy = { id: 7, name: 'real' } as Proxy
const virtualGroup = {
  id: -12,
  name: 'shared group',
  binding_type: 'proxy_ip_group',
  proxy_ip_group_id: 12,
} satisfies ProxyIPGroupVirtual

describe('proxy list item classification', () => {
  it('recognizes the backend virtual proxy-group row', () => {
    expect(isProxyIPGroupVirtual(virtualGroup)).toBe(true)
    expect(isProxyIPGroupVirtual({ id: -12 })).toBe(true)
    expect(isProxyIPGroupVirtual(realProxy)).toBe(false)
  })

  it('keeps only real proxies for single-proxy consumers', () => {
    expect(filterRealProxies([realProxy, virtualGroup])).toEqual([realProxy])
  })
})
