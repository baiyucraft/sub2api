import { beforeEach, describe, expect, it, vi } from 'vitest'

const { get, put } = vi.hoisted(() => ({
  get: vi.fn(),
  put: vi.fn(),
}))

vi.mock('../../client', () => ({
  apiClient: { get, put },
}))

import groupsAPI from '../groups'

describe('admin preferred account group API', () => {
  beforeEach(() => {
    get.mockReset()
    put.mockReset()
  })

  it('reads the group-scoped preferred account list', async () => {
    get.mockResolvedValue({ data: [{ id: 12, scheduler_preferred: true }] })

    await expect(groupsAPI.getPreferredAccounts(7)).resolves.toEqual([
      { id: 12, scheduler_preferred: true },
    ])
    expect(get).toHaveBeenCalledWith('/admin/groups/7/preferred-accounts')
  })

  it('replaces or clears the preferred account IDs without changing the endpoint contract', async () => {
    put.mockResolvedValue({ data: {} })

    await groupsAPI.updatePreferredAccounts(7, [12, 13])
    expect(put).toHaveBeenCalledWith('/admin/groups/7/preferred-accounts', {
      account_ids: [12, 13],
    })
  })
})
