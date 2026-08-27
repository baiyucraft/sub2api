import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

const source = readFileSync(resolve(dirname(fileURLToPath(import.meta.url)), '../index.ts'), 'utf8')

describe('upstream route split', () => {
  it('registers dashboard, channels and accounts without legacy page routes', () => {
    expect(source).toContain("path: '/admin/upstream/dashboard'")
    expect(source).toContain("path: '/admin/upstream/channels'")
    expect(source).toContain("path: '/admin/upstream/accounts'")
    expect(source).not.toContain("path: '/admin/upstream-configs'")
    expect(source).not.toContain("path: '/admin/upstream-management'")
  })
})
