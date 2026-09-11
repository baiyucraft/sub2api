import { beforeEach, describe, expect, it, vi } from 'vitest'

const { get } = vi.hoisted(() => ({
  get: vi.fn(),
}))

vi.mock('../../client', () => ({
  apiClient: { get },
}))

import { list, listWithEtag } from '../accounts'

describe('admin accounts preferred filter', () => {
  beforeEach(() => {
    get.mockReset()
    get.mockResolvedValue({
      status: 200,
      headers: {},
      data: { items: [], total: 0, page: 1, page_size: 20, pages: 0 }
    })
  })

  it('passes preferred=1 to the account list request', async () => {
    await list(2, 50, { group: '7', preferred: '1', scope: 'upstream' })

    expect(get).toHaveBeenCalledWith('/admin/accounts', {
      params: {
        page: 2,
        page_size: 50,
        group: '7',
        preferred: '1',
        scope: 'upstream'
      },
      signal: undefined
    })
  })

  it('includes preferred in ETag requests so filter changes use a distinct cache key', async () => {
    await listWithEtag(1, 20, { preferred: '1', group: '7' }, { etag: 'accounts-etag' })

    expect(get).toHaveBeenCalledWith('/admin/accounts', {
      params: {
        page: 1,
        page_size: 20,
        preferred: '1',
        group: '7'
      },
      headers: { 'If-None-Match': 'accounts-etag' },
      signal: undefined,
      validateStatus: expect.any(Function)
    })
  })
})
