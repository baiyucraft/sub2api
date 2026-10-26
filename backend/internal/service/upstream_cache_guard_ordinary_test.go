package service

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/upstreamcacheguard"
	"github.com/stretchr/testify/require"
)

func TestUpstreamCacheGuardOrdinaryIdentityRequiresReliableMarkerAndSharesAcrossGroups(t *testing.T) {
	runner := &UpstreamCacheGuardRunner{secret: []byte("test-only-ordinary-secret")}
	ctx := WithUpstreamCacheGuardScheduling(context.Background(), 17)
	groupA, groupB := int64(8), int64(21)
	legacy, ok := runner.logicalRequest(ctx, &groupA, "body-derived-fallback", "gpt-test", "openai")
	require.True(t, ok)
	require.Empty(t, legacy.OrdinaryRecoverySessionID)
	require.Empty(t, legacy.OrdinaryRecoveryIdentityGeneration)

	ctx = context.WithValue(ctx, cacheGuardLogicalSessionKey{}, "explicit-client-session")
	a, ok := runner.logicalRequest(ctx, &groupA, "first-body", "gpt-test", "openai")
	require.True(t, ok)
	b, ok := runner.logicalRequest(ctx, &groupB, "second-body", "another-model", "anthropic")
	require.True(t, ok)
	require.Len(t, a.OrdinaryRecoverySessionID, 64)
	require.Equal(t, a.OrdinaryRecoverySessionID, b.OrdinaryRecoverySessionID)
	require.Equal(t, a.OrdinaryRecoveryIdentityGeneration, b.OrdinaryRecoveryIdentityGeneration)
	require.NotEqual(t, a.LogicalSessionDigest, b.LogicalSessionDigest, "legacy routing identity remains group scoped")
	require.NotContains(t, a.OrdinaryRecoverySessionID, "explicit-client-session")
	require.NotEqual(t, a.LogicalSessionDigest, a.OrdinaryRecoverySessionID, "HMAC domains must differ")

	otherKeyCtx := context.WithValue(WithUpstreamCacheGuardScheduling(context.Background(), 18), cacheGuardLogicalSessionKey{}, "explicit-client-session")
	otherKey, ok := runner.logicalRequest(otherKeyCtx, &groupA, "body", "gpt-test", "openai")
	require.True(t, ok)
	require.NotEqual(t, a.OrdinaryRecoverySessionID, otherKey.OrdinaryRecoverySessionID)
	require.Equal(t, a.OrdinaryRecoveryIdentityGeneration, otherKey.OrdinaryRecoveryIdentityGeneration)

	restarted := &UpstreamCacheGuardRunner{secret: append([]byte(nil), runner.secret...)}
	afterRestart, ok := restarted.logicalRequest(ctx, &groupA, "body", "gpt-test", "openai")
	require.True(t, ok)
	require.Equal(t, a.OrdinaryRecoverySessionID, afterRestart.OrdinaryRecoverySessionID)
	require.Equal(t, a.OrdinaryRecoveryIdentityGeneration, afterRestart.OrdinaryRecoveryIdentityGeneration)
	restarted.secret = []byte("rotated-test-only-secret")
	rotated, ok := restarted.logicalRequest(ctx, &groupA, "body", "gpt-test", "openai")
	require.True(t, ok)
	require.NotEqual(t, a.OrdinaryRecoverySessionID, rotated.OrdinaryRecoverySessionID)
	require.NotEqual(t, a.OrdinaryRecoveryIdentityGeneration, rotated.OrdinaryRecoveryIdentityGeneration)
}

type ordinaryRecoveryMetadataRepository struct {
	upstreamManagementSettingRepoStub
	updated map[string]time.Time
	err     error
}

func (r *ordinaryRecoveryMetadataRepository) Get(ctx context.Context, key string) (*Setting, error) {
	if r.err != nil {
		return nil, r.err
	}
	value, err := r.GetValue(ctx, key)
	if err != nil {
		return nil, err
	}
	return &Setting{Key: key, Value: value, UpdatedAt: r.updated[key]}, nil
}

