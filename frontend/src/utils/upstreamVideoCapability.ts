import type { Account } from '@/types'

const finiteVideoCost = (value: number | null | undefined): boolean =>
  value !== null && value !== undefined && Number.isFinite(value)

/**
 * Returns whether the upstream-management video badge has enough evidence to
 * claim usable Grok video support. A pricing snapshot from another platform,
 * an allow flag without a price, or stale/invalid data is not sufficient.
 */
export const hasUsableUpstreamVideoCapability = (
  account: Pick<Account, 'platform' | 'upstream_video_pricing'> | null | undefined
): boolean => {
  const pricing = account?.upstream_video_pricing
  if (account?.platform !== 'grok' || !pricing?.supported || pricing.stale) return false
  if (pricing.status !== 'available' && pricing.status !== 'partial') return false
  if (pricing.effective_rate_multiplier == null || !Number.isFinite(pricing.effective_rate_multiplier)) return false

  return [pricing.final_cost_480p, pricing.final_cost_720p, pricing.final_cost_1080p].some(finiteVideoCost)
}
