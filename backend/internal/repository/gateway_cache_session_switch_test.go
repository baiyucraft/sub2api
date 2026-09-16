package repository

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func newSessionSwitchStoreTest(t *testing.T) (service.SessionSwitchStateStore, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	return NewSessionSwitchStateStore(client), mr
}

func testSessionSwitchScope(accountID int64) service.SessionSwitchAccountScope {
	return service.SessionSwitchAccountScope{
		SessionSwitchScope: service.SessionSwitchScope{
			APIKeyID:    101,
			GroupID:     202,
			SessionHash: "session-secret-value",
			RouteModel:  "gpt-route-secret",
		},
		AccountID: accountID,
	}
}

func TestSessionSwitchFailureCounterTripsAndKeepsFirstExpiry(t *testing.T) {
	store, mr := newSessionSwitchStoreTest(t)
	ctx := context.Background()
	scope := testSessionSwitchScope(303)
	window := 20 * time.Second
	cooldown := 45 * time.Second

	first, err := store.RecordSessionSwitchFailure(ctx, scope, window, 3, cooldown)
	require.NoError(t, err)
	require.Equal(t, int64(1), first.FailureCount)
	require.False(t, first.Tripped)
	require.True(t, first.CooldownUntil.IsZero())

	failureKey, err := sessionSwitchFailureKey(scope)
	require.NoError(t, err)
	firstTTL := mr.TTL(failureKey)
	require.Greater(t, firstTTL, 0*time.Second)

	mr.FastForward(5 * time.Second)
	second, err := store.RecordSessionSwitchFailure(ctx, scope, window, 3, cooldown)
	require.NoError(t, err)
	require.Equal(t, int64(2), second.FailureCount)
	require.False(t, second.Tripped)
	require.Less(t, mr.TTL(failureKey), firstTTL)

	third, err := store.RecordSessionSwitchFailure(ctx, scope, window, 3, cooldown)
	require.NoError(t, err)
	require.Equal(t, int64(3), third.FailureCount)
	require.True(t, third.Tripped)
	require.False(t, third.CooldownUntil.IsZero())
	require.False(t, mr.Exists(failureKey), "threshold trip must clear the failure counter")

	accountIDs, err := store.ListSessionSwitchCooldownAccountIDs(ctx, scope.SessionSwitchScope)
	require.NoError(t, err)
	require.Equal(t, []int64{303}, accountIDs)
}

func TestSessionSwitchSuccessClearsOnlyMatchingFailureCounter(t *testing.T) {
	store, _ := newSessionSwitchStoreTest(t)
	ctx := context.Background()
	firstAccount := testSessionSwitchScope(303)
	secondAccount := testSessionSwitchScope(304)

	_, err := store.RecordSessionSwitchFailure(ctx, firstAccount, time.Minute, 3, time.Minute)
	require.NoError(t, err)
	_, err = store.RecordSessionSwitchFailure(ctx, secondAccount, time.Minute, 3, time.Minute)
	require.NoError(t, err)
	require.NoError(t, store.ClearSessionSwitchFailures(ctx, firstAccount))

	first, err := store.RecordSessionSwitchFailure(ctx, firstAccount, time.Minute, 3, time.Minute)
	require.NoError(t, err)
	require.Equal(t, int64(1), first.FailureCount)
	second, err := store.RecordSessionSwitchFailure(ctx, secondAccount, time.Minute, 3, time.Minute)
	require.NoError(t, err)
	require.Equal(t, int64(2), second.FailureCount)
}

func TestSessionSwitchCooldownsAreScopeIsolatedAndExpire(t *testing.T) {
	store, mr := newSessionSwitchStoreTest(t)
	ctx := context.Background()
	first := testSessionSwitchScope(305)
	second := testSessionSwitchScope(304)
	cooldown := 30 * time.Second

	_, err := store.RecordSessionSwitchFailure(ctx, first, time.Minute, 1, cooldown)
	require.NoError(t, err)
	_, err = store.RecordSessionSwitchFailure(ctx, second, time.Minute, 1, cooldown)
	require.NoError(t, err)

	accountIDs, err := store.ListSessionSwitchCooldownAccountIDs(ctx, first.SessionSwitchScope)
	require.NoError(t, err)
	require.Equal(t, []int64{304, 305}, accountIDs)

	otherScopes := []service.SessionSwitchScope{
		{APIKeyID: 102, GroupID: 202, SessionHash: first.SessionHash, RouteModel: first.RouteModel},
		{APIKeyID: 101, GroupID: 203, SessionHash: first.SessionHash, RouteModel: first.RouteModel},
		{APIKeyID: 101, GroupID: 202, SessionHash: "other-session", RouteModel: first.RouteModel},
		{APIKeyID: 101, GroupID: 202, SessionHash: first.SessionHash, RouteModel: "other-model"},
	}
	for _, otherScope := range otherScopes {
		got, listErr := store.ListSessionSwitchCooldownAccountIDs(ctx, otherScope)
		require.NoError(t, listErr)
		require.Empty(t, got)
	}

	mr.FastForward(cooldown + time.Second)
	accountIDs, err = store.ListSessionSwitchCooldownAccountIDs(ctx, first.SessionSwitchScope)
	require.NoError(t, err)
	require.Empty(t, accountIDs)
}

