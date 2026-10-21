package service

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/upstreamcacheusage"
	"github.com/Wei-Shaw/sub2api/internal/upstreamcacheguard"
	"github.com/stretchr/testify/require"
)

// Use the same incremental refresh capability as the repository. The existing
// runner store alone follows the legacy refresh path and cannot reproduce a
// missing per-account policy epoch after a successful directory check.
type runnerBootstrapIncrementalStore struct{ *runnerTestStore }

var _ upstreamcacheguard.IncrementalStore = (*runnerBootstrapIncrementalStore)(nil)

func (s *runnerBootstrapIncrementalStore) IdentityVersions(ctx context.Context) ([]upstreamcacheguard.IdentityVersion, error) {
	rows, err := s.AccountRows(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	versions := make([]upstreamcacheguard.IdentityVersion, 0, len(rows))
	for _, row := range rows {
		v := upstreamcacheguard.IdentityVersion{AccountID: row.AccountID, UpstreamKeyID: row.UpstreamKeyID, Version: row.Version}
		for id, fact := range s.events {
			if fact.Request.AccountID == row.AccountID && fact.Request.UpstreamKeyID == row.UpstreamKeyID {
				v.FactVersion++
				if s.processed[id] {
					v.FactVersion++
				}
			}
		}
		versions = append(versions, v)
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i].AccountID < versions[j].AccountID })
	return versions, ctx.Err()
}

func (s *runnerBootstrapIncrementalStore) ActiveRuntimeRows(ctx context.Context, _ []upstreamcacheguard.SessionIdentity, _ time.Time) ([]upstreamcacheguard.RuntimeRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var rows []upstreamcacheguard.RuntimeRow
	for key, raw := range s.rows {
		if strings.HasPrefix(key, upstreamcacheguard.SessionKind+":") {
			// This fixture has one group; the base runner store uses the opaque
			// session digest directly as its row key rather than storing a group.
			rows = append(rows, upstreamcacheguard.RuntimeRow{GroupID: 1, Kind: upstreamcacheguard.SessionKind,
				Key: strings.TrimPrefix(key, upstreamcacheguard.SessionKind+":"), Version: 1, Payload: append([]byte(nil), raw...)})
		}
	}
	return rows, ctx.Err()
}

func (s *runnerBootstrapIncrementalStore) LoadSession(ctx context.Context, _ int64, key string) (*upstreamcacheguard.SessionState, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	raw := s.rows[upstreamcacheguard.SessionKind+":"+key]
	if raw == nil {
		return nil, 0, nil
	}
	var session upstreamcacheguard.SessionState
	if err := json.Unmarshal(raw, &session); err != nil {
		return nil, 0, err
	}
	return &session, 1, nil
}

func (s *runnerBootstrapIncrementalStore) AccountRowsFor(ctx context.Context, ids []int64) ([]upstreamcacheguard.AccountRuntimeRow, error) {
	rows, err := s.AccountRows(ctx)
	selected := make(map[int64]bool, len(ids))
	for _, id := range ids {
		selected[id] = true
	}
	filtered := make([]upstreamcacheguard.AccountRuntimeRow, 0, len(rows))
	for _, row := range rows {
		if selected[row.AccountID] {
			filtered = append(filtered, row)
		}
	}
	return filtered, err
}

func (s *runnerBootstrapIncrementalStore) AccountWindowsFor(ctx context.Context, ids []int64, since, until time.Time) (map[string]upstreamcacheguard.AccountWindow, error) {
	windows, err := s.AccountWindows(ctx, since, until)
	selected := make(map[int64]bool, len(ids))
	for _, id := range ids {
		selected[id] = true
	}
	for id, window := range windows {
		if !selected[window.AccountID] {
			delete(windows, id)
		}
	}
	return windows, err
}

func TestUpstreamCacheGuardIncrementalBootstrapThroughSendAdmission(t *testing.T) {
	testUpstreamCacheGuardIncrementalSendAdmission(t, false)
}

func TestUpstreamCacheGuardUnfinishedAdmissionDoesNotBlockSendAdmission(t *testing.T) {
	testUpstreamCacheGuardIncrementalSendAdmission(t, true)
}

