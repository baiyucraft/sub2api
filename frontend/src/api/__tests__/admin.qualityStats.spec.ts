import { beforeEach, describe, expect, it, vi } from 'vitest'

const { post } = vi.hoisted(() => ({ post: vi.fn() }))

vi.mock('@/api/client', () => ({ apiClient: { post } }))

import { getBatchQualityStats as getAccountQualityStats } from '@/api/admin/accounts'
import { getBatchQualityStats as getGroupQualityStats } from '@/api/admin/groups'

describe('admin quality statistics API', () => {
  beforeEach(() => post.mockReset())

  it('passes ETag and AbortSignal for account snapshots', async () => {
    const controller = new AbortController()
    post.mockResolvedValue({ status: 200, headers: { etag: '"account-v1"' }, data: { stats: {} } })

    const result = await getAccountQualityStats([2, 1], {
      etag: '"account-v0"',
      signal: controller.signal
    })

    expect(post).toHaveBeenCalledWith(
      '/admin/accounts/quality-stats/batch',
      { account_ids: [2, 1] },
      expect.objectContaining({
        headers: { 'If-None-Match': '"account-v0"' },
        signal: controller.signal,
        validateStatus: expect.any(Function)
      })
    )
    expect(result).toEqual({ notModified: false, etag: '"account-v1"', data: { stats: {} } })
  })

  it('represents a group 304 without replacing cached data', async () => {
    post.mockResolvedValue({ status: 304, headers: {}, data: '' })

    const result = await getGroupQualityStats([9], { etag: '"group-v1"' })

    const config = post.mock.calls[0][2]
    expect(config.validateStatus(304)).toBe(true)
    expect(config.validateStatus(500)).toBe(false)
    expect(config.headers).toEqual({ 'If-None-Match': '"group-v1"' })
    expect(result).toEqual({ notModified: true, etag: null, data: null })
  })
})
