import { beforeEach, describe, expect, it, vi } from 'vitest'

const { get, put } = vi.hoisted(() => ({
  get: vi.fn(),
  put: vi.fn()
}))

vi.mock('@/api/client', () => ({
  apiClient: { get, put }
}))

import {
  getTTFTGuardPolicy,
  listTTFTGuardPolicies,
  updateTTFTGuardPolicy
} from '@/api/admin/groups'

const policy = {
  group_id: 7,
  group_name: 'Premium OpenAI',
  group_platform: 'openai' as const,
  mode: 'enabled' as const,
  degradation_ttft_seconds: 12,
  min_samples: 4,
  enabled: true,
  effective_enabled: true,
  effective_degradation_ttft_seconds: 12,
  effective_min_samples: 4,
  global_enabled: true,
  global_degradation_ttft_seconds: 20,
  global_min_samples: 5,
  source: 'group' as const
}

describe('admin group TTFT Guard policy API', () => {
  beforeEach(() => {
    get.mockReset()
    put.mockReset()
  })

  it('uses the dedicated list and group endpoints', async () => {
    get.mockResolvedValueOnce({ data: [policy] })
    get.mockResolvedValueOnce({ data: policy })

    await expect(listTTFTGuardPolicies()).resolves.toEqual([policy])
    await expect(getTTFTGuardPolicy(7)).resolves.toEqual(policy)

    expect(get).toHaveBeenNthCalledWith(1, '/admin/groups/ttft-guard-policies')
    expect(get).toHaveBeenNthCalledWith(2, '/admin/groups/7/ttft-guard-policy')
  })

  it('accepts wrapped list responses and sends the three-state payload unchanged', async () => {
    get.mockResolvedValueOnce({ data: { policies: [policy] } })
    put.mockResolvedValueOnce({ data: policy })

    await expect(listTTFTGuardPolicies()).resolves.toEqual([policy])
    await expect(updateTTFTGuardPolicy(7, {
      mode: 'enabled',
      degradation_ttft_seconds: 12,
      min_samples: 4
    })).resolves.toEqual(policy)

    expect(put).toHaveBeenCalledWith('/admin/groups/7/ttft-guard-policy', {
      mode: 'enabled',
      degradation_ttft_seconds: 12,
      min_samples: 4
    })
  })
})
