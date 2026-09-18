import type { UpstreamModelCustomRule } from '@/types'
import type { ModelMappingEntry } from '@/composables/useModelWhitelist'
import { splitModelMappingObject } from '@/composables/useModelWhitelist'

function normalizedMapping(value?: Record<string, unknown> | Record<string, string> | null) {
  const result: Record<string, string> = {}
  if (!value) return result
  for (const [rawSource, rawTarget] of Object.entries(value)) {
    if (typeof rawTarget !== 'string') continue
    const source = rawSource.trim()
    const target = rawTarget.trim()
    if (source && target) result[source] = target
  }
  return result
}

export function normalizeUpstreamModelCustomRules(
  rules?: UpstreamModelCustomRule[] | null
): UpstreamModelCustomRule[] {
  const bySource = new Map<string, UpstreamModelCustomRule>()
  for (const rawRule of rules || []) {
    const source = rawRule.source?.trim()
    const action = rawRule.action
    const target = rawRule.target?.trim()
    if (!source || !['allow', 'map', 'deny'].includes(action)) continue
    if (action === 'map') {
      if (!target) continue
      bySource.set(source, { source, action, target })
    } else {
      bySource.set(source, { source, action })
    }
  }
  return [...bySource.values()].sort((a, b) => a.source.localeCompare(b.source))
}

export function buildUpstreamModelEditorState(
  autoMapping: Record<string, string> | undefined,
  rules: UpstreamModelCustomRule[] | undefined,
  currentMapping?: Record<string, unknown>
): { allowedModels: string[]; modelMappings: ModelMappingEntry[] } {
  const automatic = normalizedMapping(autoMapping)
  const display = Object.keys(automatic).length > 0 ? automatic : normalizedMapping(currentMapping)
  for (const rule of normalizeUpstreamModelCustomRules(rules)) {
    if (rule.action === 'deny') {
      delete display[rule.source]
    } else if (rule.action === 'allow') {
      display[rule.source] = rule.source
    } else if (rule.target) {
      display[rule.source] = rule.target
    }
  }
  return splitModelMappingObject(display)
}

export function buildUpstreamModelCustomRules(
  autoMapping: Record<string, string> | undefined,
  allowedModels: string[],
  modelMappings: ModelMappingEntry[]
): UpstreamModelCustomRule[] {
  const automatic = normalizedMapping(autoMapping)
  const desired: Record<string, string> = {}
  for (const rawModel of allowedModels) {
    const model = rawModel.trim()
    if (model) desired[model] = model
  }
  for (const rawMapping of modelMappings) {
    const source = rawMapping.from.trim()
    const target = rawMapping.to.trim()
    if (source && target) desired[source] = target
  }

  const sources = new Set([...Object.keys(automatic), ...Object.keys(desired)])
  const rules: UpstreamModelCustomRule[] = []
  for (const source of [...sources].sort()) {
    const automaticTarget = automatic[source]
    const desiredTarget = desired[source]
    if (automaticTarget !== undefined) {
      if (desiredTarget === undefined) {
        rules.push({ source, action: 'deny' })
      } else if (desiredTarget !== automaticTarget) {
        rules.push({ source, action: 'map', target: desiredTarget })
      }
      continue
    }
    if (desiredTarget === undefined) continue
    rules.push(
      desiredTarget === source
        ? { source, action: 'allow' }
        : { source, action: 'map', target: desiredTarget }
    )
  }
  return rules
}

export function upstreamModelRuleAvailable(
  rule: UpstreamModelCustomRule,
  autoMapping: Record<string, string> | undefined
): boolean {
  if (rule.action === 'deny') return true
  const available = new Set<string>()
  for (const [source, target] of Object.entries(normalizedMapping(autoMapping))) {
    available.add(source)
    available.add(target)
  }
  return available.has(rule.action === 'map' ? rule.target || '' : rule.source)
}
