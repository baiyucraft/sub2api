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

  it('uses V1-style segmented windows and the shared accessible Select controls', () => {
    expect(source).toContain('role="tablist"')
    expect(source).toContain('class="window-tab"')
    expect(source).toContain("import Select from '@/components/common/Select.vue'")
    expect(source).toContain(':options="statusOptions"')
  })

  it('surfaces operational signals without mixing them into traffic metrics', () => {
    expect(source).toContain("metrics.balance")
    expect(source).toContain("metrics.openIncidents")
    expect(source).toContain('recent_incidents')
    expect(source).toContain('recent_rate_changes')
    expect(source).toContain('statusPriority')
  })

  it('surfaces channel-level balance warnings and unavailable states', () => {
    expect(source).toContain('summary.balanceLow')
    expect(source).toContain('item.balance_low')
    expect(source).toContain('metrics.balanceLow')
    expect(source).toContain('metrics.balanceThreshold')
    expect(source).toContain('metrics.balanceUnavailable')
  })

  it('supports reversing the actionable status order while pinning disabled and unknown states last', () => {
    expect(source).toContain("type SortDirection = 'asc' | 'desc'")
    expect(source).toContain("const sortDirection = ref<SortDirection>('asc')")
    expect(source).toContain('3 - aBasePriority')
    expect(source).toContain('3 - bBasePriority')
    expect(source).toContain("aPinned = aBasePriority == null || a.overall_status === 'disabled'")
    expect(source).toContain('function toggleSortDirection()')
  })

  it('exposes an accessible sort toggle with directional icons', () => {
    expect(source).toContain('class="sort-toggle"')
    expect(source).toContain(':aria-label="sortDirection === \'asc\'')
    expect(source).toContain(":name=\"sortDirection === 'asc' ? 'arrowUp' : 'arrowDown'\"")
    expect(source).toContain('@click="toggleSortDirection"')
  })
})
