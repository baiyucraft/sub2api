package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func enabledOpenAITTFTGuardConfig(threshold time.Duration, minSamples int) OpenAITTFTGuardConfigSnapshot {
	return OpenAITTFTGuardConfigSnapshot{Enabled: true, Threshold: threshold, MinSamples: minSamples, Source: "global", GroupName: "test-group"}
}

func ttftGuardTestGroupID(value int64) *int64 { return &value }

func TestOpenAITTFTGuard_DegradationTriggers(t *testing.T) {
	t.Run("single critical sample", func(t *testing.T) {
		guard := newOpenAITTFTGuard()
		cfg := enabledOpenAITTFTGuardConfig(20*time.Second, 5)
		ttft := 60_000
		guard.report(100, 1, "gpt-test", true, &ttft, cfg)
		require.True(t, guard.isDegraded(100, 1, "gpt-test"))
	})

	t.Run("two consecutive elevated samples", func(t *testing.T) {
		guard := newOpenAITTFTGuard()
		cfg := enabledOpenAITTFTGuardConfig(20*time.Second, 5)
		ttft := 30_000
		guard.report(100, 1, "gpt-test", true, &ttft, cfg)
		require.False(t, guard.isDegraded(100, 1, "gpt-test"))
		guard.report(100, 1, "gpt-test", true, &ttft, cfg)
		require.True(t, guard.isDegraded(100, 1, "gpt-test"))
	})

	t.Run("recent ten sample EWMA uses cumulative minimum", func(t *testing.T) {
		guard := newOpenAITTFTGuard()
		cfg := enabledOpenAITTFTGuardConfig(20*time.Second, 20)
		ttft := 21_000
		for i := 0; i < 19; i++ {
			guard.report(100, 1, "gpt-test", true, &ttft, cfg)
		}
		require.False(t, guard.isDegraded(100, 1, "gpt-test"))
		guard.report(100, 1, "gpt-test", true, &ttft, cfg)
		require.True(t, guard.isDegraded(100, 1, "gpt-test"))
	})
}

func TestOpenAITTFTGuard_RecoveryNeedsThreeSuccessfulFastProbes(t *testing.T) {
	guard := newOpenAITTFTGuard()
	cfg := enabledOpenAITTFTGuardConfig(20*time.Second, 5)
	critical := 60_000
	guard.report(100, 1, "gpt-test", true, &critical, cfg)

	fast := 12_000
	guard.report(100, 1, "gpt-test", true, &fast, cfg)
	guard.report(100, 1, "gpt-test", false, &fast, cfg)
	guard.report(100, 1, "gpt-test", true, &fast, cfg)
	require.True(t, guard.isDegraded(100, 1, "gpt-test"), "failed fast samples reset the recovery streak")
	guard.report(100, 1, "gpt-test", true, &fast, cfg)
	guard.report(100, 1, "gpt-test", true, &fast, cfg)
	require.False(t, guard.isDegraded(100, 1, "gpt-test"))
}

func TestOpenAITTFTGuard_GlobalFivePercentProbe(t *testing.T) {
	guard := newOpenAITTFTGuard()
	cfg := enabledOpenAITTFTGuardConfig(20*time.Second, 5)
	critical := 60_000
	guard.report(100, 1, "gpt-test", true, &critical, cfg)
	guard.report(100, 2, "gpt-test", true, &critical, cfg)
	candidates := []openAITTFTGuardCandidate{{groupID: 100, accountID: 1, model: "gpt-test"}, {groupID: 100, accountID: 2, model: "gpt-test"}}

	for i := 1; i < openAITTFTGuardProbeEvery; i++ {
		excluded := guard.exclusions(candidates, nil, cfg)
		require.Len(t, excluded, 2)
	}
	firstProbe := guard.exclusions(candidates, nil, cfg)
	require.Len(t, firstProbe, 1)
	require.NotContains(t, firstProbe, int64(1))

	for i := 1; i < openAITTFTGuardProbeEvery; i++ {
		guard.exclusions(candidates, nil, cfg)
	}
	secondProbe := guard.exclusions(candidates, nil, cfg)
	require.Len(t, secondProbe, 1)
	require.NotContains(t, secondProbe, int64(2))
}

func TestOpenAITTFTGuard_ProbeCadenceIsIsolatedByGroupAndCanonicalModel(t *testing.T) {
	guard := newOpenAITTFTGuard()
	cfg := enabledOpenAITTFTGuardConfig(20*time.Second, 5)
	critical := 60_000
	guard.report(100, 1, "model-a", true, &critical, cfg)
	guard.report(100, 2, "model-b", true, &critical, cfg)
	modelA := []openAITTFTGuardCandidate{{groupID: 100, accountID: 1, model: "model-a"}}
	modelB := []openAITTFTGuardCandidate{{groupID: 100, accountID: 2, model: "model-b"}}

	for i := 1; i < openAITTFTGuardProbeEvery; i++ {
		require.Contains(t, guard.exclusions(modelA, nil, cfg), int64(1))
	}
	require.Contains(t, guard.exclusions(modelB, nil, cfg), int64(2), "model-a traffic must not advance model-b probe cadence")
	require.NotContains(t, guard.exclusions(modelA, nil, cfg), int64(1))

	for i := 2; i < openAITTFTGuardProbeEvery; i++ {
		require.Contains(t, guard.exclusions(modelB, nil, cfg), int64(2))
	}
	require.NotContains(t, guard.exclusions(modelB, nil, cfg), int64(2))
}

