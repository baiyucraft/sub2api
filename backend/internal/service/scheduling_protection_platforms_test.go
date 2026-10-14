package service

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/upstreamcacheguard"
	"github.com/stretchr/testify/require"
)

func TestSchedulingProtectionPlatformsDefaultsEmptyAndCanonicalPersistence(t *testing.T) {
	ctx := context.Background()
	repo := &openAITTFTGuardSettingsRepoStub{values: map[string]string{}}
	s := NewSettingService(repo, nil)
	require.Equal(t, []string{PlatformOpenAI}, s.SchedulingProtectionPlatformsSnapshot())
	platforms, err := s.GetSchedulingProtectionPlatforms(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{PlatformOpenAI}, platforms)
	require.NoError(t, s.SetSchedulingProtectionPlatforms(ctx, []string{" OPENAI ", PlatformAnthropic, PlatformOpenAI}))
	require.Equal(t, []string{PlatformAnthropic, PlatformOpenAI}, s.SchedulingProtectionPlatformsSnapshot())
	require.JSONEq(t, `["anthropic","openai"]`, repo.values[SettingKeySchedulingProtectionPlatforms])
	platforms = s.SchedulingProtectionPlatformsSnapshot()
	platforms[0] = PlatformComposite
	require.Equal(t, []string{PlatformAnthropic, PlatformOpenAI}, s.SchedulingProtectionPlatformsSnapshot())
	require.NoError(t, s.SetSchedulingProtectionPlatforms(ctx, []string{}))
	platforms, err = s.GetSchedulingProtectionPlatforms(ctx)
	require.NoError(t, err)
	require.NotNil(t, platforms)
	require.Empty(t, platforms)
	raw, err := json.Marshal(platforms)
	require.NoError(t, err)
	require.Equal(t, "[]", string(raw))
	require.Equal(t, "[]", repo.values[SettingKeySchedulingProtectionPlatforms])
}

func TestSchedulingProtectionPlatformsInvalidPersistenceFailsClosed(t *testing.T) {
	for _, raw := range []string{`null`, `invalid`, `["composite"]`, `["unknown"]`, `["openai","unknown"]`} {
		t.Run(raw, func(t *testing.T) {
			repo := &openAITTFTGuardSettingsRepoStub{values: map[string]string{SettingKeySchedulingProtectionPlatforms: raw}}
			s := NewSettingService(repo, nil)
			platforms, err := s.GetSchedulingProtectionPlatforms(context.Background())
			require.NoError(t, err)
			require.NotNil(t, platforms)
			require.Empty(t, platforms)
			require.Empty(t, s.SchedulingProtectionPlatformsSnapshot())
			require.Equal(t, raw, repo.values[SettingKeySchedulingProtectionPlatforms], "read must not rewrite a malformed setting")
		})
	}
}

func TestSchedulingProtectionPlatformsRejectsInvalidAndFailedWritesWithoutPublishing(t *testing.T) {
	ctx := context.Background()
	repo := &openAITTFTGuardSettingsRepoStub{values: map[string]string{}}
	bus := &openAITTFTGuardInvalidationBusStub{}
	s := NewSettingService(repo, nil)
	s.SetOpenAITTFTGuardInvalidationBus(bus)
	for _, platforms := range [][]string{{PlatformComposite}, {"unknown"}, {""}, {PlatformOpenAI, "unknown"}} {
		err := s.SetSchedulingProtectionPlatforms(ctx, platforms)
		require.True(t, infraerrors.IsBadRequest(err))
	}
	require.Empty(t, repo.values)
	require.Zero(t, bus.calls())
	repo.setErr = errors.New("write unavailable")
	require.Error(t, s.SetSchedulingProtectionPlatforms(ctx, []string{}))
	require.Equal(t, []string{PlatformOpenAI}, s.SchedulingProtectionPlatformsSnapshot())
	require.Zero(t, bus.calls())
}

type schedulingProtectionPermissiveReader struct{}

