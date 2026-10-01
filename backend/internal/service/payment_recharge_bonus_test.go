//go:build unit

package service

import (
	"context"
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/stretchr/testify/require"
)

func TestNormalizeRechargeBonusTiers(t *testing.T) {
	t.Run("sorts ascending by min amount", func(t *testing.T) {
		out, err := NormalizeRechargeBonusTiers([]RechargeBonusTier{
			{MinAmount: 1000, BonusPercent: 35},
			{MinAmount: 100, BonusPercent: 20},
			{MinAmount: 500, BonusPercent: 30},
		})
		require.NoError(t, err)
		require.Equal(t, []RechargeBonusTier{
			{MinAmount: 100, BonusPercent: 20},
			{MinAmount: 500, BonusPercent: 30},
			{MinAmount: 1000, BonusPercent: 35},
		}, out)
	})

	t.Run("empty input yields empty non-nil slice", func(t *testing.T) {
		out, err := NormalizeRechargeBonusTiers(nil)
		require.NoError(t, err)
		require.NotNil(t, out)
		require.Len(t, out, 0)
	})

	t.Run("allows zero threshold and zero percent", func(t *testing.T) {
		out, err := NormalizeRechargeBonusTiers([]RechargeBonusTier{{MinAmount: 0, BonusPercent: 0}})
		require.NoError(t, err)
		require.Len(t, out, 1)
	})

	t.Run("rejects invalid values", func(t *testing.T) {
		cases := map[string][]RechargeBonusTier{
			"negative min":        {{MinAmount: -1, BonusPercent: 10}},
			"min three decimals":  {{MinAmount: 100.123, BonusPercent: 10}},
			"negative percent":    {{MinAmount: 100, BonusPercent: -5}},
			"percent over limit":  {{MinAmount: 100, BonusPercent: 1000.01}},
			"percent 3 decimals":  {{MinAmount: 100, BonusPercent: 12.345}},
			"duplicate min":       {{MinAmount: 100, BonusPercent: 10}, {MinAmount: 100, BonusPercent: 20}},
			"duplicate min 2 dec": {{MinAmount: 100, BonusPercent: 10}, {MinAmount: 100.00, BonusPercent: 20}},
		}
		for name, tiers := range cases {
			_, err := NormalizeRechargeBonusTiers(tiers)
			require.Error(t, err, name)
		}
	})

	t.Run("rejects too many tiers", func(t *testing.T) {
		tiers := make([]RechargeBonusTier, 0, maxRechargeBonusTiers+1)
		for i := 0; i <= maxRechargeBonusTiers; i++ {
			tiers = append(tiers, RechargeBonusTier{MinAmount: float64(i + 1), BonusPercent: 1})
		}
		_, err := NormalizeRechargeBonusTiers(tiers)
		require.Error(t, err)
	})
}

func TestParseRechargeBonusTiers(t *testing.T) {
	t.Run("empty or invalid json yields empty slice", func(t *testing.T) {
		require.NotNil(t, parseRechargeBonusTiers(""))
		require.Len(t, parseRechargeBonusTiers(""), 0)
		require.Len(t, parseRechargeBonusTiers("not json"), 0)
	})

	t.Run("drops invalid entries keeps first duplicate and sorts", func(t *testing.T) {
		raw := `[{"min_amount":500,"bonus_percent":30},{"min_amount":-1,"bonus_percent":5},` +
			`{"min_amount":100,"bonus_percent":20},{"min_amount":100,"bonus_percent":99},` +
			`{"min_amount":50,"bonus_percent":5000}]`
		out := parseRechargeBonusTiers(raw)
		require.Equal(t, []RechargeBonusTier{
			{MinAmount: 100, BonusPercent: 20},
			{MinAmount: 500, BonusPercent: 30},
		}, out)
	})

	t.Run("round trips encode", func(t *testing.T) {
		tiers := []RechargeBonusTier{{MinAmount: 100, BonusPercent: 20}, {MinAmount: 500, BonusPercent: 30}}
		encoded, err := encodeRechargeBonusTiers(tiers)
		require.NoError(t, err)
		require.Equal(t, tiers, parseRechargeBonusTiers(encoded))

		empty, err := encodeRechargeBonusTiers(nil)
		require.NoError(t, err)
		require.Equal(t, "", empty)
	})
}

