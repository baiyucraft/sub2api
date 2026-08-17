import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import UpstreamModelSyncStatus from '../UpstreamModelSyncStatus.vue'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) => params ? `${key}:${JSON.stringify(params)}` : key
    })
  }
})

describe('UpstreamModelSyncStatus', () => {
  it.each([
    ['available', 'bg-emerald-50'],
    ['stale', 'bg-amber-50'],
    ['error', 'bg-red-50'],
    ['unsupported', 'bg-gray-100']
  ] as const)('renders %s status', (status, expectedClass) => {
    const wrapper = mount(UpstreamModelSyncStatus, {
      props: {
        sync: {
          mode: status === 'unsupported' ? 'manual' : 'sync_managed',
          status,
          model_count: 4,
          enforcement_expired: false
        }
      }
    })

    expect(wrapper.get('[data-status]').attributes('data-status')).toBe(status)
    expect(wrapper.get('[data-status]').classes()).toContain(expectedClass)
  })

  it('shows the 24 hour fallback marker without exposing raw upstream details', () => {
    const wrapper = mount(UpstreamModelSyncStatus, {
      props: {
        sync: {
          mode: 'sync_managed',
          status: 'stale',
          model_count: 8,
          error_code: 'upstream_error',
          enforcement_expired: true
        }
      }
    })

    expect(wrapper.get('[data-test="upstream-model-sync-expired"]').text()).toContain('fallback')
    expect(wrapper.text()).not.toContain('token')
  })

  it('shows retained-result and source context while the old whitelist is enforced', () => {
    const wrapper = mount(UpstreamModelSyncStatus, {
      props: {
        sync: {
          mode: 'sync_managed',
          status: 'error',
          source: 'live_models',
          model_count: 3,
          last_success_at: '2026-08-17T01:00:00Z',
          enforcement_expired: false
        }
      }
    })

    expect(wrapper.get('[data-test="upstream-model-sync-retained"]').text()).toContain('retained')
    expect(wrapper.get('[data-test="upstream-model-sync-status"]').attributes('title')).toContain('source.live_models')
  })
})
