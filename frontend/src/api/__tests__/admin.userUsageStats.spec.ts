import { beforeEach, describe, expect, it, vi } from 'vitest'

const { post } = vi.hoisted(() => ({ post: vi.fn() }))

vi.mock('@/api/client', () => ({ apiClient: { post } }))

import { getBatchUsersUsage } from '@/api/admin/dashboard'

describe('admin user usage statistics API', () => {
  beforeEach(() => post.mockReset())

  it('passes ETag and AbortSignal and returns a fresh snapshot', async () => {
    const controller = new AbortController()
    post.mockResolvedValue({
      status: 200,
      headers: { etag: '"users-v2"' },
      data: { stats: { 1: { user_id: 1 } } }
    })

    const result = await getBatchUsersUsage([2, 1], {
      etag: '"users-v1"',
      signal: controller.signal
    })

    expect(post).toHaveBeenCalledWith(
      '/admin/dashboard/users-usage',
      { user_ids: [2, 1] },
      expect.objectContaining({
        headers: { 'If-None-Match': '"users-v1"' },
        signal: controller.signal,
        validateStatus: expect.any(Function)
      })
    )
    expect(result).toEqual({
      notModified: false,
      etag: '"users-v2"',
      data: { stats: { 1: { user_id: 1 } } }
    })
  })

  it('represents 304 without replacing cached page data', async () => {
    post.mockResolvedValue({ status: 304, headers: {}, data: '' })

    const result = await getBatchUsersUsage([9], { etag: '"users-v1"' })
    const config = post.mock.calls[0][2]

    expect(config.validateStatus(304)).toBe(true)
    expect(config.validateStatus(500)).toBe(false)
    expect(result).toEqual({ notModified: true, etag: null, data: null })
  })
})