func TestOpenAITTFTGuard_ClearGroupRemovesAllModelProbeState(t *testing.T) {
	guard := newOpenAITTFTGuard()
	cfg := enabledOpenAITTFTGuardConfig(20*time.Second, 5)
	critical := 60_000
	guard.report(100, 1, "model-a", true, &critical, cfg)
	guard.report(100, 2, "model-b", true, &critical, cfg)
	guard.report(200, 3, "model-c", true, &critical, cfg)
	guard.exclusions([]openAITTFTGuardCandidate{{groupID: 100, accountID: 1, model: "model-a"}}, nil, cfg)
	guard.exclusions([]openAITTFTGuardCandidate{{groupID: 100, accountID: 2, model: "model-b"}}, nil, cfg)
	guard.exclusions([]openAITTFTGuardCandidate{{groupID: 200, accountID: 3, model: "model-c"}}, nil, cfg)

	guard.clearGroup(100)

	for key := range guard.entries {
		require.NotEqual(t, int64(100), key.groupID)
	}
	for key := range guard.groupProbes {
		require.NotEqual(t, int64(100), key.groupID)
	}
	require.True(t, guard.isDegraded(200, 3, "model-c"), "clearing one group must not remove another group's state")
}

func TestOpenAITTFTGuard_GroupNameChangePreservesRuntimeState(t *testing.T) {
	guard := newOpenAITTFTGuard()
	cfg := enabledOpenAITTFTGuardConfig(20*time.Second, 5)
	cfg.GroupName = "before"
	critical := 60_000
	guard.report(100, 1, "gpt-test", true, &critical, cfg)

	renamed := cfg
	renamed.GroupName = "after"
	excluded := guard.exclusions([]openAITTFTGuardCandidate{{groupID: 100, accountID: 1, model: "gpt-test"}}, nil, renamed)

	require.Contains(t, excluded, int64(1), "display-name changes must not reset degraded state")
	snapshots := guard.degradations([]int64{1})
	require.Equal(t, "after", snapshots[1][0].GroupName)
	require.Equal(t, uint64(1), snapshots[1][0].SampleCount)
}

func TestOpenAITTFTGuard_StateIsIsolatedByGroupAndModel(t *testing.T) {
	guard := newOpenAITTFTGuard()
	cfg := enabledOpenAITTFTGuardConfig(20*time.Second, 5)
	critical := 60_000
	guard.report(100, 7, "gpt-slow", true, &critical, cfg)

	groupOne := guard.exclusions([]openAITTFTGuardCandidate{{groupID: 100, accountID: 7, model: "gpt-slow"}}, nil, cfg)
	groupTwo := guard.exclusions([]openAITTFTGuardCandidate{{groupID: 200, accountID: 7, model: "gpt-slow"}}, nil, cfg)
	require.Contains(t, groupOne, int64(7))
	require.NotContains(t, groupTwo, int64(7))

	otherModel := guard.exclusions([]openAITTFTGuardCandidate{{groupID: 100, accountID: 7, model: "gpt-fast"}}, nil, cfg)
	require.NotContains(t, otherModel, int64(7))
}

func TestOpenAITTFTGuard_DifferentGroupThresholdsAreIndependent(t *testing.T) {
	guard := newOpenAITTFTGuard()
	strict := enabledOpenAITTFTGuardConfig(10*time.Second, 5)
	strict.Source = "group"
	strict.GroupName = "strict"
	lenient := enabledOpenAITTFTGuardConfig(30*time.Second, 5)
	lenient.Source = "group"
	lenient.GroupName = "lenient"
	sample := 30_000

	guard.report(101, 7, "gpt-test", true, &sample, strict)
	guard.report(202, 7, "gpt-test", true, &sample, lenient)

	require.True(t, guard.isDegraded(101, 7, "gpt-test"))
	require.False(t, guard.isDegraded(202, 7, "gpt-test"))
}

func TestOpenAITTFTGuard_GlobalChangeClearsOnlyInheritedGroups(t *testing.T) {
	guard := newOpenAITTFTGuard()
	inherited := enabledOpenAITTFTGuardConfig(20*time.Second, 5)
	custom := enabledOpenAITTFTGuardConfig(20*time.Second, 5)
	custom.Source = "group"
	critical := 60_000
	guard.report(101, 7, "gpt-test", true, &critical, inherited)
	guard.report(202, 7, "gpt-test", true, &critical, custom)

	changedGlobal := enabledOpenAITTFTGuardConfig(21*time.Second, 5)
	guard.clearInheritedConfigMismatch(changedGlobal)

	require.False(t, guard.isDegraded(101, 7, "gpt-test"))
	require.True(t, guard.isDegraded(202, 7, "gpt-test"))
}

func TestOpenAITTFTGuard_TTLAndLRU(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	guard := newOpenAITTFTGuardWithOptions(2, 15*time.Minute, func() time.Time { return now })
	cfg := enabledOpenAITTFTGuardConfig(20*time.Second, 5)
	critical := 60_000
	guard.report(100, 1, "gpt-test", true, &critical, cfg)
	now = now.Add(time.Second)
	guard.report(100, 2, "gpt-test", true, &critical, cfg)
	now = now.Add(time.Second)
	require.True(t, guard.isDegraded(100, 1, "gpt-test"))
	now = now.Add(time.Second)
	guard.report(100, 3, "gpt-test", true, &critical, cfg)

	require.True(t, guard.isDegraded(100, 1, "gpt-test"))
	require.False(t, guard.isDegraded(100, 2, "gpt-test"), "least recently used entry must be evicted")
	require.True(t, guard.isDegraded(100, 3, "gpt-test"))

	now = now.Add(15 * time.Minute)
	require.Zero(t, guard.size())
}