func testUpstreamCacheGuardIncrementalSendAdmission(t *testing.T, withUnfinished bool) {
	t.Helper()
	store := &runnerBootstrapIncrementalStore{newRunnerTestStore()}
	runner := testRunner(t, store.runnerTestStore)
	runner.engine = upstreamcacheguard.NewEngine(store)
	runner.store = store
	runner.secret = []byte("bootstrap-test-only")
	settings := upstreamcacheguard.DefaultSettings()
	settings.Mode = upstreamcacheguard.Protect
	require.NoError(t, runner.engine.ApplySettings(settings))
	group, key, config := int64(1), int64(3), int64(4)
	account := &Account{ID: 2, Platform: PlatformOpenAI, UpstreamConfigID: &config, UpstreamKeyID: &key}
	var unfinished upstreamcacheguard.Admission
	if withUnfinished {
		ctx := WithUpstreamCacheGuardScheduling(context.Background(), 7)
		req, ok := runner.logicalRequest(ctx, &group, "same-conversation", "gpt-test", "openai")
		require.True(t, ok)
		req.AccountID, req.UpstreamKeyID = account.ID, key
		// Predate both the old five-minute check and the engine's birth. This
		// represents an admission left behind by a previous runner instance.
		req.StartedAt = time.Now().UTC().Add(-7 * 24 * time.Hour)
		var err error
		unfinished, err = runner.engine.Admit(context.Background(), req)
		require.NoError(t, err)
		require.False(t, unfinished.Completed)
	}
	// This initializes the incremental policy epoch map while the new identity
	// is either unobserved or has only an unfinished historical admission.
	require.NoError(t, runner.engine.Refresh(context.Background()))

	begin := func() (context.Context, *UpstreamCacheGuardAdmission) {
		t.Helper()
		ctx := WithUpstreamCacheGuardScheduling(context.Background(), 7)
		ctx = WithUpstreamCacheGuardAttempt(ctx, 7, &group, "same-conversation", true)
		// Exercise the HTTP physical-send boundary, rather than calling the
		// engine directly and bypassing its fresh-versus-stale admission choice.
		PrepareOpenAIUpstreamCacheGuardSend(ctx, account, "gpt-test", "openai")
		require.NoError(t, runSchedulingProtectionBeforeSend(ctx, account, "gpt-test"))
		holder := ctx.Value(cacheUsageAttemptContextKey{}).(*cacheUsageAttempt)
		holder.mu.Lock()
		admission := holder.admission
		holder.mu.Unlock()
		require.NotNil(t, admission, "a new identity must not be stranded in stale-session admission")
		return ctx, admission
	}
	complete := func(ctx context.Context) {
		t.Helper()
		usage := &UpstreamCacheUsageEvidence{Status: upstreamcacheusage.Known, Protocol: "openai", TotalInputTokens: 1000, CacheReadTokens: 900}
		FinishUpstreamCacheGuardAttempt(ctx, usage, true, time.Now().UTC())
		// The holder makes duplicate terminal notifications idempotent.
		FinishUpstreamCacheGuardAttempt(ctx, usage, true, time.Now().UTC())
		runner.processPending()
	}

	firstContext, first := begin()
	require.True(t, first.Admission.Sample)
	require.False(t, first.Admission.WarmAtStart, "bootstrap must not invent a warm route")
	complete(firstContext)
	store.mu.Lock()
	require.Len(t, store.events, 1)
	for id, fact := range store.events {
		require.True(t, store.processed[id])
		require.True(t, fact.Success)
		require.False(t, fact.ValidSample(), "the first successful cold fact must be retained without entering the rate")
	}
	store.mu.Unlock()
	require.NoError(t, runner.engine.Refresh(context.Background()))

	secondContext, second := begin()
	require.Equal(t, first.Admission.SessionKey, second.Admission.SessionKey)
	require.Equal(t, first.Admission.RouteKey, second.Admission.RouteKey)
	require.NotEqual(t, first.Admission.RequestID, second.Admission.RequestID)
	require.True(t, second.Admission.WarmAtStart)
	complete(secondContext)
	require.NoError(t, runner.engine.Refresh(context.Background()))

	store.mu.Lock()
	valid := 0
	for id, fact := range store.events {
		require.True(t, store.processed[id])
		if fact.ValidSample() {
			valid++
		}
	}
	require.Len(t, store.events, 2)
	require.Equal(t, 1, valid)
	store.mu.Unlock()
	accountSnapshot, available := runner.engine.CachedAccountForGroupAt(group, account.ID, key, time.Now().UTC())
	require.True(t, available)
	require.True(t, accountSnapshot.Fresh)
	require.Equal(t, 1, accountSnapshot.ValidRequests)
	require.Equal(t, float64(90), accountSnapshot.CachePercent)
	require.Zero(t, runner.skipCounts[cacheGuardSkipStaleSessionUnbound].Load())
	if withUnfinished {
		store.mu.Lock()
		raw := append([]byte(nil), store.rows[upstreamcacheguard.SessionKind+":"+unfinished.SessionKey]...)
		store.mu.Unlock()
		var session upstreamcacheguard.SessionState
		require.NoError(t, json.Unmarshal(raw, &session))
		found := false
		for _, admission := range session.Admissions {
			if admission.RequestID == unfinished.RequestID {
				found = true
				require.Equal(t, unfinished, admission, "the historical attempt must retain its frozen classification and unfinished state")
			}
		}
		require.True(t, found, "resuming sampling must not delete the unfinished admission")
		require.False(t, first.Admission.NaturalNew, "the unfinished attempt must retain the session's original identity")
	}
}

