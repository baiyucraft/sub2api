import { beforeEach, describe, expect, it, vi } from 'vitest'

const { get, post, put, del } = vi.hoisted(() => ({
  get: vi.fn(),
  post: vi.fn(),
  put: vi.fn(),
  del: vi.fn(),
}))

vi.mock('@/api/client', () => ({
  apiClient: { get, post, put, delete: del },
}))

import { proxyIpGroupsAPI } from '@/api/admin/proxyIpGroups'

describe('proxyIpGroupsAPI', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('lists proxy IP groups from array and paginated response shapes', async () => {
    const rows = [{ id: 1, name: 'primary', member_count: 2, per_ip_concurrency: 4 }]
    get.mockResolvedValueOnce({ data: rows })
    await expect(proxyIpGroupsAPI.list()).resolves.toEqual(rows)
    expect(get).toHaveBeenLastCalledWith('/admin/proxy-ip-groups')

    get.mockResolvedValueOnce({ data: { items: rows } })
    await expect(proxyIpGroupsAPI.list()).resolves.toEqual(rows)
  })

  it('derives member_count and proxy_ids from compatible response shapes', async () => {
    get.mockResolvedValueOnce({
      data: [{ id: 2, name: 'ids only', per_ip_concurrency: 10, proxy_ids: [4, 4, 5] }],
    })
    await expect(proxyIpGroupsAPI.list()).resolves.toEqual([
      expect.objectContaining({ member_count: 2, proxy_ids: [4, 5] }),
    ])

    get.mockResolvedValueOnce({
      data: {
        id: 3,
        name: 'members only',
        per_ip_concurrency: 10,
        members: [{ id: 7 }, { id: 8 }],
      },
    })
    await expect(proxyIpGroupsAPI.getById(3)).resolves.toEqual(
      expect.objectContaining({ member_count: 2, proxy_ids: [7, 8] })
    )
  })

  it('uses the proxy IP group CRUD endpoints', async () => {
    const payload = { name: 'primary', per_ip_concurrency: 4, proxy_ids: [2, 3] }
    const row = { id: 1, member_count: 2, ...payload }
    get.mockResolvedValueOnce({ data: row })
    post.mockResolvedValueOnce({ data: row })
    put.mockResolvedValueOnce({ data: row })
    put.mockResolvedValueOnce({ data: row })
    del.mockResolvedValueOnce({ data: { message: 'ok' } })

    await expect(proxyIpGroupsAPI.getById(1)).resolves.toEqual(row)
    await expect(proxyIpGroupsAPI.create(payload)).resolves.toEqual(row)
    await expect(proxyIpGroupsAPI.update(1, payload)).resolves.toEqual(row)
    await expect(proxyIpGroupsAPI.updateMembers(1, [2, 3])).resolves.toEqual(row)
    await expect(proxyIpGroupsAPI.delete(1)).resolves.toEqual({ message: 'ok' })

    expect(get).toHaveBeenCalledWith('/admin/proxy-ip-groups/1')
    expect(post).toHaveBeenCalledWith('/admin/proxy-ip-groups', payload)
    expect(put).toHaveBeenCalledWith('/admin/proxy-ip-groups/1', payload)
    expect(put).toHaveBeenCalledWith('/admin/proxy-ip-groups/1/members', { proxy_ids: [2, 3] })
    expect(del).toHaveBeenCalledWith('/admin/proxy-ip-groups/1')
  })
})
