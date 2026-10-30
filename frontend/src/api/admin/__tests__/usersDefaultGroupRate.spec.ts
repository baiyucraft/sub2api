import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { UpdateUserRequest } from '@/types'

const { get, put } = vi.hoisted(() => ({ get: vi.fn(), put: vi.fn() }))
vi.mock('../../client', () => ({ apiClient: { get, put } }))
import { getById, update } from '../users'

describe('admin user default group percentage API', () => {
  beforeEach(() => {
    get.mockReset()
    put.mockReset()
    put.mockResolvedValue({ data: { id: 7 } })
  })

  it('reads the user default separately from group-specific percentages', async () => {
    const response = { id: 7, default_group_rate_percent: 95, group_rate_percents: { 3: 90 } }
    get.mockResolvedValue({ data: response })
    await expect(getById(7)).resolves.toEqual(response)
    expect(get).toHaveBeenCalledWith('/admin/users/7')
  })

  it.each([
    { notes: 'unchanged rate' },
    { default_group_rate_percent: null },
    { default_group_rate_percent: 95 },
    { default_group_rate_percent: 0 },
  ] satisfies UpdateUserRequest[])('preserves the omitted, null and numeric update contract: %j', async (payload) => {
    await update(7, payload)
    expect(put).toHaveBeenCalledWith('/admin/users/7', payload)
    expect(put.mock.calls[0][1]).toBe(payload)
  })

  it('sends reset and newly configured exceptions in one update', async () => {
    const payload: UpdateUserRequest = { default_group_rate_percent: 95, reset_group_rate_percents: true, group_rate_percents: { 3: 90, 9: 0 } }
    await update(7, payload)
    expect(put).toHaveBeenCalledOnce()
    expect(put).toHaveBeenCalledWith('/admin/users/7', payload)
  })
})
