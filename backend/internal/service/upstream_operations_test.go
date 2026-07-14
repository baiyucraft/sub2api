package service

import (
	"context"
	"encoding/json"
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

func TestNormalizeProviderBalanceExtra_AppliesRechargeRate(t *testing.T) {
	cfg := &UpstreamConfig{Provider: UpstreamProviderSub2API, RechargeRate: 0.1}
	out := normalizeProviderBalanceExtra(cfg, map[string]any{
		"sub2api_balance":         100.0,
		"sub2api_total_recharged": 550.0,
	})
	require.Equal(t, 10.0, out["balance_cny"])
	require.Equal(t, 45.0, out["used_cny"])
	require.Equal(t, 55.0, out["total_recharged_cny"])
	require.Equal(t, 0.1, out["recharge_rate"])
	require.Equal(t, 2, out["balance_formula_version"])
}

func TestNormalizeProviderBalanceExtra_Sub2APIPartialBalanceDoesNotInventUsedAmount(t *testing.T) {
	cfg := &UpstreamConfig{Provider: UpstreamProviderSub2API, RechargeRate: 0.1}
	out := normalizeProviderBalanceExtra(cfg, map[string]any{"sub2api_balance": 100.0})
	require.Equal(t, 10.0, out["balance_cny"])
	require.NotContains(t, out, "used_cny")
}

func TestNormalizeProviderBalanceExtra_NewAPIAdminOverrideWinsProviderRate(t *testing.T) {
	override := 9.0
	cfg := &UpstreamConfig{Provider: UpstreamProviderNewAPI, BalanceToCNYRate: &override}
	out := normalizeProviderBalanceExtra(cfg, map[string]any{
		"upstream_provider_snapshot": map[string]any{
			"currency":            "USD",
			"balance_amount":      10.0,
			"used_amount":         2.0,
			"total_amount":        12.0,
			"base_balance_amount": 10.0,
			"base_used_amount":    2.0,
			"base_total_amount":   12.0,
			"usd_exchange_rate":   7.2,
		},
	})
	require.Equal(t, 90.0, out["balance_cny"])
	require.Equal(t, 18.0, out["used_cny"])
	require.Equal(t, 108.0, out["total_recharged_cny"])
	require.Equal(t, "admin_override", out["currency_rate_source"])
}

func TestNormalizeProviderBalanceExtra_NewAPIUsesProviderRateWithoutOverride(t *testing.T) {
	cfg := &UpstreamConfig{Provider: UpstreamProviderNewAPI}
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

func TestNormalizeProviderBalanceExtra_NewAPICNYAdminOverrideWins(t *testing.T) {
	override := 0.5
	cfg := &UpstreamConfig{Provider: UpstreamProviderNewAPI, BalanceToCNYRate: &override}
	out := normalizeProviderBalanceExtra(cfg, map[string]any{
		"upstream_provider_snapshot": map[string]any{
			"currency":            "CNY",
			"balance_amount":      73.0,
			"base_balance_amount": 10.0,
		},
	})
	require.Equal(t, 5.0, out["balance_cny"])
	require.Equal(t, "admin_override", out["currency_rate_source"])
}

func TestNormalizeProviderBalanceExtra_CustomUsesAdminOverride(t *testing.T) {
	override := 0.5
	cfg := &UpstreamConfig{Provider: UpstreamProviderNewAPI, BalanceToCNYRate: &override}
	out := normalizeProviderBalanceExtra(cfg, map[string]any{
		"upstream_provider_snapshot": map[string]any{
			"currency":            "CUSTOM",
			"balance_amount":      20.0,
			"base_balance_amount": 10.0,
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
	require.Equal(t, 1.0, amounts.BaseBalanceAmount)
	require.Zero(t, amounts.USDExchangeRate)
}

func TestNormalizeNewAPIPlatform(t *testing.T) {
	require.Equal(t, PlatformAnthropic, normalizeNewAPIPlatform("Anthropic"))
	require.Equal(t, PlatformGemini, normalizeNewAPIPlatform("Google Gemini"))
	require.Equal(t, PlatformOpenAI, normalizeNewAPIPlatform("OpenAI"))
	require.Empty(t, normalizeNewAPIPlatform("unknown vendor"))
}

func TestNormalizeAndValidateUpstreamConfigRejectsInvalidRechargeRate(t *testing.T) {
	cfg := &UpstreamConfig{Name: "test", Provider: UpstreamProviderOther, SiteURL: "https://example.com", RechargeRate: -1}
	err := normalizeAndValidateUpstreamConfig(cfg, true)
	require.Error(t, err)
	require.Contains(t, err.Error(), "recharge_rate")
}

func TestNormalizeUpstreamActualRate(t *testing.T) {
	actual, err := NormalizeUpstreamActualRate(8, 0.1)
	require.NoError(t, err)
	require.Equal(t, 0.8, actual)

	actual, err = NormalizeUpstreamActualRate(0.06555, 1)
	require.NoError(t, err)
	require.Equal(t, 0.0656, actual)

	actual, err = NormalizeUpstreamActualRate(0.145, 1)
	require.NoError(t, err)
	require.Equal(t, 0.145, actual)
	require.Equal(t, 15, Sub2APIUpstreamPriority(actual))

	actual, err = NormalizeUpstreamActualRate(0.12, 0)
	require.NoError(t, err)
	require.Equal(t, 0.12, actual)

	_, err = NormalizeUpstreamActualRate(-1, 1)
	require.Error(t, err)
	_, err = NormalizeUpstreamActualRate(MaxUpstreamActualRate, 100)
	require.Error(t, err)
}

func TestClassifyUpstreamSyncFailure(t *testing.T) {
	stage, code, retryable := classifyUpstreamSyncFailure(errors.New("upstream returned status 502"), "keys_page")
	require.Equal(t, "keys_page", stage)
	require.Equal(t, "upstream", code)
	require.True(t, retryable)

	stage, code, retryable = classifyUpstreamSyncFailure(errors.New("newapi login returned status 401"), "keys_page")
	require.Equal(t, "auth", stage)
	require.Equal(t, "auth", code)
	require.False(t, retryable)

	stage, code, retryable = classifyUpstreamSyncFailure(errors.New("newapi get today usage returned incompatible response"), "keys_page")
	require.Equal(t, "profile", stage)
	require.Equal(t, "protocol", code)
	require.False(t, retryable)
}

func TestGetKeyRateTrendRejectsUnsupportedRangeBeforeRepositoryAccess(t *testing.T) {
	svc := NewUpstreamConfigService(nil, nil, nil)
	_, err := svc.GetKeyRateTrend(context.Background(), 1, 2, "90d")
	require.Error(t, err)
	require.Contains(t, err.Error(), "range must be one of 24h, 7d, 30d")
}

func TestProjectUpstreamEventsProjectsOnlyEffectiveRates(t *testing.T) {
	events := []UpstreamEvent{
		{ID: 1, ConfigID: 2, Type: "key_rate_changed", Severity: "info", Message: "legacy", Payload: map[string]any{
			"old_raw_rate": 1.0, "new_raw_rate": 2.0, "old_effective_rate": 0.5, "new_effective_rate": 1.0,
		}},
		{ID: 2, ConfigID: 2, Type: "key_actual_rate_changed", Payload: map[string]any{"old_rate": 0.7, "new_rate": 0.8, "secret": "drop"}},
		{ID: 3, ConfigID: 2, Type: "key_rate_changed", Payload: map[string]any{"old_raw_rate": 3.0, "new_raw_rate": 4.0}},
		{ID: 4, ConfigID: 2, Type: "group_removed", Payload: map[string]any{"group_id": 9.0}},
	}

	got := projectUpstreamEvents(events)
	require.Equal(t, events[0].ID, got[0].ID)
	require.Equal(t, events[0].Severity, got[0].Severity)
	require.Equal(t, events[0].Message, got[0].Message)
	require.Equal(t, map[string]any{"old_rate": 0.5, "new_rate": 1.0}, got[0].Payload)
	require.Equal(t, map[string]any{"old_rate": 0.7, "new_rate": 0.8}, got[1].Payload)
	require.Nil(t, got[2].Payload)
	require.Equal(t, events[3].Payload, got[3].Payload)
	require.Contains(t, events[0].Payload, "old_raw_rate")

	encoded, err := json.Marshal(got[:3])
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "raw_rate")
	require.NotContains(t, string(encoded), "effective_rate")
	require.NotContains(t, string(encoded), "secret")
}

func TestUpstreamKeyRateDTOJSONUsesSingleActualRateContract(t *testing.T) {
	current, previous, oldRate, newRate := 0.8, 0.7, 0.6, 0.8
	encoded, err := json.Marshal(UpstreamKeyRateTrend{
		CurrentRate:  &current,
		PreviousRate: &previous,
		Points:       []UpstreamKeyRateTrendPoint{{Bucket: "2026-07-14T00:00:00Z", RateMultiplier: current}},
		Changes:      []UpstreamKeyRateChange{{OldRate: &oldRate, NewRate: &newRate}},
	})
	require.NoError(t, err)
	contract := string(encoded)
	require.Contains(t, contract, `"current_rate":0.8`)
	require.Contains(t, contract, `"previous_rate":0.7`)
	require.Contains(t, contract, `"rate_multiplier":0.8`)
	require.Contains(t, contract, `"old_rate":0.6`)
	require.Contains(t, contract, `"new_rate":0.8`)
	for _, forbidden := range []string{"raw_rate", "effective_rate", "effective_cost_multiplier", "source_rate", "recharge_rate"} {
		require.NotContains(t, contract, forbidden)
	}
}
