import { describe, expect, it } from 'vitest'
import {
  buildUpstreamAccountName,
  createLatestRequestTracker,
  trimUpstreamNameWhitespace,
  UPSTREAM_ACCOUNT_NAME_MAX_CODE_POINTS
} from '../upstreamAccountName'

describe('upstreamAccountName', () => {
  it('trims the agreed Unicode whitespace set only at the edges', () => {
    const whitespace =
      '\u0009\u000a\u000b\u000c\u000d\u0020\u0085\u00a0\u1680' +
      '\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200a' +
      '\u2028\u2029\u202f\u205f\u3000'

    expect(trimUpstreamNameWhitespace(`${whitespace}配置 Key${whitespace}`)).toBe('配置 Key')
    expect(trimUpstreamNameWhitespace('内\u00a0部')).toBe('内\u00a0部')
    expect(trimUpstreamNameWhitespace('\u200bvalue\u200b')).toBe('\u200bvalue\u200b')
  })

  const cases = [
    ['short', '配置', 'Key', '配置-Key'],
    ['edge whitespace', '\t Config \r', '\u00a0Key\u3000', 'Config-Key'],
    ['internal whitespace', 'Config Name', 'Key\u00a0Name', 'Config Name-Key\u00a0Name'],
    [
      'both long',
      '配'.repeat(60),
      '😀'.repeat(60),
      `${'配'.repeat(49)}-${'😀'.repeat(50)}`
    ],
    ['config long', 'c'.repeat(120), 'key', `${'c'.repeat(96)}-key`],
    ['key long', '配置😀', '🔑'.repeat(120), `配置😀-${'🔑'.repeat(96)}`]
  ] as const

  it.each(cases)('builds %s names identically to the backend', (_name, configName, keyName, expected) => {
    const result = buildUpstreamAccountName(configName, keyName)

    expect(result).toBe(expected)
    expect(Array.from(result)).toHaveLength(Math.min(Array.from(expected).length, UPSTREAM_ACCOUNT_NAME_MAX_CODE_POINTS))
    expect(Array.from(result).length).toBeLessThanOrEqual(UPSTREAM_ACCOUNT_NAME_MAX_CODE_POINTS)
  })

  it('rejects missing source names instead of inventing an id-based label', () => {
    expect(buildUpstreamAccountName('\u3000', 'key')).toBe('')
    expect(buildUpstreamAccountName('config', '\u00a0\u202f')).toBe('')
  })

  it('invalidates stale upstream key requests', () => {
    const tracker = createLatestRequestTracker()
    const first = tracker.begin()
    const second = tracker.begin()

    expect(tracker.isCurrent(first)).toBe(false)
    expect(tracker.isCurrent(second)).toBe(true)

    tracker.invalidate()
    expect(tracker.isCurrent(second)).toBe(false)
  })
})
