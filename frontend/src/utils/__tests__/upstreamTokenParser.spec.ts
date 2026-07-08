import { describe, expect, it } from 'vitest'

import { parseUpstreamTokenPaste } from '../upstreamTokenParser'

const NOW = new Date('2026-07-08T00:00:00Z')

function makeJWT(exp: number) {
  const header = base64Url(JSON.stringify({ alg: 'HS256', typ: 'JWT' }))
  const payload = base64Url(JSON.stringify({ exp }))
  return `${header}.${payload}.signature`
}

function base64Url(value: string) {
  return btoa(value).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/g, '')
}

describe('parseUpstreamTokenPaste', () => {
  it('parses explicit access and refresh tokens from JSON', () => {
    const result = parseUpstreamTokenPaste(JSON.stringify({
      access_token: 'access_token_value_123456',
      refresh_token: 'refresh_token_value_123456'
    }), NOW)

    expect(result.accessCandidates).toHaveLength(1)
    expect(result.accessCandidates[0]).toMatchObject({
      kind: 'access',
      source: 'field',
      value: 'access_token_value_123456'
    })
    expect(result.refreshCandidates).toHaveLength(1)
    expect(result.refreshCandidates[0]).toMatchObject({
      kind: 'refresh',
      source: 'field',
      value: 'refresh_token_value_123456'
    })
  })

  it('parses nested localStorage-style JSON strings', () => {
    const result = parseUpstreamTokenPaste(JSON.stringify({
      auth: JSON.stringify({
        state: {
          sub2api_access_token: 'nested_access_token_123456',
          sub2api_refresh_token: 'nested_refresh_token_123456'
        }
      })
    }), NOW)

    expect(result.accessCandidates[0]?.value).toBe('nested_access_token_123456')
    expect(result.refreshCandidates[0]?.value).toBe('nested_refresh_token_123456')
  })

  it('parses Authorization Bearer as an access token', () => {
    const result = parseUpstreamTokenPaste('Authorization: Bearer bearer_token_value_123456', NOW)

    expect(result.accessCandidates).toHaveLength(1)
    expect(result.accessCandidates[0]).toMatchObject({
      kind: 'access',
      source: 'bearer',
      value: 'bearer_token_value_123456'
    })
  })

  it('keeps bare JWTs as unknown candidates and exposes unverified expiry', () => {
    const jwt = makeJWT(1783497600)
    const result = parseUpstreamTokenPaste(jwt, NOW)

    expect(result.unknownCandidates).toHaveLength(1)
    expect(result.unknownCandidates[0]).toMatchObject({
      kind: 'unknown',
      source: 'jwt',
      value: jwt,
      expiresAt: '2026-07-08T08:00:00.000Z',
      expired: false
    })
  })

  it('marks expired JWTs without rejecting refresh tokens', () => {
    const expiredJWT = makeJWT(1704067200)
    const result = parseUpstreamTokenPaste(JSON.stringify({
      access_token: expiredJWT,
      refresh_token: 'still_valid_refresh_token_123456'
    }), NOW)

    expect(result.accessCandidates[0]?.expired).toBe(true)
    expect(result.refreshCandidates[0]?.value).toBe('still_valid_refresh_token_123456')
  })

  it('keeps multiple explicit candidates for user selection', () => {
    const result = parseUpstreamTokenPaste([
      'access_token=first_access_token_123456',
      'access_token=second_access_token_123456'
    ].join('\n'), NOW)

    expect(result.accessCandidates.map((item) => item.value)).toEqual([
      'first_access_token_123456',
      'second_access_token_123456'
    ])
  })

  it('does not leak or return unrecognized paste content', () => {
    const result = parseUpstreamTokenPaste('nothing useful here', NOW)

    expect(result.accessCandidates).toHaveLength(0)
    expect(result.refreshCandidates).toHaveLength(0)
    expect(result.unknownCandidates).toHaveLength(0)
  })
})
