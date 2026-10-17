import { describe, expect, it } from 'vitest'
import { reactive, toRaw } from 'vue'
import { replaceEqualDeep } from '../replaceEqualDeep'

describe('replaceEqualDeep', () => {
  it('preserves equal roots and branches through Vue proxies', () => {
    const previous = reactive({ health: { history: [{ at: 'one', status: 'healthy' }] }, quality: { score: 80 } })
    const next = JSON.parse(JSON.stringify(previous))
    expect(replaceEqualDeep(previous, next)).toBe(toRaw(previous))
    next.quality.score = 81
    const shared = replaceEqualDeep(previous, next)
    expect(shared).not.toBe(toRaw(previous))
    expect(shared.health).toBe(toRaw(previous.health))
    expect(shared.quality.score).toBe(81)
    expect(previous.quality.score).toBe(80)
    expect(next.quality).not.toBe(shared.quality)
  })

  it('detects deleted/new fields, null, undefined and ordered arrays', () => {
    const before = { optional: null, items: [{ id: 1 }, { id: 2 }], health: { state: 'ok' } }
    const after = { items: [{ id: 2 }, { id: 1 }], health: { state: 'ok' }, added: null }
    const shared = replaceEqualDeep<unknown>(before, after) as typeof after
    expect(shared).toEqual(after)
    expect(shared).not.toHaveProperty('optional')
    expect(shared.health).toBe(before.health)
    expect(replaceEqualDeep<unknown>({ value: undefined }, {})).toEqual({})
    expect(replaceEqualDeep([1], [1, 2])).toEqual([1, 2])
    expect(replaceEqualDeep([1, 2], [1])).toEqual([1])
    const ordered = { a: 1, b: 2 }
    const reordered = replaceEqualDeep(ordered, { b: 2, a: 1 })
    expect(reordered).not.toBe(ordered)
    expect(Object.keys(reordered)).toEqual(['b', 'a'])
  })

  it('keeps arbitrary JSON keys safe and leaves both frozen inputs intact', () => {
    const before = Object.freeze({ safe: Object.freeze({ x: 1 }) })
    const next = JSON.parse('{"safe":{"x":1},"__proto__":{"polluted":true}}')
    const shared = replaceEqualDeep<unknown>(before, next) as Record<string, unknown>
    expect(shared.safe).toBe(before.safe)
    expect(Object.prototype.hasOwnProperty.call(shared, '__proto__')).toBe(true)
    expect(Object.getPrototypeOf(shared)).toBe(Object.prototype)
    expect(({} as Record<string, unknown>).polluted).toBeUndefined()
    expect(next).toEqual(JSON.parse('{"safe":{"x":1},"__proto__":{"polluted":true}}'))
  })

  it('preserves primitive identity rules, including NaN and signed zero', () => {
    const previous = { values: [null, undefined, NaN, -0, 'same', false, 0] }
    expect(replaceEqualDeep(previous, { values: [...previous.values] })).toBe(previous)
    const changed = replaceEqualDeep(previous, { values: [null, undefined, NaN, 0, 'same', false, -0] })
    expect(changed).not.toBe(previous)
    expect(Object.is(changed.values[3], 0)).toBe(true)
    expect(Object.is(changed.values[6], -0)).toBe(true)
    expect(Object.is(previous.values[3], -0)).toBe(true)
    expect(replaceEqualDeep(reactive({ value: 1 }), reactive({ value: 2 }))).toEqual({ value: 2 })
  })
})
