import type { Proxy, ProxyIPGroupVirtual, ProxyListItem } from '@/types'

/** Group binding rows have positive IDs; negative IDs are legacy response compatibility only. */
export const isProxyIPGroupVirtual = (
  proxy: Pick<ProxyListItem, 'id' | 'binding_type' | 'proxy_ip_group_id'>,
): proxy is ProxyIPGroupVirtual => (
  proxy.binding_type === 'proxy_ip_group'
  || proxy.id <= 0
)

/** Keep only real proxy endpoints from a mixed proxy/proxy-group response. */
export const filterRealProxies = (proxies: ProxyListItem[]): Proxy[] =>
  proxies.filter((proxy): proxy is Proxy => !isProxyIPGroupVirtual(proxy))
