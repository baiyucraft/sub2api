import { beforeEach, describe, expect, it, vi } from 'vitest'

const { get } = vi.hoisted(() => ({ get: vi.fn() }))
vi.mock('@/api/client', () => ({ apiClient: { get } }))

import { list, status } from '@/api/channelMonitor'

describe('user channel monitor range API', () => {
  beforeEach(() => get.mockReset())

  it('uses the unified range query for list and detail requests', async () => {
    get.mockResolvedValueOnce({ data: { range: '15d', items: [] } })
    await list({ range: '15d' })
    expect(get).toHaveBeenLastCalledWith('/channel-monitors', {
      params: { range: '15d' },
      signal: undefined,
    })

    get.mockResolvedValueOnce({ data: { id: 7, models: [] } })
    await status(7, '30d')
    expect(get).toHaveBeenLastCalledWith('/channel-monitors/7/status', { params: { range: '30d' } })
  })

  it('defaults both requests to 24 hours', async () => {
    get.mockResolvedValue({ data: { range: '24h', items: [] } })
    await list()
    await status(7)
    expect(get.mock.calls[0][1].params).toEqual({ range: '24h' })
    expect(get.mock.calls[1][1].params).toEqual({ range: '24h' })
  })
})
