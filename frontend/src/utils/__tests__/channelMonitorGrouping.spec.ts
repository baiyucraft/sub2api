import { describe, expect, it } from 'vitest'
import { groupMonitorItems } from '@/utils/channelMonitorGrouping'

function monitor(id: number, provider: string): { id: number; provider: string } {
  return { id, provider }
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
})
