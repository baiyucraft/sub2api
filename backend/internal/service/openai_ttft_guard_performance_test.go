package service

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTTFTIndexedRuntimeMatchesFrozenReference(t *testing.T) {
	now := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	g := newOpenAITTFTGuardWithOptions(4096, 15*time.Minute, clock)
	ref := newReferenceTTFTGuardWithOptions(4096, 15*time.Minute, clock)
	svc := &OpenAIGatewayService{openaiTTFTGuard: g}
	rng := rand.New(rand.NewSource(1016))
	configs := []OpenAITTFTGuardConfigSnapshot{
		{Enabled: true, Threshold: 20 * time.Second, MinSamples: 5, Source: "global"},
		{Enabled: true, Threshold: 20 * time.Second, MinSamples: 5, Source: "group"},
		{Enabled: true, Threshold: 40 * time.Second, MinSamples: 12, Source: "group"},
	}
	for step := 0; step < 1200; step++ {
		group := rng.Intn(len(configs))
		cfg := configs[group]
		groupID := int64(group + 1)
		id := int64(rng.Intn(16) + 1)
		key := int64(100 + rng.Intn(3))
		model := fmt.Sprintf("m%d", rng.Intn(3))
		now = now.Add(time.Duration(rng.Intn(20)) * time.Second)
		if step%113 == 0 {
			now = now.Add(15*time.Minute - time.Nanosecond)
		}
		if step%67 == 0 {
			now = now.Add(-time.Second)
		}
		if step%91 == 0 {
			cfg.Enabled = !cfg.Enabled
			configs[group] = cfg
		}
		ms := []int{1000, 10000, 20000, 30000, 60000, 120000}[rng.Intn(6)]
		ok := rng.Intn(4) != 0
		g.reportForKey(groupID, id, key, model, ok, &ms, cfg)
		ref.reportForKey(groupID, id, key, model, ok, &ms, cfg)
		candidates := make([]openAITTFTGuardCandidate, 0, 16)
		referenceCandidates := make([]referenceTTFTGuardCandidate, 0, 16)
		for account := int64(1); account <= 16; account++ {
			candidates = append(candidates, openAITTFTGuardCandidate{groupID: groupID, accountID: account, upstreamKeyID: key, model: model})
			referenceCandidates = append(referenceCandidates, referenceTTFTGuardCandidate{groupID: groupID, accountID: account, upstreamKeyID: key, model: model})
		}
		tickets := map[int64]*openAITTFTTrial{}
		referenceTickets := map[int64]*referenceTTFTTrial{}
		require.Equal(t, ref.exclusions(referenceCandidates, nil, cfg, referenceTickets), g.exclusions(candidates, nil, cfg, tickets), "exclusions step %d", step)
		for id, ticket := range tickets {
			require.NotNil(t, referenceTickets[id])
			ticket.finish(ok, &ms)
			referenceTickets[id].finish(ok, &ms)
		}
		require.Equal(t, ref.quality(referenceCandidates, cfg), g.quality(candidates, cfg), "quality step %d", step)
		request := TTFTProtectionSnapshotRequest{AccountID: id, KeyID: key, GroupID: groupID, Config: cfg}
		require.Equal(t, ref.snapshots(id, key, groupID, cfg), svc.TTFTProtectionSnapshotsBatch([]TTFTProtectionSnapshotRequest{request})[0], "snapshot step %d", step)
		if step%17 == 0 {
			require.Equal(t, ref.degradations([]int64{1, 3, 7, 11, 16}), g.degradations([]int64{1, 3, 7, 11, 16}), "degradations step %d", step)
		}
		require.Equal(t, len(g.entries)+len(g.observations), len(g.expiryItems))
	}
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
	require.Empty(t, g.entries)
	require.Len(t, g.observations, 1)
	now = start.Add(31 * time.Minute)
	g.size()
	require.Empty(t, g.observations)
	require.Empty(t, g.observationModels)
	require.Empty(t, g.observationAccounts)
	require.Empty(t, g.projectionKeys)
	require.Empty(t, g.expiryItems)
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
