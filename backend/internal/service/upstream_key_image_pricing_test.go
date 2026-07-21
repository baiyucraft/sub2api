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
	require.InDelta(t, 0.05, *pricing.FinalCost1K, 1e-12)
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
