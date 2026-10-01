package service

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"unicode/utf8"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/shopspring/decimal"
)

// 充值赠送阶梯：余额充值订单按用户输入的支付金额命中阶梯（取不超过该金额的最大 MinAmount），
// 在到账基数（输入金额 × 充值倍率）之上额外赠送 BonusPercent% 的 USD 余额。
// 订阅订单不参与。赠送在下单时按当时配置计算并落到 payment_orders.bonus_amount，后续改配置不影响已建订单。
const (
	// SettingRechargeBonusTiers 存 JSON 数组（RechargeBonusTier 列表），空/缺失表示未启用赠送。
	SettingRechargeBonusTiers = "RECHARGE_BONUS_TIERS"
	// SettingRechargeBonusNotice 充值页金额卡顶部展示的 Markdown 活动文案，空表示不展示。
	SettingRechargeBonusNotice = "RECHARGE_BONUS_NOTICE"
)

const (
	maxRechargeBonusTiers       = 20
	maxRechargeBonusPercent     = 1000
	maxRechargeBonusNoticeRunes = 10000
	rechargeBonusAmountEpsilon  = 1e-9
)

// RechargeBonusTier 一个赠送档位：支付金额 ≥ MinAmount 时赠送到账基数的 BonusPercent%。
type RechargeBonusTier struct {
	MinAmount    float64 `json:"min_amount"`
	BonusPercent float64 `json:"bonus_percent"`
}

func rechargeBonusValueValid(v float64, max float64) bool {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > max {
		return false
	}
	d := decimal.NewFromFloat(v)
	return d.Equal(d.Round(2))
}

// NormalizeRechargeBonusTiers 严格归一化（写路径）：任何非法项直接报错；
// 成功时返回按 MinAmount 升序排序的副本。
func NormalizeRechargeBonusTiers(raw []RechargeBonusTier) ([]RechargeBonusTier, error) {
	if len(raw) == 0 {
		return []RechargeBonusTier{}, nil
	}
	if len(raw) > maxRechargeBonusTiers {
		return nil, fmt.Errorf("recharge bonus tiers exceed limit of %d", maxRechargeBonusTiers)
	}
	out := make([]RechargeBonusTier, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, tier := range raw {
		if !rechargeBonusValueValid(tier.MinAmount, math.MaxFloat64) {
			return nil, fmt.Errorf("recharge bonus tier min amount must be a non-negative number with at most 2 decimals")
		}
		if !rechargeBonusValueValid(tier.BonusPercent, maxRechargeBonusPercent) {
			return nil, fmt.Errorf("recharge bonus tier percent must be between 0 and %d with at most 2 decimals", maxRechargeBonusPercent)
		}
		key := decimal.NewFromFloat(tier.MinAmount).Round(2).String()
		if _, dup := seen[key]; dup {
			return nil, fmt.Errorf("duplicate recharge bonus tier min amount: %s", key)
		}
		seen[key] = struct{}{}
		out = append(out, RechargeBonusTier{MinAmount: tier.MinAmount, BonusPercent: tier.BonusPercent})
	}
	sortRechargeBonusTiers(out)
	return out, nil
}

func sortRechargeBonusTiers(tiers []RechargeBonusTier) {
	sort.SliceStable(tiers, func(i, j int) bool {
		return tiers[i].MinAmount < tiers[j].MinAmount
	})
}

// encodeRechargeBonusTiers 序列化为设置值；空列表存空串，与「未配置」保持同一形态。
func encodeRechargeBonusTiers(tiers []RechargeBonusTier) (string, error) {
	if len(tiers) == 0 {
		return "", nil
	}
	raw, err := json.Marshal(tiers)
	if err != nil {
		return "", fmt.Errorf("marshal recharge bonus tiers: %w", err)
	}
	return string(raw), nil
}

// parseRechargeBonusTiers 宽松解析（读路径）：非法条目丢弃而非报错，避免历史错配置阻断下单。
// 同一 MinAmount 重复时保留先出现的档位。始终返回非 nil 切片，便于 JSON 输出为 []。
func parseRechargeBonusTiers(raw string) []RechargeBonusTier {
	out := make([]RechargeBonusTier, 0)
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return out
	}
	var items []RechargeBonusTier
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		slog.Warn("[Payment] parseRechargeBonusTiers: unmarshal failed", "error", err)
		return out
	}
	seen := make(map[string]struct{}, len(items))
	for _, tier := range items {
		if !rechargeBonusValueValid(tier.MinAmount, math.MaxFloat64) || !rechargeBonusValueValid(tier.BonusPercent, maxRechargeBonusPercent) {
			continue
		}
		key := decimal.NewFromFloat(tier.MinAmount).Round(2).String()
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, tier)
	}
	sortRechargeBonusTiers(out)
	return out
}