func TestOpenAITTFTGuard_DegradationSnapshots(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	guard := newOpenAITTFTGuardWithOptions(32, 15*time.Minute, func() time.Time { return now })
	cfg := enabledOpenAITTFTGuardConfig(20*time.Second, 5)

	critical := 61_000
	guard.report(100, 1, "z-model", true, &critical, cfg)
	zDegradedAt := now
	now = now.Add(time.Minute)
	elevated := 31_000
	guard.report(100, 1, "a-model", true, &elevated, cfg)
	now = now.Add(time.Minute)
	guard.report(100, 1, "a-model", true, &elevated, cfg)
	aDegradedAt := now
	guard.report(100, 2, "other-model", true, &critical, cfg)

	key, ok := openAITTFTGuardKeyFor(100, 1, "a-model")
	require.True(t, ok)
	touchedAt := guard.entries[key].lastTouchedAt
	snapshots := guard.degradations([]int64{2, 1, 1, 0})
	require.Equal(t, touchedAt, guard.entries[key].lastTouchedAt, "snapshot reads must not refresh LRU timestamps")
	require.Len(t, snapshots, 2)
	require.Len(t, snapshots[1], 2)
	require.Equal(t, "a-model", snapshots[1][0].Model)
	require.Equal(t, "z-model", snapshots[1][1].Model)

	aSnapshot := snapshots[1][0]
	require.Equal(t, int64(100), aSnapshot.GroupID)
	require.Equal(t, "test-group", aSnapshot.GroupName)
	require.Equal(t, "global", aSnapshot.PolicySource)
	require.Equal(t, "consecutive_elevated", aSnapshot.Reason)
	require.Equal(t, int64(20_000), aSnapshot.ThresholdMs)
	require.Equal(t, int64(31_000), aSnapshot.LastTTFTMs)
	require.InDelta(t, 31_000, aSnapshot.EWMAms, 0.001)
	require.Equal(t, uint64(2), aSnapshot.SampleCount)
	require.Equal(t, aDegradedAt, aSnapshot.DegradedAt)
	require.Equal(t, now, aSnapshot.LastSampleAt)
	require.Equal(t, now.Add(15*time.Minute), aSnapshot.ExpiresAt)
	require.Zero(t, aSnapshot.RecoverySamples)
	require.Equal(t, openAITTFTGuardRecoverySamples, aSnapshot.RecoverySamplesRequired)

	zSnapshot := snapshots[1][1]
	require.Equal(t, "critical_sample", zSnapshot.Reason)
	require.Equal(t, zDegradedAt, zSnapshot.DegradedAt)
	require.Equal(t, int64(61_000), zSnapshot.LastTTFTMs)
}

func TestOpenAITTFTGuard_DegradationSnapshotsTrackRecoveryAndCleanup(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	guard := newOpenAITTFTGuardWithOptions(32, 15*time.Minute, func() time.Time { return now })
	cfg := enabledOpenAITTFTGuardConfig(20*time.Second, 5)
	critical := 60_000
	guard.report(100, 1, "gpt-test", true, &critical, cfg)

	fast := 10_000
	now = now.Add(time.Minute)
	guard.report(100, 1, "gpt-test", true, &fast, cfg)
	snapshots := guard.degradations([]int64{1})
	require.Equal(t, 1, snapshots[1][0].RecoverySamples)
	require.Equal(t, now.Add(15*time.Minute), snapshots[1][0].ExpiresAt)

	guard.report(100, 1, "gpt-test", true, &fast, cfg)
	guard.report(100, 1, "gpt-test", true, &fast, cfg)
	require.Empty(t, guard.degradations([]int64{1}), "recovery must remove the degradation snapshot")

	guard.report(100, 1, "gpt-test", true, &critical, cfg)
	now = now.Add(15 * time.Minute)
	require.Empty(t, guard.degradations([]int64{1}), "TTL cleanup must remove stale degradation snapshots")

	now = now.Add(time.Second)
	guard.report(100, 1, "gpt-test", true, &critical, cfg)
	disabled := cfg
	disabled.Enabled = false
	guard.exclusions([]openAITTFTGuardCandidate{{groupID: 100, accountID: 1, model: "gpt-test"}}, nil, disabled)
	require.Empty(t, guard.degradations([]int64{1}))
	require.Zero(t, guard.size(), "disabling the guard through a snapshot read must clear transient state")
}

func TestOpenAITTFTGuard_DegradationSnapshotReportsEWMAReason(t *testing.T) {
	guard := newOpenAITTFTGuard()
	cfg := enabledOpenAITTFTGuardConfig(20*time.Second, 5)
	ttft := 21_000
	for i := 0; i < 5; i++ {
		guard.report(100, 9, "gpt-ewma", true, &ttft, cfg)
	}

	snapshots := guard.degradations([]int64{9})
	require.Len(t, snapshots[9], 1)
	require.Equal(t, "ewma", snapshots[9][0].Reason)
}

