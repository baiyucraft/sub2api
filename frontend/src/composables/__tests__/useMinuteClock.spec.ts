import { afterEach, describe, expect, it, vi } from 'vitest'
import { effectScope } from 'vue'
import { useMinuteClock } from '../useMinuteClock'

describe('useMinuteClock', () => {
  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
  })

  it('shares one timer, advances unchanged labels at minute boundaries, and pauses hidden pages', async () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-10-17T00:00:00Z'))
    const hidden = vi.spyOn(document, 'hidden', 'get').mockReturnValue(false)
    const firstScope = effectScope()
    const secondScope = effectScope()
    const first = firstScope.run(useMinuteClock)!
    const second = secondScope.run(useMinuteClock)!
    expect(first).toBe(second)
    expect(vi.getTimerCount()).toBe(1)
    const initial = first.value
    await vi.advanceTimersByTimeAsync(59_000)
    expect(first.value).toBe(initial)
    await vi.advanceTimersByTimeAsync(1000)
    expect(first.value).toBe(initial + 60_000)
    hidden.mockReturnValue(true)
    await vi.advanceTimersByTimeAsync(60_000)
    expect(first.value).toBe(initial + 60_000)
    hidden.mockReturnValue(false)
    await vi.advanceTimersByTimeAsync(1000)
    expect(first.value).toBe(initial + 121_000)
    firstScope.stop()
    expect(vi.getTimerCount()).toBe(1)
    secondScope.stop()
    expect(vi.getTimerCount()).toBe(0)
  })
})
