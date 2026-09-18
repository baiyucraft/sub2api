import { beforeEach, describe, expect, it, vi } from 'vitest'

const { get, put } = vi.hoisted(() => ({
  get: vi.fn(),
  put: vi.fn(),
}))

vi.mock('../../client', () => ({
  apiClient: { get, put },
}))

import groupsAPI from '../groups'

describe('admin group custom rate percentage API', () => {
  beforeEach(() => {
    get.mockReset()
    put.mockReset()
  })

  it('reads the persisted percentage together with the compatible effective rate', async () => {
    const response = [{
      user_id: 7,
      user_name: 'user',
      user_email: 'user@example.test',
      user_notes: '',
      user_status: 'active',
      rate_percent: 50,
      rate_multiplier: 0.25,
      rpm_override: null,
    }]
    get.mockResolvedValue({ data: response })

    await expect(groupsAPI.getGroupRateMultipliers(3)).resolves.toEqual(response)
    expect(get).toHaveBeenCalledWith('/admin/groups/3/rate-multipliers')
  })

  it('writes only rate_percent entries', async () => {
    put.mockResolvedValue({ data: { message: 'ok' } })

    await groupsAPI.batchSetGroupRateMultipliers(3, [
      { user_id: 7, rate_percent: 50 },
      { user_id: 8, rate_percent: 0 },
    ])

    expect(put).toHaveBeenCalledWith('/admin/groups/3/rate-multipliers', {
      entries: [
        { user_id: 7, rate_percent: 50 },
        { user_id: 8, rate_percent: 0 },
      ],
    })
  })
})