func TestSessionSwitchRedisKeysHashSensitiveComponents(t *testing.T) {
	store, mr := newSessionSwitchStoreTest(t)
	scope := testSessionSwitchScope(303)
	_, err := store.RecordSessionSwitchFailure(context.Background(), scope, time.Minute, 1, time.Minute)
	require.NoError(t, err)

	keys := mr.Keys()
	require.NotEmpty(t, keys)
	for _, key := range keys {
		require.NotContains(t, key, scope.SessionHash)
		require.NotContains(t, key, scope.RouteModel)
		require.True(t, strings.HasPrefix(key, sessionSwitchStatePrefix))
	}
	failureKey, err := sessionSwitchFailureKey(scope)
	require.NoError(t, err)
	cooldownKey, err := sessionSwitchCooldownKey(scope.SessionSwitchScope)
	require.NoError(t, err)
	failureHashTag := failureKey[strings.Index(failureKey, "{") : strings.Index(failureKey, "}")+1]
	require.Contains(t, cooldownKey, failureHashTag, "Lua keys must share one Redis Cluster hash slot")
}

func TestSessionSwitchStoreRejectsInvalidInputAndReturnsRedisErrors(t *testing.T) {
	store, mr := newSessionSwitchStoreTest(t)
	ctx := context.Background()
	scope := testSessionSwitchScope(303)

	_, err := store.RecordSessionSwitchFailure(ctx, scope, 0, 3, time.Minute)
	require.Error(t, err)
	_, err = store.RecordSessionSwitchFailure(ctx, scope, time.Nanosecond, 3, time.Minute)
	require.Error(t, err)
	_, err = store.RecordSessionSwitchFailure(ctx, scope, time.Minute, 3, time.Nanosecond)
	require.Error(t, err)
	invalidScope := scope
	invalidScope.SessionHash = " "
	_, err = store.RecordSessionSwitchFailure(ctx, invalidScope, time.Minute, 3, time.Minute)
	require.Error(t, err)

	mr.Close()
	_, err = store.RecordSessionSwitchFailure(ctx, scope, time.Minute, 3, time.Minute)
	require.Error(t, err, "Redis failures must be returned for the caller's fail-open policy")
}

func TestGatewayCacheComposesSessionSwitchStoreWithoutLosingOptionalStores(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { require.NoError(t, client.Close()) })

	cache := NewGatewayCache(client)
	require.Implements(t, (*service.SessionSwitchStateStore)(nil), cache)
	require.Implements(t, (*service.LiveCallStore)(nil), cache)
	require.Implements(t, (*service.CyberSessionBlockStore)(nil), cache)
	require.Implements(t, (*service.OpenAIWSSessionPreemptionCache)(nil), cache)
}

func TestGatewayCacheSpeedFirstStickyBindingsAreAPIKeyIsolated(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	cache := NewGatewayCache(client)
	groupID := int64(22)
	sessionHash := "shared-client-session"
	ttl := time.Minute

	firstCtx := service.WithSessionSwitchStickyScope(context.Background(), &service.APIKey{ID: 101, SchedulingMode: service.APIKeySchedulingModeSpeedFirst})
	secondCtx := service.WithSessionSwitchStickyScope(context.Background(), &service.APIKey{ID: 102, SchedulingMode: service.APIKeySchedulingModeSpeedFirst})
	firstStickyCtx := service.WithSessionSwitchStickyOperation(firstCtx)
	secondStickyCtx := service.WithSessionSwitchStickyOperation(secondCtx)
	require.NoError(t, cache.SetSessionAccountID(firstStickyCtx, groupID, sessionHash, 301, ttl))
	require.NoError(t, cache.SetSessionAccountID(secondStickyCtx, groupID, sessionHash, 302, ttl))

	firstAccountID, err := cache.GetSessionAccountID(firstStickyCtx, groupID, sessionHash)
	require.NoError(t, err)
	require.Equal(t, int64(301), firstAccountID)
	secondAccountID, err := cache.GetSessionAccountID(secondStickyCtx, groupID, sessionHash)
	require.NoError(t, err)
	require.Equal(t, int64(302), secondAccountID)

	require.NoError(t, cache.DeleteSessionAccountID(firstStickyCtx, groupID, sessionHash))
	_, err = cache.GetSessionAccountID(firstStickyCtx, groupID, sessionHash)
	require.ErrorIs(t, err, service.ErrStickySessionNotFound)
	secondAccountID, err = cache.GetSessionAccountID(secondStickyCtx, groupID, sessionHash)
	require.NoError(t, err)
	require.Equal(t, int64(302), secondAccountID, "switching one API key must not alter another key's sticky binding")

	require.NoError(t, cache.SetSessionAccountID(context.Background(), groupID, sessionHash, 303, ttl))
	plainAccountID, err := cache.GetSessionAccountID(context.Background(), groupID, sessionHash)
	require.NoError(t, err)
	require.Equal(t, int64(303), plainAccountID, "cache-first requests must keep the upstream sticky namespace")

	require.NoError(t, cache.SetSessionAccountID(firstCtx, groupID, "response-owner-state", 401, ttl))
	nonStickyAccountID, err := cache.GetSessionAccountID(secondCtx, groupID, "response-owner-state")
	require.NoError(t, err)
	require.Equal(t, int64(401), nonStickyAccountID, "non-sticky GatewayCache state must not inherit the API-key namespace")
}
