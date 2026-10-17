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
      },
      signal: undefined
    })
  })

  it('forwards cancellation to the initial upstream list transport', async () => {
    const controller = new AbortController()
    await listAccounts({ page: 1 }, { signal: controller.signal })
    expect(get).toHaveBeenCalledWith('/admin/upstream-management/accounts', {
      params: { page: 1 }, signal: controller.signal
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

  it('keeps inline quality and exposes evaluation headers on both 200 and 304', async () => {
    const evaluatedAt = '2026-10-17T08:00:00Z'
    const page = { items: [], total: 0, page: 1, page_size: 20, pages: 0, quality_stats: {}, quality_stats_status: 'ok' }
    get.mockResolvedValueOnce({ status: 200, headers: { 'x-account-evaluated-at': evaluatedAt }, data: page })
    const first = await listAccounts({ include_quality: '1' })
    expect(first.quality_stats_status).toBe('ok')
    expect(first.evaluatedAt).toBe(evaluatedAt)
    expect(first.evaluationRequestedAt).toEqual(expect.any(Number))
    get.mockResolvedValueOnce({ status: 304, headers: { etag: 'same', 'x-account-evaluated-at': evaluatedAt }, data: '' })
    const result = await listAccountsWithEtag({ include_quality: '1' }, { etag: 'same' })
    expect(result).toEqual({ notModified: true, data: null, etag: 'same', evaluatedAt, evaluationRequestedAt: expect.any(Number) })
  })

  it.each([200, 304])('retains request start across a slow %i evaluation response', async status => {
    const requestStart = Date.now()
    vi.spyOn(Date, 'now').mockReturnValue(requestStart)
    get.mockImplementationOnce(async () => {
      vi.mocked(Date.now).mockReturnValue(requestStart + 2500)
      return { status, headers: { 'x-account-evaluated-at': '2026-10-17T08:00:00Z' }, data: { items: [] } }
    })
    try {
      const result = status === 200 ? await listAccounts() : await listAccountsWithEtag()
      expect(result.evaluationRequestedAt).toBe(requestStart)
    } finally { vi.mocked(Date.now).mockRestore() }
  })
})
