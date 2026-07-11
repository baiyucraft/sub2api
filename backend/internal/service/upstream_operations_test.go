package service

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeProviderBalanceExtra_Sub2APIUsesCNY(t *testing.T) {
	cfg := &UpstreamConfig{Provider: UpstreamProviderSub2API}
	out := normalizeProviderBalanceExtra(cfg, map[string]any{
		"sub2api_balance":         12.5,
		"sub2api_total_recharged": 20.0,
	})
	require.Equal(t, 12.5, out["balance_cny"])
	require.Equal(t, 7.5, out["used_cny"])
	require.Equal(t, 20.0, out["total_recharged_cny"])
	require.Equal(t, 1.0, out["currency_to_cny_rate"])
}

func TestNormalizeProviderBalanceExtra_NewAPIUSDUsesProviderRate(t *testing.T) {
	override := 9.0
	cfg := &UpstreamConfig{Provider: UpstreamProviderNewAPI, BalanceToCNYRate: &override}
	out := normalizeProviderBalanceExtra(cfg, map[string]any{
		"upstream_provider_snapshot": map[string]any{
			"currency":          "USD",
			"balance_amount":    10.0,
			"used_amount":       2.0,
			"total_amount":      12.0,
			"usd_exchange_rate": 7.2,
		},
	})
	require.Equal(t, 72.0, out["balance_cny"])
	require.Equal(t, 14.4, out["used_cny"])
	require.Equal(t, 86.4, out["total_recharged_cny"])
	require.Equal(t, "provider", out["currency_rate_source"])
}

func TestNormalizeProviderBalanceExtra_CustomUsesAdminOverride(t *testing.T) {
	override := 0.5
	cfg := &UpstreamConfig{Provider: UpstreamProviderNewAPI, BalanceToCNYRate: &override}
	out := normalizeProviderBalanceExtra(cfg, map[string]any{
		"upstream_provider_snapshot": map[string]any{
			"currency":       "CUSTOM",
			"balance_amount": 10.0,
		},
	})
	require.Equal(t, 5.0, out["balance_cny"])
	require.Equal(t, "admin_override", out["currency_rate_source"])
}

func TestNormalizeProviderBalanceExtra_UnavailableRateClearsStaleCNY(t *testing.T) {
	cfg := &UpstreamConfig{Provider: UpstreamProviderNewAPI}
	out := normalizeProviderBalanceExtra(cfg, map[string]any{
		"upstream_provider_snapshot": map[string]any{
			"currency":       "USD",
			"balance_amount": 10.0,
		},
	})
	require.Contains(t, out, "balance_cny")
	require.Nil(t, out["balance_cny"])
	require.Nil(t, out["currency_to_cny_rate"])
	require.Equal(t, "unavailable", out["currency_rate_source"])
}

func TestNewAPIQuotaAmountsDoesNotInventUSDExchangeRate(t *testing.T) {
	amounts := newAPIQuotaAmounts(500000, 250000, nil)
	require.Equal(t, "USD", amounts.Currency)
	require.Equal(t, 1.0, amounts.BalanceAmount)
	require.Zero(t, amounts.USDExchangeRate)
}

func TestNormalizeNewAPIPlatform(t *testing.T) {
	require.Equal(t, PlatformAnthropic, normalizeNewAPIPlatform("Anthropic"))
	require.Equal(t, PlatformGemini, normalizeNewAPIPlatform("Google Gemini"))
	require.Equal(t, PlatformOpenAI, normalizeNewAPIPlatform("OpenAI"))
	require.Empty(t, normalizeNewAPIPlatform("unknown vendor"))
}

func TestApplyEffectiveCostMultipliers(t *testing.T) {
	rate := 0.065
	key := &UpstreamKey{RateMultiplier: &rate}
	applyEffectiveCostMultipliers(&UpstreamConfig{RechargeRate: 0.8}, []*UpstreamKey{key})
	require.NotNil(t, key.EffectiveCostMultiplier)
	require.InDelta(t, 0.052, *key.EffectiveCostMultiplier, 1e-12)
	require.Equal(t, 5, Sub2APIUpstreamPriority(*key.EffectiveCostMultiplier))
}

func TestNormalizeAndValidateUpstreamConfigRejectsInvalidRechargeRate(t *testing.T) {
	cfg := &UpstreamConfig{Name: "test", Provider: UpstreamProviderOther, BaseURL: "https://example.com", RechargeRate: -1}
	err := normalizeAndValidateUpstreamConfig(cfg, true)
	require.Error(t, err)
	require.Contains(t, err.Error(), "recharge_rate")
}

func TestClassifyUpstreamSyncFailure(t *testing.T) {
	stage, code, retryable := classifyUpstreamSyncFailure(errors.New("upstream returned status 502"), "keys_page")
	require.Equal(t, "keys_page", stage)
	require.Equal(t, "upstream", code)
	require.True(t, retryable)
}
