package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParseSub2APIAvailableGroupImagePricing(t *testing.T) {
	snapshot, ok := parseSub2APIAvailableGroupImagePricing(map[string]any{
		"allow_image_generation": true,
		"image_rate_independent": true,
		"image_rate_multiplier":  0,
		"image_price_1k":         0,
		"image_price_2k":         0.2,
		"image_price_4k":         0.4,
	})
	require.True(t, ok)
	require.Equal(t, UpstreamKeyImagePricingStatusAvailable, snapshot.Status)
	require.NotNil(t, snapshot.ImageRateMultiplier)
	require.Zero(t, *snapshot.ImageRateMultiplier)
	require.NotNil(t, snapshot.ImagePrice1K)
	require.Zero(t, *snapshot.ImagePrice1K)

	malformed, ok := parseSub2APIAvailableGroupImagePricing(map[string]any{
		"allow_image_generation": true,
		"image_price_1k":         "not-a-number",
	})
	require.True(t, ok)
	require.Equal(t, UpstreamKeyImagePricingStatusUnavailable, malformed.Status)

	disabled, ok := parseSub2APIAvailableGroupImagePricing(map[string]any{"allow_image_generation": false})
	require.True(t, ok)
	require.Equal(t, UpstreamKeyImagePricingStatusDisabled, disabled.Status)
}

func TestParseSub2APIAvailableGroupImagePricingFillsBuiltinDefaults(t *testing.T) {
	snapshot, ok := parseSub2APIAvailableGroupImagePricingForPlatform(map[string]any{
		"allow_image_generation": true,
		"platform":               "openai",
	}, "openai")
	require.True(t, ok)
	require.Equal(t, UpstreamKeyImagePricingStatusPartial, snapshot.Status)
	require.Equal(t, "builtin_default", snapshot.PricingSource)
	require.Equal(t, []string{"1K", "2K", "4K"}, snapshot.DefaultedTiers)
	require.InDelta(t, 0.134, *snapshot.ImagePrice1K, 1e-12)
	require.InDelta(t, 0.201, *snapshot.ImagePrice2K, 1e-12)
	require.InDelta(t, 0.268, *snapshot.ImagePrice4K, 1e-12)

	mixed, ok := parseSub2APIAvailableGroupImagePricingForPlatform(map[string]any{
		"allow_image_generation": true,
		"image_price_1k":         0.09,
	}, "grok")
	require.True(t, ok)
	require.Equal(t, UpstreamKeyImagePricingStatusPartial, mixed.Status)
	require.Equal(t, "mixed", mixed.PricingSource)
	require.Equal(t, []string{"2K", "4K"}, mixed.DefaultedTiers)
	require.InDelta(t, 0.09, *mixed.ImagePrice1K, 1e-12)
	require.InDelta(t, 0.02, *mixed.ImagePrice2K, 1e-12)
	require.InDelta(t, 0.02, *mixed.ImagePrice4K, 1e-12)
}

func TestParseSub2APIAvailableGroupImagePricingRejectsInvalidPrice(t *testing.T) {
	snapshot, ok := parseSub2APIAvailableGroupImagePricingForPlatform(map[string]any{
		"allow_image_generation": true,
		"image_price_1k":         "not-a-number",
	}, "openai")
	require.True(t, ok)
	require.Equal(t, UpstreamKeyImagePricingStatusUnavailable, snapshot.Status)
	require.Nil(t, snapshot.ImagePrice1K)
}

