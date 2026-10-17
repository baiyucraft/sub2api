package service

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTTFTIndexedRuntimeFactsAndExpiryInvariants(t *testing.T) {
	now := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	g := newOpenAITTFTGuardWithOptions(128, 15*time.Minute, func() time.Time { return now })
	cfg := enabledOpenAITTFTGuardConfig(20*time.Second, 2)
	o := OpenAITTFTGuardObservation{GroupID: 1, AccountID: 1, UpstreamKeyID: 10, Model: "model", AttemptID: "a1", LogicalRequestID: "r1", RuntimeID: g.runtimeID, AlgorithmVersion: openAITTFTGuardAlgorithmVersion, SentAt: now, FirstTokenAt: now.Add(time.Second), CompletedAt: now.Add(2 * time.Second), TTFTMs: 60000, Success: true}
	now = o.CompletedAt
	require.True(t, g.reportObservation(o, cfg, true))
	require.False(t, g.reportObservation(o, cfg, true), "a physical attempt is sampled once")
	key, _ := g.sampleKeyLocked(1, 10, "model")
	e := g.projectionAtLocked(key, 1, cfg, now.Add(2*time.Second))
	require.Equal(t, uint64(1), e.sampleCount)
	require.Equal(t, "protected", e.stateAt(now.Add(2*time.Second), g.ttl))
	now = now.Add(16 * time.Minute)
	late := o
	late.AttemptID, late.CompletedAt = "late", now
	require.True(t, g.reportObservation(late, cfg, true))
	require.Equal(t, "insufficient", e.stateAt(now, g.ttl))
}

func TestTTFTIndexedExpiryBoundariesAndStaleDeadline(t *testing.T) {
	start := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	now := start
	g := newOpenAITTFTGuardWithOptions(8, 15*time.Minute, func() time.Time { return now })
	cfg := enabledOpenAITTFTGuardConfig(20*time.Second, 5)
	slow := 60000
	g.reportForKey(1, 1, 10, "a", true, &slow, cfg)
	now = start.Add(time.Minute)
	g.reportForKey(1, 1, 10, "a", true, &slow, cfg)
	// Rescheduling updates one indexed item rather than retaining stale heap nodes.
	require.Len(t, g.expiries, 2)
	now = start.Add(15 * time.Minute)
	require.True(t, g.isDegraded(1, 1, "a"))
	now = start.Add(16*time.Minute - time.Nanosecond)
	require.True(t, g.isDegraded(1, 1, "a"))
	now = now.Add(time.Nanosecond)
	require.False(t, g.isDegraded(1, 1, "a"))
	require.Len(t, g.entries, 1, "expired protection keeps its revalidation tombstone")
	for _, entry := range g.entries {
		require.Equal(t, "insufficient", entry.stateAt(now, g.ttl))
		require.True(t, entry.needsRevalidation)
	}
	require.Len(t, g.observations, 1)
	now = start.Add(31 * time.Minute)
	g.size()
	require.Empty(t, g.observations)
	require.Empty(t, g.observationModels)
	require.Empty(t, g.observationAccounts)
	require.Len(t, g.projectionKeys, 1, "bounded tombstone survives raw observation expiry")
	require.Empty(t, g.expiryItems)
	svc := &OpenAIGatewayService{openaiTTFTGuard: g}
	views := svc.TTFTProtectionSnapshotsBatchAt([]TTFTProtectionSnapshotRequest{{AccountID: 1, KeyID: 10, GroupID: 1, Config: cfg}}, now)
	require.Len(t, views, 1)
	require.Len(t, views[0], 1, "snapshot keeps the projection tombstone visible after raw expiry")
	require.Equal(t, "a", views[0][0].Model)
	require.Equal(t, "insufficient", views[0][0].State)
	require.Empty(t, g.degradations([]int64{1}), "expired raw evidence is not an active degradation")
}

func TestTTFTBatchSnapshotConcurrentReportsAndCallerOwnership(t *testing.T) {
	now := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	g := newOpenAITTFTGuardWithOptions(128, 15*time.Minute, func() time.Time { return now })
	svc := &OpenAIGatewayService{openaiTTFTGuard: g}
	cfg := enabledOpenAITTFTGuardConfig(20*time.Second, 5)
	requests := []TTFTProtectionSnapshotRequest{{AccountID: 1, KeyID: 10, GroupID: 1, Config: cfg}, {AccountID: 2, KeyID: 20, GroupID: 1, Config: cfg}}
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			ms := 1000
			for i := 0; i < 200; i++ {
				if worker%2 == 0 {
					g.reportForKey(1, int64(worker%2+1), 10, "model", true, &ms, cfg)
				} else {
					svc.TTFTProtectionSnapshotsBatch(requests)
				}
			}
		}(worker)
	}
	wg.Wait()
	views := svc.TTFTProtectionSnapshotsBatch(requests)
	require.Len(t, views, 2)
	require.NotEmpty(t, views[0])
	views[0][0].State = "changed"
	require.NotEqual(t, "changed", svc.TTFTProtectionSnapshotsBatch(requests)[0][0].State)
	require.Empty(t, g.groupProbes, "administrative reads never advance recovery cadence")
}