func TestOrdinaryRecoveryConfigurationGenerationDurableAndDetectsReturnToSameValue(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 26, 1, 0, 0, 0, time.UTC)
	repo := &ordinaryRecoveryMetadataRepository{upstreamManagementSettingRepoStub: upstreamManagementSettingRepoStub{values: map[string]string{
		SettingKeyUpstreamCacheGuard:            `{"mode":"protect"}`,
		SettingKeySchedulingProtectionPlatforms: `["openai"]`,
	}}, updated: map[string]time.Time{SettingKeyUpstreamCacheGuard: at, SettingKeySchedulingProtectionPlatforms: at}}
	revisions, err := NewSettingService(repo, nil).ordinaryRecoverySettingRevisions(ctx)
	require.NoError(t, err)
	otherProcess, err := NewSettingService(repo, nil).ordinaryRecoverySettingRevisions(ctx)
	require.NoError(t, err)
	require.Equal(t, revisions, otherProcess)
	repo.updated[SettingKeySchedulingProtectionPlatforms] = at.Add(time.Minute)
	afterPauseResume, err := NewSettingService(repo, nil).ordinaryRecoverySettingRevisions(ctx)
	require.NoError(t, err)
	require.NotEqual(t, revisions[SettingKeySchedulingProtectionPlatforms], afterPauseResume[SettingKeySchedulingProtectionPlatforms])
	require.Equal(t, revisions[SettingKeyUpstreamCacheGuard], afterPauseResume[SettingKeyUpstreamCacheGuard])
	repo.err = errors.New("metadata unavailable")
	_, err = NewSettingService(repo, nil).ordinaryRecoverySettingRevisions(ctx)
	require.Error(t, err)
}

func TestOrdinaryRecoveryPolicyGenerationSharesEqualRulesAndIgnoresReadOrder(t *testing.T) {
	settings := DefaultUpstreamCacheGuardSettings()
	settings.Mode = upstreamcacheguard.Protect
	at := time.Date(2026, 10, 26, 1, 0, 0, 0, time.UTC)
	otherTime := at.Add(time.Minute)
	policies := map[int64]GroupSchedulingProtectionResolvedPolicy{
		8:  {GroupID: 8, Cache: settings, PlatformSelected: true},
		21: {GroupID: 21, Cache: settings, PlatformSelected: true},
	}
	records := []GroupSchedulingProtectionPolicyRecord{
		{TTFT: GroupTTFTGuardPolicyRecord{GroupID: 8}, UpdatedAt: &at},
		{TTFT: GroupTTFTGuardPolicyRecord{GroupID: 21}, UpdatedAt: &otherTime},
	}
	revisions := map[string]string{"cache": "stable-a", "platform": "stable-b"}
	first := ordinaryRecoveryPolicyGenerations(settings, policies, records, revisions)
	reversed := ordinaryRecoveryPolicyGenerations(settings, policies, []GroupSchedulingProtectionPolicyRecord{records[1], records[0]}, revisions)
	require.Equal(t, first, reversed)
	require.Len(t, first, 1, "equal policies must share a recovery generation")
	newTime := otherTime.Add(time.Minute)
	records[0].UpdatedAt = &newTime
	require.NotEqual(t, first, ordinaryRecoveryPolicyGenerations(settings, policies, records, revisions))
	require.Equal(t, upstreamcacheguard.PolicySignature(settings), upstreamcacheguard.PolicySignature(policies[8].Cache), "the released signature stays unchanged")
}

func TestOrdinaryRecoveryPolicySnapshotDetectsDurableTimestampWithoutAdvancingOnRead(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 26, 1, 0, 0, 0, time.UTC)
	repo := &ordinaryRecoveryMetadataRepository{upstreamManagementSettingRepoStub: upstreamManagementSettingRepoStub{values: map[string]string{SettingKeySchedulingProtectionPlatforms: `["openai"]`}}, updated: map[string]time.Time{SettingKeySchedulingProtectionPlatforms: at}}
	policies := &schedulingProtectionRepoStub{records: []GroupSchedulingProtectionPolicyRecord{{TTFT: GroupTTFTGuardPolicyRecord{GroupID: 8, GroupPlatform: PlatformOpenAI}}}}
	service := NewGroupSchedulingProtectionPolicyService(policies, NewSettingService(repo, nil), nil, nil)
	first := service.ProtectionPoliciesSnapshot()
	require.NotEmpty(t, first.ordinaryRecoveryGenerations)
	require.NoError(t, service.RefreshProtectionPolicies(ctx))
	require.Same(t, first, service.ProtectionPoliciesSnapshot(), "an unchanged verification must preserve the snapshot")
	repo.updated[SettingKeySchedulingProtectionPlatforms] = at.Add(time.Minute)
	require.NoError(t, service.RefreshProtectionPolicies(ctx))
	second := service.ProtectionPoliciesSnapshot()
	require.NotSame(t, first, second)
	require.NotEqual(t, first.ordinaryRecoveryGenerations, second.ordinaryRecoveryGenerations)
	require.Equal(t, first.Revision()+1, second.Revision())
}