func validateRechargeBonusNotice(notice string) error {
	if utf8.RuneCountInString(notice) > maxRechargeBonusNoticeRunes {
		return fmt.Errorf("recharge bonus notice exceeds %d characters", maxRechargeBonusNoticeRunes)
	}
	return nil
}

// matchRechargeBonusTier 返回不超过 paymentAmount 的最大档位；tiers 需已按 MinAmount 升序。
func matchRechargeBonusTier(tiers []RechargeBonusTier, paymentAmount float64) (RechargeBonusTier, bool) {
	if math.IsNaN(paymentAmount) || math.IsInf(paymentAmount, 0) || paymentAmount <= 0 {
		return RechargeBonusTier{}, false
	}
	var matched RechargeBonusTier
	found := false
	for _, tier := range tiers {
		if paymentAmount+rechargeBonusAmountEpsilon < tier.MinAmount {
			break
		}
		matched = tier
		found = true
	}
	return matched, found
}

// calculateRechargeBonus 赠送金额 = 到账基数 × 百分比，保留两位小数（四舍五入）。
func calculateRechargeBonus(baseCredited, bonusPercent float64) float64 {
	if baseCredited <= 0 || bonusPercent <= 0 || math.IsNaN(baseCredited) || math.IsNaN(bonusPercent) {
		return 0
	}
	return decimal.NewFromFloat(baseCredited).
		Mul(decimal.NewFromFloat(bonusPercent)).
		Div(decimal.NewFromInt(100)).
		Round(2).
		InexactFloat64()
}

// resolveRechargeBonus 按支付金额命中阶梯并计算赠送；未命中或赠送为 0 时返回 (0, 0)。
func resolveRechargeBonus(tiers []RechargeBonusTier, paymentAmount, baseCredited float64) (bonus float64, percent float64) {
	tier, ok := matchRechargeBonusTier(tiers, paymentAmount)
	if !ok {
		return 0, 0
	}
	bonus = calculateRechargeBonus(baseCredited, tier.BonusPercent)
	if bonus <= 0 {
		return 0, 0
	}
	return bonus, tier.BonusPercent
}

// addRechargeBonus 到账总额 = 基数 + 赠送，两位小数。
func addRechargeBonus(baseCredited, bonus float64) float64 {
	return decimal.NewFromFloat(baseCredited).
		Add(decimal.NewFromFloat(bonus)).
		Round(2).
		InexactFloat64()
}

// paymentOrderAmountWithoutBonus 订单到账金额剔除赠送后的实充部分（USD），用于推广返利基数。
func paymentOrderAmountWithoutBonus(o *dbent.PaymentOrder) float64 {
	if o == nil {
		return 0
	}
	if o.OrderType != payment.OrderTypeBalance || o.BonusAmount <= 0 {
		return o.Amount
	}
	base := decimal.NewFromFloat(o.Amount).
		Sub(decimal.NewFromFloat(o.BonusAmount)).
		Round(2).
		InexactFloat64()
	if base < 0 {
		return 0
	}
	return base
}