func TestDeriveUpstreamKeyImagePricingUsesSourceOrIndependentRate(t *testing.T) {
	now := time.Date(2026, 7, 21, 1, 2, 3, 0, time.UTC)
	price1, price2, price4 := 0.1, 0.2, 0.4
	sourceRate := 0.5
	key := &UpstreamKey{
		SourceRateMultiplier: &sourceRate,
		Extra: map[string]any{Sub2APIImagePricingSnapshotExtraKey: sub2APIImagePricingSnapshotMap(sub2APIImagePricingSnapshot{
			Version:              sub2APIImagePricingSnapshotVersion,
			Status:               UpstreamKeyImagePricingStatusAvailable,
			AllowImageGeneration: true,
			ImagePrice1K:         &price1,
			ImagePrice2K:         &price2,
			ImagePrice4K:         &price4,
			ObservedAt:           &now,
		})},
	}
	config := &UpstreamConfig{Provider: UpstreamProviderSub2API, RechargeRate: 2}
	pricing := deriveUpstreamKeyImagePricing(key, config)
	require.Equal(t, UpstreamKeyImagePricingStatusAvailable, pricing.Status)
	require.False(t, pricing.RateIndependent)
	require.NotNil(t, pricing.EffectiveRateMultiplier)
	require.InDelta(t, 1.0, *pricing.EffectiveRateMultiplier, 1e-12)
	require.InDelta(t, 0.1, *pricing.FinalCost1K, 1e-12)

	independent := 0.25
	key.Extra[Sub2APIImagePricingSnapshotExtraKey] = sub2APIImagePricingSnapshotMap(sub2APIImagePricingSnapshot{
		Version:              sub2APIImagePricingSnapshotVersion,
		Status:               UpstreamKeyImagePricingStatusAvailable,
		AllowImageGeneration: true,
		ImageRateIndependent: true,
		ImageRateMultiplier:  &independent,
		ImagePrice1K:         &price1,
		ImagePrice2K:         &price2,
		ImagePrice4K:         &price4,
		ObservedAt:           &now,
	})
	pricing = deriveUpstreamKeyImagePricing(key, config)
	require.True(t, pricing.RateIndependent)
	require.NotNil(t, pricing.EffectiveRateMultiplier)
	require.InDelta(t, 0.5, *pricing.EffectiveRateMultiplier, 1e-12)
	require.InDelta(t, 0.05, *pricing.FinalCost1K, 1e-12)
}

func TestDeriveUpstreamKeyImagePricingKeepsBuiltinDefaultsUsable(t *testing.T) {
	now := time.Now().UTC()
	snapshot, ok := parseSub2APIAvailableGroupImagePricingForPlatform(map[string]any{
		"allow_image_generation": true,
		"platform":               "openai",
	}, "openai")
	require.True(t, ok)
	snapshot.ObservedAt = &now
	rate := 0.5
	key := &UpstreamKey{SourceRateMultiplier: &rate, Extra: map[string]any{
		Sub2APIImagePricingSnapshotExtraKey: sub2APIImagePricingSnapshotMap(snapshot),
	}}
	pricing := deriveUpstreamKeyImagePricing(key, &UpstreamConfig{Provider: UpstreamProviderSub2API, RechargeRate: 1})
	require.True(t, pricing.Supported)
	require.Equal(t, UpstreamKeyImagePricingStatusPartial, pricing.Status)
	require.Equal(t, "builtin_default", pricing.PricingSource)
	require.Equal(t, []string{"1K", "2K", "4K"}, pricing.DefaultedTiers)
	require.InDelta(t, 0.067, *pricing.FinalCost1K, 1e-12)
}

func TestDeriveUpstreamKeyImagePricingHydratesLegacyPartialSnapshot(t *testing.T) {
	rate := 1.0
	platform := "grok"
	key := &UpstreamKey{Platform: &platform, SourceRateMultiplier: &rate, Extra: map[string]any{
		Sub2APIImagePricingSnapshotExtraKey: sub2APIImagePricingSnapshotMap(sub2APIImagePricingSnapshot{
			Version: sub2APIImagePricingSnapshotVersion, Status: UpstreamKeyImagePricingStatusPartial,
			AllowImageGeneration: true,
		})}}
	pricing := deriveUpstreamKeyImagePricing(key, &UpstreamConfig{Provider: UpstreamProviderSub2API, RechargeRate: 1})
	require.Equal(t, "builtin_default", pricing.PricingSource)
	require.InDelta(t, 0.02, *pricing.FinalCost1K, 1e-12)
	require.InDelta(t, 0.02, *pricing.FinalCost2K, 1e-12)
	require.InDelta(t, 0.02, *pricing.FinalCost4K, 1e-12)
}

