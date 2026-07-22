package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func enabledOpenAITTFTGuardConfig(threshold time.Duration, minSamples int) OpenAITTFTGuardConfigSnapshot {
	return OpenAITTFTGuardConfigSnapshot{Enabled: true, Threshold: threshold, MinSamples: minSamples}
}

func TestOpenAITTFTGuard_DegradationTriggers(t *testing.T) {
	t.Run("single critical sample", func(t *testing.T) {
		guard := newOpenAITTFTGuard()
		cfg := enabledOpenAITTFTGuardConfig(20*time.Second, 5)
		ttft := 60_000
		guard.report(1, "gpt-test", true, &ttft, cfg)
		require.True(t, guard.isDegraded(1, "gpt-test"))
	})

	t.Run("two consecutive elevated samples", func(t *testing.T) {
		guard := newOpenAITTFTGuard()
		cfg := enabledOpenAITTFTGuardConfig(20*time.Second, 5)
		ttft := 30_000
		guard.report(1, "gpt-test", true, &ttft, cfg)
		require.False(t, guard.isDegraded(1, "gpt-test"))
		guard.report(1, "gpt-test", true, &ttft, cfg)
		require.True(t, guard.isDegraded(1, "gpt-test"))
	})

	t.Run("recent ten sample EWMA uses cumulative minimum", func(t *testing.T) {
		guard := newOpenAITTFTGuard()
		cfg := enabledOpenAITTFTGuardConfig(20*time.Second, 20)
		ttft := 21_000
		for i := 0; i < 19; i++ {
			guard.report(1, "gpt-test", true, &ttft, cfg)
		}
		require.False(t, guard.isDegraded(1, "gpt-test"))
		guard.report(1, "gpt-test", true, &ttft, cfg)
		require.True(t, guard.isDegraded(1, "gpt-test"))
	})
}

func TestOpenAITTFTGuard_RecoveryNeedsThreeSuccessfulFastProbes(t *testing.T) {
	guard := newOpenAITTFTGuard()
	cfg := enabledOpenAITTFTGuardConfig(20*time.Second, 5)
	critical := 60_000
	guard.report(1, "gpt-test", true, &critical, cfg)

	fast := 12_000
	guard.report(1, "gpt-test", true, &fast, cfg)
	guard.report(1, "gpt-test", false, &fast, cfg)
	guard.report(1, "gpt-test", true, &fast, cfg)
	require.True(t, guard.isDegraded(1, "gpt-test"), "failed fast samples reset the recovery streak")
	guard.report(1, "gpt-test", true, &fast, cfg)
	guard.report(1, "gpt-test", true, &fast, cfg)
	require.False(t, guard.isDegraded(1, "gpt-test"))
}