func TestMatchRechargeBonusTier(t *testing.T) {
	tiers := []RechargeBonusTier{
		{MinAmount: 100, BonusPercent: 20},
		{MinAmount: 500, BonusPercent: 30},
		{MinAmount: 1000, BonusPercent: 35},
	}
	cases := []struct {
		amount  float64
		percent float64
		ok      bool
	}{
		{amount: 0, ok: false},
		{amount: 99.99, ok: false},
		{amount: 100, percent: 20, ok: true},
		{amount: 499.99, percent: 20, ok: true},
		{amount: 500, percent: 30, ok: true},
		{amount: 1000, percent: 35, ok: true},
		{amount: 1000000, percent: 35, ok: true},
	}
	for _, tc := range cases {
		tier, ok := matchRechargeBonusTier(tiers, tc.amount)
		require.Equal(t, tc.ok, ok, "amount %v", tc.amount)
		if ok {
			require.Equal(t, tc.percent, tier.BonusPercent, "amount %v", tc.amount)
		}
	}

	t.Run("float boundary 0.1+0.2 still matches 0.3 threshold", func(t *testing.T) {
		_, ok := matchRechargeBonusTier([]RechargeBonusTier{{MinAmount: 0.3, BonusPercent: 1}}, 0.1+0.2)
		require.True(t, ok)
	})

	t.Run("no tiers never matches", func(t *testing.T) {
		_, ok := matchRechargeBonusTier(nil, 100)
		require.False(t, ok)
	})
}

func TestResolveRechargeBonus(t *testing.T) {
	tiers := []RechargeBonusTier{{MinAmount: 100, BonusPercent: 20}, {MinAmount: 500, BonusPercent: 30}}

	t.Run("bonus is percent of credited base rounded to cents", func(t *testing.T) {
		bonus, pct := resolveRechargeBonus(tiers, 100, 100)
		require.Equal(t, 20.0, bonus)
		require.Equal(t, 20.0, pct)
		require.Equal(t, 120.0, addRechargeBonus(100, bonus))

		// 33.33 * 15% = 4.9995 → 5.00
		bonus, _ = resolveRechargeBonus([]RechargeBonusTier{{MinAmount: 1, BonusPercent: 15}}, 33.33, 33.33)
		require.Equal(t, 5.0, bonus)
	})

	t.Run("threshold uses payment amount while bonus uses credited base", func(t *testing.T) {
		// 1000 CNY paid, multiplier 0.14 → base 140 USD; tier matched by 1000 (30%)
		base := calculateCreditedBalance(1000, 0.14)
		bonus, pct := resolveRechargeBonus(tiers, 1000, base)
		require.Equal(t, 30.0, pct)
		require.Equal(t, 42.0, bonus)
		require.Equal(t, 182.0, addRechargeBonus(base, bonus))
	})

	t.Run("no match or zero percent yields zero", func(t *testing.T) {
		bonus, pct := resolveRechargeBonus(tiers, 50, 50)
		require.Zero(t, bonus)
		require.Zero(t, pct)

		bonus, pct = resolveRechargeBonus([]RechargeBonusTier{{MinAmount: 10, BonusPercent: 0}}, 50, 50)
		require.Zero(t, bonus)
		require.Zero(t, pct)
	})
}

func TestParsePaymentConfigRechargeBonus(t *testing.T) {
	svc := &PaymentConfigService{}

	t.Run("defaults", func(t *testing.T) {
		cfg := svc.parsePaymentConfig(map[string]string{})
		require.NotNil(t, cfg.RechargeBonusTiers)
		require.Len(t, cfg.RechargeBonusTiers, 0)
		require.Equal(t, "", cfg.RechargeBonusNotice)
	})

	t.Run("reads tiers and notice", func(t *testing.T) {
		cfg := svc.parsePaymentConfig(map[string]string{
			SettingRechargeBonusTiers:  `[{"min_amount":500,"bonus_percent":30},{"min_amount":100,"bonus_percent":20}]`,
			SettingRechargeBonusNotice: "**满 100 送 20%**",
		})
		require.Equal(t, []RechargeBonusTier{{MinAmount: 100, BonusPercent: 20}, {MinAmount: 500, BonusPercent: 30}}, cfg.RechargeBonusTiers)
		require.Equal(t, "**满 100 送 20%**", cfg.RechargeBonusNotice)
	})
}

