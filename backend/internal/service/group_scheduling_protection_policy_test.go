package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/upstreamcacheguard"
	"github.com/stretchr/testify/require"
)

func TestGroupSchedulingProtectionCustomIndependentAndThresholdValidation(t *testing.T) {
	mode, priority := "enabled", "ttft_first"
	threshold, recovery, samples := 60.0, 85.0, 12
	record := GroupSchedulingProtectionPolicyRecord{TTFT: GroupTTFTGuardPolicyRecord{GroupID: 7, GroupName: "pro", GroupPlatform: PlatformOpenAI}, CacheMode: &mode, ThresholdPercent: &threshold, RecoveryThresholdPercent: &recovery, MinValidRequests: &samples, Priority: &priority}
	for _, globalMode := range []upstreamcacheguard.Mode{upstreamcacheguard.Disabled, upstreamcacheguard.Observe} {
		global := DefaultUpstreamCacheGuardSettings()
		global.Mode = globalMode
		view, resolved, err := normalizeSchedulingProtectionPolicy(record, DefaultOpenAITTFTGuardSettings(), global, SchedulingPriorityCacheFirst)
		require.NoError(t, err)
		require.Equal(t, upstreamcacheguard.Protect, resolved.Cache.Mode)
		require.Equal(t, threshold, resolved.Cache.ThresholdPercent)
		require.Equal(t, priority, resolved.Priority)
		require.Equal(t, "group", view.Cache.Source)
		require.Equal(t, global.WindowMinutes, resolved.Cache.WindowMinutes)
	}
	recovery = threshold
	_, _, err := normalizeSchedulingProtectionPolicy(record, DefaultOpenAITTFTGuardSettings(), DefaultUpstreamCacheGuardSettings(), SchedulingPriorityCacheFirst)
	require.Error(t, err)
}

func TestGroupSchedulingProtectionInheritedAndUnsupportedTTFTProjection(t *testing.T) {
	global := DefaultUpstreamCacheGuardSettings()
	global.Mode = upstreamcacheguard.Observe
	ttft := DefaultOpenAITTFTGuardSettings()
	ttft.Enabled = true
	record := GroupSchedulingProtectionPolicyRecord{TTFT: GroupTTFTGuardPolicyRecord{GroupID: 3, GroupName: "claude", GroupPlatform: PlatformAnthropic}}
	view, resolved, err := normalizeSchedulingProtectionPolicy(record, ttft, global, SchedulingPriorityTTFTFirst)
	require.NoError(t, err)
	require.Equal(t, "inherit", view.Cache.Mode)
	require.Equal(t, upstreamcacheguard.Observe, resolved.Cache.Mode)
	require.Equal(t, SchedulingPriorityTTFTFirst, resolved.Priority)
	require.False(t, resolved.TTFT.Enabled)
	require.Equal(t, "global", view.PrioritySource)
}

func TestGroupSchedulingProtectionSnapshotsAreCopies(t *testing.T) {
	cache := DefaultUpstreamCacheGuardSettings()
	cache.GroupIDs = []int64{7}
	s := &GroupSchedulingProtectionPolicyService{snapshot: map[int64]GroupSchedulingProtectionResolvedPolicy{7: {GroupID: 7, Cache: cache}}}
	// Prevent background refresh in this isolated snapshot ownership test.
	s.loadedAt = time.Now()
	policies := s.ProtectionPolicies()
	policies[7].Cache.GroupIDs[0] = 99
	delete(policies, 7)
	require.Equal(t, int64(7), s.ProtectionPolicies()[7].Cache.GroupIDs[0])
}

func TestGroupSchedulingProtectionGlobalPriorityDefaultAndValidation(t *testing.T) {
	repo := &upstreamManagementSettingRepoStub{values: map[string]string{}}
	s := NewSettingService(repo, nil)
	priority, err := s.GetSchedulingProtectionPriority(context.Background())
	require.NoError(t, err)
	require.Equal(t, SchedulingPriorityCacheFirst, priority)
	require.Error(t, s.SetSchedulingProtectionPriority(context.Background(), "weighted"))
	require.NoError(t, s.SetSchedulingProtectionPriority(context.Background(), SchedulingPriorityTTFTFirst))
	priority, err = s.GetSchedulingProtectionPriority(context.Background())
	require.NoError(t, err)
	require.Equal(t, SchedulingPriorityTTFTFirst, priority)
}

func TestGroupSchedulingProtectionManagementPriorityMatchesCacheRuntimeSettings(t *testing.T) {
	repo := &upstreamManagementSettingRepoStub{values: map[string]string{}}
	settings := NewSettingService(repo, nil)
	upstream := NewUpstreamConfigService(nil, nil, nil)
	upstream.SetHealthProbeDependencies(nil, settings)
	management, err := upstream.GetManagementSettings(context.Background())
	require.NoError(t, err)
	management.SchedulingPriority = SchedulingPriorityTTFTFirst
	management.CacheGuard.Priority = SchedulingPriorityCacheFirst
	require.NoError(t, upstream.SetManagementSettings(context.Background(), management))
	cache, err := settings.GetUpstreamCacheGuardSettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, SchedulingPriorityTTFTFirst, cache.Priority)
	current, err := upstream.GetManagementSettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, current.SchedulingPriority, current.CacheGuard.Priority)
}
