import { beforeEach, describe, expect, it, vi } from 'vitest'

const { put } = vi.hoisted(() => ({
  put: vi.fn()
}))

vi.mock('../client', () => ({
  apiClient: {
    put
  }
}))

import { updateKeyPlatform } from '@/api/admin/upstreamConfigs'

describe('admin upstream key platform API', () => {
  beforeEach(() => {
    put.mockReset()
  })

  it('updates one key platform through the nested platform resource', async () => {
    put.mockResolvedValue({ data: { id: 9, platform: 'anthropic' } })

    const result = await updateKeyPlatform(4, 9, {
      platform: 'anthropic',
      expected_updated_at: '2026-07-14T01:02:03Z'
    })

    expect(put).toHaveBeenCalledWith('/admin/upstream-configs/4/keys/9/platform', {
      platform: 'anthropic',
      expected_updated_at: '2026-07-14T01:02:03Z'
    })
    expect(result).toEqual({ id: 9, platform: 'anthropic' })
  })

  it('passes the explicit bound-account disable flag only for confirmed retries', async () => {
    put.mockResolvedValue({ data: { id: 9, platform: 'gemini', bound_account_count: 0 } })

    await updateKeyPlatform(4, 9, {
      platform: 'gemini',
      expected_updated_at: '2026-07-14T01:02:03Z',
      disable_bound_accounts: true
    })

    expect(put).toHaveBeenCalledWith('/admin/upstream-configs/4/keys/9/platform', {
      platform: 'gemini',
      expected_updated_at: '2026-07-14T01:02:03Z',
      disable_bound_accounts: true
    })
  })
})
