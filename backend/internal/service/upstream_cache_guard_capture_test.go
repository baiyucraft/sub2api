package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/upstreamcacheusage"
	"github.com/Wei-Shaw/sub2api/internal/upstreamcacheguard"
	"github.com/stretchr/testify/require"
)

func cacheGuardCaptureSendFixture(t *testing.T) (*UpstreamCacheGuardRunner, *runnerBootstrapIncrementalStore, *Account, int64) {
	t.Helper()
	store := &runnerBootstrapIncrementalStore{newRunnerTestStore()}
	runner := testRunner(t, store.runnerTestStore)
	runner.engine = upstreamcacheguard.NewEngine(store)
	runner.store = store
	runner.secret = []byte("capture-send-test-only")
	settings := upstreamcacheguard.DefaultSettings()
	settings.Mode = upstreamcacheguard.Protect
	settings.TrialPercent = 100
	require.NoError(t, runner.engine.ApplySettings(settings))
	group, key, config := int64(1), int64(3), int64(4)
	account := &Account{ID: 2, Platform: PlatformOpenAI, UpstreamConfigID: &config, UpstreamKeyID: &key}
	return runner, store, account, group
}

func beginCacheGuardCaptureSend(t *testing.T, account *Account, group int64, session string) (context.Context, *UpstreamCacheGuardAdmission) {
	t.Helper()
	ctx := WithUpstreamCacheGuardScheduling(context.Background(), 7)
	ctx = WithUpstreamCacheGuardAttempt(ctx, 7, &group, session, true)
	PrepareOpenAIUpstreamCacheGuardSend(ctx, account, "gpt-test", "openai")
	require.NoError(t, runSchedulingProtectionBeforeSend(ctx, account, "gpt-test"))
	holder := ctx.Value(cacheUsageAttemptContextKey{}).(*cacheUsageAttempt)
	holder.mu.Lock()
	defer holder.mu.Unlock()
	return ctx, holder.admission
}

func finishCacheGuardCaptureSend(t *testing.T, runner *UpstreamCacheGuardRunner, ctx context.Context, read int64) {
	t.Helper()
	usage := &UpstreamCacheUsageEvidence{Status: upstreamcacheusage.Known, Protocol: "openai", TotalInputTokens: 1000, CacheReadTokens: read}
	FinishUpstreamCacheGuardAttempt(ctx, usage, true, time.Now().UTC())
	FinishUpstreamCacheGuardAttempt(ctx, usage, true, time.Now().UTC())
	runner.processPending()
}

func TestUpstreamCacheGuardUnavailableProtectionStillCapturesAtSend(t *testing.T) {
	runner, store, account, group := cacheGuardCaptureSendFixture(t)
	// No refresh has succeeded. An ordinary selection must still persist its
	// pre-send classification without pretending a usable preference exists.
	firstContext, first := beginCacheGuardCaptureSend(t, account, group, "capture-without-preference")
	require.NotNil(t, first)
	require.True(t, first.Admission.CaptureOnly)
	require.True(t, first.Admission.NaturalNew)
	require.False(t, first.Admission.WarmAtStart)
	require.False(t, first.Admission.Trial)
	finishCacheGuardCaptureSend(t, runner, firstContext, 0)

	// Keep the snapshot unavailable: the route must warm from its durable cold
	// completion rather than relying on a newly published account snapshot.
	secondContext, second := beginCacheGuardCaptureSend(t, account, group, "capture-without-preference")
	require.NotNil(t, second)
	require.True(t, second.Admission.CaptureOnly)
	require.True(t, second.Admission.WarmAtStart)
	require.False(t, second.Admission.NaturalNew)
	require.False(t, second.Admission.Trial)
	finishCacheGuardCaptureSend(t, runner, secondContext, 0)

	store.mu.Lock()
	require.Len(t, store.events, 2)
	valid := 0
	for id, fact := range store.events {
		require.True(t, store.processed[id])
		require.True(t, fact.Admission.CaptureOnly)
		require.True(t, fact.Success)
		if fact.ValidSample() {
			valid++
			require.Equal(t, int64(0), fact.RawUsage.CacheReadTokens, "known zero must remain an effective sample")
		}
	}
	require.Equal(t, 1, valid)
	var session upstreamcacheguard.SessionState
	require.NoError(t, json.Unmarshal(store.rows[upstreamcacheguard.SessionKind+":"+first.Admission.SessionKey], &session))
	var quota upstreamcacheguard.GroupState
	require.NoError(t, json.Unmarshal(store.rows[upstreamcacheguard.GroupKind+":quota"], &quota))
	store.mu.Unlock()
	require.Zero(t, session.LastAccountID)
	require.Empty(t, session.LastRouteKey)
	require.Empty(t, session.CohortID)
	require.Zero(t, session.TrialAccountID)
	require.True(t, session.ResidenceUntil.IsZero())
	require.Equal(t, int64(1), quota.NaturalNewCount)
	require.Zero(t, quota.TrialCount)
	require.NoError(t, runner.engine.Refresh(context.Background()))
	projection, available := runner.engine.CachedAccountForGroupAt(group, account.ID, *account.UpstreamKeyID, time.Now().UTC())
	require.True(t, available)
	require.Equal(t, 1, projection.ValidRequests)
	require.Zero(t, projection.CachePercent)
}

func TestUpstreamCacheGuardCaptureDatabaseFailureAllowsOrdinarySend(t *testing.T) {
	runner, store, account, group := cacheGuardCaptureSendFixture(t)
	store.mu.Lock()
	store.completeDown = true
	store.mu.Unlock()
	ctx, admission := beginCacheGuardCaptureSend(t, account, group, "database-unavailable")
	require.Nil(t, admission, "failed durable read must not fabricate cold/new classification")
	finishCacheGuardCaptureSend(t, runner, ctx, 900)
	store.mu.Lock()
	require.Empty(t, store.rows)
	require.Empty(t, store.events)
	store.mu.Unlock()
	require.Equal(t, uint64(1), runner.skipCounts[cacheGuardSkipAdmissionError].Load())
}

func TestUpstreamCacheGuardFreshSendKeepsOrdinaryAdmission(t *testing.T) {
	runner, store, account, group := cacheGuardCaptureSendFixture(t)
	require.NoError(t, runner.engine.Refresh(context.Background()))
	ctx, first := beginCacheGuardCaptureSend(t, account, group, "fresh-ordinary")
	require.NotNil(t, first)
	require.False(t, first.Admission.CaptureOnly)
	require.False(t, first.Admission.WarmAtStart)
	finishCacheGuardCaptureSend(t, runner, ctx, 900)
	require.NoError(t, runner.engine.Refresh(context.Background()))
	ctx, second := beginCacheGuardCaptureSend(t, account, group, "fresh-ordinary")
	require.NotNil(t, second)
	require.False(t, second.Admission.CaptureOnly)
	require.True(t, second.Admission.WarmAtStart)
	finishCacheGuardCaptureSend(t, runner, ctx, 900)
	store.mu.Lock()
	var session upstreamcacheguard.SessionState
	require.NoError(t, json.Unmarshal(store.rows[upstreamcacheguard.SessionKind+":"+first.Admission.SessionKey], &session))
	store.mu.Unlock()
	require.Equal(t, account.ID, session.LastAccountID)
	require.Equal(t, second.Admission.RouteKey, session.LastRouteKey)
}
