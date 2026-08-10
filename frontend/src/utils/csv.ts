export function escapeCsvCell(value: unknown): string {
  let text = value == null ? '' : String(value)
  // Prevent spreadsheet formula execution when an imported account name or
  // diagnostic message begins with a formula sigil.
  if (typeof value === 'string' && /^[=+\-@\t\r]/.test(text)) text = `'${text}`
  return `"${text.replace(/"/g, '""')}"`
}
