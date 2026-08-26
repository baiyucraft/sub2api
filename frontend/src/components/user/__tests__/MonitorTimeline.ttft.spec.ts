import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import MonitorTimeline from '../monitor/MonitorTimeline.vue'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

describe('MonitorTimeline TTFT and status colors', () => {
  it('keeps operational/degraded/error colors and hides non-positive TTFT', () => {
    const wrapper = mount(MonitorTimeline, {
      props: {
        countdownSeconds: 0,
        length: 3,
        buckets: [
          { status: 'error', latency_ms: 900, ttft_ms: 0, ping_latency_ms: null, checked_at: '2026-08-26T00:00:03Z' },
          { status: 'degraded', latency_ms: 800, ttft_ms: null, ping_latency_ms: null, checked_at: '2026-08-26T00:00:02Z' },
          { status: 'operational', latency_ms: 700, ttft_ms: 120, ping_latency_ms: null, checked_at: '2026-08-26T00:00:01Z' },
        ],
      },
    })

    const bars = wrapper.findAll('.flex-1.min-w-0')
    expect(bars).toHaveLength(3)
    expect(bars[0].classes()).toContain('bg-emerald-500')
    expect(bars[1].classes()).toContain('bg-amber-500')
    expect(bars[2].classes()).toContain('bg-red-500')
    expect(bars[2].attributes('title')).toContain('monitorCommon.latencyEmpty')
    expect(bars[0].attributes('title')).toContain('120ms')
  })
})
