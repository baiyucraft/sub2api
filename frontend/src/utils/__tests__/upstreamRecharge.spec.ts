import { describe, expect, it } from 'vitest'
import { buildUpstreamRechargeURL } from '../upstreamRecharge'

describe('upstream recharge destinations', () => {
  it.each([
    ['sub2api', '/purchase'], ['newapi', '/console/topup'], ['other', ''], ['unknown', '']
  ])('resolves the %s website destination', (provider, path) => {
    expect(buildUpstreamRechargeURL({ provider, site_url: ' https://upstream.example/saved/path/?token=synthetic#dashboard ' })).toBe(`https://upstream.example/saved/path${path || '/'}`)
  })

  it.each(['', 'not a URL', 'javascript:alert(1)', 'data:text/html,test', 'ftp://upstream.example', 'https://user:password@upstream.example'])('does not create a link for %s', site_url => {
    expect(buildUpstreamRechargeURL({ provider: 'sub2api', site_url })).toBeUndefined()
  })

  it('preserves the configured website host and port', () => {
    expect(buildUpstreamRechargeURL({ provider: 'sub2api', site_url: 'http://upstream.example:8088' })).toBe('http://upstream.example:8088/purchase')
  })
})