func TestUpstreamCacheGuardOrdinaryAPIViewIsPureAndTimeBoundaryChangesResponse(t *testing.T) {
	settings := DefaultUpstreamCacheGuardSettings()
	settings.Mode = upstreamcacheguard.Protect
	settings.MinValidRequests = 5
	start := time.Date(2026, 10, 26, 1, 0, 0, 0, time.UTC)
	state := upstreamcacheguard.AccountSnapshot{AccountState: upstreamcacheguard.AccountState{
		Phase: upstreamcacheguard.Cooling, ValidRequests: 100, CachePercent: 95,
		TotalInputTokens: 10000, CacheReadTokens: 9500,
		CooldownUntil: start.Add(-time.Minute), UpdatedAt: start,
		TrialValidRequests: 2, TrialSessions: 1,
		OrdinaryRecovery: &upstreamcacheguard.OrdinaryRecoveryState{
			QualifiedSince: start, QualifiedUntil: start.Add(15 * time.Minute), LastVerifiedAt: start,
			ValidRequests: 50, Sessions: 3, TotalInputTokens: 10000, CacheReadTokens: 9600,
		},
	}}
	before, err := json.Marshal(state)
	require.NoError(t, err)
	first := upstreamCacheGuardSummaryAt(state, true, settings, start.Add(time.Minute))
	require.Equal(t, "protected", first.State)
	require.Equal(t, "waiting_recovery_validation", first.Reason)
	require.Equal(t, 2, first.RecoveryValidRequests, "trial progress must stay separate")
	require.Equal(t, 1, first.RecoverySessions)
	require.NotNil(t, first.ordinaryRecovery)
	require.Equal(t, 50, first.ordinaryRecovery.ValidRequests)
	require.Equal(t, "waiting_validation_time", first.ordinaryRecovery.Reason)
	var item UpstreamCacheGuardState
	applyCacheGuardGroupSummary(&item, first)
	firstBody, err := json.Marshal(item)
	require.NoError(t, err)
	require.Contains(t, string(firstBody), `"ordinary_recovery"`)
	second := upstreamCacheGuardSummaryAt(state, true, settings, start.Add(10*time.Minute))
	require.Equal(t, "protected", second.State, "time or a management read cannot restore the account")
	require.Equal(t, "awaiting_fresh_evidence", second.ordinaryRecovery.Reason)
	applyCacheGuardGroupSummary(&item, second)
	secondBody, err := json.Marshal(item)
	require.NoError(t, err)
	require.NotEqual(t, string(firstBody), string(secondBody), "a full-response ETag must see the validation boundary")
	after, err := json.Marshal(state)
	require.NoError(t, err)
	require.Equal(t, before, after)
	MarkUpstreamCacheGuardPlatformNotSelected(&item)
	require.Nil(t, item.OrdinaryRecovery)
	require.Nil(t, upstreamCacheGuardSummaryAt(state, false, settings, start).ordinaryRecovery)
}

func ordinaryServicePolicySnapshot(revision uint64, global UpstreamCacheGuardSettings, generation string) *GroupSchedulingProtectionPolicySnapshot {
	cache := global
	cache.GroupIDs = []int64{8}
	return &GroupSchedulingProtectionPolicySnapshot{
		revision: revision, globalCache: &global,
		policies:                    map[int64]GroupSchedulingProtectionResolvedPolicy{8: {GroupID: 8, Cache: cache}},
		cachePolicies:               map[int64]UpstreamCacheGuardSettings{8: cache},
		ordinaryRecoveryGenerations: map[string]string{upstreamcacheguard.PolicySignature(cache): generation},
	}
}

