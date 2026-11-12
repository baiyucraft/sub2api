//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGatewayServiceRecordUsage_AccountStatsPreservesCacheTTLBreakdown(t *testing.T) {
	const model = "claude-fable-5-2"
	for _, pricingSource := range []string{"model_pricing", "custom_account_rule"} {
		for _, tt := range []struct {
			name             string
			cache5m          int
			cache1h          int
			overrideTarget   string
			wantCacheCost    float64
			wantAccountCost  float64
			wantCustomerCost float64
		}{
			{name: "1h", cache1h: 10000, wantCacheCost: 0.16, wantAccountCost: 0.1482, wantCustomerCost: 0.1882},
			{name: "mixed", cache5m: 4000, cache1h: 6000, wantCacheCost: 0.136, wantAccountCost: 0.1102, wantCustomerCost: 0.1642},
			{name: "5m", cache5m: 10000, wantCacheCost: 0.1, wantAccountCost: 0.0532, wantCustomerCost: 0.1282},
			{name: "aggregate_only", wantCacheCost: 0.1, wantAccountCost: 0.0532, wantCustomerCost: 0.1282},
			{name: "override_1h", cache5m: 10000, overrideTarget: "1h", wantCacheCost: 0.16, wantAccountCost: 0.1482, wantCustomerCost: 0.1882},
		} {
			t.Run(pricingSource+"/"+tt.name, func(t *testing.T) {
				usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
				userRepo := &openAIRecordUsageUserRepoStub{}
				svc := newGatewayRecordUsageServiceForTest(usageRepo, userRepo, &openAIRecordUsageSubRepoStub{})
				groupID := int64(906)
				channel := &Channel{ID: 1, Status: StatusActive}
				wantAccountCost := tt.wantCustomerCost
				if pricingSource == "custom_account_rule" {
					inputPrice, outputPrice, cacheReadPrice := 8e-6, 40e-6, 0.2e-6
					cacheWritePrice, cacheWrite1hPrice := 2.5e-6, 12e-6
					channel.AccountStatsPricingRules = []AccountStatsPricingRule{{
						GroupIDs: []int64{groupID},
						Pricing: []ChannelModelPricing{{
							Models: []string{model}, BillingMode: BillingModeToken,
							InputPrice: &inputPrice, OutputPrice: &outputPrice, CacheReadPrice: &cacheReadPrice,
							CacheWritePrice: &cacheWritePrice, CacheWrite1hPrice: &cacheWrite1hPrice,
						}},
					}}
					wantAccountCost = tt.wantAccountCost
				}
				svc.channelService = newTestChannelServiceForStats(t, channel, groupID, PlatformAnthropic)
				svc.resolver = NewModelPricingResolver(svc.channelService, svc.billingService)
				accountMultiplier := 0.8
				account := &Account{ID: 706, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, RateMultiplier: &accountMultiplier}
				if tt.overrideTarget != "" {
					account.Type = AccountTypeOAuth
					account.Extra = map[string]any{
						"cache_ttl_override_enabled": true,
						"cache_ttl_override_target":  tt.overrideTarget,
					}
				}
				err := svc.RecordUsage(context.Background(), &RecordUsageInput{
					Result: &ForwardResult{
						RequestID: "account_stats_cache_ttl_" + pricingSource + "_" + tt.name,
						Model:     model, UpstreamModel: model,
						Usage: ClaudeUsage{
							InputTokens: 1000, OutputTokens: 500, CacheReadInputTokens: 1000,
							CacheCreationInputTokens: 10000,
							CacheCreation5mTokens:    tt.cache5m,
							CacheCreation1hTokens:    tt.cache1h,
						},
					},
					APIKey: &APIKey{ID: 806, GroupID: &groupID, Group: &Group{
						ID: groupID, Platform: PlatformAnthropic, RateMultiplier: 0.5,
					}},
					User: &User{ID: 606}, Account: account,
				})
				require.NoError(t, err)
				require.NotNil(t, usageRepo.lastLog)
				log := usageRepo.lastLog
				require.InDelta(t, tt.wantCacheCost, log.CacheCreationCost, 1e-12)
				require.InDelta(t, tt.wantCustomerCost, log.TotalCost, 1e-12)
				require.InDelta(t, tt.wantCustomerCost*0.5, log.ActualCost, 1e-12)
				require.Equal(t, 1, userRepo.deductCalls)
				require.InDelta(t, tt.wantCustomerCost*0.5, userRepo.lastAmount, 1e-12)
				require.Equal(t, tt.overrideTarget != "", log.CacheTTLOverridden)
				require.NotNil(t, log.AccountRateMultiplier)
				require.Equal(t, accountMultiplier, *log.AccountRateMultiplier)
				require.NotNil(t, log.AccountStatsCost)
				require.InDelta(t, wantAccountCost, *log.AccountStatsCost, 1e-12,
					"account cost must preserve the effective cache TTL breakdown before applying the account multiplier")
			})
		}
	}
}

func TestGatewayServiceRecordUsage_AccountStatsOneHourCacheCost(t *testing.T) {
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	userRepo := &openAIRecordUsageUserRepoStub{}
	svc := newGatewayRecordUsageServiceForTest(usageRepo, userRepo, &openAIRecordUsageSubRepoStub{})
	groupID := int64(907)
	svc.channelService = newTestChannelServiceForStats(t, &Channel{ID: 1, Status: StatusActive}, groupID, PlatformAnthropic)
	svc.resolver = NewModelPricingResolver(svc.channelService, svc.billingService)
	accountMultiplier := 0.8
	err := svc.RecordUsage(context.Background(), &RecordUsageInput{
		Result: &ForwardResult{
			RequestID: "account_stats_one_hour_cache_cost",
			Model:     "claude-fable-5-2", UpstreamModel: "claude-fable-5-2",
			Usage: ClaudeUsage{
				InputTokens: 4, OutputTokens: 1668,
				CacheCreationInputTokens: 498870, CacheCreation1hTokens: 498870,
			},
		},
		APIKey: &APIKey{ID: 807, GroupID: &groupID, Group: &Group{
			ID: groupID, Platform: PlatformAnthropic, RateMultiplier: 1,
		}},
		User:    &User{ID: 607},
		Account: &Account{ID: 707, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, RateMultiplier: &accountMultiplier},
	})
	require.NoError(t, err)
	require.NotNil(t, usageRepo.lastLog)
	log := usageRepo.lastLog
	require.InDelta(t, 7.98192, log.CacheCreationCost, 1e-12)
	require.InDelta(t, 8.048672, log.ActualCost, 1e-12)
	require.InDelta(t, 8.048672, userRepo.lastAmount, 1e-12)
	require.NotNil(t, log.AccountStatsCost)
	require.NotNil(t, log.AccountRateMultiplier)
	require.InDelta(t, 8.048672, *log.AccountStatsCost, 1e-12)
	require.InDelta(t, 6.4389376, (*log.AccountStatsCost)*(*log.AccountRateMultiplier), 1e-12)
}
