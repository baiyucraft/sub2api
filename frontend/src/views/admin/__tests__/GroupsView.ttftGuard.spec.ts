import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

const currentDir = dirname(fileURLToPath(import.meta.url))
const source = readFileSync(resolve(currentDir, '../GroupsView.vue'), 'utf8')

describe('GroupsView group TTFT Guard entry', () => {
  it('only exposes the action for OpenAI-capable groups and loads list summaries', () => {
    expect(source).toContain('supportsGroupTTFTGuard(row.platform)')
    expect(source).toContain('platform === "openai" || platform === "composite"')
    expect(source).toContain('adminAPI.groups.listTTFTGuardPolicies')
    expect(source).toContain('ttftGuardPolicySummary(ttftGuardPolicies.get(row.id))')
  })

  it('opens a dedicated modal and updates only the saved group summary', () => {
    expect(source).toContain('GroupTTFTGuardPolicyModal')
    expect(source).toContain('@saved="handleTTFTGuardPolicySaved"')
    expect(source).toContain('next.set(policy.group_id, policy)')
  })
})
