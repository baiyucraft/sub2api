import { describe, expect, it } from 'vitest'

import { parseUpstreamTokenPaste } from '../upstreamTokenParser'

const NOW = new Date('2026-07-08T00:00:00Z')
const RAW_REFRESH_TOKEN = `rt_${'a'.repeat(64)}`

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

  it('parses browser localStorage table rows without treating metadata as tokens', () => {
    const jwt = makeJWT(1783598154)
    const refresh = `rt_${'b'.repeat(64)}`
    const result = parseUpstreamTokenPaste([
      `auth_token\t${jwt}`,
      'auth_user\t{"id":27,"email":"owner@example.com","total_recharged":169.169316}',
      'channel-status-auto-refresh\t{"enabled":true,"interval_seconds":60}',
      `refresh_token\t${refresh}`,
      'token_expires_at\t1783598152643',
      'sub2api_login_agreement_consent\t{"revision":"1225230147c6dfe6"}'
    ].join('\n'), NOW)

    expect(result.accessCandidates).toHaveLength(1)
    expect(result.accessCandidates[0]).toMatchObject({
      kind: 'access',
      source: 'field',
      value: jwt,
      label: 'auth_token',
      expiresAt: '2026-07-09T11:55:54.000Z',
      expired: false
    })
    expect(result.refreshCandidates).toHaveLength(1)
    expect(result.refreshCandidates[0]).toMatchObject({
      kind: 'refresh',
      source: 'field',
      value: refresh,
      label: 'refresh_token'
    })
    expect(result.unknownCandidates).toHaveLength(0)
  })

  it('does not parse localStorage rows separated only by spaces', () => {
    const jwt = makeJWT(1783598154)
    const result = parseUpstreamTokenPaste([
      `auth_token ${jwt}`,
      `refresh_token rt_${'c'.repeat(64)}`
    ].join('\n'), NOW)

    expect(result.accessCandidates).toHaveLength(0)
    expect(result.refreshCandidates).toHaveLength(0)
    expect(result.unknownCandidates.map((item) => item.value)).toContain(jwt)
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

  it('parses a bare Sub2API refresh token when pasted alone', () => {
    const result = parseUpstreamTokenPaste(RAW_REFRESH_TOKEN, NOW)

    expect(result.refreshCandidates).toHaveLength(1)
    expect(result.refreshCandidates[0]).toMatchObject({
      kind: 'refresh',
      source: 'raw_refresh',
      value: RAW_REFRESH_TOKEN,
      label: 'Sub2API refresh token'
    })
  })

  it('parses a bare Sub2API refresh token with surrounding whitespace or quotes', () => {
    const result = parseUpstreamTokenPaste(`  "${RAW_REFRESH_TOKEN}"  `, NOW)

    expect(result.refreshCandidates[0]?.value).toBe(RAW_REFRESH_TOKEN)
  })

  it('keeps explicit refresh token fields as the preferred source', () => {
    const result = parseUpstreamTokenPaste(`refresh_token=${RAW_REFRESH_TOKEN}`, NOW)

    expect(result.refreshCandidates).toHaveLength(1)
    expect(result.refreshCandidates[0]).toMatchObject({
      source: 'field',
      value: RAW_REFRESH_TOKEN,
      label: 'refresh_token'
    })
  })

  it.each([
    ['short token', `rt_${'a'.repeat(63)}`],
    ['long token', `rt_${'a'.repeat(65)}`],
    ['non-hex token', `rt_${'g'.repeat(64)}`],
    ['uppercase prefix', `RT_${'a'.repeat(64)}`],
    ['embedded token', `hello ${RAW_REFRESH_TOKEN} world`]
  ])('does not parse invalid bare Sub2API refresh token: %s', (_caseName, value) => {
    const result = parseUpstreamTokenPaste(value, NOW)

    expect(result.refreshCandidates).toHaveLength(0)
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
