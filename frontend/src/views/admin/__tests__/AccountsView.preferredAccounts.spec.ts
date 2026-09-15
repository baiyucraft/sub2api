import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const currentDir = dirname(fileURLToPath(import.meta.url))
const source = readFileSync(resolve(currentDir, '../AccountsView.vue'), 'utf8')

describe('admin AccountsView preferred account wiring', () => {
  it('enables the shared group preferred toggle for ordinary and upstream accounts', () => {
    expect(source).toContain(':interactive="true"')
    expect(source).toContain('@toggle-preferred="handleTogglePreferred(row, $event.groupId, $event.preferred)"')
    expect(source).not.toContain("if (props.scope !== 'upstream') return")
  })

  it('keeps the existing group-scoped preferred account API operations', () => {
    expect(source).toContain('adminAPI.groups.setAccountPreferred(groupID, account.id)')
    expect(source).toContain('adminAPI.groups.clearAccountPreferred(groupID, account.id)')
  })
})
