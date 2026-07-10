export const UPSTREAM_ACCOUNT_NAME_MAX_CODE_POINTS = 100

const CONFIG_NAME_BUDGET = 49
const KEY_NAME_BUDGET = 50
const NAME_SEPARATOR = '-'

export interface LatestRequestTracker {
  begin: () => number
  isCurrent: (requestID: number) => boolean
  invalidate: () => void
}

export function createLatestRequestTracker(): LatestRequestTracker {
  let currentRequestID = 0
  return {
    begin: () => {
      currentRequestID += 1
      return currentRequestID
    },
    isCurrent: (requestID) => requestID === currentRequestID,
    invalidate: () => {
      currentRequestID += 1
    }
  }
}

function isUpstreamNameWhitespace(codePoint: number): boolean {
  return (
    (codePoint >= 0x0009 && codePoint <= 0x000d) ||
    codePoint === 0x0020 ||
    codePoint === 0x0085 ||
    codePoint === 0x00a0 ||
    codePoint === 0x1680 ||
    (codePoint >= 0x2000 && codePoint <= 0x200a) ||
    codePoint === 0x2028 ||
    codePoint === 0x2029 ||
    codePoint === 0x202f ||
    codePoint === 0x205f ||
    codePoint === 0x3000
  )
}

export function trimUpstreamNameWhitespace(value: string): string {
  const codePoints = Array.from(value)
  let start = 0
  let end = codePoints.length

  while (start < end && isUpstreamNameWhitespace(codePoints[start].codePointAt(0) ?? -1)) {
    start += 1
  }
  while (end > start && isUpstreamNameWhitespace(codePoints[end - 1].codePointAt(0) ?? -1)) {
    end -= 1
  }

  return codePoints.slice(start, end).join('')
}

export function buildUpstreamAccountName(configName: string, keyName: string): string {
  const normalizedConfigName = trimUpstreamNameWhitespace(configName)
  const normalizedKeyName = trimUpstreamNameWhitespace(keyName)
  if (!normalizedConfigName || !normalizedKeyName) {
    return ''
  }

  const configCodePoints = Array.from(normalizedConfigName)
  const keyCodePoints = Array.from(normalizedKeyName)
  if (configCodePoints.length + keyCodePoints.length + 1 <= UPSTREAM_ACCOUNT_NAME_MAX_CODE_POINTS) {
    return `${normalizedConfigName}${NAME_SEPARATOR}${normalizedKeyName}`
  }

  let configBudget = CONFIG_NAME_BUDGET
  let keyBudget = KEY_NAME_BUDGET
  if (configCodePoints.length < configBudget) {
    keyBudget += configBudget - configCodePoints.length
    configBudget = configCodePoints.length
  }
  if (keyCodePoints.length < keyBudget) {
    configBudget += keyBudget - keyCodePoints.length
    keyBudget = keyCodePoints.length
  }

  return `${configCodePoints.slice(0, configBudget).join('')}${NAME_SEPARATOR}${keyCodePoints.slice(0, keyBudget).join('')}`
}
