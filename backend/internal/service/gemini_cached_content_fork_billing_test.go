//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/stretchr/testify/require"
)

func TestGeminiCachedContentQuoteExplicitZeroKeepsQuotaMeter(t *testing.T) {
	svc, usageRepo := newGeminiCacheBillingGatewayForTest(t)
	zero := 0.0
	svc.userGroupRateResolver = newUserGroupRateResolver(&userGroupRateResolverRepoStub{percent: &zero}, nil, 0, nil, "test")
	groupID := int64(33)
	apiKey := &APIKey{ID: 821, GroupID: &groupID, User: &User{ID: 621},
		Group: &Group{ID: groupID, Platform: PlatformGemini, RateMultiplier: 2, Hydrated: true}}
	for _, tokens := range []int{0, 1000} {
		input := &RecordUsageInput{
			Result: &ForwardResult{RequestID: GeminiCachedContentCreateUsageRequestID("zero"), Model: "gemini-2.5-pro",
				Usage: ClaudeUsage{InputTokens: tokens}, CacheStorageTokenHours: 1000},
			APIKey: apiKey, User: apiKey.User, Account: &Account{ID: 713, Platform: PlatformGemini},
		}
		quote := svc.QuoteUsageCosts(context.Background(), input)
		require.NotNil(t, quote)
		require.Zero(t, quote.ActualCost)
		require.True(t, quote.QuotaMeterCostSet)
		require.InDelta(t, (float64(tokens)*0.00000125+1000*0.0000045)*2, quote.QuotaMeterCost, 1e-12)
		require.NoError(t, svc.RecordUsage(context.Background(), input))
		require.Zero(t, usageRepo.lastLog.ActualCost, "缓存创建和仅存储延期都不能向零倍率用户收费")
	}
}

func TestGeminiCachedContentQuoteUsesOriginalBillingIdentity(t *testing.T) {
	svc, _ := newGeminiCacheBillingGatewayForTest(t)
	user := &User{ID: 621}
	originalID, routingID := int64(33), int64(34)
	original := &APIKey{ID: 821, GroupID: &originalID, User: user,
		Group: &Group{ID: originalID, Platform: PlatformGemini, RateMultiplier: 2, Hydrated: true}}
	routing := &APIKey{ID: 821, GroupID: &routingID, User: user,
		Group: &Group{ID: routingID, Platform: PlatformGemini, RateMultiplier: 9, Hydrated: true}}
	ctx := WithChannelCustomizationBillingSnapshot(context.Background(), original, nil, true)
	ctx = WithChannelCustomizationModel(ctx, "gemini-2.5-pro", "gemini-3.8-flash")
	input := &RecordUsageInput{APIKey: routing, Result: &ForwardResult{
		Model: "gemini-3.8-flash", UpstreamModel: "gemini-3.8-flash", CacheStorageTokenHours: 1000,
	}}
	require.True(t, svc.CacheStoragePriced(ctx, input))
	require.InDelta(t, 1000*0.0000045*2, svc.QuoteUsageCost(ctx, input), 1e-12)
	ctx = WithChannelCustomizationModel(ctx, "unpriced-public-alias", "gemini-3.8-flash")
	require.False(t, svc.CacheStoragePriced(ctx, input), "自定义映射不允许以目标模型存储价替代原模型价格")
}

type fixedChargeForkCacheStub struct {
	fixedChargeCacheStub
	platformEntry *UserPlatformQuotaCacheEntry
}

func (s *fixedChargeForkCacheStub) GetUserPlatformQuotaCache(context.Context, int64, string) (*UserPlatformQuotaCacheEntry, bool, error) {
	return s.platformEntry, s.platformEntry != nil, nil
}

func TestReserveFixedChargeZeroActualKeepsKeyAndPlatformHeadroom(t *testing.T) {
	ctx := context.Background()
	user := &User{ID: 1}
	cfg := &config.Config{}
	cfg.Billing.InflightReservation.Enabled = true
	cache := &fixedChargeForkCacheStub{}
	svc := NewBillingCacheService(cache, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(svc.Stop)
	key := &APIKey{ID: 5, Quota: 10, QuotaUsed: 7}
	first, err := svc.ReserveFixedChargeCosts(ctx, user, key, nil, nil, 0, 3, "")
	require.NoError(t, err, "零余额用户的实付为零，Key 剩余额度刚好够本笔计量")
	require.NotNil(t, first)
	require.Empty(t, cache.reserved["user:1"], "零实付不能建立余额预留")
	_, err = svc.ReserveFixedChargeCosts(ctx, user, key, nil, nil, 0, 3, "")
	require.ErrorIs(t, err, ErrAPIKeyQuotaExhausted, "尚未落账的第一笔必须占用 Key headroom")
	first.Release()

	limit := 5.0
	now := time.Now()
	day, week := timezone.StartOfDay(now), timezone.StartOfWeek(now)
	cache.platformEntry = &UserPlatformQuotaCacheEntry{
		SchemaVersion: UserPlatformQuotaCacheSchemaV1, DailyLimitUSD: &limit, DailyUsageUSD: 1,
		DailyWindowStart: &day, WeeklyWindowStart: &week, MonthlyWindowStart: &now,
	}
	svc.userPlatformQuotaRepo = &fakeQuotaRepo{}
	first, err = svc.ReserveFixedChargeCosts(ctx, user, nil, nil, nil, 0, 3, PlatformGemini)
	require.NoError(t, err)
	require.NotNil(t, first)
	_, err = svc.ReserveFixedChargeCosts(ctx, user, nil, nil, nil, 0, 3, PlatformGemini)
	require.ErrorIs(t, err, ErrUserPlatformDailyQuotaExhausted, "平台额度也必须计入尚未落账的缓存费")
	first.Release()
	for scope, reserved := range cache.reserved {
		require.Empty(t, reserved, scope)
	}

	zero := 0.0
	group := &Group{ID: 2, SubscriptionType: SubscriptionTypeSubscription, DailyLimitUSD: &zero}
	res, err := svc.ReserveFixedChargeCosts(ctx, user, nil, group, &UserSubscription{ID: 6}, 0, 3, PlatformGemini)
	require.NoError(t, err, "订阅按实付金额计量，零实付无需存储费订阅预留且豁免平台额度")
	res.Release()
}
