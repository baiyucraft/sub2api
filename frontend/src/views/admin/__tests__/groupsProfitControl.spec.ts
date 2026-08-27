import { describe, expect, it } from "vitest";

import {
  profitDecimalToPercent,
  profitPercentToDecimal,
  validateProfitControlFormState,
  calculateProfitControlMaxAccountRate,
  formatProfitControlMaxAccountRate,
  type ProfitControlFormState,
} from "../groupsProfitControl";

const formState = (
  overrides: Partial<ProfitControlFormState> = {},
): ProfitControlFormState => ({
  platform: "openai",
  profit_control_enabled: true,
  profit_min_margin_percent: 30,
  profit_safety_buffer_percent: 0,
  ...overrides,
});

describe("profitPercentToDecimal", () => {
  it("converts percent input to backend decimal", () => {
    expect(profitPercentToDecimal(30)).toBe(0.3);
    expect(profitPercentToDecimal(5)).toBe(0.05);
    expect(profitPercentToDecimal(33.33)).toBe(0.3333);
    expect(profitPercentToDecimal(99.99)).toBe(0.9999);
  });

  it("rounds to four decimal places matching decimal(10,4) storage", () => {
    expect(profitPercentToDecimal(33.333)).toBe(0.3333);
    expect(profitPercentToDecimal(0.005)).toBe(0.0001);
  });

  it("treats empty, invalid and non-positive input as zero", () => {
    expect(profitPercentToDecimal("")).toBe(0);
    expect(profitPercentToDecimal(null)).toBe(0);
    expect(profitPercentToDecimal(undefined)).toBe(0);
    expect(profitPercentToDecimal("abc")).toBe(0);
    expect(profitPercentToDecimal(-5)).toBe(0);
    expect(profitPercentToDecimal(0)).toBe(0);
  });
});

describe("profitDecimalToPercent", () => {
  it("converts backend decimal to percent without float tail noise", () => {
    expect(profitDecimalToPercent(0.3)).toBe(30);
    expect(profitDecimalToPercent(0.05)).toBe(5);
    expect(profitDecimalToPercent(0.3333)).toBe(33.33);
    expect(profitDecimalToPercent(0.9999)).toBe(99.99);
  });

  it("treats missing and non-positive values as zero", () => {
    expect(profitDecimalToPercent(null)).toBe(0);
    expect(profitDecimalToPercent(undefined)).toBe(0);
    expect(profitDecimalToPercent(0)).toBe(0);
    expect(profitDecimalToPercent(-0.3)).toBe(0);
  });

  it("round-trips representative storage values", () => {
    for (const decimal of [0.05, 0.1, 0.3, 0.3333, 0.5, 0.75, 0.9999]) {
      expect(profitPercentToDecimal(profitDecimalToPercent(decimal))).toBe(
        decimal,
      );
    }
  });
});