func TestUpstreamCacheGuardRecoveryConfigurationPublishesMatchingBundleAndPausesDuringDirectSave(t *testing.T) {
	ctx := context.Background()
	global := DefaultUpstreamCacheGuardSettings()
	global.Mode = upstreamcacheguard.Protect
	global.GroupIDs = []int64{21, 8, 8}
	encoded, err := json.Marshal(global)
	require.NoError(t, err)
	repo := &upstreamManagementSettingRepoStub{values: map[string]string{SettingKeyUpstreamCacheGuard: string(encoded)}}
	settings := NewSettingService(repo, nil)
	store := newRunnerTestStore()
	runner := &UpstreamCacheGuardRunner{engine: upstreamcacheguard.NewEngine(store), store: store, settings: settings, secret: []byte("test-only-secret")}
	require.NoError(t, runner.engine.ApplySettings(global))
	provider := &evaluationPolicyProvider{}
	initial := ordinaryServicePolicySnapshot(1, global, "durable-a")
	provider.current.Store(initial)
	runner.SetProtectionPolicyResolver(provider)
	require.True(t, runner.appliedOrdinary)
	firstEpoch := runner.engine.PolicyVersion()
	for range 5 {
		runner.refresh()
		runner.applyPolicySnapshot()
	}
	require.Equal(t, firstEpoch, runner.engine.PolicyVersion(), "unchanged background verification must preserve recovery clocks and policy epoch")

	previousRunner := installedCacheGuardRunner.Swap(runner)
	t.Cleanup(func() { installedCacheGuardRunner.Store(previousRunner) })
	changed := global
	changed.WindowMinutes = 30
	readBeforeSave := runner.localSettingsRevision.Load()
	require.NoError(t, settings.SetUpstreamCacheGuardSettings(ctx, changed))
	require.NoError(t, runner.applyRecoverySettings(global, readBeforeSave))
	require.Equal(t, 30, runner.engine.Settings().WindowMinutes, "a direct global save is immediately active")
	require.Equal(t, global.WindowMinutes, runner.engine.ResolvedSettings(8).WindowMinutes, "the previous resolved group snapshot remains until its replacement arrives")
	require.False(t, runner.appliedOrdinary, "new global settings must never pair with an old recovery generation")
	require.Equal(t, firstEpoch+1, runner.engine.PolicyVersion(), "the bundle changes in a single publication")
	pausedEpoch := runner.engine.PolicyVersion()
	runner.engine.SetOrdinaryRecoveryPolicyGenerations(nil)
	require.Equal(t, pausedEpoch, runner.engine.PolicyVersion(), "the core must actually have no ordinary generation during the mismatch")
	runner.refresh()
	runner.applyPolicySnapshot()
	require.Equal(t, pausedEpoch, runner.engine.PolicyVersion(), "a repeated mismatched snapshot must be a no-op")

	// A -> B -> A while the immutable policy service is still on A must not
	// revive the original generation merely because the values match again.
	require.NoError(t, settings.SetUpstreamCacheGuardSettings(ctx, global))
	require.False(t, runner.appliedOrdinary)
	runner.refresh()
	require.False(t, runner.appliedOrdinary)
	provider.current.Store(nil)
	runner.applyPolicySnapshot()
	require.True(t, runner.ordinaryWritePending, "a missing policy snapshot cannot acknowledge a durable configuration save")
	provider.current.Store(initial)
	runner.applyPolicySnapshot()
	require.False(t, runner.appliedOrdinary, "an older snapshot after a provider outage must remain blocked")
	returnedEpoch := runner.engine.PolicyVersion()
	replacement := ordinaryServicePolicySnapshot(2, global, "durable-a-after-save")
	provider.current.Store(replacement)
	runner.refresh()
	require.True(t, runner.appliedOrdinary)
	require.False(t, runner.ordinaryWritePending)
	require.Equal(t, returnedEpoch+1, runner.engine.PolicyVersion())
	restoredEpoch := runner.engine.PolicyVersion()
	runner.engine.SetOrdinaryRecoveryPolicyGenerations(replacement.ordinaryRecoveryGenerations)
	require.Equal(t, restoredEpoch, runner.engine.PolicyVersion(), "the matching durable generation must be installed in the core")
	runner.refresh()
	require.Equal(t, restoredEpoch, runner.engine.PolicyVersion())

	// Remote/global refreshes use the same bundle path without creating a
	// local-write invalidation on every one-second tick.
	encoded, err = json.Marshal(changed)
	require.NoError(t, err)
	repo.values[SettingKeyUpstreamCacheGuard] = string(encoded)
	runner.refresh()
	require.Equal(t, 30, runner.engine.Settings().WindowMinutes)
	require.False(t, runner.appliedOrdinary)
	provider.current.Store(ordinaryServicePolicySnapshot(3, changed, "durable-b"))
	runner.refresh()
	require.True(t, runner.appliedOrdinary)
	require.Equal(t, 30, runner.engine.ResolvedSettings(8).WindowMinutes)
	finalEpoch := runner.engine.PolicyVersion()
	runner.refresh()
	require.Equal(t, finalEpoch, runner.engine.PolicyVersion())

	provider.current.Store(nil)
	runner.applyPolicySnapshot()
	require.False(t, runner.appliedOrdinary)
	require.Equal(t, changed.WindowMinutes, runner.engine.ResolvedSettings(8).WindowMinutes)
	nilEpoch := runner.engine.PolicyVersion()
	runner.applyPolicySnapshot()
	require.Equal(t, nilEpoch, runner.engine.PolicyVersion())
}

