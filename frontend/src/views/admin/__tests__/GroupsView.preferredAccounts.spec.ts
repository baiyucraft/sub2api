import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

const currentDir = dirname(fileURLToPath(import.meta.url))
const source = readFileSync(resolve(currentDir, '../GroupsView.vue'), 'utf8')

describe('admin GroupsView preferred account pool', () => {
  it('loads and saves only the selected accounts through the group-scoped API', () => {
    expect(source).toContain('adminAPI.groups.getPreferredAccounts(group.id)')
    expect(source).toContain('adminAPI.groups.updatePreferredAccounts(')
    expect(source).toContain('[...new Set(preferredAccountIDs.value)]')
  })

  it('searches and displays bound account identity and scheduling metadata', () => {
    expect(source).toContain('const filteredPreferredAccounts = computed')
    expect(source).toContain('account.upstream_config_name')
    expect(source).toContain('account.upstream_key_name')
    expect(source).toContain('account.rate_multiplier')
    expect(source).toContain('account.status')
    expect(source).toContain('preferred_account_count')
  })

  it('keeps the required fallback and hard-gate guidance visible', () => {
    expect(source).toContain("admin.groups.preferredAccounts.hint")
    expect(source).toContain("admin.groups.preferredAccounts.disclaimer")
    expect(source).toContain("admin.groups.preferredAccounts.empty")
    expect(source).toContain("admin.groups.preferredAccounts.noMatch")
  })
})
