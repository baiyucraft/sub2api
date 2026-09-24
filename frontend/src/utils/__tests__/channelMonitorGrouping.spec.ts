import { describe, expect, it } from 'vitest'
import { groupMonitorItems } from '@/utils/channelMonitorGrouping'

function monitor(id: number, provider: string, current_public_rate?: number | null): { id: number; provider: string; current_public_rate?: number | null } {
  return { id, provider, current_public_rate }
}

describe('groupMonitorItems', () => {
  it('groups into the four user-facing categories in their configured order', () => {
    const groups = groupMonitorItems([
      monitor(3, 'anthropic') as never,
      monitor(1, 'openai') as never,
      monitor(2, 'openai') as never,
      monitor(4, 'kimi') as never,
      monitor(5, 'zhipu') as never,
      monitor(6, 'deepseek') as never,
      monitor(7, 'minimax') as never,
      monitor(8, 'grok') as never,
    ])

    expect(groups.map((group) => group.provider)).toEqual(['openai', 'anthropic', '__domestic__', '__other__'])
    expect(groups[0].items.map((item) => item.id)).toEqual([1, 2])
    expect(groups[2].items.map((item) => item.id)).toEqual([4, 5, 6, 7])
    expect(groups[2].items.map((item) => item.provider)).toEqual(['kimi', 'zhipu', 'deepseek', 'minimax'])
  })

  it('places blank and unknown providers in the trailing other group', () => {
    const groups = groupMonitorItems([
      monitor(1, '') as never,
      monitor(2, 'custom-provider') as never,
      monitor(3, 'gemini') as never,
    ])

    expect(groups.map((group) => group.provider)).toEqual(['__other__'])
    expect(groups[0].items.map((item) => item.id)).toEqual([1, 2, 3])
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
