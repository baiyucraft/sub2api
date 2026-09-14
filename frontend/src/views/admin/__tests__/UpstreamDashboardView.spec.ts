import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

const source = readFileSync(resolve(dirname(fileURLToPath(import.meta.url)), '../UpstreamDashboardView.vue'), 'utf8')

describe('UpstreamDashboardView contract', () => {
  it('offers the six supported windows and automatic refresh', () => {
    expect(source).toContain("['1h','24h','today','7d','15d','30d']")
    expect(source).toContain('setInterval(load, 60000)')
  })

  it('keeps the summary icon frames and responsive six-item layout consistent', () => {
    expect(source).toMatch(/\.summary-row\s*\{[\s\S]*grid-template-columns:\s*repeat\(6,\s*minmax\(0,\s*1fr\)\)/)
    expect(source).toMatch(/\.summary-icon-rose\s*\{[\s\S]*background:\s*rgb\(255 228 230\);[\s\S]*color:\s*rgb\(225 29 72\)/)
    expect(source).toMatch(/@media \(max-width:\s*1000px\)[\s\S]*\.summary-row\s*\{[\s\S]*grid-template-columns:\s*repeat\(3,\s*minmax\(0,\s*1fr\)\)/)
    expect(source).toMatch(/@media \(max-width:\s*760px\)[\s\S]*\.summary-row\s*\{[\s\S]*grid-template-columns:\s*repeat\(2,\s*minmax\(0,\s*1fr\)\)/)
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
    expect(source).toContain("metrics.windowCost")
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
    expect(source).toContain("const sortDirection = ref<SortDirection>('desc')")
    expect(source).toContain('3 - aBasePriority')
    expect(source).toContain('3 - bBasePriority')
    expect(source).toContain("aPinned = aBasePriority == null || a.overall_status === 'disabled'")
    expect(source).toContain('function toggleSortDirection()')
  })

  it('defaults to window cost descending and puts operational status first', () => {
    expect(source).toContain("const sortField = ref<SortField>('upstream_cost')")
    expect(source).toContain("const statusPriority: Record<string, number> = { operational: 0, degraded: 1, critical: 2, data_insufficient: 3, disabled: 4 }")
    expect(source).toContain("else if (sortField.value === 'status') sortDirection.value = 'asc'")
  })

  it('supports sorting by status, success rate, and window upstream cost', () => {
    expect(source).toContain("type SortField = 'status' | 'success_rate' | 'upstream_cost'")
    expect(source).toContain("sortByWindowCost")
    expect(source).toContain("sortField.value === 'upstream_cost'")
    expect(source).toContain('aCost == null && bCost != null')
    expect(source).toContain("sortDirection.value = 'desc'")
  })

  it('exposes an accessible sort toggle with directional icons', () => {
    expect(source).toContain('class="sort-toggle"')
    expect(source).toContain(':aria-label="sortDirection === \'asc\'')
    expect(source).toContain(":name=\"sortDirection === 'asc' ? 'arrowUp' : 'arrowDown'\"")
    expect(source).toContain('@click="toggleSortDirection"')
  })

  it('keeps high-frequency decision metrics on cards and moves trends to detail', () => {
    expect(source).toContain('class="card-core-metrics"')
    expect(source).toContain('class="window-cost-metric"')
    expect(source).toContain('class="request-metric"')
    expect(source).toContain('class="latency-metrics"')
    expect(source).not.toContain('class="card-trend"')
    expect(source).not.toContain('<TrendBars :points="item.trend" />')
    expect(source).toContain('<TrendBars v-if="detail.trend?.length" :points="detail.trend" compact />')
  })

  it('uses neutral cards with a status rail and stable numeric layout', () => {
    expect(source).toContain('.dashboard-card::before')
    expect(source).toContain('.dashboard-card.status-operational::before')
    expect(source).toContain('font-variant-numeric: tabular-nums')
    expect(source).toContain('.dashboard-card.status-operational, .dashboard-card.status-degraded, .dashboard-card.status-critical { border-color: rgb(226 232 240); background: white; }')
  })

  it('styles positive, negative, and unavailable profit values distinctly', () => {
    expect(source).toContain('const profitClass = (value: number | null | undefined)')
    expect(source).toContain('profit-value-negative')
    expect(source).toContain('class="detail-big-value" :class="profitClass(detail.estimated_gross_profit)"')
  })

  it('keeps render-function trend bars styled outside scoped CSS', () => {
    expect(source).toContain(':global(.trend-bars)')
    expect(source).toContain(':global(.trend-bar)')
  })

  it('keeps detail sections numbered in sequence', () => {
    expect([...source.matchAll(/section-number">(\d+)<\/span>/g)].map(match => match[1])).toEqual(['00', '01', '02', '03', '04', '05', '06'])
  })

  it('uses the shared BaseDialog for upstream details instead of a side drawer', () => {
    expect(source).toContain("import BaseDialog from '@/components/common/BaseDialog.vue'")
    expect(source).toContain('<BaseDialog :show="selected !== null" :title="selected?.name || \'\'" width="wide" :close-on-click-outside="true" @close="closeDetail">')
    expect(source).toContain('class="detail-context"')
    expect(source).not.toContain('detail-drawer')
    expect(source).not.toContain('slide-enter')
    expect(source).not.toContain('onKeydown')
    expect(source).not.toContain('closeButtonRef')
  })

  it('keeps the modal detail hierarchy compact and responsive', () => {
    expect(source).toContain('class="detail-cost-label"')
    expect(source).toContain('class="detail-cost-value"')
    expect(source).toContain('class="detail-inline-metrics"')
    expect(source).toContain('class="detail-secondary-grid"')
    expect(source).toMatch(/\.detail-kpis\s*\{[\s\S]*grid-template-columns:\s*repeat\(4,\s*minmax\(0,\s*1fr\)/)
    expect(source).toMatch(/\.detail-empty\s*\{[\s\S]*border:\s*1px dashed/)
    expect(source).toMatch(/@media \(max-width:\s*760px\)[\s\S]*\.detail-kpis\s*\{[\s\S]*grid-template-columns:\s*repeat\(2,\s*minmax\(0,\s*1fr\)/)
    expect(source).toMatch(/@media \(max-width:\s*560px\)[\s\S]*\.detail-secondary-grid\s*\{\s*grid-template-columns:\s*1fr/)
  })
})