func TestUpdatePaymentConfigRechargeBonus(t *testing.T) {
	ctx := context.Background()

	t.Run("persists normalized tiers and trimmed notice", func(t *testing.T) {
		repo := &paymentConfigSettingRepoStub{values: map[string]string{}}
		svc := &PaymentConfigService{settingRepo: repo}
		tiers := []RechargeBonusTier{{MinAmount: 500, BonusPercent: 30}, {MinAmount: 100, BonusPercent: 20}}
		notice := "  活动文案  "
		require.NoError(t, svc.UpdatePaymentConfig(ctx, UpdatePaymentConfigRequest{
			RechargeBonusTiers:  &tiers,
			RechargeBonusNotice: &notice,
		}))
		require.Equal(t, `[{"min_amount":100,"bonus_percent":20},{"min_amount":500,"bonus_percent":30}]`, repo.updates[SettingRechargeBonusTiers])
		require.Equal(t, "活动文案", repo.updates[SettingRechargeBonusNotice])

		cfg, err := svc.GetPaymentConfig(ctx)
		require.NoError(t, err)
		require.Equal(t, []RechargeBonusTier{{MinAmount: 100, BonusPercent: 20}, {MinAmount: 500, BonusPercent: 30}}, cfg.RechargeBonusTiers)
	})

	t.Run("empty tiers clears setting and omitted fields are untouched", func(t *testing.T) {
		repo := &paymentConfigSettingRepoStub{values: map[string]string{
			SettingRechargeBonusTiers:  `[{"min_amount":100,"bonus_percent":20}]`,
			SettingRechargeBonusNotice: "keep me",
		}}
		svc := &PaymentConfigService{settingRepo: repo}
		empty := []RechargeBonusTier{}
		require.NoError(t, svc.UpdatePaymentConfig(ctx, UpdatePaymentConfigRequest{RechargeBonusTiers: &empty}))
		value, ok := repo.updates[SettingRechargeBonusTiers]
		require.True(t, ok)
		require.Equal(t, "", value)
		_, touched := repo.updates[SettingRechargeBonusNotice]
		require.False(t, touched)
		require.Equal(t, "keep me", repo.values[SettingRechargeBonusNotice])
	})

	t.Run("rejects invalid tiers", func(t *testing.T) {
		repo := &paymentConfigSettingRepoStub{values: map[string]string{}}
		svc := &PaymentConfigService{settingRepo: repo}
		bad := []RechargeBonusTier{{MinAmount: 100, BonusPercent: 20}, {MinAmount: 100, BonusPercent: 30}}
		err := svc.UpdatePaymentConfig(ctx, UpdatePaymentConfigRequest{RechargeBonusTiers: &bad})
		require.Error(t, err)
		require.Nil(t, repo.updates)
	})
}

func TestAffiliateRebateBaseAmountExcludesRechargeBonus(t *testing.T) {
	require.Equal(t, 100.0, affiliateRebateBaseAmount(&dbent.PaymentOrder{
		OrderType: payment.OrderTypeBalance, Amount: 130, BonusAmount: 30,
	}))
	require.Equal(t, 130.0, affiliateRebateBaseAmount(&dbent.PaymentOrder{
		OrderType: payment.OrderTypeBalance, Amount: 130,
	}))
	// 订阅订单不受 bonus 字段影响
	require.Equal(t, 50.0, affiliateRebateBaseAmount(&dbent.PaymentOrder{
		OrderType: payment.OrderTypeSubscription, Amount: 50, BonusAmount: 30,
	}))
	// 异常数据：赠送大于总额时钳到 0
	require.Equal(t, 0.0, affiliateRebateBaseAmount(&dbent.PaymentOrder{
		OrderType: payment.OrderTypeBalance, Amount: 10, BonusAmount: 30,
	}))
}
