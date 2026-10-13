package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/upstreamcacheguard"
	"github.com/stretchr/testify/require"
)

func TestUpstreamCacheGuardSettingsDefaultsAndOldClientPreservation(t *testing.T) {
	repo := &upstreamManagementSettingRepoStub{values: map[string]string{}}
	settings := NewSettingService(repo, nil)
	got, err := settings.GetUpstreamCacheGuardSettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, upstreamcacheguard.Disabled, got.Mode)
	got.Mode = upstreamcacheguard.Observe
	got.GroupIDs = []int64{42}
	require.NoError(t, settings.SetUpstreamCacheGuardSettings(context.Background(), got))
	service := NewUpstreamConfigService(nil, nil, nil)
	service.SetHealthProbeDependencies(nil, settings)
	before := repo.values[SettingKeyUpstreamCacheGuard]
	management, err := service.GetManagementSettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, got, *management.CacheGuard)
	management.CacheGuard = nil // A pre-feature client cannot erase the new field.
	require.NoError(t, service.SetManagementSettings(context.Background(), management))
	require.Equal(t, before, repo.values[SettingKeyUpstreamCacheGuard])
}
func TestUpstreamCacheGuardInvalidSettingsRejectBeforeAnyWrite(t *testing.T) {
	repo := &upstreamManagementSettingRepoStub{values: map[string]string{}}
	settings := NewSettingService(repo, nil)
	service := NewUpstreamConfigService(nil, nil, nil)
	service.SetHealthProbeDependencies(nil, settings)
	management, err := service.GetManagementSettings(context.Background())
	require.NoError(t, err)
	management.CacheGuard.RecoveryThresholdPercent = management.CacheGuard.ThresholdPercent
	require.Error(t, service.SetManagementSettings(context.Background(), management))
	require.Zero(t, repo.setMultipleCalls)
	require.Empty(t, repo.values)
	for _, bad := range []string{`{"mode":"protect"}`, `not-json`} {
		repo.values[SettingKeyUpstreamCacheGuard] = bad
		value, err := settings.GetUpstreamCacheGuardSettings(context.Background())
		require.NoError(t, err)
		require.Equal(t, upstreamcacheguard.Disabled, value.Mode)
	}
}
func TestUpstreamCacheGuardCacheBandsBeforeTTFTAndHardExclusions(t *testing.T) {
	states := map[int64]upstreamcacheguard.AccountSnapshot{}
	for id, rate := range map[int64]float64{1: 91, 2: 87, 3: 84, 4: 20} {
		states[id] = upstreamcacheguard.AccountSnapshot{AccountState: upstreamcacheguard.AccountState{AccountID: id, CachePercent: rate}}
	}
	require.Equal(t, [][]int64{{2}, {1}, {3}, {4}}, cacheGuardRateTiers([]int64{4, 3, 2, 1}, states, map[int64]struct{}{1: {}}))
	states[2] = upstreamcacheguard.AccountSnapshot{AccountState: upstreamcacheguard.AccountState{AccountID: 2, CachePercent: 86}}
	require.Equal(t, []int64{2}, cacheGuardRateTiers([]int64{1, 2}, states, map[int64]struct{}{1: {}})[0]) // exactly five points belongs to the same band
	p := cacheGuardPreference{ScopeIDs: map[int64]struct{}{1: {}, 2: {}, 3: {}}, CandidateIDs: []int64{1, 2, 3, 9}}
	base := map[int64]struct{}{3: {}, 77: {}}
	excluded := p.exclusionsFor([]int64{2}, base)
	require.Contains(t, excluded, int64(3))
	require.Contains(t, excluded, int64(77))
	require.NotContains(t, excluded, int64(2))
	require.NotContains(t, excluded, int64(9))
	require.Len(t, base, 2)
	p.Migration = true
	require.Contains(t, p.exclusionsFor([]int64{2}, base), int64(9)) // ordinary accounts cannot become cache migration targets
}
func TestUpstreamCacheGuardLogicalIdentityIsolatedAndRedacted(t *testing.T) {
	r := &UpstreamCacheGuardRunner{secret: []byte("test-secret")}
	group := int64(8)
	ctx := WithUpstreamCacheGuardScheduling(context.Background(), 12)
	a, ok := r.logicalRequest(ctx, &group, "private-cache-key", "model-a", "openai")
	require.True(t, ok)
	b, ok := r.logicalRequest(ctx, &group, "private-cache-key", "model-b", "openai_ws")
	require.True(t, ok)
	require.Equal(t, a.LogicalSessionDigest, b.LogicalSessionDigest)
	otherGroup := int64(9)
	c, _ := r.logicalRequest(ctx, &otherGroup, "private-cache-key", "model-a", "openai")
	require.NotEqual(t, a.LogicalSessionDigest, c.LogicalSessionDigest)
	d, _ := r.logicalRequest(WithUpstreamCacheGuardScheduling(context.Background(), 13), &group, "private-cache-key", "model-a", "openai")
	require.NotEqual(t, a.LogicalSessionDigest, d.LogicalSessionDigest)
	raw, err := json.Marshal(a)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "private-cache-key")
	require.NotContains(t, string(raw), "test-secret")
	_, ok = r.logicalRequest(context.Background(), &group, "private-cache-key", "model-a", "openai")
	require.False(t, ok)
}

func TestUpstreamCacheGuardLatencyRefinesCacheBandOnly(t *testing.T) {
	latency := func(id int64) (float64, bool) { return map[int64]float64{1: 500, 2: 100, 3: 100, 4: 10}[id], id != 9 }
	require.Equal(t, [][]int64{{2, 3}, {1}, {4}}, cacheGuardLatencyTiers([][]int64{{1, 2, 3}, {4}}, latency))
	require.Equal(t, [][]int64{{1, 9, 2}}, cacheGuardLatencyTiers([][]int64{{1, 9, 2}}, latency))
}