func TestOpenAITTFTGuard_ConfigChangeClearsTransientState(t *testing.T) {
	guard := newOpenAITTFTGuard()
	cfg := enabledOpenAITTFTGuardConfig(20*time.Second, 5)
	critical := 60_000
	guard.report(100, 1, "gpt-test", true, &critical, cfg)
	require.True(t, guard.isDegraded(100, 1, "gpt-test"))

	changed := enabledOpenAITTFTGuardConfig(21*time.Second, 5)
	require.Empty(t, guard.exclusions([]openAITTFTGuardCandidate{{groupID: 100, accountID: 1, model: "gpt-test"}}, nil, changed))
	require.Zero(t, guard.size())
	guard.report(100, 1, "gpt-test", true, &critical, changed)
	require.False(t, guard.isDegraded(100, 1, "gpt-test"), "60s is below the new 3T critical threshold")

	disabled := changed
	disabled.Enabled = false
	guard.exclusions([]openAITTFTGuardCandidate{{groupID: 100, accountID: 1, model: "gpt-test"}}, nil, disabled)
	require.Zero(t, guard.size())
}

func TestOpenAITTFTGuard_ConcurrentAccess(t *testing.T) {
	guard := newOpenAITTFTGuardWithOptions(4096, 15*time.Minute, time.Now)
	cfg := enabledOpenAITTFTGuardConfig(20*time.Second, 5)
	critical := 60_000
	fast := 5_000

	var wg sync.WaitGroup
	for worker := 0; worker < 32; worker++ {
		worker := worker
		wg.Add(1)
		go func() {
			defer wg.Done()
			for iteration := 0; iteration < 100; iteration++ {
				accountID := int64(worker*100 + iteration + 1)
				model := "gpt-concurrent"
				sample := &fast
				if iteration%3 == 0 {
					sample = &critical
				}
				guard.report(100, accountID, model, true, sample, cfg)
				guard.exclusions([]openAITTFTGuardCandidate{{groupID: 100, accountID: accountID, model: model}}, nil, cfg)
				guard.degradations([]int64{accountID})
				_ = guard.isDegraded(100, accountID, model)
			}
		}()
	}
	wg.Wait()

	require.LessOrEqual(t, guard.size(), 4096)
}

func TestNormalizeOpenAITTFTGuardConfig_InvalidProviderFailsOpen(t *testing.T) {
	invalid := normalizeOpenAITTFTGuardConfig(OpenAITTFTGuardConfigSnapshot{Enabled: true, Threshold: time.Second, MinSamples: 1})
	require.False(t, invalid.Enabled)
	require.Equal(t, defaultOpenAITTFTGuardThreshold, invalid.Threshold)
	require.Equal(t, defaultOpenAITTFTGuardMinSamples, invalid.MinSamples)
}

type openAITTFTGuardPolicyResolverStub struct {
	policy GroupTTFTGuardResolvedPolicy
	err    error
}

func (s *openAITTFTGuardPolicyResolverStub) Resolve(context.Context, int64) (GroupTTFTGuardResolvedPolicy, error) {
	return s.policy, s.err
}

func (s *openAITTFTGuardPolicyResolverStub) Invalidate(int64) {}

func TestOpenAIGatewayService_TTFTGuardPolicyReadFailureFailsOpenWithoutClearingState(t *testing.T) {
	groupID := int64(100)
	resolver := &openAITTFTGuardPolicyResolverStub{policy: GroupTTFTGuardResolvedPolicy{
		GroupID: groupID, GroupName: "pro", Enabled: true, Threshold: 20 * time.Second, MinSamples: 5, Source: GroupTTFTGuardSourceGroup,
	}}
	svc := &OpenAIGatewayService{groupTTFTGuardPolicyResolver: resolver}
	svc.SetOpenAITTFTGuardUpstreamOnly(false)
	critical := 60_000
	svc.reportOpenAITTFTGuard(groupID, 1, "gpt-test", true, &critical)
	require.True(t, svc.getOpenAITTFTGuard().isDegraded(groupID, 1, "gpt-test"))

	resolver.err = errors.New("temporary policy read failure")
	excluded := svc.openAITTFTGuardExclusions(context.Background(), &groupID, PlatformOpenAI, "gpt-test", OpenAIUpstreamTransportAny, "", "", false, nil)

	require.Empty(t, excluded, "policy read failures must fail open")
	require.True(t, svc.getOpenAITTFTGuard().isDegraded(groupID, 1, "gpt-test"), "policy read failures must preserve accumulated state")
}

func TestOpenAIGatewayService_TTFTGuardExplicitDisabledClearsState(t *testing.T) {
	groupID := int64(100)
	resolver := &openAITTFTGuardPolicyResolverStub{policy: GroupTTFTGuardResolvedPolicy{
		GroupID: groupID, GroupName: "pro", Enabled: true, Threshold: 20 * time.Second, MinSamples: 5, Source: GroupTTFTGuardSourceGroup,
	}}
	svc := &OpenAIGatewayService{groupTTFTGuardPolicyResolver: resolver}
	svc.SetOpenAITTFTGuardUpstreamOnly(false)
	critical := 60_000
	svc.reportOpenAITTFTGuard(groupID, 1, "gpt-test", true, &critical)

	resolver.policy.Enabled = false
	resolver.policy.Source = GroupTTFTGuardSourceDisabled
	svc.openAITTFTGuardExclusions(context.Background(), &groupID, PlatformOpenAI, "gpt-test", OpenAIUpstreamTransportAny, "", "", false, nil)

	require.False(t, svc.getOpenAITTFTGuard().isDegraded(groupID, 1, "gpt-test"))
}