func (schedulingProtectionPermissiveReader) PlatformSelected(string) bool { return true }

func TestSchedulingProtectionPlatformsHelperRejectsCompositeEvenWithPermissiveReader(t *testing.T) {
	require.True(t, SchedulingProtectionPlatformSelected(nil, PlatformOpenAI))
	require.False(t, SchedulingProtectionPlatformSelected(nil, PlatformAnthropic))
	require.False(t, SchedulingProtectionPlatformSelected(schedulingProtectionPermissiveReader{}, PlatformComposite))
	require.False(t, SchedulingProtectionPlatformSelected(schedulingProtectionPermissiveReader{}, "unknown"))
}

func TestSetManagementSettingsSchedulingPlatformsOmissionPreservesAndInvalidPreventsAllWrites(t *testing.T) {
	ctx := context.Background()
	repo := &upstreamManagementSettingRepoStub{values: map[string]string{SettingKeySchedulingProtectionPlatforms: `["anthropic"]`}}
	s := NewSettingService(repo, nil)
	upstream := NewUpstreamConfigService(nil, nil, nil)
	upstream.SetHealthProbeDependencies(nil, s)
	settings := validUpstreamManagementSettings()
	require.NoError(t, upstream.SetManagementSettings(ctx, settings))
	require.JSONEq(t, `["anthropic"]`, repo.values[SettingKeySchedulingProtectionPlatforms])
	settings.SchedulingProtectionPlatforms = []string{}
	require.NoError(t, upstream.SetManagementSettings(ctx, settings))
	require.Equal(t, "[]", repo.values[SettingKeySchedulingProtectionPlatforms])
	got, err := upstream.GetManagementSettings(ctx)
	require.NoError(t, err)
	require.NotNil(t, got.SchedulingProtectionPlatforms)
	require.Empty(t, got.SchedulingProtectionPlatforms)
	before := make(map[string]string, len(repo.values))
	for key, value := range repo.values {
		before[key] = value
	}
	calls := repo.setMultipleCalls
	settings.SchedulingProtectionPlatforms = []string{PlatformComposite}
	settings.TTFTGuard.Enabled = true
	require.Error(t, upstream.SetManagementSettings(ctx, settings))
	require.Equal(t, calls, repo.setMultipleCalls)
	require.Equal(t, before, repo.values)
}

type schedulingProtectionRepoStub struct {
	records []GroupSchedulingProtectionPolicyRecord
	puts    int
}

func (s *schedulingProtectionRepoStub) List(context.Context) ([]GroupSchedulingProtectionPolicyRecord, error) {
	return append([]GroupSchedulingProtectionPolicyRecord{}, s.records...), nil
}

func (s *schedulingProtectionRepoStub) Get(_ context.Context, id int64) (*GroupSchedulingProtectionPolicyRecord, error) {
	for _, record := range s.records {
		if record.TTFT.GroupID == id {
			return &record, nil
		}
	}
	return nil, errors.New("group not found")
}

func (s *schedulingProtectionRepoStub) Put(_ context.Context, _ int64, _ GroupSchedulingProtectionPolicyWrite) (bool, error) {
	s.puts++
	return true, nil
}

func protectionScopeInput(mode string) GroupSchedulingProtectionPolicyInput {
	return GroupSchedulingProtectionPolicyInput{TTFT: &GroupTTFTGuardPolicyInput{Mode: mode}, Cache: &GroupCacheGuardPolicyInput{Mode: mode}, Priority: SchedulingPriorityInherit}
}

