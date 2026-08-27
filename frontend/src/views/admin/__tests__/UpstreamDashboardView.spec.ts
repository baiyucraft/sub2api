import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

const source = readFileSync(resolve(dirname(fileURLToPath(import.meta.url)), '../UpstreamDashboardView.vue'), 'utf8')

describe('UpstreamDashboardView contract', () => {
  it('offers the five supported windows and automatic refresh', () => {
    expect(source).toContain("['1h','24h','7d','15d','30d']")
    expect(source).toContain('setInterval(load, 60000)')
  })

  it('keeps traffic and probes separate and renders status classes', () => {
    expect(source).toContain("sections.traffic")
    expect(source).toContain("sections.probe")
    expect(source).toContain('statusClass(item.overall_status)')
    expect(source).toContain('estimatedUnavailable')
  })
})
