export type ParsedTokenKind = 'access' | 'refresh' | 'unknown'
export type ParsedTokenSource = 'field' | 'bearer' | 'jwt'

export interface ParsedTokenCandidate {
  id: string
  kind: ParsedTokenKind
  value: string
  source: ParsedTokenSource
  label: string
  expiresAt?: string
  expired?: boolean
}

export interface ParsedUpstreamTokens {
  accessCandidates: ParsedTokenCandidate[]
  refreshCandidates: ParsedTokenCandidate[]
  unknownCandidates: ParsedTokenCandidate[]
}

const TOKEN_VALUE_RE = /^[A-Za-z0-9._~+/=-]{12,}$/
const JWT_RE = /\beyJ[A-Za-z0-9_-]*\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\b/g
const JWT_FULL_RE = /^eyJ[A-Za-z0-9_-]*\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$/
const BEARER_RE = /(?:authorization\s*:\s*)?bearer\s+([A-Za-z0-9._~+/=-]{12,})/gi
const FIELD_RE = /["']?([A-Za-z0-9_./:-]*(?:access_token|refresh_token|sub2api_access_token|sub2api_refresh_token)[A-Za-z0-9_./:-]*)["']?\s*[:=]\s*["']?([A-Za-z0-9._~+/=-]{12,})/gi

const SENSITIVE_FIELD_KEYS = new Set([
  'access_token',
  'refresh_token',
  'sub2api_access_token',
  'sub2api_refresh_token'
])

export function parseUpstreamTokenPaste(input: string, now = new Date()): ParsedUpstreamTokens {
  const candidates: ParsedTokenCandidate[] = []
  const seen = new Set<string>()

  const addCandidate = (
    kind: ParsedTokenKind,
    source: ParsedTokenSource,
    value: string,
    label: string
  ) => {
    const token = normalizeToken(value)
    if (!token || seen.has(token)) return
    seen.add(token)
    const jwtInfo = decodeJwtExpiry(token, now)
    candidates.push({
      id: `${source}-${candidates.length}`,
      kind,
      source,
      value: token,
      label,
      ...jwtInfo
    })
  }

  collectFromJSONLike(input, addCandidate)
  collectFromFields(input, addCandidate)
  collectFromBearer(input, addCandidate)
  collectFromJWT(input, addCandidate)

  return {
    accessCandidates: candidates.filter((item) => item.kind === 'access'),
    refreshCandidates: candidates.filter((item) => item.kind === 'refresh'),
    unknownCandidates: candidates.filter((item) => item.kind === 'unknown')
  }
}

function collectFromFields(
  input: string,
  addCandidate: (kind: ParsedTokenKind, source: ParsedTokenSource, value: string, label: string) => void
) {
  FIELD_RE.lastIndex = 0
  for (const match of input.matchAll(FIELD_RE)) {
    const key = match[1] || ''
    addCandidate(kindFromKey(key), 'field', match[2] || '', fieldLabel(key))
  }
}

function collectFromBearer(
  input: string,
  addCandidate: (kind: ParsedTokenKind, source: ParsedTokenSource, value: string, label: string) => void
) {
  BEARER_RE.lastIndex = 0
  for (const match of input.matchAll(BEARER_RE)) {
    addCandidate('access', 'bearer', match[1] || '', 'Bearer token')
  }
}

function collectFromJWT(
  input: string,
  addCandidate: (kind: ParsedTokenKind, source: ParsedTokenSource, value: string, label: string) => void
) {
  JWT_RE.lastIndex = 0
  for (const match of input.matchAll(JWT_RE)) {
    addCandidate('unknown', 'jwt', match[0], 'JWT')
  }
}

function collectFromJSONLike(
  input: string,
  addCandidate: (kind: ParsedTokenKind, source: ParsedTokenSource, value: string, label: string) => void
) {
  const parsed = tryParseJSON(input)
  if (parsed === undefined) return
  visitJSON(parsed, addCandidate, new Set())
}

function visitJSON(
  value: unknown,
  addCandidate: (kind: ParsedTokenKind, source: ParsedTokenSource, value: string, label: string) => void,
  visited: Set<unknown>
) {
  if (value === null || value === undefined) return
  if (typeof value === 'string') {
    const nested = tryParseJSON(value)
    if (nested !== undefined) visitJSON(nested, addCandidate, visited)
    return
  }
  if (typeof value !== 'object') return
  if (visited.has(value)) return
  visited.add(value)

  if (Array.isArray(value)) {
    value.forEach((item) => visitJSON(item, addCandidate, visited))
    return
  }

  for (const [key, rawValue] of Object.entries(value as Record<string, unknown>)) {
    if (typeof rawValue === 'string' && isSensitiveFieldKey(key)) {
      addCandidate(kindFromKey(key), 'field', rawValue, fieldLabel(key))
    }
    visitJSON(rawValue, addCandidate, visited)
  }
}

function tryParseJSON(input: string): unknown {
  const trimmed = input.trim()
  if (!trimmed || !/^[{[]/.test(trimmed)) return undefined
  try {
    return JSON.parse(trimmed)
  } catch {
    return undefined
  }
}

function normalizeToken(value: string): string {
  const token = value.trim().replace(/^Bearer\s+/i, '').replace(/^["']|["']$/g, '')
  return TOKEN_VALUE_RE.test(token) ? token : ''
}

function isSensitiveFieldKey(key: string): boolean {
  const normalized = normalizeKey(key)
  return SENSITIVE_FIELD_KEYS.has(normalized)
}

function kindFromKey(key: string): ParsedTokenKind {
  const normalized = normalizeKey(key)
  if (normalized.includes('refresh_token')) return 'refresh'
  if (normalized.includes('access_token')) return 'access'
  return 'unknown'
}

function fieldLabel(key: string): string {
  const normalized = normalizeKey(key)
  if (normalized.includes('sub2api_access_token')) return 'sub2api_access_token'
  if (normalized.includes('sub2api_refresh_token')) return 'sub2api_refresh_token'
  if (normalized.includes('access_token')) return 'access_token'
  if (normalized.includes('refresh_token')) return 'refresh_token'
  return key
}

function normalizeKey(key: string): string {
  return key.trim().toLowerCase().replace(/[-.]/g, '_')
}

function decodeJwtExpiry(token: string, now: Date): Pick<ParsedTokenCandidate, 'expiresAt' | 'expired'> {
  if (!JWT_FULL_RE.test(token)) return {}
  const parts = token.split('.')
  if (parts.length !== 3) return {}
  try {
    const payload = JSON.parse(base64UrlDecode(parts[1])) as { exp?: unknown }
    if (typeof payload.exp !== 'number' || !Number.isFinite(payload.exp)) return {}
    const expires = new Date(payload.exp * 1000)
    return {
      expiresAt: expires.toISOString(),
      expired: expires.getTime() <= now.getTime()
    }
  } catch {
    return {}
  }
}

function base64UrlDecode(value: string): string {
  const normalized = value.replace(/-/g, '+').replace(/_/g, '/')
  const padded = normalized.padEnd(Math.ceil(normalized.length / 4) * 4, '=')
  return decodeURIComponent(
    Array.from(atob(padded), (char) => `%${char.charCodeAt(0).toString(16).padStart(2, '0')}`).join('')
  )
}