func TestGroupSchedulingProtectionScopeFiltersListsRetainsRawPoliciesAndRejectsExcludedWrites(t *testing.T) {
	ctx := context.Background()
	mode, threshold, samples := GroupTTFTGuardModeEnabled, 10, 3
	repo := &schedulingProtectionRepoStub{}
	for i, platform := range []string{PlatformOpenAI, PlatformAnthropic, PlatformComposite} {
		repo.records = append(repo.records, GroupSchedulingProtectionPolicyRecord{
			TTFT: GroupTTFTGuardPolicyRecord{GroupID: int64(i + 1), GroupPlatform: platform, Mode: &mode, DegradationTTFTSeconds: &threshold, MinSamples: &samples}, CacheMode: &mode})
	}
	settings := NewSettingService(&upstreamManagementSettingRepoStub{values: map[string]string{}}, nil)
	s := NewGroupSchedulingProtectionPolicyService(repo, settings, nil, nil)
	views, err := s.ListProtectionPolicies(ctx)
	require.NoError(t, err)
	require.Len(t, views, 1)
	require.Equal(t, int64(1), views[0].GroupID)
	view, err := s.GetProtectionPolicy(ctx, 2)
	require.NoError(t, err)
	require.False(t, view.PlatformSelected)
	require.Equal(t, mode, view.Cache.Mode)
	require.Equal(t, GroupTTFTGuardSourceGroup, view.Cache.Source)
	require.Equal(t, upstreamcacheguard.Disabled, view.Cache.EffectiveMode)
	require.Equal(t, upstreamcacheguard.Protect, s.ProtectionPolicies()[2].Cache.Mode, "raw settings preserve the runtime's evidence cohort")
	for _, id := range []int64{2, 3} {
		for _, writeMode := range []string{GroupTTFTGuardModeInherit, GroupTTFTGuardModeDisabled, GroupTTFTGuardModeEnabled} {
			_, err := s.PutProtectionPolicy(ctx, id, protectionScopeInput(writeMode))
			require.ErrorContains(t, err, "GROUP_PROTECTION_PLATFORM_NOT_SELECTED")
		}
	}
	require.Zero(t, repo.puts)
	require.NoError(t, settings.SetSchedulingProtectionPlatforms(ctx, []string{}))
	resolved, err := s.ResolveProtection(ctx, 1)
	require.NoError(t, err)
	require.False(t, resolved.PlatformSelected)
	require.False(t, resolved.TTFT.Enabled)
	require.Equal(t, upstreamcacheguard.Disabled, resolved.Cache.Mode)
	require.Equal(t, mode, resolved.CacheMode)
	require.Equal(t, GroupTTFTGuardSourceGroup, resolved.TTFT.Source)
	require.True(t, s.ProtectionPolicies()[1].TTFT.Enabled)
	require.NoError(t, settings.SetSchedulingProtectionPlatforms(ctx, []string{PlatformOpenAI}))
	resolved, err = s.ResolveProtection(ctx, 1)
	require.NoError(t, err)
	require.True(t, resolved.TTFT.Enabled)
	require.Equal(t, upstreamcacheguard.Protect, resolved.Cache.Mode)
}

func TestGroupSchedulingProtectionSelectedNonOpenAICacheDoesNotEnableTTFT(t *testing.T) {
	ctx := context.Background()
	repo := &schedulingProtectionRepoStub{records: []GroupSchedulingProtectionPolicyRecord{{TTFT: GroupTTFTGuardPolicyRecord{GroupID: 1, GroupPlatform: PlatformAnthropic}}}}
	settings := NewSettingService(&upstreamManagementSettingRepoStub{values: map[string]string{}}, nil)
	require.NoError(t, settings.SetSchedulingProtectionPlatforms(ctx, []string{PlatformAnthropic}))
	s := NewGroupSchedulingProtectionPolicyService(repo, settings, nil, nil)
	input := protectionScopeInput(GroupTTFTGuardModeEnabled)
	_, err := s.PutProtectionPolicy(ctx, 1, input)
	require.ErrorContains(t, err, "GROUP_TTFT_GUARD_UNSUPPORTED_PLATFORM")
	require.Zero(t, repo.puts)
	input.TTFT.Mode = GroupTTFTGuardModeDisabled
	_, err = s.PutProtectionPolicy(ctx, 1, input)
	require.NoError(t, err)
	require.Equal(t, 1, repo.puts)
	resolved, err := s.ResolveProtection(ctx, 1)
	require.NoError(t, err)
	require.True(t, resolved.PlatformSelected)
	require.False(t, resolved.TTFT.Enabled)
}

