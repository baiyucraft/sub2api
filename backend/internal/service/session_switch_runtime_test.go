package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type sessionSwitchRuntimeCacheStub struct {
	GatewayCache
	recordCalls int
	clearCalls  int
	listCalls   int
	deleteCalls int
	recordErr   error
	clearErr    error
	listErr     error
	listIDs     []int64
	failures    int64
}

func (s *sessionSwitchRuntimeCacheStub) RecordSessionSwitchFailure(_ context.Context, _ SessionSwitchAccountScope, _ time.Duration, threshold int, cooldown time.Duration) (SessionSwitchFailureState, error) {
	s.recordCalls++
	if s.recordErr != nil {
		return SessionSwitchFailureState{}, s.recordErr
	}
	s.failures++
	state := SessionSwitchFailureState{FailureCount: s.failures}
	if s.failures >= int64(threshold) {
		state.Tripped = true
		state.CooldownUntil = time.Now().Add(cooldown)
		s.failures = 0
	}
	return state, nil
}

func (s *sessionSwitchRuntimeCacheStub) ClearSessionSwitchFailures(_ context.Context, _ SessionSwitchAccountScope) error {
	s.clearCalls++
	if s.clearErr == nil {
		s.failures = 0
	}
	return s.clearErr
}

func (s *sessionSwitchRuntimeCacheStub) ListSessionSwitchCooldownAccountIDs(_ context.Context, _ SessionSwitchScope) ([]int64, error) {
	s.listCalls++
	return append([]int64(nil), s.listIDs...), s.listErr
}

func (s *sessionSwitchRuntimeCacheStub) DeleteSessionAccountID(_ context.Context, _ int64, _ string) error {
	s.deleteCalls++
	return nil
}

func newSessionSwitchRuntimeForTest(cache *sessionSwitchRuntimeCacheStub) sessionSwitchRuntime {
	settingService := NewSettingService(&upstreamManagementSettingRepoStub{values: map[string]string{}}, nil)
	_, available := settingService.WarmSessionSwitchSettings(context.Background())
	if !available {
		panic("session switch settings test cache failed to warm")
	}
	return sessionSwitchRuntime{
		cache:          cache,
		settingService: settingService,
	}
}

func testSessionSwitchAPIKey(mode string) (*APIKey, *int64) {
	groupID := int64(22)
	return &APIKey{ID: 11, GroupID: &groupID, SchedulingMode: mode}, &groupID
}

func TestSessionSwitchRuntimeCacheFirstDoesNotTouchState(t *testing.T) {
	cache := &sessionSwitchRuntimeCacheStub{listIDs: []int64{33}}
	runtime := newSessionSwitchRuntimeForTest(cache)
	apiKey, groupID := testSessionSwitchAPIKey(APIKeySchedulingModeCacheFirst)

	require.Nil(t, runtime.excludedAccountIDs(context.Background(), apiKey, groupID, "session", "model"))
	require.False(t, runtime.recordFailure(context.Background(), apiKey, groupID, "session", "model", 33, 502))
	runtime.clearFailures(context.Background(), apiKey, groupID, "session", "model", 33)
	require.Zero(t, cache.listCalls)
	require.Zero(t, cache.recordCalls)
	require.Zero(t, cache.clearCalls)
}

func TestSessionSwitchRuntimeTripsThresholdAndDeletesStickyBinding(t *testing.T) {
	cache := &sessionSwitchRuntimeCacheStub{}
	runtime := newSessionSwitchRuntimeForTest(cache)
	deleteCalls := 0
	runtime.deleteStickySession = func(context.Context, int64, string) error {
		deleteCalls++
		return nil
	}
	apiKey, groupID := testSessionSwitchAPIKey(APIKeySchedulingModeSpeedFirst)
	ctx := context.Background()

	require.False(t, runtime.recordFailure(ctx, apiKey, groupID, "session", "model", 33, 502))
	require.False(t, runtime.recordFailure(ctx, apiKey, groupID, "session", "model", 33, 502))
	require.True(t, runtime.recordFailure(ctx, apiKey, groupID, "session", "model", 33, 502))
	require.Equal(t, 3, cache.recordCalls)
	require.Equal(t, 1, deleteCalls)
	require.Zero(t, cache.deleteCalls)
}

func TestSessionSwitchRuntimeIgnoresUnconfiguredStatusAndMissingSession(t *testing.T) {
	cache := &sessionSwitchRuntimeCacheStub{}
	runtime := newSessionSwitchRuntimeForTest(cache)
	apiKey, groupID := testSessionSwitchAPIKey(APIKeySchedulingModeSpeedFirst)

	require.False(t, runtime.recordFailure(context.Background(), apiKey, groupID, "session", "model", 33, 429))
	require.False(t, runtime.recordFailure(context.Background(), apiKey, groupID, "", "model", 33, 502))
	require.Zero(t, cache.recordCalls)
}

func TestSessionSwitchRuntimeListsCooldownsAndClearsFailures(t *testing.T) {
	cache := &sessionSwitchRuntimeCacheStub{listIDs: []int64{0, 33, 44}}
	runtime := newSessionSwitchRuntimeForTest(cache)
	apiKey, groupID := testSessionSwitchAPIKey(APIKeySchedulingModeSpeedFirst)

	excluded := runtime.excludedAccountIDs(context.Background(), apiKey, groupID, "session", "model")
	require.Equal(t, map[int64]struct{}{33: {}, 44: {}}, excluded)
	require.Equal(t, 1, cache.listCalls)

	cache.failures = 2
	runtime.clearFailures(context.Background(), apiKey, groupID, "session", "model", 33)
	require.Equal(t, 1, cache.clearCalls)
	require.Zero(t, cache.failures)
}

func TestSessionSwitchRuntimeStateErrorsFailOpen(t *testing.T) {
	cache := &sessionSwitchRuntimeCacheStub{
		recordErr: errors.New("record unavailable"),
		clearErr:  errors.New("clear unavailable"),
		listErr:   errors.New("list unavailable"),
	}
	runtime := newSessionSwitchRuntimeForTest(cache)
	apiKey, groupID := testSessionSwitchAPIKey(APIKeySchedulingModeSpeedFirst)

	require.Nil(t, runtime.excludedAccountIDs(context.Background(), apiKey, groupID, "session", "model"))
	require.False(t, runtime.recordFailure(context.Background(), apiKey, groupID, "session", "model", 33, 502))
	runtime.clearFailures(context.Background(), apiKey, groupID, "session", "model", 33)
	require.Equal(t, 1, cache.listCalls)
	require.Equal(t, 1, cache.recordCalls)
	require.Equal(t, 1, cache.clearCalls)
}

func TestSessionSwitchRuntimeMissingSettingsStoreFailsOpen(t *testing.T) {
	cache := &sessionSwitchRuntimeCacheStub{listIDs: []int64{33}}
	runtime := sessionSwitchRuntime{
		cache:          cache,
		settingService: NewSettingService(nil, nil),
	}
	apiKey, groupID := testSessionSwitchAPIKey(APIKeySchedulingModeSpeedFirst)

	require.Nil(t, runtime.excludedAccountIDs(context.Background(), apiKey, groupID, "session", "model"))
	require.False(t, runtime.recordFailure(context.Background(), apiKey, groupID, "session", "model", 33, 502))
	require.Zero(t, cache.listCalls)
	require.Zero(t, cache.recordCalls)
}
