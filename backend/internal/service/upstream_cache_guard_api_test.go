package service

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/upstreamcacheguard"
	"github.com/stretchr/testify/require"
)

func TestUpstreamCacheGuardAPILinkedProtectionPreservesNativeEvidence(t *testing.T) {
	for _, mode := range []upstreamcacheguard.Mode{upstreamcacheguard.Protect, upstreamcacheguard.Observe} {
		t.Run(string(mode), func(t *testing.T) {
			localRate := 95.0
			item := UpstreamCacheGuardState{GroupID: 2, State: "normal", Reason: "healthy", RatePercent: &localRate, ValidRequests: 42}
			source := upstreamcacheguard.ProtectionSource{GroupID: 1, ThresholdPercent: 75, RecoveryThresholdPercent: 80,
				Phase: upstreamcacheguard.Trial, ValidRequests: 100, CachePercent: 40, MinValidRequests: 20,
				TrialValidRequests: 25, TrialCachePercent: 82, TrialSessions: 3, FirstTrialAt: time.Now()}
			derived := upstreamcacheguard.DerivedProtection{Protected: true, Reason: upstreamcacheguard.ProtectionReasonThresholdCascade,
				PrimarySource: &source, Sources: []upstreamcacheguard.ProtectionSource{source}}
			applyCacheGuardLinkedProtection(&item, &derived, mode, map[int64]string{1: "Source"})
			wantState := "protected"
			if mode == upstreamcacheguard.Observe {
				wantState = "observing"
			}
			require.Equal(t, wantState, item.State)
			require.Equal(t, "normal", item.NativeState)
			require.Equal(t, "healthy", item.NativeReason)
			require.Equal(t, "linked_group_protection", item.Reason)
			require.Equal(t, 95.0, *item.RatePercent)
			require.Equal(t, 42, item.ValidRequests)
			require.Zero(t, item.RecoveryValidRequests)
			require.Nil(t, item.RecoveryRatePercent)
			require.Len(t, item.LinkedProtection.Sources, 1)
			linked := item.LinkedProtection.Sources[0]
			require.Equal(t, int64(1), item.LinkedProtection.PrimarySourceGroupID)
			require.Equal(t, "Source", linked.GroupName)
			require.Equal(t, 100, linked.ValidRequests)
			require.Equal(t, 25, linked.RecoveryValidRequests)
			require.Equal(t, 82.0, *linked.RecoveryRatePercent)
		})
	}
}

func TestUpstreamCacheGuardAPILinkedUnknownAndLegacyCompatibility(t *testing.T) {
	item := UpstreamCacheGuardState{GroupID: 2, State: "insufficient", Reason: "insufficient_samples"}
	encoded, err := json.Marshal(item)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "linked_protection")
	source := upstreamcacheguard.ProtectionSource{GroupID: 1, Phase: upstreamcacheguard.Cooling, ValidRequests: 21, CachePercent: 0}
	derived := upstreamcacheguard.DerivedProtection{Protected: true, PrimarySource: &source, Sources: []upstreamcacheguard.ProtectionSource{source}}
	applyCacheGuardLinkedProtection(&item, &derived, upstreamcacheguard.Protect, nil)
	require.Nil(t, item.RatePercent, "source zero must not replace this group's unknown rate")
	require.Zero(t, item.ValidRequests)
	require.Nil(t, item.RecoveryRatePercent)
	require.Equal(t, "insufficient", item.NativeState)
	require.Equal(t, 0.0, *item.LinkedProtection.Sources[0].RatePercent)
	require.Nil(t, item.LinkedProtection.Sources[0].RecoveryRatePercent, "missing source recovery samples are unknown")
}

func TestUpstreamCacheGuardAPIShowsLinkedProtectionWithoutLocalRuntime(t *testing.T) {
	svc, ctx, _, _ := cacheGuardSelectionFixture(t, false, []float64{60}, map[int64]bool{1: true}, false, time.Time{}, nil)
	account, err := svc.accountRepo.GetByID(ctx, 1)
	require.NoError(t, err)
	account.GroupIDs = []int64{100, 200}
	account.Groups = []*Group{{ID: 100, Name: "Source"}, {ID: 200, Name: "Target"}}
	states := UpstreamCacheGuardAccountStates(account)
	require.Len(t, states, 2)
	target := states[1]
	require.Equal(t, int64(200), target.GroupID)
	require.Equal(t, "protected", target.State)
	require.Equal(t, "insufficient", target.NativeState)
	require.Nil(t, target.RatePercent, "an absent native runtime must not acquire the source's cache rate")
	require.Zero(t, target.ValidRequests)
	require.Nil(t, target.RecoveryRatePercent)
	require.NotNil(t, target.LinkedProtection)
	require.Len(t, target.LinkedProtection.Sources, 1)
	require.Equal(t, int64(100), target.LinkedProtection.PrimarySourceGroupID)
	require.Equal(t, "Source", target.LinkedProtection.Sources[0].GroupName)
	require.Equal(t, 21, target.LinkedProtection.Sources[0].ValidRequests)
	require.Equal(t, 60.0, *target.LinkedProtection.Sources[0].RatePercent)
	rotatedKey := int64(99)
	account.UpstreamKeyID = &rotatedKey
	for _, rotated := range UpstreamCacheGuardAccountStates(account) {
		require.Equal(t, "insufficient", rotated.State, "a rebound key must not display old-key native or linked protection")
		require.Nil(t, rotated.RatePercent)
		require.Zero(t, rotated.ValidRequests)
		require.Nil(t, rotated.LinkedProtection)
	}
}