type schedulingProtectionCountingSettingsRepo struct {
	upstreamManagementSettingRepoStub
	gets int
}

func (r *schedulingProtectionCountingSettingsRepo) GetValue(ctx context.Context, key string) (string, error) {
	r.gets++
	return r.upstreamManagementSettingRepoStub.GetValue(ctx, key)
}

func TestGroupSchedulingProtectionHotScopeAndGroupPlatformAvoidDatabaseReads(t *testing.T) {
	settingsRepo := &schedulingProtectionCountingSettingsRepo{upstreamManagementSettingRepoStub: upstreamManagementSettingRepoStub{values: map[string]string{}}}
	settings := NewSettingService(settingsRepo, nil)
	repo := &schedulingProtectionRepoStub{records: []GroupSchedulingProtectionPolicyRecord{{TTFT: GroupTTFTGuardPolicyRecord{GroupID: 1, GroupPlatform: PlatformOpenAI}}}}
	s := NewGroupSchedulingProtectionPolicyService(repo, settings, nil, nil)
	gets := settingsRepo.gets
	for i := 0; i < 100; i++ {
		require.True(t, s.PlatformSelected(PlatformOpenAI))
		require.False(t, s.PlatformSelected(PlatformComposite))
		platform, ok := s.ProtectionGroupPlatform(1)
		require.True(t, ok)
		require.Equal(t, PlatformOpenAI, platform)
		_, err := s.ResolveProtection(context.Background(), 1)
		require.NoError(t, err)
	}
	require.Equal(t, gets, settingsRepo.gets)
	platforms := s.ProtectionPlatformsSnapshot()
	platforms[0] = PlatformComposite
	require.True(t, s.PlatformSelected(PlatformOpenAI))
}

type schedulingProtectionFanoutBus struct {
	mu       sync.Mutex
	handlers []func(int64)
}

func (b *schedulingProtectionFanoutBus) SubscribeUpdates(_ context.Context, handler func(int64)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.handlers = append(b.handlers, handler)
}

func (b *schedulingProtectionFanoutBus) NotifyUpdate(_ context.Context, id int64) error {
	b.mu.Lock()
	handlers := append([]func(int64){}, b.handlers...)
	b.mu.Unlock()
	for _, handler := range handlers {
		handler(id)
	}
	return nil
}

func (b *schedulingProtectionFanoutBus) NotifyGlobalUpdate(ctx context.Context) error {
	return b.NotifyUpdate(ctx, 0)
}

