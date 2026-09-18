import { describe, expect, it } from 'vitest'
import {
  buildUpstreamModelCustomRules,
  buildUpstreamModelEditorState,
  upstreamModelRuleAvailable
} from '../upstreamModelCustomRules'

describe('upstreamModelCustomRules', () => {
  it('derives allow, map, and deny rules from the combined editor state', () => {
    const rules = buildUpstreamModelCustomRules(
      { 'gpt-a': 'gpt-a', 'gpt-b': 'gpt-b' },
      ['gpt-b', 'custom-model'],
      [{ from: 'public-a', to: 'gpt-a' }]
    )

    expect(rules).toEqual([
      { source: 'custom-model', action: 'allow' },
      { source: 'gpt-a', action: 'deny' },
      { source: 'public-a', action: 'map', target: 'gpt-a' }
    ])
  })

  it('keeps unavailable custom mappings visible for review and deletion', () => {
    const editor = buildUpstreamModelEditorState(
      { 'gpt-a': 'gpt-a' },
      [{ source: 'waiting', action: 'map', target: 'missing-upstream-model' }]
    )

    expect(editor.allowedModels).toEqual(['gpt-a'])
    expect(editor.modelMappings).toEqual([
      { from: 'waiting', to: 'missing-upstream-model' }
    ])
    expect(
      upstreamModelRuleAvailable(
        { source: 'waiting', action: 'map', target: 'missing-upstream-model' },
        { 'gpt-a': 'gpt-a' }
      )
    ).toBe(false)
  })

  it('restores the automatic model when a deny rule is removed', () => {
    const withDeny = buildUpstreamModelEditorState(
      { 'gpt-a': 'gpt-a' },
      [{ source: 'gpt-a', action: 'deny' }]
    )
    const afterDelete = buildUpstreamModelEditorState({ 'gpt-a': 'gpt-a' }, [])

    expect(withDeny.allowedModels).toEqual([])
    expect(afterDelete.allowedModels).toEqual(['gpt-a'])
  })

  it('removes account-level allow and map rules without retaining hidden editor state', () => {
    const automatic = { 'gpt-a': 'gpt-a' }
    const withRules = buildUpstreamModelEditorState(automatic, [
      { source: 'custom-model', action: 'allow' },
      { source: 'public-model', action: 'map', target: 'gpt-a' }
    ])
    const afterDelete = buildUpstreamModelEditorState(automatic, [])

    expect(withRules.allowedModels).toEqual(['gpt-a', 'custom-model'])
    expect(withRules.modelMappings).toEqual([{ from: 'public-model', to: 'gpt-a' }])
    expect(afterDelete.allowedModels).toEqual(['gpt-a'])
    expect(afterDelete.modelMappings).toEqual([])
    expect(
      buildUpstreamModelCustomRules(
        automatic,
        afterDelete.allowedModels,
        afterDelete.modelMappings
      )
    ).toEqual([])
  })
})