func TestUpstreamCacheGuardConfigurationIdentityMatchesCoreDefaultsAndGroupSet(t *testing.T) {
	settings := DefaultUpstreamCacheGuardSettings()
	settings.GroupIDs = []int64{21, 8, 8}
	legacy := settings
	legacy.GroupIDs = []int64{8, 21}
	legacy.Priority = ""
	legacy.ProtectionEvidenceExpiryMinutes = 0
	require.Equal(t, cacheGuardConfigurationIdentity(settings), cacheGuardConfigurationIdentity(legacy))
	legacy.CooldownMinutes++
	require.NotEqual(t, cacheGuardConfigurationIdentity(settings), cacheGuardConfigurationIdentity(legacy))
	require.Equal(t, []int64{21, 8, 8}, settings.GroupIDs, "canonical matching cannot mutate the retained snapshot")
}

type ordinaryBlockingSettingsRepository struct {
	upstreamManagementSettingRepoStub
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (r *ordinaryBlockingSettingsRepository) GetValue(ctx context.Context, key string) (string, error) {
	value, err := r.upstreamManagementSettingRepoStub.GetValue(ctx, key)
	if key == SettingKeyUpstreamCacheGuard {
		r.once.Do(func() {
			close(r.started)
			<-r.release
		})
	}
	return value, err
}

func TestUpstreamCacheGuardGlobalRefreshBlockedBeforeSaveCannotReinstallOlderSettings(t *testing.T) {
	global := DefaultUpstreamCacheGuardSettings()
	global.Mode = upstreamcacheguard.Protect
	encoded, err := json.Marshal(global)
	require.NoError(t, err)
	repo := &ordinaryBlockingSettingsRepository{
		upstreamManagementSettingRepoStub: upstreamManagementSettingRepoStub{values: map[string]string{SettingKeyUpstreamCacheGuard: string(encoded)}},
		started:                           make(chan struct{}), release: make(chan struct{}),
	}
	settings := NewSettingService(repo, nil)
	store := newRunnerTestStore()
	runner := &UpstreamCacheGuardRunner{engine: upstreamcacheguard.NewEngine(store), store: store, settings: settings, secret: []byte("test-only-secret")}
	require.NoError(t, runner.engine.ApplySettings(global))
	provider := &evaluationPolicyProvider{}
	provider.current.Store(ordinaryServicePolicySnapshot(1, global, "durable-a"))
	runner.SetProtectionPolicyResolver(provider)
	oldRunner := installedCacheGuardRunner.Swap(runner)
	t.Cleanup(func() { installedCacheGuardRunner.Store(oldRunner) })
	release := sync.OnceFunc(func() { close(repo.release) })
	defer release()
	done := make(chan struct{})
	go func() {
		defer close(done)
		runner.refresh()
	}()
	select {
	case <-repo.started:
	case <-time.After(5 * time.Second):
		t.Fatal("global setting read did not reach the fixed barrier")
	}
	changed := global
	changed.WindowMinutes = 30
	require.NoError(t, settings.SetUpstreamCacheGuardSettings(context.Background(), changed))
	epoch := runner.engine.PolicyVersion()
	release()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("refresh failed to finish after the read barrier was released")
	}
	require.Equal(t, 30, runner.engine.Settings().WindowMinutes)
	require.Equal(t, epoch, runner.engine.PolicyVersion(), "the delayed older database response must not publish a second bundle")
	require.False(t, runner.appliedOrdinary)
	require.True(t, runner.ordinaryWritePending)
}
