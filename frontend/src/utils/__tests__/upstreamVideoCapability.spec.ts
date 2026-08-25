import { describe, expect, it } from 'vitest'

import { hasUsableUpstreamVideoCapability } from '../upstreamVideoCapability'

const pricing = {
  supported: true,
  status: 'available',
  stale: false,
  rate_independent: false,
  effective_rate_multiplier: 1,
  final_cost_480p: null,
  final_cost_720p: 0.07,
  final_cost_1080p: null
}

describe('hasUsableUpstreamVideoCapability', () => {
  it('only admits Grok pricing snapshots', () => {
    expect(hasUsableUpstreamVideoCapability({ platform: 'openai', upstream_video_pricing: pricing })).toBe(false)
    expect(hasUsableUpstreamVideoCapability({ platform: 'grok', upstream_video_pricing: pricing })).toBe(true)
  })

  it('rejects an allow flag and multiplier without an actual video price', () => {
    expect(hasUsableUpstreamVideoCapability({
      platform: 'grok',
      upstream_video_pricing: { ...pricing, final_cost_480p: null, final_cost_720p: null, final_cost_1080p: null }
    })).toBe(false)
  })

  it('rejects stale or unsupported snapshots', () => {
    expect(hasUsableUpstreamVideoCapability({ platform: 'grok', upstream_video_pricing: { ...pricing, stale: true } })).toBe(false)
    expect(hasUsableUpstreamVideoCapability({ platform: 'grok', upstream_video_pricing: { ...pricing, supported: false } })).toBe(false)
  })
})
