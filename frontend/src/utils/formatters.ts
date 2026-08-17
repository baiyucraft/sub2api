/**
 * 格式化缓存 token 数量（1K/1M 缩写）
 */
export function formatCacheTokens(tokens: number): string {
  if (tokens >= 1000000) return `${(tokens / 1000000).toFixed(1)}M`
  if (tokens >= 1000) return `${(tokens / 1000).toFixed(1)}K`
  return tokens.toLocaleString()
}

/**
 * 自适应精度格式化倍率：保留至多 10 位小数并去掉末尾多余的 0，
 * 但至少保留 2 位小数（0.045 -> "0.045"，0.3 -> "0.30"，1 -> "1.00"）。
 * 十进制定点显示与后端 NUMERIC(20,10) 的有效精度一致，避免把原始
 * 上游倍率（例如 0.045）显示或排序前归一成 0.05。
 */
export function formatMultiplier(val: number): string {
  if (!Number.isFinite(val)) return String(val)
  const abs = Math.abs(val)
  if (abs > 0 && abs < 1e-10) return val.toPrecision(10)
  const fixed = val.toFixed(10)
  const [integer, fraction = ''] = fixed.split('.')
  const trimmed = fraction.replace(/0+$/, '')
  return trimmed.length < 2 ? `${integer}.${trimmed.padEnd(2, '0')}` : `${integer}.${trimmed}`
}