func TestDeriveUpstreamKeyImagePricingPreservesPreciseEffectiveRate(t *testing.T) {
	price := 1.0
	source := 0.045
	key := &UpstreamKey{
		SourceRateMultiplier: &source,
		Extra: map[string]any{Sub2APIImagePricingSnapshotExtraKey: sub2APIImagePricingSnapshotMap(sub2APIImagePricingSnapshot{
			Version:              sub2APIImagePricingSnapshotVersion,
			Status:               UpstreamKeyImagePricingStatusAvailable,
			AllowImageGeneration: true,
			ImagePrice1K:         &price,
			ImagePrice2K:         &price,
			ImagePrice4K:         &price,
		})},
	}

	pricing := deriveUpstreamKeyImagePricing(key, &UpstreamConfig{Provider: UpstreamProviderSub2API, RechargeRate: 1})
	require.NotNil(t, pricing.EffectiveRateMultiplier)
	require.InDelta(t, 0.045, *pricing.EffectiveRateMultiplier, 1e-12)
	require.InDelta(t, 0.045, *pricing.FinalCost1K, 1e-12)
}

func TestDeriveUpstreamKeyImagePricingMarksConfigFailureStale(t *testing.T) {
	price := 0.1
	key := &UpstreamKey{SourceRateMultiplier: float64PtrForImagePricing(1), Extra: map[string]any{Sub2APIImagePricingSnapshotExtraKey: sub2APIImagePricingSnapshotMap(sub2APIImagePricingSnapshot{
		Version: sub2APIImagePricingSnapshotVersion, Status: UpstreamKeyImagePricingStatusAvailable,
		AllowImageGeneration: true, ImagePrice1K: &price, ImagePrice2K: &price, ImagePrice4K: &price,
	})}}
	lastError := "temporary upstream failure"
	pricing := deriveUpstreamKeyImagePricing(key, &UpstreamConfig{Provider: UpstreamProviderSub2API, LastError: &lastError})
	require.True(t, pricing.Stale)
}

func TestMergeSub2APIImagePricingSnapshotsPreservesOnlyValidHistory(t *testing.T) {
	remoteID := int64(77)
	price := 0.1
	previous := sub2APIImagePricingSnapshot{
		Version: sub2APIImagePricingSnapshotVersion, Status: UpstreamKeyImagePricingStatusAvailable,
		AllowImageGeneration: true, ImagePrice1K: &price, ImagePrice2K: &price, ImagePrice4K: &price,
	}
	repo := &upstreamConfigServiceRepo{keys: []UpstreamKey{{
		UpstreamConfigID: 9, RemoteKeyID: &remoteID,
		Extra: map[string]any{Sub2APIImagePricingSnapshotExtraKey: sub2APIImagePricingSnapshotMap(previous)},
	}}}
	svc := NewUpstreamConfigService(repo, nil, nil)
	snapshot := &upstreamProviderSnapshot{Keys: []UpstreamKey{{RemoteKeyID: &remoteID, Extra: map[string]any{}}}}
	svc.mergeSub2APIImagePricingSnapshots(context.Background(), &UpstreamConfig{ID: 9}, snapshot)

	retained, ok := parseSub2APIImagePricingSnapshot(snapshot.Keys[0].Extra)
	require.True(t, ok)
	require.True(t, retained.Stale)
	require.False(t, snapshot.Partial)
	require.Empty(t, snapshot.Warnings)

	disabled := sub2APIImagePricingSnapshot{
		Version: sub2APIImagePricingSnapshotVersion, Status: UpstreamKeyImagePricingStatusDisabled,
	}
	snapshot.Keys[0].Extra[Sub2APIImagePricingSnapshotExtraKey] = sub2APIImagePricingSnapshotMap(disabled)
	svc.mergeSub2APIImagePricingSnapshots(context.Background(), &UpstreamConfig{ID: 9}, snapshot)
	overridden, ok := parseSub2APIImagePricingSnapshot(snapshot.Keys[0].Extra)
	require.True(t, ok)
	require.Equal(t, UpstreamKeyImagePricingStatusDisabled, overridden.Status)
	require.False(t, overridden.Stale)
}

func TestMergeSub2APIImagePricingSnapshotsFirstUnavailableIsNotStale(t *testing.T) {
	remoteID := int64(88)
	svc := NewUpstreamConfigService(&upstreamConfigServiceRepo{}, nil, nil)
	snapshot := &upstreamProviderSnapshot{Keys: []UpstreamKey{{RemoteKeyID: &remoteID, Extra: map[string]any{}}}}
	svc.mergeSub2APIImagePricingSnapshots(context.Background(), &UpstreamConfig{ID: 10}, snapshot)

	pricing, ok := parseSub2APIImagePricingSnapshot(snapshot.Keys[0].Extra)
	require.True(t, ok)
	require.Equal(t, UpstreamKeyImagePricingStatusUnavailable, pricing.Status)
	require.False(t, pricing.Stale)
}