func TestProtectionPolicyRevisionChangesOnlyWithContent(t *testing.T) {
	settings := NewSettingService(&openAITTFTGuardSettingsRepoStub{values: map[string]string{}}, nil)
	repo := &schedulingProtectionRepoStub{records: []GroupSchedulingProtectionPolicyRecord{{TTFT: GroupTTFTGuardPolicyRecord{GroupID: 1, GroupPlatform: PlatformOpenAI, GroupName: "first"}}}}
	s := NewGroupSchedulingProtectionPolicyService(repo, settings, nil, nil)
	first := s.ProtectionPoliciesSnapshot()
	require.Equal(t, uint64(1), first.Revision())
	require.NoError(t, s.RefreshProtectionPolicies(context.Background()))
	require.Same(t, first, s.ProtectionPoliciesSnapshot())
	repo.records[0].TTFT.GroupName = "next"
	require.NoError(t, s.RefreshProtectionPolicies(context.Background()))
	next := s.ProtectionPoliciesSnapshot()
	require.Equal(t, uint64(2), next.Revision())
	p, _ := first.Policy(1)
	require.Equal(t, "first", p.TTFT.GroupName)
	p, _ = next.Policy(1)
	require.Equal(t, "next", p.TTFT.GroupName)
	p.Cache.GroupIDs = append(p.Cache.GroupIDs, 99)
	check, _ := s.ProtectionPolicy(1)
	require.Empty(t, check.Cache.GroupIDs)
	svc := &OpenAIGatewayService{groupProtectionPolicyResolver: s}
	svc.syncTTFTProtectionPolicies()
	g := svc.getOpenAITTFTGuard()
	require.Same(t, next, g.policySnapshot)
	g.inheritedKnown = true
	svc.syncTTFTProtectionPolicies()
	require.True(t, g.inheritedKnown, "unchanged revision must skip runtime resynchronization")
}

func BenchmarkTTFTProtectionPerformance(b *testing.B) {
	for _, n := range []int{100, 1000, 4096} {
		for _, baseline := range []bool{true, false} {
			label := "indexed"
			if baseline {
				label = "baseline"
			}
			b.Run(fmt.Sprintf("report/%d/%s", n, label), func(b *testing.B) {
				now := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
				clock := func() time.Time { return now }
				cfg := enabledOpenAITTFTGuardConfig(20*time.Second, 5)
				ms := 1000
				g := newOpenAITTFTGuardWithOptions(4096, 15*time.Minute, clock)
				ref := newReferenceTTFTGuardWithOptions(4096, 15*time.Minute, clock)
				for i := 1; i <= n; i++ {
					if baseline {
						ref.reportForKey(1, int64(i), int64(i), "model", true, &ms, cfg)
					} else {
						g.reportForKey(1, int64(i), int64(i), "model", true, &ms, cfg)
					}
				}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					id := int64(i%n + 1)
					if baseline {
						ref.reportForKey(1, id, id, "model", true, &ms, cfg)
					} else {
						g.reportForKey(1, id, id, "model", true, &ms, cfg)
					}
				}
			})
			for _, page := range []int{20, 100} {
				b.Run(fmt.Sprintf("admin/%d/page%d/%s", n, page, label), func(b *testing.B) {
					now := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
					clock := func() time.Time { return now }
					cfg := enabledOpenAITTFTGuardConfig(20*time.Second, 5)
					ms := 60000
					g := newOpenAITTFTGuardWithOptions(4096, 15*time.Minute, clock)
					ref := newReferenceTTFTGuardWithOptions(4096, 15*time.Minute, clock)
					for i := 1; i <= n; i++ {
						if baseline {
							ref.reportForKey(1, int64(i), int64(i), "model", true, &ms, cfg)
						} else {
							g.reportForKey(1, int64(i), int64(i), "model", true, &ms, cfg)
						}
					}
					svc := &OpenAIGatewayService{openaiTTFTGuard: g}
					requests := make([]TTFTProtectionSnapshotRequest, page)
					for i := range requests {
						id := int64(i%n + 1)
						requests[i] = TTFTProtectionSnapshotRequest{AccountID: id, KeyID: id, GroupID: 1, Config: cfg}
					}
					b.ReportAllocs()
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						if baseline {
							for _, r := range requests {
								ref.snapshots(r.AccountID, r.KeyID, r.GroupID, r.Config)
							}
						} else {
							svc.TTFTProtectionSnapshotsBatch(requests)
						}
					}
				})
			}
		}
	}
}

func TestTTFTManagedPolicyRejectsOlderRequestConfig(t *testing.T) {
	now := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	g := newOpenAITTFTGuardWithOptions(128, 15*time.Minute, func() time.Time { return now })
	old := enabledOpenAITTFTGuardConfig(20*time.Second, 5)
	current := enabledOpenAITTFTGuardConfig(10*time.Second, 5)
	g.groupConfigs[1] = current
	g.policiesManaged = true
	g.recountActivePoliciesLocked()
	ms := 30000
	for i := 0; i < 5; i++ {
		g.reportForKey(1, 1, 10, "model", true, &ms, old)
	}
	candidates := []openAITTFTGuardCandidate{{groupID: 1, accountID: 1, upstreamKeyID: 10, model: "model"}}
	require.Empty(t, g.qualityAt(candidates, old, now))
	require.Empty(t, g.exclusionsAt(candidates, nil, old, now))
	require.Nil(t, g.claimTrial(1, &Account{ID: 1}, "model", old))
	require.Empty(t, g.groupProbes, "old request must not consume a recovery slot")
	require.Equal(t, current, g.groupConfigs[1])
	require.NotEmpty(t, g.qualityAt(candidates, current, now), "completion remains evidence under current policy")
	require.Equal(t, uint64(5), g.observations[openAITTFTGuardKey{accountID: 1, upstreamKeyID: 10, model: "model"}].sampleCount)
	g.clearForConfig(1, old)
	g.clearGroup(1)
	g.clearInheritedConfigMismatch(old)
	require.Equal(t, current, g.groupConfigs[1])
}