func TestOpenAITTFTGuard_GlobalFivePercentProbe(t *testing.T) {
	guard := newOpenAITTFTGuard()
	cfg := enabledOpenAITTFTGuardConfig(20*time.Second, 5)
	critical := 60_000
	guard.report(1, "gpt-test", true, &critical, cfg)
	guard.report(2, "gpt-test", true, &critical, cfg)
	candidates := []openAITTFTGuardCandidate{{accountID: 1, model: "gpt-test"}, {accountID: 2, model: "gpt-test"}}

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

func TestOpenAITTFTGuard_StateIsGlobalAcrossGroupsAndIsolatedByModel(t *testing.T) {
	guard := newOpenAITTFTGuard()
	cfg := enabledOpenAITTFTGuardConfig(20*time.Second, 5)
	critical := 60_000
	guard.report(7, "gpt-slow", true, &critical, cfg)

	// Group membership is intentionally absent from the state key. Any group
	// containing this account observes the same model-specific degradation.
	groupOne := guard.exclusions([]openAITTFTGuardCandidate{{accountID: 7, model: "gpt-slow"}}, nil, cfg)
	groupTwo := guard.exclusions([]openAITTFTGuardCandidate{{accountID: 7, model: "gpt-slow"}}, nil, cfg)
	require.Contains(t, groupOne, int64(7))
	require.Contains(t, groupTwo, int64(7))

	otherModel := guard.exclusions([]openAITTFTGuardCandidate{{accountID: 7, model: "gpt-fast"}}, nil, cfg)
	require.NotContains(t, otherModel, int64(7))
}

func TestOpenAITTFTGuard_TTLAndLRU(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	guard := newOpenAITTFTGuardWithOptions(2, 15*time.Minute, func() time.Time { return now })
	cfg := enabledOpenAITTFTGuardConfig(20*time.Second, 5)
	critical := 60_000
	guard.report(1, "gpt-test", true, &critical, cfg)
	now = now.Add(time.Second)
	guard.report(2, "gpt-test", true, &critical, cfg)
	now = now.Add(time.Second)
	require.True(t, guard.isDegraded(1, "gpt-test"))
	now = now.Add(time.Second)
	guard.report(3, "gpt-test", true, &critical, cfg)

	require.True(t, guard.isDegraded(1, "gpt-test"))
	require.False(t, guard.isDegraded(2, "gpt-test"), "least recently used entry must be evicted")
	require.True(t, guard.isDegraded(3, "gpt-test"))

	now = now.Add(15 * time.Minute)
	require.Zero(t, guard.size())
}

func TestOpenAITTFTGuard_DegradationSnapshots(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	guard := newOpenAITTFTGuardWithOptions(32, 15*time.Minute, func() time.Time { return now })
	cfg := enabledOpenAITTFTGuardConfig(20*time.Second, 5)

	critical := 61_000
	guard.report(1, "z-model", true, &critical, cfg)
	zDegradedAt := now
	now = now.Add(time.Minute)
	elevated := 31_000
	guard.report(1, "a-model", true, &elevated, cfg)
	now = now.Add(time.Minute)
	guard.report(1, "a-model", true, &elevated, cfg)
	aDegradedAt := now
	guard.report(2, "other-model", true, &critical, cfg)

	key, ok := openAITTFTGuardKeyFor(1, "a-model")
	require.True(t, ok)
	touchedAt := guard.entries[key].lastTouchedAt
	snapshots := guard.degradations([]int64{2, 1, 1, 0}, cfg)
	require.Equal(t, touchedAt, guard.entries[key].lastTouchedAt, "snapshot reads must not refresh LRU timestamps")
	require.Len(t, snapshots, 2)
	require.Len(t, snapshots[1], 2)
	require.Equal(t, "a-model", snapshots[1][0].Model)
	require.Equal(t, "z-model", snapshots[1][1].Model)

	aSnapshot := snapshots[1][0]
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
	guard.report(1, "gpt-test", true, &critical, cfg)

	fast := 10_000
	now = now.Add(time.Minute)
	guard.report(1, "gpt-test", true, &fast, cfg)
	snapshots := guard.degradations([]int64{1}, cfg)
	require.Equal(t, 1, snapshots[1][0].RecoverySamples)
	require.Equal(t, now.Add(15*time.Minute), snapshots[1][0].ExpiresAt)

	guard.report(1, "gpt-test", true, &fast, cfg)
	guard.report(1, "gpt-test", true, &fast, cfg)
	require.Empty(t, guard.degradations([]int64{1}, cfg), "recovery must remove the degradation snapshot")

	guard.report(1, "gpt-test", true, &critical, cfg)
	now = now.Add(15 * time.Minute)
	require.Empty(t, guard.degradations([]int64{1}, cfg), "TTL cleanup must remove stale degradation snapshots")

	now = now.Add(time.Second)
	guard.report(1, "gpt-test", true, &critical, cfg)
	disabled := cfg
	disabled.Enabled = false
	require.Empty(t, guard.degradations([]int64{1}, disabled))
	require.Zero(t, guard.size(), "disabling the guard through a snapshot read must clear transient state")
}

func TestOpenAITTFTGuard_DegradationSnapshotReportsEWMAReason(t *testing.T) {
	guard := newOpenAITTFTGuard()
	cfg := enabledOpenAITTFTGuardConfig(20*time.Second, 5)
	ttft := 21_000
	for i := 0; i < 5; i++ {
		guard.report(9, "gpt-ewma", true, &ttft, cfg)
	}

	snapshots := guard.degradations([]int64{9}, cfg)
	require.Len(t, snapshots[9], 1)
	require.Equal(t, "ewma", snapshots[9][0].Reason)
}

func TestOpenAITTFTGuard_ConfigChangeClearsTransientState(t *testing.T) {
	guard := newOpenAITTFTGuard()
	cfg := enabledOpenAITTFTGuardConfig(20*time.Second, 5)
	critical := 60_000
	guard.report(1, "gpt-test", true, &critical, cfg)
	require.True(t, guard.isDegraded(1, "gpt-test"))

	changed := enabledOpenAITTFTGuardConfig(21*time.Second, 5)
	require.Empty(t, guard.exclusions([]openAITTFTGuardCandidate{{accountID: 1, model: "gpt-test"}}, nil, changed))
	require.Zero(t, guard.size())
	guard.report(1, "gpt-test", true, &critical, changed)
	require.False(t, guard.isDegraded(1, "gpt-test"), "60s is below the new 3T critical threshold")

	disabled := changed
	disabled.Enabled = false
	guard.exclusions(nil, nil, disabled)
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
				guard.report(accountID, model, true, sample, cfg)
				guard.exclusions([]openAITTFTGuardCandidate{{accountID: accountID, model: model}}, nil, cfg)
				guard.degradations([]int64{accountID}, cfg)
				_ = guard.isDegraded(accountID, model)
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
	svc.ReportOpenAIAccountScheduleResult(1, "upstream-slow", true, &critical)

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
	account := Account{ID: 11, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 2}
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
	svc.ReportOpenAIAccountScheduleResult(account.ID, "gpt-test", true, &critical)

	selection, _, err := svc.SelectAccountWithScheduler(ctx, nil, "", "", "gpt-test", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.Equal(t, account.ID, selection.Account.ID)
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func TestOpenAIGatewayService_TTFTGuardDoesNotMutateCallerExclusions(t *testing.T) {
	ctx := context.Background()
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
	svc.ReportOpenAIAccountScheduleResult(12, "gpt-test", true, &critical)
	callerExcluded := map[int64]struct{}{13: {}}

	selection, _, err := svc.SelectAccountWithScheduler(ctx, nil, "", "", "gpt-test", callerExcluded, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.Equal(t, int64(12), selection.Account.ID, "fail-open must retain the caller exclusion")
	require.Equal(t, map[int64]struct{}{13: {}}, callerExcluded)
	if selection != nil && selection.ReleaseFunc != nil {
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
			svc.ReportOpenAIAccountScheduleResult(21, "gpt-test", true, &critical)

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
	critical := 60_000
	svc.ReportOpenAIAccountScheduleResult(31, "gpt-test", true, &critical)
	require.True(t, svc.getOpenAITTFTGuard().isDegraded(31, "gpt-test"))
}

func TestOpenAIGatewayService_TTFTGuardMovablePreviousResponseStillApplies(t *testing.T) {
	ctx := context.Background()
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
	svc.ReportOpenAIAccountScheduleResult(41, "gpt-test", true, &critical)

	selection, _, err := svc.SelectAccountWithSchedulerForCapability(
		ctx, nil, "response-can-move", "", "gpt-test", nil, OpenAIUpstreamTransportAny,
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
	svc.ReportOpenAIAccountScheduleResult(account.ID, "gpt-test", true, &critical)
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
			svc.ReportOpenAIAccountScheduleResult(53, "gpt-test", true, &critical)
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

func TestOpenAIGatewayService_TTFTGuardSharesStateAcrossGroupsWithoutCrossGroupSelection(t *testing.T) {
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
			svc.ReportOpenAIAccountScheduleResult(61, "gpt-test", true, &critical)

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
			require.Equal(t, int64(63), selectionTwo.Account.ID)
			if selectionTwo.ReleaseFunc != nil {
				selectionTwo.ReleaseFunc()
			}
		})
	}
}

func TestOpenAIGatewayService_TTFTGuardKeepsUngroupedAccountsIsolated(t *testing.T) {
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
	require.Equal(t, int64(72), selection.Account.ID)
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}
