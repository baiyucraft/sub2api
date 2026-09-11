import { describe, expect, it } from 'vitest'
import { groupMonitorItems } from '@/utils/channelMonitorGrouping'

function monitor(id: number, provider: string, current_public_rate?: number | null): { id: number; provider: string; current_public_rate?: number | null } {
  return { id, provider, current_public_rate }
}

describe('groupMonitorItems', () => {
  it('groups by provider and keeps the configured provider order', () => {
    const groups = groupMonitorItems([
      monitor(3, 'anthropic') as never,
      monitor(1, 'openai') as never,
      monitor(2, 'openai') as never,
    ])

    expect(groups.map((group) => group.provider)).toEqual(['openai', 'anthropic'])
    expect(groups[0].items.map((item) => item.id)).toEqual([1, 2])
  })

  it('places blank and unknown providers in the trailing other group', () => {
    const groups = groupMonitorItems([
      monitor(1, '') as never,
      monitor(2, 'custom-provider') as never,
      monitor(3, 'gemini') as never,
    ])

    expect(groups.map((group) => group.provider)).toEqual(['gemini', '__other__'])
    expect(groups[1].items.map((item) => item.id)).toEqual([1, 2])
  })

  it('sorts each platform by current rate, keeps missing rates last, and breaks ties by id', () => {
    const groups = groupMonitorItems([
      monitor(9, 'openai', null),
      monitor(8, 'openai', 0.2),
      monitor(7, 'openai', 0.1),
      monitor(6, 'openai', 0.1),
    ])

    expect(groups[0].items.map((item) => item.id)).toEqual([6, 7, 8, 9])
  })
})
