import type { UserMonitorView } from '@/api/channelMonitor'
import { PROVIDERS } from '@/constants/channelMonitor'

export interface MonitorCardGroup {
  provider: string
  items: UserMonitorView[]
}

/** Group user-facing V1 monitor cards by provider in a stable display order. */
export function groupMonitorItems(items: UserMonitorView[]): MonitorCardGroup[] {
  const groups = new Map<string, UserMonitorView[]>()
  for (const item of items) {
    const rawProvider = item.provider?.trim() || ''
    const provider = PROVIDERS.includes(rawProvider as (typeof PROVIDERS)[number])
      ? rawProvider
      : '__other__'
    const rows = groups.get(provider) || []
    rows.push(item)
    groups.set(provider, rows)
  }

  const providerOrder = new Map<string, number>(PROVIDERS.map((provider, index) => [provider, index]))
  return Array.from(groups.entries())
    .sort(([left], [right]) => {
      if (left === '__other__') return 1
      if (right === '__other__') return -1
      const leftOrder = providerOrder.get(left)
      const rightOrder = providerOrder.get(right)
      if (leftOrder != null && rightOrder != null) return leftOrder - rightOrder
      if (leftOrder != null) return -1
      if (rightOrder != null) return 1
      return left.localeCompare(right)
    })
    .map(([provider, groupItems]) => ({ provider, items: groupItems }))
}
