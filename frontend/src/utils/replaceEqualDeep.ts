import { toRaw } from 'vue'

const rawValue = <T>(value: T): T => value !== null && typeof value === 'object' ? toRaw(value) : value

const isPlainObject = (value: unknown): value is Record<string, unknown> => {
  if (value === null || typeof value !== 'object') return false
  const prototype = Object.getPrototypeOf(value)
  return prototype === Object.prototype || prototype === null
}

/** Share equal JSON subtrees without serializing or mutating either snapshot. */
export function replaceEqualDeep<T>(previous: T, incoming: T): T {
  const before = rawValue(previous)
  const next = rawValue(incoming)
  if (Object.is(before, next)) return before
  if (Array.isArray(before) && Array.isArray(next)) {
    let result: unknown[] | undefined
    for (let index = 0; index < next.length; index += 1) {
      const shared = replaceEqualDeep(before[index], next[index])
      if (!result && (before.length !== next.length || !Object.is(shared, rawValue(before[index])))) result = before.slice(0, index)
      if (result) result.push(shared)
    }
    return (result ?? (before.length === next.length ? before : [])) as T
  }
  if (isPlainObject(before) && isPlainObject(next)) {
    const oldKeys = Object.keys(before)
    const keys = Object.keys(next)
    const sameKeys = oldKeys.length === keys.length && keys.every((key, index) => key === oldKeys[index])
    let result: Record<string, unknown> | undefined = sameKeys ? undefined : { ...next }
    keys.forEach((key, index) => {
      const owns = Object.prototype.hasOwnProperty.call(before, key)
      const shared = owns ? replaceEqualDeep(before[key], next[key]) : next[key]
      if (!result && (!owns || key !== oldKeys[index] || !Object.is(shared, rawValue(before[key])))) result = { ...before }
      // defineProperty also handles a JSON property literally named __proto__.
      if (result) Object.defineProperty(result, key, { value: shared, enumerable: true, writable: true, configurable: true })
    })
    return (result ?? before) as T
  }
  return next
}