func TestUpstreamCacheGuardPendingFactBlocksOnlyItsAccount(t *testing.T) {
	store := &runnerBootstrapIncrementalStore{newRunnerTestStore()}
	runner := testRunner(t, store.runnerTestStore)
	runner.engine = upstreamcacheguard.NewEngine(store)
	runner.store = store
	runner.secret = []byte("pending-fact-test-only")
	settings := upstreamcacheguard.DefaultSettings()
	settings.Mode = upstreamcacheguard.Protect
	require.NoError(t, runner.engine.ApplySettings(settings))
	group, key, config := int64(1), int64(3), int64(4)
	target := &Account{ID: 2, Platform: PlatformOpenAI, UpstreamConfigID: &config, UpstreamKeyID: &key}
	unaffected := &Account{ID: 9, Platform: PlatformOpenAI, UpstreamConfigID: &config, UpstreamKeyID: &key}
	seed := func(account *Account, session string, startedAt time.Time) (upstreamcacheguard.Request, upstreamcacheguard.Admission) {
		t.Helper()
		ctx := WithUpstreamCacheGuardScheduling(context.Background(), 7)
		req, ok := runner.logicalRequest(ctx, &group, session, "gpt-test", "openai")
		require.True(t, ok)
		req.AccountID, req.UpstreamKeyID, req.StartedAt = account.ID, key, startedAt
		admission, err := runner.engine.Admit(context.Background(), req)
		require.NoError(t, err)
		return req, admission
	}
	now := time.Now().UTC()
	req, admission := seed(target, "delayed-completion", now.Add(-10*time.Second))
	seed(unaffected, "still-in-flight", now)
	// This is an actual completed result awaiting consumption, rather than an
	// unfinished admission without an observed outcome. It remains a sampling
	// gap after the five-second outbox grace, even when the consumer is another
	// process and this runner has no local sampling-failure marker.
	fact := upstreamcacheguard.Evidence{ID: "delayed-completion-fact", Request: req, Admission: admission,
		CompletedAt: now.Add(-6 * time.Second), Success: true,
		RawUsage: upstreamcacheguard.RawUsage{Status: upstreamcacheusage.Known, TotalInputTokens: 1000, CacheReadTokens: 900}}
	require.NoError(t, store.Enqueue(context.Background(), fact))
	require.NoError(t, runner.engine.Refresh(context.Background()))
	_, available := runner.engine.CachedAccountForGroupAt(group, target.ID, key, time.Now().UTC())
	require.False(t, available, "a completed but unconsumed fact must not be treated as a harmless in-flight request")
	other, available := runner.engine.CachedAccountForGroupAt(group, unaffected.ID, key, time.Now().UTC())
	require.True(t, available)
	require.True(t, other.Fresh, "one account's outbox lag must not block another account")

	ctx := WithUpstreamCacheGuardScheduling(context.Background(), 7)
	ctx = WithUpstreamCacheGuardAttempt(ctx, 7, &group, "another-session", true)
	PrepareOpenAIUpstreamCacheGuardSend(ctx, unaffected, "gpt-test", "openai")
	require.NoError(t, runSchedulingProtectionBeforeSend(ctx, unaffected, "gpt-test"))
	holder := ctx.Value(cacheUsageAttemptContextKey{}).(*cacheUsageAttempt)
	holder.mu.Lock()
	otherAdmission := holder.admission
	holder.mu.Unlock()
	require.NotNil(t, otherAdmission)

	runner.processPending()
	require.NoError(t, runner.engine.Refresh(context.Background()))
	recovered, available := runner.engine.CachedAccountForGroupAt(group, target.ID, key, time.Now().UTC())
	require.True(t, available)
	require.True(t, recovered.Fresh)
	require.Zero(t, recovered.ValidRequests, "consuming the first cold completion must not manufacture a warm sample")
	store.mu.Lock()
	processed := store.processed[fact.ID]
	store.mu.Unlock()
	require.True(t, processed)
}