describe("validateProfitControlFormState", () => {
  it("passes valid enabled configurations", () => {
    expect(validateProfitControlFormState(formState())).toBeNull();
    expect(
      validateProfitControlFormState(
        formState({
          profit_min_margin_percent: 0,
          profit_safety_buffer_percent: 0,
        }),
      ),
    ).toBeNull();
    expect(
      validateProfitControlFormState(
        formState({
          profit_min_margin_percent: 60,
          profit_safety_buffer_percent: 39.99,
        }),
      ),
    ).toBeNull();
  });

  it("validates all five supported platforms and skips unsupported platforms", () => {
    expect(
      validateProfitControlFormState(
        formState({ profit_control_enabled: false, profit_min_margin_percent: 200 }),
      ),
    ).toBeNull();
    expect(
      validateProfitControlFormState(
        formState({ platform: "anthropic", profit_min_margin_percent: 200 }),
      ),
    ).toBe("marginRangeError");
    for (const platform of ["openai", "anthropic", "gemini", "grok", "antigravity"]) {
      expect(validateProfitControlFormState(formState({ platform }))).toBeNull();
    }
    expect(
      validateProfitControlFormState(
        formState({ platform: "composite", profit_min_margin_percent: 200 }),
      ),
    ).toBeNull();
  });

  it("treats empty inputs as zero", () => {
    expect(
      validateProfitControlFormState(
        formState({
          profit_min_margin_percent: "",
          profit_safety_buffer_percent: null,
        }),
      ),
    ).toBeNull();
  });

  it("rejects out-of-range margin and buffer", () => {
    expect(
      validateProfitControlFormState(
        formState({ profit_min_margin_percent: 100 }),
      ),
    ).toBe("marginRangeError");
    expect(
      validateProfitControlFormState(
        formState({ profit_min_margin_percent: -1 }),
      ),
    ).toBe("marginRangeError");
    expect(
      validateProfitControlFormState(
        formState({ profit_safety_buffer_percent: 100 }),
      ),
    ).toBe("bufferRangeError");
    expect(
      validateProfitControlFormState(
        formState({ profit_safety_buffer_percent: -0.1 }),
      ),
    ).toBe("bufferRangeError");
  });

  it("rejects margin plus buffer reaching 100 percent", () => {
    expect(
      validateProfitControlFormState(
        formState({
          profit_min_margin_percent: 60,
          profit_safety_buffer_percent: 40,
        }),
      ),
    ).toBe("sumTooHigh");
  });

  // 上界按换算后的小数判定：99.999% 会被 profitPercentToDecimal 四舍五入
  // 进位成 1.0，后端 validProfitControlRatio 要求严格 < 1，前端必须先拦下。
  it("rejects percents that round up to a decimal the backend rejects", () => {
    expect(profitPercentToDecimal(99.999)).toBe(1);
    expect(
      validateProfitControlFormState(
        formState({ profit_min_margin_percent: 99.999 }),
      ),
    ).toBe("marginRangeError");
    expect(
      validateProfitControlFormState(
        formState({
          profit_min_margin_percent: 0,
          profit_safety_buffer_percent: 99.999,
        }),
      ),
    ).toBe("bufferRangeError");
  });

  it("still accepts the largest percent that survives the rounding", () => {
    expect(profitPercentToDecimal(99.99)).toBe(0.9999);
    expect(
      validateProfitControlFormState(
        formState({ profit_min_margin_percent: 99.99 }),
      ),
    ).toBeNull();
  });
});

describe("calculateProfitControlMaxAccountRate", () => {
  const fields = {
    platform: "openai",
    profit_control_enabled: true,
    profit_min_margin: 0.05,
    profit_safety_buffer: 0.0389,
    peak_rate_enabled: false,
  };

  it("uses the supplied downstream rate and truncates to four decimals", () => {
    expect(calculateProfitControlMaxAccountRate(fields, 0.1, new Date("2026-08-27T00:00:00Z"))).toBe(0.0911);
    expect(formatProfitControlMaxAccountRate(0.0911)).toBe("0.0911");
  });

  it("applies the current peak multiplier only during the configured subscription window", () => {
    const peakFields = {
      ...fields,
      subscription_type: "subscription",
      peak_rate_enabled: true,
      peak_start: "14:00",
      peak_end: "18:00",
      peak_rate_multiplier: 2,
    };
    expect(calculateProfitControlMaxAccountRate(peakFields, 0.1, new Date("2026-08-27T07:00:00Z"), "Asia/Shanghai")).toBe(0.1822);
    expect(calculateProfitControlMaxAccountRate(peakFields, 0.1, new Date("2026-08-27T03:00:00Z"), "Asia/Shanghai")).toBe(0.0911);
  });

  it("returns null when profit control is disabled or inputs are invalid", () => {
    expect(calculateProfitControlMaxAccountRate({ ...fields, profit_control_enabled: false }, 0.1)).toBeNull();
    expect(calculateProfitControlMaxAccountRate(fields, -1)).toBeNull();
    expect(calculateProfitControlMaxAccountRate({ ...fields, profit_min_margin_percent: "bad" }, 0.1)).toBeNull();
    expect(calculateProfitControlMaxAccountRate({ ...fields, profit_min_margin: 0.8, profit_safety_buffer: 0.2 }, 0.1)).toBeNull();
  });
});
