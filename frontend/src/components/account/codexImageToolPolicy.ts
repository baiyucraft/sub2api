import type { UpstreamImagePricing } from '@/types'

export type CodexImageToolMode = 'auto' | 'inherit' | 'enabled' | 'disabled' | 'block'
export type UpstreamImagePermission = 'allowed' | 'denied' | 'unknown'

function nestedOpenAIExtra(extra: Record<string, unknown> | undefined): Record<string, unknown> | undefined {
  const nested = extra?.openai
  return nested && typeof nested === 'object' && !Array.isArray(nested) ? nested as Record<string, unknown> : undefined
}

export function readCodexImageToolMode(extra: Record<string, unknown> | undefined, upstreamBound: boolean): CodexImageToolMode {
  const nested = nestedOpenAIExtra(extra)
  const explicitPolicy = typeof extra?.codex_image_generation_explicit_tool_policy === 'string'
    ? extra.codex_image_generation_explicit_tool_policy
    : nested?.codex_image_generation_explicit_tool_policy
  if (typeof explicitPolicy === 'string' && ['strip', 'remove', 'drop'].includes(explicitPolicy.trim().toLowerCase())) {
    return 'block'
  }
  const bridge = [extra?.codex_image_generation_bridge, extra?.codex_image_generation_bridge_enabled,
    nested?.codex_image_generation_bridge, nested?.codex_image_generation_bridge_enabled].find(value => typeof value === 'boolean')
  if (bridge === true) return 'enabled'
  if (bridge === false) return 'disabled'
  if (typeof explicitPolicy === 'string') return 'inherit'
  return upstreamBound ? 'auto' : 'inherit'
}

export function upstreamImagePermission(snapshot: UpstreamImagePricing | undefined, staleAfterSeconds = 86400, now = Date.now()): UpstreamImagePermission {
  if (!snapshot || !['available', 'partial', 'disabled'].includes(snapshot.status)) return 'unknown'
  if (snapshot.supported === false) return 'denied'
  if (snapshot.supported !== true || snapshot.stale || !snapshot.observed_at) return 'unknown'
  const observedAt = Date.parse(snapshot.observed_at)
  const maxAge = staleAfterSeconds > 0 ? staleAfterSeconds : 86400
  if (!Number.isFinite(observedAt) || now - observedAt > maxAge * 1000 || observedAt > now + 60000) return 'unknown'
  return 'allowed'
}

export function applyCodexImageToolMode(extra: Record<string, unknown>, mode: CodexImageToolMode, upstreamBound: boolean): void {
  const nested = nestedOpenAIExtra(extra)
  if (nested) {
    const remaining = { ...nested }
    delete remaining.codex_image_generation_bridge_enabled
    delete remaining.codex_image_generation_bridge
    delete remaining.codex_image_generation_explicit_tool_policy
    extra.openai = remaining
  }
  delete extra.codex_image_generation_bridge_enabled
  delete extra.codex_image_generation_bridge
  delete extra.codex_image_generation_explicit_tool_policy
  if (upstreamBound && mode !== 'auto' && mode !== 'block') extra.codex_image_generation_explicit_tool_policy = 'allow'
  if (mode === 'enabled' || mode === 'disabled') extra.codex_image_generation_bridge = mode === 'enabled'
  if (mode === 'block') extra.codex_image_generation_explicit_tool_policy = 'strip'
}
