import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import UpstreamKeyRateTrendPanel from '../UpstreamKeyRateTrendPanel.vue'
import zh from '@/i18n/locales/zh/admin/upstreamConfigs'
import en from '@/i18n/locales/en/admin/upstreamConfigs'
import type { UpstreamKeyRateTrend } from '@/api/admin/upstreamConfigs'

let activeLocale: 'zh' | 'en' = 'zh'
vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => {
      let value: unknown = activeLocale === 'zh' ? zh : en
      for (const part of key.replace('admin.', '').split('.')) value = (value as Record<string, unknown>)?.[part]
      return typeof value === 'string' ? value : key
    } })
  }
})

const trend: UpstreamKeyRateTrend = {
  range: '24h', config_id: 1, key_id: 9, key_name: 'key', points: [],
  changes: [
    { type: 'key_confidence_distribution_reset', occurred_at: '2026-10-07T13:34:39Z' },
    { type: 'future_event', occurred_at: '2026-10-07T13:35:39Z' }
  ]
}

describe('UpstreamKeyRateTrendPanel event labels', () => {
  it.each([
    { locale: 'zh' as const, label: 'Key 分布采样系列重置' },
    { locale: 'en' as const, label: 'Key Distribution Sampling Series Reset' }
  ])('recognizes distribution resets in $locale and hides unknown event identifiers', ({ locale, label }) => {
    activeLocale = locale
    const wrapper = mount(UpstreamKeyRateTrendPanel, {
      props: { trend }, global: { stubs: { UpstreamKeyRateTrendChart: true } }
    })
    const labels = wrapper.findAll('.operation-row .font-medium').map(node => node.text())
    expect(labels[0]).toBe(label)
    expect(labels[1]).toBe((locale === 'zh' ? zh : en).upstreamConfigs.operations.eventTypes.unknown)
    expect(wrapper.text()).not.toContain('key_confidence_distribution_reset')
    expect(wrapper.text()).not.toContain('future_event')
  })
})
