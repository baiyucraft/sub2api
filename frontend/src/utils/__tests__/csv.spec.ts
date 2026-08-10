import { describe, expect, it } from 'vitest'
import { escapeCsvCell } from '../csv'

describe('escapeCsvCell', () => {
  it.each(['=1+1', '+SUM(A1)', '-2+3', '@cmd', '\tformula'])('neutralizes spreadsheet formula prefix: %s', (value) => {
    expect(escapeCsvCell(value)).toBe(`"'${value}"`)
  })

  it('quotes and doubles embedded quotes', () => {
    expect(escapeCsvCell('a"b')).toBe('"a""b"')
  })
})
