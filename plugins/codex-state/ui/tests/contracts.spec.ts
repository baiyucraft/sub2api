import { describe, expect, it } from 'vitest'
import { diagnosticCodes, models, normalizeConfig, normalizeResources, normalizeSecretFlags, normalizeStatus } from '../src/contracts'
import { en, zh } from '../src/i18n'

describe('Codex STATE contracts', () => {
  it('discards proxy values even from a stale host and accepts only boolean secret flags', () => {
    const config = normalizeConfig({ version: 1, harvest_proxy_url: 'SECRET', dial_proxy_url: 'PRIVATE', _host_secrets: { harvest_proxy_url: true } })
    expect(config.harvest_proxy_url).toBe('')
    expect(config.dial_proxy_url).toBe('')
    expect(config).not.toHaveProperty('_host_secrets')
    expect(JSON.stringify(config)).not.toMatch(/SECRET|PRIVATE/)
    expect(normalizeSecretFlags({ harvest_proxy_url: true, dial_proxy_url: 'PRIVATE', other: true })).toEqual({ harvest_proxy_url: true, dial_proxy_url: false })
  })
  it('starts with every switch off and serializes only the fixed configuration schema', () => {
    expect(normalizeConfig({})).toEqual({ version: 1, enabled: false, harvest_proxy_url: '', dial_proxy_url: '', accounts: [] })
    const config = normalizeConfig({ accounts: [{ account_id: 123, models: {} }], access_token: 'SECRET', extra: {} })
    expect(config.accounts[0].models).toEqual(Object.fromEntries(models.map(m => [m, { enabled: false, ticket_plan: 'pro' }])))
    expect(JSON.stringify(config)).not.toContain('SECRET')
  })

  it('reads only eligible resource metadata and discards ordinary account extra and credentials', () => {
    const result = normalizeResources({ accounts: [
      { id: 1, name: 'OAuth', platform: 'openai', account_type: 'oauth', group_ids: [9], business_egress_configured: true, extra: { ticket: 'SECRET' }, credentials: 'SECRET' },
      { id: 2, name: 'Key', platform: 'openai', account_type: 'apikey' },
      { id: 3, platform: 'grok', account_type: 'oauth' },
      { id: 4, name: 'Session', platform: 'openai', account_type: 'setup-token' },
    ], groups: [{ id: 9, name: 'G', access_token: 'SECRET' }], proxies: [{ password: 'SECRET' }] })
    expect(result.accounts.map(a => a.id)).toEqual([1, 4])
    expect(result.groups).toEqual([{ id: 9, name: 'G' }])
    expect(JSON.stringify(result)).not.toContain('SECRET')
    expect(JSON.stringify(result)).not.toContain('credentials')
  })

  it('projects exact engine slots, cooldown and allowlisted diagnostic codes', () => {
    const status = normalizeStatus({ healthy: true, status_json: JSON.stringify({ running: true,
      accounts: [{ account_id: 123, models: { 'gpt-6-astra': {
        state: 'cooldown', strikes: 2, attempts: 5, refreshing: false, cooldown_until: '2026-09-20T15:00:00Z',
        active: { expires_at: '2026-09-20T16:00:00Z', remaining_seconds: 3600, usable: false, state: 'SECRET', fingerprint: 'SECRET' },
        ready: { expires_at: '2026-09-20T17:00:00Z', remaining_seconds: 7200, usable: true },
        last_error: 'watchdog_persistence_failed',
      } } }], logs: [{ at: '2026-09-20T14:00:00Z', account_id: 123, model: 'gpt-6-astra', code: 'harvest_persisted', raw: 'SECRET' }],
    }) })
    expect(status.running).toBe(true)
    expect(status.accounts[0].models['gpt-6-astra']).toMatchObject({ state: 'cooldown', strikes: 2, attempts: 5, active: { usable: false }, ready: { usable: true }, last_error: 'watchdog_persistence_failed' })
    expect(status.logs[0].message).toBe('harvest_persisted')
    expect(JSON.stringify(status)).not.toContain('SECRET')
  })

  it('does not retain raw errors, ticket envelopes, URLs or HTTP headers in status or logs', () => {
    const secret = 'Authorization: Bearer SECRET http://user:SECRET@pool.example STATE=SECRET'
    const status = normalizeStatus({ healthy: true, message: secret, status_json: JSON.stringify({
      running: true, accounts: [{ account_id: 1, models: { 'gpt-5.6-sol': { state: secret, last_error: secret, active: secret } } }],
      logs: [{ at: secret, code: secret, message: secret, model: secret, headers: secret }],
    }) })
    expect(status.accounts[0].models['gpt-5.6-sol'].last_error).toBe('redacted')
    expect(status.logs[0].message).toBe('redacted')
    expect(JSON.stringify(status)).not.toContain('SECRET')
    expect(normalizeStatus({ healthy: true, status_json: secret }).running).toBe(false)
    expect(normalizeStatus({ healthy: true, status_json: '{"running":false}' }).running).toBe(false)
  })

  it('keeps Chinese and English aligned including all runtime diagnostic keys', () => {
    expect(Object.keys(en).sort()).toEqual(Object.keys(zh).sort())
    for (const key of diagnosticCodes) { expect(en[key]).toBeTruthy(); expect(zh[key]).toBeTruthy() }
  })
})
