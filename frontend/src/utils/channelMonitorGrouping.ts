import type { UserMonitorView } from '@/api/channelMonitor'

const DOMESTIC_PROVIDERS = new Set(['kimi', 'zhipu', 'deepseek', 'minimax'])
const DISPLAY_ORDER = ['openai', 'anthropic', '__domestic__', '__other__'] as const

export interface MonitorCardGroup {
  provider: string
  items: UserMonitorView[]
}

/** Group only the V1 display; each item retains its original provider. */
export function groupMonitorItems(items: UserMonitorView[]): MonitorCardGroup[] {
  const groups = new Map<string, UserMonitorView[]>()
  for (const item of items) {
    const rawProvider = item.provider?.trim() || ''
    const provider = rawProvider === 'openai' || rawProvider === 'anthropic'
      ? rawProvider
      : DOMESTIC_PROVIDERS.has(rawProvider) ? '__domestic__' : '__other__'
    const rows = groups.get(provider) || []
    rows.push(item)
    groups.set(provider, rows)
  }

  return DISPLAY_ORDER.filter((provider) => groups.has(provider))
    .map((provider) => ({
      provider,
      items: [...(groups.get(provider) || [])].sort(compareMonitorItemsByCurrentRate),
    }))
}

/** Sort a platform's channels by the currently effective public rate. */
function compareMonitorItemsByCurrentRate(left: UserMonitorView, right: UserMonitorView): number {
  const leftRate = left.current_public_rate
  const rightRate = right.current_public_rate
  const leftMissing = leftRate == null || Number.isNaN(leftRate)
  const rightMissing = rightRate == null || Number.isNaN(rightRate)

  if (leftMissing && rightMissing) return left.id - right.id
  if (leftMissing) return 1
  if (rightMissing) return -1
  if (leftRate !== rightRate) return leftRate - rightRate
  return left.id - right.id
}
