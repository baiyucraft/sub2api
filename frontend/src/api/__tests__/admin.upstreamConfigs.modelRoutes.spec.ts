import { beforeEach, describe, expect, it, vi } from 'vitest'

const { get, post, put, del } = vi.hoisted(() => ({
  get: vi.fn(),
  post: vi.fn(),
  put: vi.fn(),
  del: vi.fn()
}))

vi.mock('../client', () => ({
  apiClient: { get, post, put, delete: del }
}))

import {
  createKeyModelRoute,
  listKeyModelRoutes,
  listModelRoutes,
  removeKeyModelRoute,
  updateKeyModelRoute
} from '@/api/admin/upstreamConfigs'

describe('admin upstream key model route API', () => {
  beforeEach(() => {
    get.mockReset()
    post.mockReset()
    put.mockReset()
    del.mockReset()
  })

  it('lists routes for one key and for the whole upstream config', async () => {
    get.mockResolvedValue({ data: [{ id: 3, upstream_key_id: 9, public_model: 'glm-5.3' }] })

    await expect(listKeyModelRoutes(4, 9)).resolves.toHaveLength(1)
    expect(get).toHaveBeenNthCalledWith(1, '/admin/upstream-configs/4/keys/9/model-routes')

    await expect(listModelRoutes(4)).resolves.toHaveLength(1)
    expect(get).toHaveBeenNthCalledWith(2, '/admin/upstream-configs/4/model-routes')
  })

  it('creates, updates and deletes a key model route through nested resources', async () => {
    const payload = {
      public_model: 'glm-5.3',
      upstream_model: 'glm-5-plus',
      target_platform: 'zhipu' as const,
      api_protocol: 'chat',
      enabled: true,
      priority: 1
    }
    post.mockResolvedValue({ data: { id: 3, ...payload } })
    put.mockResolvedValue({ data: { id: 3, ...payload } })
    del.mockResolvedValue({ data: { message: 'deleted' } })

    await createKeyModelRoute(4, 9, payload)
    expect(post).toHaveBeenCalledWith('/admin/upstream-configs/4/keys/9/model-routes', payload)

    await updateKeyModelRoute(4, 9, 3, payload)
    expect(put).toHaveBeenCalledWith('/admin/upstream-configs/4/keys/9/model-routes/3', payload)

    await removeKeyModelRoute(4, 9, 3)
    expect(del).toHaveBeenCalledWith('/admin/upstream-configs/4/keys/9/model-routes/3')
  })
})