func float64PtrForImagePricing(value float64) *float64 { return &value }

func videoPricingFloatPtr(value float64) *float64 {
	return &value
}

func testVideoPricingKey(snapshot map[string]any, sourceRate *float64) *UpstreamKey {
	return &UpstreamKey{
		SourceRateMultiplier: sourceRate,
		Extra: map[string]any{
			Sub2APIVideoPricingSnapshotExtraKey: snapshot,
		},
	}
}

func TestDeriveUpstreamKeyVideoPricingRequiresIndependentMultiplier(t *testing.T) {
	key := testVideoPricingKey(map[string]any{
		"version":                float64(sub2APIVideoPricingSnapshotVersion),
		"status":                 UpstreamKeyImagePricingStatusAvailable,
		"allow_video_generation": true,
		"video_rate_independent": true,
		"video_price_720p":       2.0,
	}, videoPricingFloatPtr(1.5))

	pricing := DeriveUpstreamKeyVideoPricingForAccount(key, &UpstreamConfig{RechargeRate: 1})

	require.False(t, pricing.Supported)
	require.Equal(t, UpstreamKeyImagePricingStatusPartial, pricing.Status)
	require.Nil(t, pricing.EffectiveRateMultiplier)
}

func TestDeriveUpstreamKeyVideoPricingUsesSharedSourceMultiplier(t *testing.T) {
	key := testVideoPricingKey(map[string]any{
		"version":                float64(sub2APIVideoPricingSnapshotVersion),
		"status":                 UpstreamKeyImagePricingStatusAvailable,
		"allow_video_generation": true,
		"video_rate_independent": false,
		"video_price_720p":       2.0,
	}, videoPricingFloatPtr(1.5))

	pricing := DeriveUpstreamKeyVideoPricingForAccount(key, &UpstreamConfig{RechargeRate: 1})

	require.True(t, pricing.Supported)
	require.Equal(t, UpstreamKeyImagePricingStatusAvailable, pricing.Status)
	require.InDelta(t, 1.5, *pricing.EffectiveRateMultiplier, 1e-12)
	require.InDelta(t, 3.0, *pricing.FinalCost720p, 1e-12)
}

func TestDeriveUpstreamKeyVideoPricingUsesIndependentMultiplier(t *testing.T) {
	key := testVideoPricingKey(map[string]any{
		"version":                float64(sub2APIVideoPricingSnapshotVersion),
		"status":                 UpstreamKeyImagePricingStatusAvailable,
		"allow_video_generation": true,
		"video_rate_independent": true,
		"video_rate_multiplier":  0.75,
		"video_price_720p":       2.0,
	}, videoPricingFloatPtr(1.5))

	pricing := DeriveUpstreamKeyVideoPricingForAccount(key, &UpstreamConfig{RechargeRate: 1})

	require.True(t, pricing.Supported)
	require.Equal(t, UpstreamKeyImagePricingStatusAvailable, pricing.Status)
	require.InDelta(t, 0.75, *pricing.EffectiveRateMultiplier, 1e-12)
	require.InDelta(t, 1.5, *pricing.FinalCost720p, 1e-12)
}

func TestDeriveUpstreamKeyVideoPricingRejectsMissingSharedMultiplier(t *testing.T) {
	key := testVideoPricingKey(map[string]any{
		"version":                float64(sub2APIVideoPricingSnapshotVersion),
		"status":                 UpstreamKeyImagePricingStatusAvailable,
		"allow_video_generation": true,
		"video_rate_independent": false,
		"video_price_720p":       2.0,
	}, nil)

	pricing := DeriveUpstreamKeyVideoPricingForAccount(key, &UpstreamConfig{RechargeRate: 1})

	require.False(t, pricing.Supported)
	require.Equal(t, UpstreamKeyImagePricingStatusPartial, pricing.Status)
	require.Nil(t, pricing.EffectiveRateMultiplier)
}
