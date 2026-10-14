import { beforeEach, describe, expect, it, vi } from 'vitest'
import { updateSettings, type UpstreamManagementSettings } from '../upstreamManagement'
import { SCHEDULING_PROTECTION_SETTINGS_UPDATED } from '@/utils/schedulingProtection'

const { put } = vi.hoisted(() => ({ put: vi.fn() }))
vi.mock('../../client', () => ({ apiClient: { put } }))

beforeEach(() => { put.mockReset() })

describe('scheduling protection settings refresh', () => {
  it('notifies mounted views only after an atomic settings update succeeds, preserving an empty scope', async () => {
    const payload = { scheduling_protection_platforms: [] } as UpstreamManagementSettings
    let resolve!: (value: { data: UpstreamManagementSettings }) => void
    put.mockImplementation(() => new Promise(done => { resolve = done }))
    const notify = vi.fn()
    window.addEventListener(SCHEDULING_PROTECTION_SETTINGS_UPDATED, notify)
    try {
      const result = updateSettings(payload)
      expect(notify).not.toHaveBeenCalled()
      expect(put).toHaveBeenCalledWith('/admin/upstream-management/settings', payload)
      resolve({ data: payload })
      expect(await result).toEqual(payload)
      expect(notify).toHaveBeenCalledOnce()
    } finally { window.removeEventListener(SCHEDULING_PROTECTION_SETTINGS_UPDATED, notify) }
  })

  it('does not announce an uncommitted range after settings persistence fails', async () => {
    put.mockRejectedValue(new Error('write failed'))
    const notify = vi.fn()
    window.addEventListener(SCHEDULING_PROTECTION_SETTINGS_UPDATED, notify)
    try {
      await expect(updateSettings({ scheduling_protection_platforms: ['openai'] } as UpstreamManagementSettings)).rejects.toThrow('write failed')
      expect(notify).not.toHaveBeenCalled()
    } finally { window.removeEventListener(SCHEDULING_PROTECTION_SETTINGS_UPDATED, notify) }
  })
})
