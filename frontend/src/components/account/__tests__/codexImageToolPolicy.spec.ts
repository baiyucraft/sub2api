import { describe, expect, it } from 'vitest'
import type { UpstreamImagePricing } from '@/types'
import { applyCodexImageToolMode, readCodexImageToolMode, upstreamImagePermission } from '../codexImageToolPolicy'

describe('Codex image tool policy', () => {
  it('defaults only bound accounts without an override to auto', () => {
    expect(readCodexImageToolMode(undefined, true)).toBe('auto')
    expect(readCodexImageToolMode({}, false)).toBe('inherit')
    expect(readCodexImageToolMode({ codex_image_generation_explicit_tool_policy: 'allow' }, true)).toBe('inherit')
    expect(readCodexImageToolMode({ codex_image_generation_bridge: false }, true)).toBe('disabled')
    expect(readCodexImageToolMode({ codex_image_generation_bridge_enabled: true }, true)).toBe('enabled')
    expect(readCodexImageToolMode({ codex_image_generation_explicit_tool_policy: ' DROP ' }, true)).toBe('block')
  })

  it('reads nested legacy overrides with the same independent precedence as the backend', () => {
    expect(readCodexImageToolMode({ openai: { codex_image_generation_explicit_tool_policy: 'strip' } }, true)).toBe('block')
    expect(readCodexImageToolMode({ openai: { codex_image_generation_bridge_enabled: false } }, true)).toBe('disabled')
    expect(readCodexImageToolMode({ codex_image_generation_bridge: true, openai: { codex_image_generation_explicit_tool_policy: 'strip' } }, true)).toBe('block')
    expect(readCodexImageToolMode({ codex_image_generation_explicit_tool_policy: 'allow', openai: { codex_image_generation_explicit_tool_policy: 'strip' } }, true)).toBe('inherit')
  })

  it('removes nested policy keys without mutating unrelated nested data or the original object', () => {
    const nested = { codex_image_generation_bridge: false, codex_image_generation_explicit_tool_policy: 'strip', other: { status: 'available' } }
    const extra = { openai: nested }
    applyCodexImageToolMode(extra, 'auto', false)
    expect(extra.openai).toEqual({ other: nested.other })
    expect(nested.codex_image_generation_explicit_tool_policy).toBe('strip')
    expect(readCodexImageToolMode(extra, false)).toBe('inherit')
  })

  it.each([
    [undefined, 'unknown'],
    [{ status: 'available', supported: true, stale: false, observed_at: '2026-10-05T00:00:00Z' }, 'allowed'],
    [{ status: 'partial', supported: true, stale: false, observed_at: '2026-10-05T00:00:00Z' }, 'allowed'],
    [{ status: 'disabled', supported: false, stale: false }, 'denied'],
    [{ status: 'available', supported: false, stale: false }, 'denied'],
    [{ status: 'unavailable', supported: false, stale: false }, 'unknown'],
    [{ status: 'available', supported: true, stale: true }, 'unknown'],
    [{ status: 'disabled', supported: false, stale: true }, 'denied'],
    [{ status: 'future', supported: true, stale: false }, 'unknown'],
    [{ status: 'partial', stale: false }, 'unknown']
  ])('classifies capability independently from price completeness: %j', (snapshot, expected) => {
    expect(upstreamImagePermission(snapshot as UpstreamImagePricing | undefined, 86400, Date.parse('2026-10-05T12:00:00Z'))).toBe(expected)
  })

  it('ages only positive snapshots using the group limit, with a 24h fallback', () => {
    const snapshot = { status: 'partial', supported: true, stale: false, observed_at: '2026-10-05T00:00:00Z' } as UpstreamImagePricing
    const now = Date.parse('2026-10-05T12:00:00Z')
    expect(upstreamImagePermission(snapshot, 3600, now)).toBe('unknown')
    expect(upstreamImagePermission(snapshot, 0, now)).toBe('allowed')
    expect(upstreamImagePermission(snapshot, 86400, now + 86400000)).toBe('unknown')
    expect(upstreamImagePermission({ ...snapshot, supported: false }, 3600, now + 86400000)).toBe('denied')
    expect(upstreamImagePermission({ ...snapshot, observed_at: '2026-10-06T00:00:00Z' }, 86400, now)).toBe('unknown')
  })

  it('restores automatic following without copying its result into manual overrides or changing the snapshot', () => {
    const snapshot = { status: 'disabled', allow_image_generation: false, observed_at: '2026-10-05T00:00:00Z' }
    const extra: Record<string, unknown> = {
      codex_image_generation_bridge: true,
      codex_image_generation_bridge_enabled: true,
      codex_image_generation_explicit_tool_policy: 'strip',
      sub2api_image_pricing_snapshot: snapshot
    }
    applyCodexImageToolMode(extra, 'auto', true)
    expect(extra).toEqual({ sub2api_image_pricing_snapshot: snapshot })
    expect(extra.sub2api_image_pricing_snapshot).toBe(snapshot)
    expect(readCodexImageToolMode(extra, true)).toBe('auto')
  })
})
