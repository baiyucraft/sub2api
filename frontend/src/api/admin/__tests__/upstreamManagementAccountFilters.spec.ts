import { beforeEach, describe, expect, it, vi } from 'vitest'

const { get } = vi.hoisted(() => ({
  get: vi.fn()
}))

vi.mock('../../client', () => ({
  apiClient: { get }
}))

import { listAccounts, listAccountsWithEtag } from '../upstreamManagement'

describe('upstream account quality filter API', () => {
  beforeEach(() => {
    get.mockReset()
    get.mockResolvedValue({
      status: 200,
      headers: {},
      data: { items: [], total: 0, page: 1, page_size: 20, pages: 0 }
    })
  })

  it('passes quality_filter through the upstream account list request', async () => {
    await listAccounts({ page: 2, page_size: 50, group: '7', quality_filter: '1h-A', scope: 'upstream' })

    expect(get).toHaveBeenCalledWith('/admin/upstream-management/accounts', {
      params: {
        page: 2,
        page_size: 50,
        group: '7',
        quality_filter: '1h-A',
        scope: 'upstream'
      }
    })
  })

  it('keeps quality_filter in ETag requests', async () => {
    await listAccountsWithEtag(
      { quality_filter: '24h-B', group: '7' },
      { etag: 'upstream-accounts-etag' }
    )

    expect(get).toHaveBeenCalledWith('/admin/upstream-management/accounts', {
      params: { quality_filter: '24h-B', group: '7' },
      headers: { 'If-None-Match': 'upstream-accounts-etag' },
      signal: undefined,
      validateStatus: expect.any(Function)
    })
  })
})