func TestGroupTTFTGuardPolicySchedulingScopeCrossInstanceInvalidatesCustomWithoutClearingHistory(t *testing.T) {
	ctx := context.Background()
	repo := &openAITTFTGuardSettingsRepoStub{values: map[string]string{}}
	bus := &schedulingProtectionFanoutBus{}
	writer := NewSettingService(repo, nil)
	reader := NewSettingService(repo, nil)
	mode, threshold, samples := GroupTTFTGuardModeEnabled, 10, 3
	groupRepo := &groupTTFTGuardPolicyRepoStub{record: GroupTTFTGuardPolicyRecord{GroupID: 1, GroupPlatform: PlatformOpenAI, Mode: &mode, DegradationTTFTSeconds: &threshold, MinSamples: &samples}}
	s := NewGroupTTFTGuardPolicyService(groupRepo, reader, bus)
	protectionRepo := &schedulingProtectionRepoStub{records: []GroupSchedulingProtectionPolicyRecord{{TTFT: groupRepo.record, CacheMode: &mode}}}
	protection := NewGroupSchedulingProtectionPolicyService(protectionRepo, reader, s, bus)
	writer.SetOpenAITTFTGuardInvalidationBus(bus)
	runtimeClears := 0
	s.SetRuntimeInvalidator(func(int64) { runtimeClears++ })
	resolved, err := s.Resolve(ctx, 1)
	require.NoError(t, err)
	require.True(t, resolved.Enabled)
	require.NotEmpty(t, s.cache)
	require.NoError(t, writer.SetSchedulingProtectionPlatforms(ctx, []string{}))
	require.Empty(t, reader.SchedulingProtectionPlatformsSnapshot())
	require.Empty(t, s.cache, "custom policy cache must also be invalidated")
	require.Zero(t, runtimeClears, "scope changes preserve runtime history")
	joint, err := protection.ResolveProtection(ctx, 1)
	require.NoError(t, err)
	require.False(t, joint.PlatformSelected)
	require.False(t, joint.TTFT.Enabled)
	require.Equal(t, upstreamcacheguard.Disabled, joint.Cache.Mode)
	require.Equal(t, mode, protection.ProtectionPolicies()[1].CacheMode)
	require.True(t, protection.ProtectionPolicies()[1].TTFT.Enabled)
	require.Equal(t, upstreamcacheguard.Protect, protection.ProtectionPolicies()[1].Cache.Mode)
	resolved, err = s.Resolve(ctx, 1)
	require.NoError(t, err)
	require.False(t, resolved.Enabled)
	require.Equal(t, mode, resolved.Mode)
	require.Equal(t, GroupTTFTGuardSourceGroup, resolved.Source)
	view, err := s.GetPolicy(ctx, 1)
	require.NoError(t, err)
	require.False(t, view.PlatformSelected)
	require.False(t, view.EffectiveEnabled)
	require.Equal(t, mode, view.Mode)
	views, err := s.ListPolicies(ctx)
	require.NoError(t, err)
	require.Empty(t, views)
	for _, writeMode := range []string{GroupTTFTGuardModeInherit, GroupTTFTGuardModeDisabled, GroupTTFTGuardModeEnabled} {
		_, err := s.PutPolicy(ctx, 1, GroupTTFTGuardPolicyInput{Mode: writeMode})
		require.ErrorContains(t, err, "GROUP_PROTECTION_PLATFORM_NOT_SELECTED")
	}
	require.Empty(t, groupRepo.putMode)
	require.NoError(t, writer.SetSchedulingProtectionPlatforms(ctx, []string{PlatformOpenAI}))
	resolved, err = s.Resolve(ctx, 1)
	require.NoError(t, err)
	require.True(t, resolved.Enabled)
	require.Equal(t, threshold, int(resolved.Threshold.Seconds()))
	joint, err = protection.ResolveProtection(ctx, 1)
	require.NoError(t, err)
	require.True(t, joint.PlatformSelected)
	require.True(t, joint.TTFT.Enabled)
	require.Equal(t, upstreamcacheguard.Protect, joint.Cache.Mode)
	gets := groupRepo.gets
	for i := 0; i < 10; i++ {
		_, err = s.Resolve(ctx, 1)
		require.NoError(t, err)
	}
	require.Equal(t, gets, groupRepo.gets, "cached resolution stays hot")
}

func TestGroupTTFTGuardPolicyCompositeIsNeverSupportedOrWritable(t *testing.T) {
	repo := &groupTTFTGuardPolicyRepoStub{record: GroupTTFTGuardPolicyRecord{GroupID: 1, GroupPlatform: PlatformComposite}}
	s := newGroupTTFTGuardPolicyServiceForTest(repo, *DefaultOpenAITTFTGuardSettings())
	_, err := s.Resolve(context.Background(), 1)
	require.ErrorContains(t, err, "GROUP_TTFT_GUARD_UNSUPPORTED_PLATFORM")
	for _, mode := range []string{GroupTTFTGuardModeInherit, GroupTTFTGuardModeDisabled, GroupTTFTGuardModeEnabled} {
		_, err = s.PutPolicy(context.Background(), 1, GroupTTFTGuardPolicyInput{Mode: mode})
		require.ErrorContains(t, err, "GROUP_PROTECTION_PLATFORM_NOT_SELECTED")
	}
	require.Empty(t, repo.putMode)
}
