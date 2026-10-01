import type { RechargeBonusTier } from '@/types/payment'

export type { RechargeBonusTier }

export const MAX_RECHARGE_BONUS_TIERS = 20
export const MAX_RECHARGE_BONUS_PERCENT = 1000

const AMOUNT_EPSILON = 1e-9

// 后台编辑态：两个值都允许留空（未完成的行在提交时丢弃）。
export interface RechargeBonusTierDraft {
  min_amount: number | null
  bonus_percent: number | null
}

export interface RechargeBonusInterval {
  from: number
  /** null 表示开区间（≥ from） */
  to: number | null
  percent: number
}

export interface RechargeBonusQuote {
  /** 命中档位的百分比；未命中或赠送为 0 时为 0 */
  percent: number
  /** 到账基数（支付金额 × 倍率，USD） */
  base: number
  /** 赠送额度（USD） */
  bonus: number
  /** 到账总额 = base + bonus */
  credited: number
  tier: RechargeBonusTier | null
}

export function roundRechargeAmount(value: number): number {
  if (!Number.isFinite(value)) return 0
  return Math.round((value + Number.EPSILON) * 100) / 100
}

function hasAtMostTwoDecimals(value: number): boolean {
  return Math.abs(roundRechargeAmount(value) - value) < AMOUNT_EPSILON
}

export function isRechargeBonusMinAmountValid(value: unknown): value is number {
  return typeof value === 'number' && Number.isFinite(value) && value >= 0 && hasAtMostTwoDecimals(value)
}

export function isRechargeBonusPercentValid(value: unknown): value is number {
  return (
    typeof value === 'number' &&
    Number.isFinite(value) &&
    value >= 0 &&
    value <= MAX_RECHARGE_BONUS_PERCENT &&
    hasAtMostTwoDecimals(value)
  )
}

function minAmountKey(value: number): string {
  return roundRechargeAmount(value).toFixed(2)
}

function collectTiers(candidates: { min_amount: unknown; bonus_percent: unknown }[]): RechargeBonusTier[] {
  const seen = new Set<string>()
  const out: RechargeBonusTier[] = []
  for (const item of candidates) {
    const minAmount = item.min_amount
    const percent = item.bonus_percent
    if (!isRechargeBonusMinAmountValid(minAmount) || !isRechargeBonusPercentValid(percent)) continue
    const key = minAmountKey(minAmount)
    if (seen.has(key)) continue
    seen.add(key)
    out.push({ min_amount: minAmount, bonus_percent: percent })
  }
  out.sort((a, b) => a.min_amount - b.min_amount)
  return out
}

// 读路径宽松归一（checkout-info / 后台 GET 回填）：非法行丢弃，同阈值保留先出现，按阈值升序。
export function normalizeRechargeBonusTiers(raw: unknown): RechargeBonusTier[] {
  if (!Array.isArray(raw)) return []
  const candidates: { min_amount: unknown; bonus_percent: unknown }[] = []
  for (const item of raw) {
    if (!item || typeof item !== 'object') continue
    const record = item as { min_amount?: unknown; bonus_percent?: unknown }
    candidates.push({
      min_amount: record.min_amount === null || record.min_amount === undefined || record.min_amount === '' ? NaN : Number(record.min_amount),
      bonus_percent: record.bonus_percent === null || record.bonus_percent === undefined || record.bonus_percent === '' ? NaN : Number(record.bonus_percent),
    })
  }
  return collectTiers(candidates)
}

// 提交清洗：留空/非法的行整行丢弃，同阈值保留先出现，按阈值升序。
export function sanitizeRechargeBonusTiersForSubmit(
  tiers: RechargeBonusTierDraft[] | null | undefined,
): RechargeBonusTier[] {
  if (!Array.isArray(tiers)) return []
  return collectTiers(
    tiers.map((tier) => ({
      min_amount: tier?.min_amount === null || tier?.min_amount === undefined ? NaN : Number(tier.min_amount),
      bonus_percent: tier?.bonus_percent === null || tier?.bonus_percent === undefined ? NaN : Number(tier.bonus_percent),
    })),
  )
}

// 编辑器即时校验：同一阈值在其他行已出现时返回 true。
export function isDuplicateRechargeBonusMinAmount(tiers: RechargeBonusTierDraft[], index: number): boolean {
  const current = tiers[index]?.min_amount
  if (!isRechargeBonusMinAmountValid(current)) return false
  const key = minAmountKey(current)
  return tiers.some((tier, i) => i !== index && isRechargeBonusMinAmountValid(tier.min_amount) && minAmountKey(tier.min_amount) === key)
}

// 命中规则：取不超过支付金额的最大阈值档位；与后端 matchRechargeBonusTier 一致。
export function matchRechargeBonusTier(tiers: RechargeBonusTier[], paymentAmount: number): RechargeBonusTier | null {
  if (!Number.isFinite(paymentAmount) || paymentAmount <= 0) return null
  let matched: RechargeBonusTier | null = null
  for (const tier of tiers) {
    if (paymentAmount + AMOUNT_EPSILON < tier.min_amount) continue
    if (!matched || tier.min_amount > matched.min_amount) matched = tier
  }
  return matched
}

// 赠送额度 = 到账基数 × 百分比，保留两位小数；与后端 calculateRechargeBonus 一致。
export function calculateRechargeBonus(baseCredited: number, percent: number): number {
  if (!Number.isFinite(baseCredited) || !Number.isFinite(percent) || baseCredited <= 0 || percent <= 0) return 0
  return roundRechargeAmount((baseCredited * percent) / 100)
}

// 充值页报价：阈值按支付金额比较，赠送按到账基数（支付金额 × 倍率）计算。
export function quoteRechargeBonus(
  tiers: RechargeBonusTier[],
  paymentAmount: number,
  multiplier = 1,
): RechargeBonusQuote {
  const amount = Number.isFinite(paymentAmount) && paymentAmount > 0 ? paymentAmount : 0
  const rate = Number.isFinite(multiplier) && multiplier > 0 ? multiplier : 1
  const base = roundRechargeAmount(amount * rate)
  const tier = matchRechargeBonusTier(tiers, amount)
  const bonus = tier ? calculateRechargeBonus(base, tier.bonus_percent) : 0
  return {
    percent: bonus > 0 && tier ? tier.bonus_percent : 0,
    base,
    bonus,
    credited: roundRechargeAmount(base + bonus),
    tier,
  }
}

// 区间预览：把升序阈值列表展开为 [from, to) 区间；首档阈值 > 0 时补一段「不赠送」。
export function describeRechargeBonusIntervals(tiers: RechargeBonusTier[]): RechargeBonusInterval[] {
  const sorted = [...tiers].sort((a, b) => a.min_amount - b.min_amount)
  const out: RechargeBonusInterval[] = []
  if (sorted.length === 0) return out
  if (sorted[0]!.min_amount > 0) {
    out.push({ from: 0, to: sorted[0]!.min_amount, percent: 0 })
  }
  sorted.forEach((tier, index) => {
    out.push({ from: tier.min_amount, to: sorted[index + 1]?.min_amount ?? null, percent: tier.bonus_percent })
  })
  return out
}

// 展示用：去掉多余的小数 0（20 → "20"，12.5 → "12.5"）。
export function formatRechargeBonusNumber(value: number): string {
  if (!Number.isFinite(value)) return '0'
  return String(Number(value.toFixed(2)))
}
