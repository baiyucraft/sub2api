/**
 * Common component types
 */

export interface Column {
  key: string
  label: string
  sortable?: boolean
  class?: string
  /** Stack the label above the value in the mobile card layout. */
  mobileStacked?: boolean
  formatter?: (value: any, row: any) => string
}
