// 分组利润控制表单辅助：百分比 <-> 小数换算与提交前校验。
// 后端按小数存 decimal(10,4)（0.30 = 30%），界面按百分比输入展示；
// 固定 4 位小数精度，避免 0.3 * 100 = 30.000000000000004 之类的浮点尾数回显。

export const profitPercentToDecimal = (
  value: number | string | null | undefined,
): number => {
  const parsed = Number(value);
  if (!Number.isFinite(parsed) || parsed <= 0) {
    return 0;
  }
  return Math.round(parsed * 100) / 10000;
};

export const profitDecimalToPercent = (
  value: number | null | undefined,
): number => {
  const parsed = Number(value);
  if (!Number.isFinite(parsed) || parsed <= 0) {
    return 0;
  }
  return Math.round(parsed * 1e6) / 1e4;
};

export type ProfitControlFormState = {
  platform: string;
  profit_control_enabled: boolean;
  profit_min_margin_percent: number | string | null;
  profit_safety_buffer_percent: number | string | null;
};

export const isProfitControlPlatform = (platform: string): boolean =>
  ["openai", "anthropic", "gemini", "grok", "antigravity"].includes(platform);

// 提交前校验：margin/buffer 各自 ∈ [0,1)，且相加 < 1（否则阈值 <= 0，
// 所有可核价账号都会被排除）。返回 null 表示通过，否则返回错误信息的 i18n key
//（相对 admin.groups.profitControl 前缀）。仅支持平台且开关开启时才校验。
//
// 上界校验必须落在 profitPercentToDecimal 的换算结果上，而不是界面百分比：
// 后端 validProfitControlRatio 校验的是小数 [0,1)，而 99.999% 会被四舍五入
// 进位成 1.0——按百分比判 `< 100` 会让前端放行、后端 400。
export const validateProfitControlFormState = (
  form: ProfitControlFormState,
): string | null => {
  if (!isProfitControlPlatform(form.platform) || !form.profit_control_enabled) {
    return null;
  }
  const marginPercent = Number(form.profit_min_margin_percent || 0);
  const bufferPercent = Number(form.profit_safety_buffer_percent || 0);
  if (!Number.isFinite(marginPercent) || marginPercent < 0) {
    return "marginRangeError";
  }
  if (!Number.isFinite(bufferPercent) || bufferPercent < 0) {
    return "bufferRangeError";
  }
  // 校验实际提交给后端的小数值，边界按构造对齐。
  const margin = profitPercentToDecimal(marginPercent);
  const buffer = profitPercentToDecimal(bufferPercent);
  if (margin >= 1) {
    return "marginRangeError";
  }
  if (buffer >= 1) {
    return "bufferRangeError";
  }
  if (margin + buffer >= 1) {
    return "sumTooHigh";
  }
  return null;
};

export type ProfitControlRatePreviewFields = {
  platform?: string | null;
  subscription_type?: string | null;
  profit_control_enabled?: boolean | null;
  profit_min_margin?: number | string | null;
  profit_safety_buffer?: number | string | null;
  profit_min_margin_percent?: number | string | null;
  profit_safety_buffer_percent?: number | string | null;
  peak_rate_enabled?: boolean | null;
  peak_start?: string | null;
  peak_end?: string | null;
  peak_rate_multiplier?: number | string | null;
};

const parseClockMinutes = (value: string | null | undefined): number | null => {
  const match = /^(\d{2}):(\d{2})$/.exec(String(value ?? ""));
  if (!match) return null;
  const hours = Number(match[1]);
  const minutes = Number(match[2]);
  return hours <= 23 && minutes <= 59 ? hours * 60 + minutes : null;
};

/** Returns the multiplier active at `at`, matching Group.PeakMultiplierAt. */
export const peakMultiplierAt = (
  fields: ProfitControlRatePreviewFields,
  at: Date | number = new Date(),
  timeZone?: string | null,
): number | null => {
  if (!fields.peak_rate_enabled || (fields.subscription_type !== undefined && fields.subscription_type !== "subscription")) return 1;
  const start = parseClockMinutes(fields.peak_start);
  const end = parseClockMinutes(fields.peak_end);
  const multiplier = Number(fields.peak_rate_multiplier);
  if (start === null || end === null || start >= end || !Number.isFinite(multiplier) || multiplier < 0) {
    return null;
  }
  if (!timeZone) return null;
  const date = at instanceof Date ? at : new Date(at);
  if (Number.isNaN(date.getTime())) return null;
  try {
    const parts = new Intl.DateTimeFormat("en-US", {
      timeZone,
      hour: "2-digit",
      minute: "2-digit",
      hourCycle: "h23",
    }).formatToParts(date);
    const hour = Number(parts.find((part) => part.type === "hour")?.value);
    const minute = Number(parts.find((part) => part.type === "minute")?.value);
    if (!Number.isFinite(hour) || !Number.isFinite(minute)) return null;
    return hour * 60 + minute >= start && hour * 60 + minute < end ? multiplier : 1;
  } catch {
    return null;
  }
};

/**
 * Calculates the highest upstream account multiplier admitted by profit control.
 * The result is intentionally truncated (never rounded) to four decimals.
 */
export const calculateProfitControlMaxAccountRate = (
  fields: ProfitControlRatePreviewFields,
  downstreamRate: number | string | null | undefined,
  at: Date | number = new Date(),
  timeZone?: string | null,
): number | null => {
  if (!fields.profit_control_enabled || !isProfitControlPlatform(String(fields.platform ?? ""))) {
    return null;
  }
  const downstream = Number(downstreamRate);
  if (!Number.isFinite(downstream) || downstream < 0) return null;
  const marginPercent = fields.profit_min_margin_percent;
  const bufferPercent = fields.profit_safety_buffer_percent;
  if (marginPercent !== undefined && (!Number.isFinite(Number(marginPercent)) || Number(marginPercent) < 0)) return null;
  if (bufferPercent !== undefined && (!Number.isFinite(Number(bufferPercent)) || Number(bufferPercent) < 0)) return null;
  const margin = marginPercent !== undefined
    ? profitPercentToDecimal(marginPercent)
    : Number(fields.profit_min_margin ?? 0);
  const buffer = bufferPercent !== undefined
    ? profitPercentToDecimal(bufferPercent)
    : Number(fields.profit_safety_buffer ?? 0);
  if (!Number.isFinite(margin) || !Number.isFinite(buffer) || margin < 0 || buffer < 0 || margin + buffer >= 1) {
    return null;
  }
  const peak = peakMultiplierAt(fields, at, timeZone);
  if (peak === null) return null;
  const raw = downstream * peak * (1 - margin - buffer);
  if (!Number.isFinite(raw) || raw < 0) return null;
  return Math.floor((raw + 1e-12) * 10000) / 10000;
};

export const formatProfitControlMaxAccountRate = (value: number | null | undefined): string =>
  value === null || value === undefined || !Number.isFinite(value) ? "-" : value.toFixed(4);