func TestOpenAIGatewayService_TTFTGuardOnlyValidGlobalDisableClearsState(t *testing.T) {
	groupID := int64(100)
	critical := 60_000
	enabled := enabledOpenAITTFTGuardConfig(20*time.Second, 5)

	t.Run("invalid global snapshot preserves state", func(t *testing.T) {
		svc := &OpenAIGatewayService{}
		svc.SetOpenAITTFTGuardUpstreamOnly(false)
		svc.getOpenAITTFTGuard().report(groupID, 1, "gpt-test", true, &critical, enabled)
		svc.SetOpenAITTFTGuardConfigProvider(openAITTFTGuardConfigProviderFunc(func() OpenAITTFTGuardConfigSnapshot {
			return OpenAITTFTGuardConfigSnapshot{Enabled: false, Threshold: time.Second, MinSamples: 1, Source: GroupTTFTGuardSourceGlobal}
		}))

		svc.openAITTFTGuardExclusions(context.Background(), &groupID, PlatformOpenAI, "gpt-test", OpenAIUpstreamTransportAny, "", "", false, nil)
		require.True(t, svc.getOpenAITTFTGuard().isDegraded(groupID, 1, "gpt-test"))
	})

	t.Run("valid global disable clears state", func(t *testing.T) {
		svc := &OpenAIGatewayService{}
		svc.SetOpenAITTFTGuardUpstreamOnly(false)
		svc.getOpenAITTFTGuard().report(groupID, 1, "gpt-test", true, &critical, enabled)
		svc.SetOpenAITTFTGuardConfigProvider(openAITTFTGuardConfigProviderFunc(func() OpenAITTFTGuardConfigSnapshot {
			return OpenAITTFTGuardConfigSnapshot{Enabled: false, Threshold: 20 * time.Second, MinSamples: 5, Source: GroupTTFTGuardSourceGlobal}
		}))

		svc.openAITTFTGuardExclusions(context.Background(), &groupID, PlatformOpenAI, "gpt-test", OpenAIUpstreamTransportAny, "", "", false, nil)
		require.False(t, svc.getOpenAITTFTGuard().isDegraded(groupID, 1, "gpt-test"))
	})
}

