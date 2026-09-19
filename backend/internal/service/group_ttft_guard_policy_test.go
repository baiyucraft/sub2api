package service

import (
	"context"
	"sync"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

type groupTTFTGuardPolicyRepoStub struct {
	record  GroupTTFTGuardPolicyRecord
	putMode string
	putT    int
	putN    int
	gets    int
}

func (s *groupTTFTGuardPolicyRepoStub) List(context.Context) ([]GroupTTFTGuardPolicyRecord, error) {
	return []GroupTTFTGuardPolicyRecord{s.record}, nil
}
func (s *groupTTFTGuardPolicyRepoStub) Get(context.Context, int64) (*GroupTTFTGuardPolicyRecord, error) {
	s.gets++
	record := s.record
	return &record, nil
}
func (s *groupTTFTGuardPolicyRepoStub) Put(_ context.Context, _ int64, mode string, threshold, minSamples int) (bool, error) {
	s.putMode, s.putT, s.putN = mode, threshold, minSamples
	s.record.Mode = &s.putMode
	s.record.DegradationTTFTSeconds = &s.putT
	s.record.MinSamples = &s.putN
	return true, nil
}

type groupTTFTGuardGlobalStub struct {
	settings        OpenAITTFTGuardSettings
	snapshot        *OpenAITTFTGuardConfigSnapshot
	refreshSnapshot OpenAITTFTGuardConfigSnapshot
	refreshOK       bool
}

func (s *groupTTFTGuardGlobalStub) GetOpenAITTFTGuardSettings(context.Context) (*OpenAITTFTGuardSettings, error) {
	settings := s.settings
	return &settings, nil
}

func (s *groupTTFTGuardGlobalStub) OpenAITTFTGuardConfigSnapshot() OpenAITTFTGuardConfigSnapshot {
	if s.snapshot != nil {
		return *s.snapshot
	}
	return openAITTFTGuardSnapshot(&s.settings)
}

func (s *groupTTFTGuardGlobalStub) RefreshOpenAITTFTGuardConfig(context.Context) (OpenAITTFTGuardConfigSnapshot, bool) {
	if s.refreshSnapshot.Threshold == 0 {
		return s.OpenAITTFTGuardConfigSnapshot(), s.refreshOK
	}
	s.snapshot = &s.refreshSnapshot
	return s.refreshSnapshot, s.refreshOK
}

func newGroupTTFTGuardPolicyServiceForTest(repo *groupTTFTGuardPolicyRepoStub, global OpenAITTFTGuardSettings) *GroupTTFTGuardPolicyService {
	return &GroupTTFTGuardPolicyService{
		repo: repo, globalSettings: &groupTTFTGuardGlobalStub{settings: global},
		cacheTTL: time.Hour, cache: make(map[int64]cachedGroupTTFTGuardPolicy),
		cacheGenerations: make(map[int64]uint64),
	}
}

type blockingGroupTTFTGuardPolicyRepo struct {
	mu      sync.Mutex
	record  GroupTTFTGuardPolicyRecord
	started chan struct{}
	release chan struct{}
	gets    int
}

func (r *blockingGroupTTFTGuardPolicyRepo) List(context.Context) ([]GroupTTFTGuardPolicyRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return []GroupTTFTGuardPolicyRecord{r.record}, nil
}

func (r *blockingGroupTTFTGuardPolicyRepo) Get(ctx context.Context, _ int64) (*GroupTTFTGuardPolicyRecord, error) {
	r.mu.Lock()
	r.gets++
	getNumber := r.gets
	record := r.record
	r.mu.Unlock()
	if getNumber == 1 {
		close(r.started)
		select {
		case <-r.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return &record, nil
}

func (r *blockingGroupTTFTGuardPolicyRepo) Put(context.Context, int64, string, int, int) (bool, error) {
	return false, nil
}

func TestGroupTTFTGuardPolicyResolveModesAndIdentity(t *testing.T) {
	mode := GroupTTFTGuardModeEnabled
	threshold, samples := 9, 3
	repo := &groupTTFTGuardPolicyRepoStub{record: GroupTTFTGuardPolicyRecord{
		GroupID: 7, GroupName: "pro", GroupPlatform: PlatformOpenAI,
		Mode: &mode, DegradationTTFTSeconds: &threshold, MinSamples: &samples,
	}}
	svc := newGroupTTFTGuardPolicyServiceForTest(repo, OpenAITTFTGuardSettings{Enabled: false, DegradationTTFTSeconds: 20, MinSamples: 5})

	resolved, err := svc.Resolve(context.Background(), 7)
	require.NoError(t, err)
	require.Equal(t, int64(7), resolved.GroupID)
	require.Equal(t, "pro", resolved.GroupName)
	require.True(t, resolved.Enabled)
	require.Equal(t, 9*time.Second, resolved.Threshold)
	require.Equal(t, GroupTTFTGuardSourceGroup, resolved.Source)

	mode = GroupTTFTGuardModeDisabled
	repo.record.Mode = &mode
	resolved, err = svc.Resolve(context.Background(), 7)
	require.NoError(t, err)
	require.True(t, resolved.Enabled, "cached policy should remain until invalidated")
	svc.Invalidate(7)
	resolved, err = svc.Resolve(context.Background(), 7)
	require.NoError(t, err)
	require.False(t, resolved.Enabled)
	require.Equal(t, GroupTTFTGuardSourceDisabled, resolved.Source)
}

func TestGroupTTFTGuardPolicyInheritUsesGlobalAndPutValidates(t *testing.T) {
	repo := &groupTTFTGuardPolicyRepoStub{record: GroupTTFTGuardPolicyRecord{GroupID: 8, GroupName: "mix", GroupPlatform: PlatformComposite}}
	svc := newGroupTTFTGuardPolicyServiceForTest(repo, OpenAITTFTGuardSettings{Enabled: true, DegradationTTFTSeconds: 30, MinSamples: 6})

	resolved, err := svc.Resolve(context.Background(), 8)
	require.NoError(t, err)
	require.True(t, resolved.Enabled)
	require.Equal(t, 30*time.Second, resolved.Threshold)
	require.Equal(t, GroupTTFTGuardSourceGlobal, resolved.Source)

	_, err = svc.PutPolicy(context.Background(), 8, GroupTTFTGuardPolicyInput{Mode: GroupTTFTGuardModeEnabled})
	require.True(t, infraerrors.IsBadRequest(err))
	tValue, nValue := 15, 4
	view, err := svc.PutPolicy(context.Background(), 8, GroupTTFTGuardPolicyInput{
		Mode: GroupTTFTGuardModeEnabled, DegradationTTFTSeconds: &tValue, MinSamples: &nValue,
	})
	require.NoError(t, err)
	require.Equal(t, 15, view.EffectiveDegradationTTFTSeconds)
	require.Equal(t, 15, *view.DegradationTTFTSeconds)
	require.Equal(t, "enabled", repo.putMode)
}

func TestGroupTTFTGuardPolicyRejectsUnsupportedPlatformAndRanges(t *testing.T) {
	repo := &groupTTFTGuardPolicyRepoStub{record: GroupTTFTGuardPolicyRecord{GroupID: 9, GroupName: "claude", GroupPlatform: PlatformAnthropic}}
	svc := newGroupTTFTGuardPolicyServiceForTest(repo, *DefaultOpenAITTFTGuardSettings())
	_, err := svc.Resolve(context.Background(), 9)
	require.True(t, infraerrors.IsBadRequest(err))

	repo.record.GroupPlatform = PlatformOpenAI
	badThreshold, samples := 4, 2
	_, err = svc.PutPolicy(context.Background(), 9, GroupTTFTGuardPolicyInput{
		Mode: GroupTTFTGuardModeEnabled, DegradationTTFTSeconds: &badThreshold, MinSamples: &samples,
	})
	require.True(t, infraerrors.IsBadRequest(err))
}

func TestGroupTTFTGuardPolicyCachedInheritUsesHotGlobalSnapshot(t *testing.T) {
	repo := &groupTTFTGuardPolicyRepoStub{record: GroupTTFTGuardPolicyRecord{
		GroupID: 10, GroupName: "inherited", GroupPlatform: PlatformOpenAI,
	}}
	svc := newGroupTTFTGuardPolicyServiceForTest(repo, OpenAITTFTGuardSettings{
		Enabled: true, DegradationTTFTSeconds: 20, MinSamples: 4,
	})

	resolved, err := svc.Resolve(context.Background(), 10)
	require.NoError(t, err)
	require.True(t, resolved.Enabled)
	require.Equal(t, 20*time.Second, resolved.Threshold)
	require.Equal(t, 1, repo.gets)

	global := svc.globalSettings.(*groupTTFTGuardGlobalStub)
	global.snapshot = &OpenAITTFTGuardConfigSnapshot{
		Enabled: false, Threshold: 45 * time.Second, MinSamples: 8, Source: GroupTTFTGuardSourceGlobal,
	}
	resolved, err = svc.Resolve(context.Background(), 10)
	require.NoError(t, err)
	require.False(t, resolved.Enabled)
	require.Equal(t, 45*time.Second, resolved.Threshold)
	require.Equal(t, 8, resolved.MinSamples)
	require.Equal(t, 1, repo.gets, "hot global refresh must not reload the group policy row")
}

func TestGroupTTFTGuardPolicyCachedCustomIgnoresHotGlobalSnapshot(t *testing.T) {
	mode := GroupTTFTGuardModeEnabled
	threshold, samples := 12, 3
	repo := &groupTTFTGuardPolicyRepoStub{record: GroupTTFTGuardPolicyRecord{
		GroupID: 11, GroupName: "custom", GroupPlatform: PlatformOpenAI,
		Mode: &mode, DegradationTTFTSeconds: &threshold, MinSamples: &samples,
	}}
	svc := newGroupTTFTGuardPolicyServiceForTest(repo, OpenAITTFTGuardSettings{
		Enabled: true, DegradationTTFTSeconds: 20, MinSamples: 4,
	})

	resolved, err := svc.Resolve(context.Background(), 11)
	require.NoError(t, err)
	global := svc.globalSettings.(*groupTTFTGuardGlobalStub)
	global.snapshot = &OpenAITTFTGuardConfigSnapshot{
		Enabled: false, Threshold: 60 * time.Second, MinSamples: 10, Source: GroupTTFTGuardSourceGlobal,
	}
	resolved, err = svc.Resolve(context.Background(), 11)
	require.NoError(t, err)
	require.True(t, resolved.Enabled)
	require.Equal(t, 12*time.Second, resolved.Threshold)
	require.Equal(t, 3, resolved.MinSamples)
	require.Equal(t, GroupTTFTGuardSourceGroup, resolved.Source)
	require.Equal(t, 1, repo.gets)
}

func TestGroupTTFTGuardPolicyInvalidateClearsCacheAndRuntime(t *testing.T) {
	repo := &groupTTFTGuardPolicyRepoStub{record: GroupTTFTGuardPolicyRecord{
		GroupID: 12, GroupName: "runtime", GroupPlatform: PlatformOpenAI,
	}}
	svc := newGroupTTFTGuardPolicyServiceForTest(repo, OpenAITTFTGuardSettings{
		Enabled: true, DegradationTTFTSeconds: 20, MinSamples: 4,
	})
	_, err := svc.Resolve(context.Background(), 12)
	require.NoError(t, err)

	var invalidated []int64
	svc.SetRuntimeInvalidator(func(groupID int64) { invalidated = append(invalidated, groupID) })
	svc.Invalidate(12)
	require.Equal(t, []int64{12}, invalidated)

	_, err = svc.Resolve(context.Background(), 12)
	require.NoError(t, err)
	require.Equal(t, 2, repo.gets)
}

func TestGroupTTFTGuardPolicyInvalidatePreventsStaleReadFromRefillingCache(t *testing.T) {
	mode := GroupTTFTGuardModeEnabled
	oldThreshold, newThreshold, samples := 10, 25, 3
	repo := &blockingGroupTTFTGuardPolicyRepo{
		record: GroupTTFTGuardPolicyRecord{
			GroupID: 13, GroupName: "race", GroupPlatform: PlatformOpenAI,
			Mode: &mode, DegradationTTFTSeconds: &oldThreshold, MinSamples: &samples,
		},
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	svc := &GroupTTFTGuardPolicyService{
		repo:             repo,
		globalSettings:   &groupTTFTGuardGlobalStub{settings: *DefaultOpenAITTFTGuardSettings()},
		cacheTTL:         time.Hour,
		cache:            make(map[int64]cachedGroupTTFTGuardPolicy),
		cacheGenerations: make(map[int64]uint64),
	}

	resolvedCh := make(chan GroupTTFTGuardResolvedPolicy, 1)
	errCh := make(chan error, 1)
	go func() {
		resolved, err := svc.Resolve(context.Background(), 13)
		if err != nil {
			errCh <- err
			return
		}
		resolvedCh <- resolved
	}()

	<-repo.started
	repo.mu.Lock()
	repo.record.DegradationTTFTSeconds = &newThreshold
	repo.mu.Unlock()
	svc.Invalidate(13)
	close(repo.release)

	select {
	case err := <-errCh:
		require.NoError(t, err)
	case resolved := <-resolvedCh:
		require.Equal(t, 25*time.Second, resolved.Threshold)
	case <-time.After(time.Second):
		t.Fatal("policy resolve did not finish")
	}

	resolved, err := svc.Resolve(context.Background(), 13)
	require.NoError(t, err)
	require.Equal(t, 25*time.Second, resolved.Threshold)
	repo.mu.Lock()
	require.Equal(t, 2, repo.gets, "the retried value should be cached")
	repo.mu.Unlock()
}

func TestGroupTTFTGuardPolicyGlobalRefreshInvalidatesOnlyInheritedRuntime(t *testing.T) {
	global := &groupTTFTGuardGlobalStub{
		settings: OpenAITTFTGuardSettings{Enabled: true, DegradationTTFTSeconds: 20, MinSamples: 5},
		refreshSnapshot: OpenAITTFTGuardConfigSnapshot{
			Enabled: true, Threshold: 30 * time.Second, MinSamples: 6, Source: GroupTTFTGuardSourceGlobal,
		},
		refreshOK: true,
	}
	svc := &GroupTTFTGuardPolicyService{globalSettings: global}
	gateway := &OpenAIGatewayService{}
	svc.SetGlobalRuntimeInvalidator(gateway.InvalidateInheritedOpenAITTFTGuardRuntime)
	critical := 90_000
	inherited := enabledOpenAITTFTGuardConfig(20*time.Second, 5)
	inherited.Source = GroupTTFTGuardSourceGlobal
	custom := enabledOpenAITTFTGuardConfig(20*time.Second, 5)
	custom.Source = GroupTTFTGuardSourceGroup
	gateway.getOpenAITTFTGuard().report(14, 1, "gpt-test", true, &critical, inherited)
	gateway.getOpenAITTFTGuard().report(15, 2, "gpt-test", true, &critical, custom)

	svc.refreshGlobalSettingsAndInvalidateInherited(context.Background())

	require.False(t, gateway.getOpenAITTFTGuard().isDegraded(14, 1, "gpt-test"))
	require.True(t, gateway.getOpenAITTFTGuard().isDegraded(15, 2, "gpt-test"))
}

func TestGroupTTFTGuardPolicyGlobalRefreshFailurePreservesRuntime(t *testing.T) {
	global := &groupTTFTGuardGlobalStub{
		settings: OpenAITTFTGuardSettings{Enabled: true, DegradationTTFTSeconds: 20, MinSamples: 5},
		refreshSnapshot: OpenAITTFTGuardConfigSnapshot{
			Enabled: true, Threshold: 30 * time.Second, MinSamples: 6, Source: GroupTTFTGuardSourceGlobal,
		},
		refreshOK: false,
	}
	svc := &GroupTTFTGuardPolicyService{globalSettings: global}
	gateway := &OpenAIGatewayService{}
	svc.SetGlobalRuntimeInvalidator(gateway.InvalidateInheritedOpenAITTFTGuardRuntime)
	critical := 60_000
	inherited := enabledOpenAITTFTGuardConfig(20*time.Second, 5)
	inherited.Source = GroupTTFTGuardSourceGlobal
	gateway.getOpenAITTFTGuard().report(16, 1, "gpt-test", true, &critical, inherited)

	svc.refreshGlobalSettingsAndInvalidateInherited(context.Background())

	require.True(t, gateway.getOpenAITTFTGuard().isDegraded(16, 1, "gpt-test"))
}
