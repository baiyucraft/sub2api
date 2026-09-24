import { beforeEach, describe, expect, it, vi } from 'vitest'

const { get } = vi.hoisted(() => ({ get: vi.fn() }))
vi.mock('@/api/client', () => ({ apiClient: { get } }))

import { getAll, getAllWithCount, list } from '@/api/admin/proxies'

describe.each([
  { name: 'paginated list', load: () => list(), wrap: (items: unknown[]) => ({ items, total: items.length, pages: 1 }) },
  { name: 'account selector', load: getAll, wrap: (items: unknown[]) => items },
  { name: 'account selector with counts', load: getAllWithCount, wrap: (items: unknown[]) => items },
])('$name', ({ load, wrap }) => {
  beforeEach(() => { get.mockReset() })

  it.each(['', undefined, null, {}, { items: null }, { items: {} }])(
    'rejects an empty or malformed successful response: %j',
    async (data) => {
      get.mockResolvedValue({ data })
      await expect(load()).rejects.toThrow('Invalid proxy list response')
    },
  )

  it.each([{ items: [] }, { items: [{ id: 1, name: 'valid proxy' }] }])('preserves valid rows: %j', async ({ items }) => {
    const data = wrap(items)
    get.mockResolvedValue({ data })
    await expect(load()).resolves.toBe(data)
  })

  it('accepts a mixed real-proxy and virtual proxy-group response', async () => {
    const items = [
      { id: 7, name: 'real proxy' },
      { id: -12, name: 'group proxy', binding_type: 'proxy_ip_group', proxy_ip_group_id: 12 },
    ]
    get.mockResolvedValue({ data: items })
    await expect(getAll()).resolves.toBe(items)
  })
})