func TestOpenAIGatewayService_TTFTGuardMappedModelCrossesPriority(t *testing.T) {
	ctx := context.Background()
	groupID := int64(901)
	accounts := []Account{
		{
			ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true,
			Concurrency: 1, Priority: 2,
			Credentials: map[string]any{"model_mapping": map[string]any{"client-model": "upstream-slow"}},
		},
		{
			ID: 2, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true,
			Concurrency: 1, Priority: 3,
			Credentials: map[string]any{"model_mapping": map[string]any{"client-model": "upstream-fast"}},
		},
	}
	svc := &OpenAIGatewayService{
		accountRepo:        schedulerTestOpenAIAccountRepo{accounts: accounts},
		cfg:                &config.Config{},
		rateLimitService:   newOpenAIAdvancedSchedulerRateLimitService("true"),
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
	}
	svc.SetOpenAITTFTGuardConfigProvider(openAITTFTGuardConfigProviderFunc(func() OpenAITTFTGuardConfigSnapshot {
		return enabledOpenAITTFTGuardConfig(20*time.Second, 5)
	}))
	critical := 60_000
	svc.ReportOpenAIAccountScheduleResultForGroup(&groupID, &accounts[0], "upstream-slow", true, &critical)

	selection, _, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", "", "client-model", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.Equal(t, int64(2), selection.Account.ID)
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func TestOpenAIGatewayService_TTFTGuardFailOpenKeepsOnlyAccount(t *testing.T) {
	ctx := context.Background()
	groupID := int64(100)
	account := Account{ID: 11, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 2, GroupIDs: []int64{groupID}}
	svc := &OpenAIGatewayService{
		accountRepo:        schedulerTestOpenAIAccountRepo{accounts: []Account{account}},
		cfg:                &config.Config{},
		rateLimitService:   newOpenAIAdvancedSchedulerRateLimitService("true"),
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
	}
	svc.SetOpenAITTFTGuardConfigProvider(openAITTFTGuardConfigProviderFunc(func() OpenAITTFTGuardConfigSnapshot {
		return enabledOpenAITTFTGuardConfig(20*time.Second, 5)
	}))
	critical := 60_000
	svc.ReportOpenAIAccountScheduleResultForGroup(&groupID, &account, "gpt-test", true, &critical)

	selection, _, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", "", "gpt-test", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.Equal(t, account.ID, selection.Account.ID)
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func TestOpenAIGatewayService_TTFTGuardDoesNotMutateCallerExclusions(t *testing.T) {
	ctx := context.Background()
	groupID := int64(100)
	accounts := []Account{
		{ID: 12, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 2},
		{ID: 13, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 3},
	}
	svc := &OpenAIGatewayService{
		accountRepo:        schedulerTestOpenAIAccountRepo{accounts: accounts},
		cfg:                &config.Config{},
		rateLimitService:   newOpenAIAdvancedSchedulerRateLimitService("true"),
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
	}
	svc.SetOpenAITTFTGuardConfigProvider(openAITTFTGuardConfigProviderFunc(func() OpenAITTFTGuardConfigSnapshot {
		return enabledOpenAITTFTGuardConfig(20*time.Second, 5)
	}))
	critical := 60_000
	svc.SetOpenAITTFTGuardUpstreamOnly(false)
	svc.ReportOpenAIAccountScheduleResultForGroup(&groupID, &accounts[0], "gpt-test", true, &critical)
	callerExcluded := map[int64]struct{}{13: {}}

	selection, _, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", "", "gpt-test", callerExcluded, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.Equal(t, int64(12), selection.Account.ID, "fail-open must retain the caller exclusion")
	require.Equal(t, map[int64]struct{}{13: {}}, callerExcluded)
	if selection != nil && selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func TestOpenAIGatewayService_UpstreamHealthExclusionAndFailOpen(t *testing.T) {
	ctx := context.Background()
	configID := int64(93000)
	slowKeyID, healthyKeyID := int64(93001), int64(93002)
	registry := GlobalUpstreamHealthRegistry()
	registry.Hydrate(UpstreamHealthSnapshot{KeyID: slowKeyID, Status: UpstreamHealthSuspended, ObservationEnabled: true})
	registry.Hydrate(UpstreamHealthSnapshot{KeyID: healthyKeyID, Status: UpstreamHealthHealthy, ObservationEnabled: true})
	t.Cleanup(func() {
		registry.Hydrate(UpstreamHealthSnapshot{KeyID: slowKeyID, Status: UpstreamHealthHealthy, ObservationEnabled: true})
		registry.Hydrate(UpstreamHealthSnapshot{KeyID: healthyKeyID, Status: UpstreamHealthHealthy, ObservationEnabled: true})
	})

	accounts := []Account{
		{ID: 31, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 2, UpstreamConfigID: &configID, UpstreamKeyID: &slowKeyID},
		{ID: 32, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 3, UpstreamConfigID: &configID, UpstreamKeyID: &healthyKeyID},
	}
	svc := &OpenAIGatewayService{
		accountRepo:                 schedulerTestOpenAIAccountRepo{accounts: accounts},
		cfg:                         &config.Config{},
		rateLimitService:            newOpenAIAdvancedSchedulerRateLimitService("true"),
		concurrencyService:          NewConcurrencyService(schedulerTestConcurrencyCache{}),
		openaiTTFTGuardUpstreamOnly: true,
	}

	selection, _, err := svc.SelectAccountWithScheduler(ctx, nil, "", "", "gpt-test", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.Equal(t, int64(32), selection.Account.ID)
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}

	callerExcluded := map[int64]struct{}{32: {}}
	selection, _, err = svc.SelectAccountWithScheduler(ctx, nil, "", "", "gpt-test", callerExcluded, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.Equal(t, int64(31), selection.Account.ID, "health fail-open must retain caller exclusions")
	require.Equal(t, map[int64]struct{}{32: {}}, callerExcluded)
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func TestOpenAIGatewayService_TTFTGuardPreservesStickyBinding(t *testing.T) {
	for _, advanced := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "advanced"}[advanced], func(t *testing.T) {
			ctx := context.Background()
			groupID := int64(902)
			accounts := []Account{
				{ID: 21, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 2},
				{ID: 22, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 3},
			}
			cache := &schedulerTestGatewayCache{sessionBindings: map[string]int64{"sticky": 21}}
			svc := &OpenAIGatewayService{
				accountRepo:        schedulerTestOpenAIAccountRepo{accounts: accounts},
				cache:              cache,
				cfg:                &config.Config{},
				rateLimitService:   newOpenAIAdvancedSchedulerRateLimitService(map[bool]string{false: "false", true: "true"}[advanced]),
				concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
			}
			svc.SetOpenAITTFTGuardConfigProvider(openAITTFTGuardConfigProviderFunc(func() OpenAITTFTGuardConfigSnapshot {
				return enabledOpenAITTFTGuardConfig(20*time.Second, 5)
			}))
			critical := 60_000
			svc.SetOpenAITTFTGuardUpstreamOnly(false)
			svc.ReportOpenAIAccountScheduleResultForGroup(&groupID, &accounts[0], "gpt-test", true, &critical)

			selection, _, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", "sticky", "gpt-test", nil, OpenAIUpstreamTransportAny, false)
			require.NoError(t, err)
			require.NotNil(t, selection)
			require.Equal(t, int64(22), selection.Account.ID)
			require.Equal(t, int64(21), cache.sessionBindings["sticky"])
			if selection.ReleaseFunc != nil {
				selection.ReleaseFunc()
			}
		})
	}
}

func TestOpenAITTFTGuardContextPreservesOnlyExcludedStickyAccount(t *testing.T) {
	ctx := withOpenAITTFTGuardExcludedIDs(context.Background(), map[int64]struct{}{23: {}})
	require.True(t, openAITTFTGuardExcludedAccount(ctx, 23))
	require.False(t, openAITTFTGuardExcludedAccount(ctx, 24), "a new session without the excluded sticky account must still bind normally")
}

func TestOpenAIGatewayService_TTFTGuardReportsWhenAdvancedSchedulerDisabled(t *testing.T) {
	svc := &OpenAIGatewayService{
		cfg:              &config.Config{},
		rateLimitService: newOpenAIAdvancedSchedulerRateLimitService("false"),
	}
	svc.SetOpenAITTFTGuardConfigProvider(openAITTFTGuardConfigProviderFunc(func() OpenAITTFTGuardConfigSnapshot {
		return enabledOpenAITTFTGuardConfig(20*time.Second, 5)
	}))
	svc.SetOpenAITTFTGuardUpstreamOnly(false)
	critical := 60_000
	svc.ReportOpenAIAccountScheduleResultForGroup(ttftGuardTestGroupID(100), 31, "gpt-test", true, &critical)
	require.True(t, svc.getOpenAITTFTGuard().isDegraded(100, 31, "gpt-test"))
}

func TestOpenAIGatewayService_TTFTGuardUpstreamOnlyUsesSchedulerEligibility(t *testing.T) {
	ctx := context.Background()
	configID, keyID := int64(71), int64(72)
	upstream := Account{
		ID: 61, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true,
		Concurrency: 1, Priority: 1, UpstreamConfigID: &configID, UpstreamKeyID: &keyID, GroupIDs: []int64{100},
	}
	ordinary := Account{ID: 62, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 2, GroupIDs: []int64{100}}
	svc := &OpenAIGatewayService{
		accountRepo:                 schedulerTestOpenAIAccountRepo{accounts: []Account{upstream, ordinary}},
		cfg:                         &config.Config{},
		rateLimitService:            newOpenAIAdvancedSchedulerRateLimitService("true"),
		concurrencyService:          NewConcurrencyService(schedulerTestConcurrencyCache{}),
		openaiTTFTGuardUpstreamOnly: true,
	}
	svc.SetOpenAITTFTGuardConfigProvider(openAITTFTGuardConfigProviderFunc(func() OpenAITTFTGuardConfigSnapshot {
		return enabledOpenAITTFTGuardConfig(20*time.Second, 5)
	}))

	groupID := int64(100)
	_, _, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", "", "gpt-test", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	critical := 60_000
	svc.ReportOpenAIAccountScheduleResultForGroup(&groupID, &upstream, "gpt-test", true, &critical)
	svc.ReportOpenAIAccountScheduleResultForGroup(&groupID, &ordinary, "gpt-test", true, &critical)

	require.True(t, svc.getOpenAITTFTGuard().isDegraded(100, upstream.ID, "gpt-test"))
	require.False(t, svc.getOpenAITTFTGuard().isDegraded(100, ordinary.ID, "gpt-test"))
}

func TestOpenAIGatewayService_TTFTGuardMovablePreviousResponseStillApplies(t *testing.T) {
	ctx := context.Background()
	groupID := int64(100)
	accounts := []Account{
		{ID: 41, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 2},
		{ID: 42, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 3},
	}
	svc := &OpenAIGatewayService{
		accountRepo:        schedulerTestOpenAIAccountRepo{accounts: accounts},
		cfg:                &config.Config{},
		rateLimitService:   newOpenAIAdvancedSchedulerRateLimitService("true", "true"),
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
	}
	svc.SetOpenAITTFTGuardConfigProvider(openAITTFTGuardConfigProviderFunc(func() OpenAITTFTGuardConfigSnapshot {
		return enabledOpenAITTFTGuardConfig(20*time.Second, 5)
	}))
	critical := 60_000
	svc.SetOpenAITTFTGuardUpstreamOnly(false)
	svc.ReportOpenAIAccountScheduleResultForGroup(&groupID, &accounts[0], "gpt-test", true, &critical)

	selection, _, err := svc.SelectAccountWithSchedulerForCapability(
		ctx, &groupID, "response-can-move", "", "gpt-test", nil, OpenAIUpstreamTransportAny,
		OpenAIEndpointCapabilityResponses, false, true, true,
	)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.Equal(t, int64(42), selection.Account.ID)
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func TestOpenAIGatewayService_TTFTGuardHardPreviousResponseSkipsOverlay(t *testing.T) {
	ctx := context.Background()
	groupID := int64(903)
	account := Account{
		ID: 51, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 2,
		GroupIDs: []int64{groupID},
		Extra: map[string]any{
			"openai_apikey_responses_websockets_v2_enabled": true,
		},
	}
	fastAccount := Account{ID: 52, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 3, GroupIDs: []int64{groupID}}
	svc := &OpenAIGatewayService{
		accountRepo:        schedulerGroupAwareOpenAIAccountRepo{schedulerTestOpenAIAccountRepo{accounts: []Account{account, fastAccount}}},
		cache:              &schedulerTestGatewayCache{},
		cfg:                newSchedulerTestOpenAIWSV2Config(),
		rateLimitService:   newOpenAIAdvancedSchedulerRateLimitService("true"),
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
	}
	svc.SetOpenAITTFTGuardConfigProvider(openAITTFTGuardConfigProviderFunc(func() OpenAITTFTGuardConfigSnapshot {
		return enabledOpenAITTFTGuardConfig(20*time.Second, 5)
	}))
	critical := 60_000
	svc.SetOpenAITTFTGuardUpstreamOnly(false)
	svc.ReportOpenAIAccountScheduleResultForGroup(&groupID, &account, "gpt-test", true, &critical)
	require.NoError(t, svc.getOpenAIWSStateStore().BindResponseAccount(ctx, groupID, "hard-response", account.ID, time.Hour))

	selection, _, err := svc.SelectAccountWithSchedulerForCapability(
		ctx, &groupID, "hard-response", "", "gpt-test", nil, OpenAIUpstreamTransportAny,
		OpenAIEndpointCapabilityResponses, false, false, true,
	)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.Equal(t, account.ID, selection.Account.ID)
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func TestOpenAIGatewayService_TTFTGuardUnknownOrStalePreviousResponseUsesOverlay(t *testing.T) {
	for _, testCase := range []struct {
		name           string
		boundAccountID int64
	}{
		{name: "unknown"},
		{name: "stale", boundAccountID: 999},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := context.Background()
			groupID := int64(904)
			accounts := []Account{
				{ID: 53, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 2, GroupIDs: []int64{groupID}},
				{ID: 54, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 3, GroupIDs: []int64{groupID}},
			}
			svc := &OpenAIGatewayService{
				accountRepo:        schedulerGroupAwareOpenAIAccountRepo{schedulerTestOpenAIAccountRepo{accounts: accounts}},
				cache:              &schedulerTestGatewayCache{},
				cfg:                newSchedulerTestOpenAIWSV2Config(),
				rateLimitService:   newOpenAIAdvancedSchedulerRateLimitService("true"),
				concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
			}
			svc.SetOpenAITTFTGuardConfigProvider(openAITTFTGuardConfigProviderFunc(func() OpenAITTFTGuardConfigSnapshot {
				return enabledOpenAITTFTGuardConfig(20*time.Second, 5)
			}))
			critical := 60_000
			svc.SetOpenAITTFTGuardUpstreamOnly(false)
			svc.ReportOpenAIAccountScheduleResultForGroup(&groupID, &accounts[0], "gpt-test", true, &critical)
			if testCase.boundAccountID > 0 {
				require.NoError(t, svc.getOpenAIWSStateStore().BindResponseAccount(ctx, groupID, "missing-response", testCase.boundAccountID, time.Hour))
			}

			selection, _, err := svc.SelectAccountWithSchedulerForCapability(
				ctx, &groupID, "missing-response", "", "gpt-test", nil, OpenAIUpstreamTransportAny,
				OpenAIEndpointCapabilityResponses, false, false, true,
			)
			require.NoError(t, err)
			require.NotNil(t, selection)
			require.Equal(t, int64(54), selection.Account.ID)
			if selection.ReleaseFunc != nil {
				selection.ReleaseFunc()
			}
		})
	}
}

func TestOpenAIGatewayService_TTFTGuardKeepsStateIndependentAcrossGroups(t *testing.T) {
	for _, advanced := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "advanced"}[advanced], func(t *testing.T) {
			groupOne := int64(911)
			groupTwo := int64(912)
			accounts := []Account{
				{ID: 61, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 2, GroupIDs: []int64{groupOne, groupTwo}},
				{ID: 62, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 3, GroupIDs: []int64{groupOne}},
				{ID: 63, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 3, GroupIDs: []int64{groupTwo}},
			}
			svc := &OpenAIGatewayService{
				accountRepo:        schedulerGroupAwareOpenAIAccountRepo{schedulerTestOpenAIAccountRepo{accounts: accounts}},
				cfg:                &config.Config{},
				rateLimitService:   newOpenAIAdvancedSchedulerRateLimitService(map[bool]string{false: "false", true: "true"}[advanced]),
				concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
			}
			svc.SetOpenAITTFTGuardConfigProvider(openAITTFTGuardConfigProviderFunc(func() OpenAITTFTGuardConfigSnapshot {
				return enabledOpenAITTFTGuardConfig(20*time.Second, 5)
			}))
			critical := 60_000
			svc.SetOpenAITTFTGuardUpstreamOnly(false)
			svc.ReportOpenAIAccountScheduleResultForGroup(&groupOne, &accounts[0], "gpt-test", true, &critical)

			selectionOne, _, err := svc.SelectAccountWithScheduler(context.Background(), &groupOne, "", "", "gpt-test", nil, OpenAIUpstreamTransportAny, false)
			require.NoError(t, err)
			require.NotNil(t, selectionOne)
			require.Equal(t, int64(62), selectionOne.Account.ID)
			if selectionOne.ReleaseFunc != nil {
				selectionOne.ReleaseFunc()
			}

			selectionTwo, _, err := svc.SelectAccountWithScheduler(context.Background(), &groupTwo, "", "", "gpt-test", nil, OpenAIUpstreamTransportAny, false)
			require.NoError(t, err)
			require.NotNil(t, selectionTwo)
			require.Equal(t, int64(61), selectionTwo.Account.ID)
			if selectionTwo.ReleaseFunc != nil {
				selectionTwo.ReleaseFunc()
			}
		})
	}
}

func TestOpenAIGatewayService_TTFTGuardUnscopedReportDoesNotPolluteGroupState(t *testing.T) {
	groupID := int64(921)
	accounts := []Account{
		{ID: 71, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 2},
		{ID: 72, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 3},
		{ID: 73, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 1, GroupIDs: []int64{groupID}},
	}
	svc := &OpenAIGatewayService{
		accountRepo:        schedulerGroupAwareOpenAIAccountRepo{schedulerTestOpenAIAccountRepo{accounts: accounts}},
		cfg:                &config.Config{},
		rateLimitService:   newOpenAIAdvancedSchedulerRateLimitService("true"),
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
	}
	svc.SetOpenAITTFTGuardConfigProvider(openAITTFTGuardConfigProviderFunc(func() OpenAITTFTGuardConfigSnapshot {
		return enabledOpenAITTFTGuardConfig(20*time.Second, 5)
	}))
	critical := 60_000
	svc.ReportOpenAIAccountScheduleResult(71, "gpt-test", true, &critical)

	selection, _, err := svc.SelectAccountWithScheduler(context.Background(), nil, "", "", "gpt-test", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.Equal(t, int64(71), selection.Account.ID)
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}
