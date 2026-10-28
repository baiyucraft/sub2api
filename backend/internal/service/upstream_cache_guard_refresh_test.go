package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/upstreamcacheguard"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

type cacheGuardRefreshProbeStore struct {
	*runnerTestStore
	rows    func(context.Context) ([]upstreamcacheguard.RuntimeRow, error)
	pending func(context.Context) (bool, error)
	enqueue func(context.Context, upstreamcacheguard.Evidence) error
}

func (s *cacheGuardRefreshProbeStore) Rows(ctx context.Context, ids []int64) ([]upstreamcacheguard.RuntimeRow, error) {
	if s.rows != nil {
		return s.rows(ctx)
	}
	return s.runnerTestStore.Rows(ctx, ids)
}

func (s *cacheGuardRefreshProbeStore) HasPendingBefore(ctx context.Context, cutoff time.Time) (bool, error) {
	if s.pending != nil {
		return s.pending(ctx)
	}
	return s.runnerTestStore.HasPendingBefore(ctx, cutoff)
}

func (s *cacheGuardRefreshProbeStore) Enqueue(ctx context.Context, evidence upstreamcacheguard.Evidence) error {
	if s.enqueue != nil {
		return s.enqueue(ctx, evidence)
	}
	return s.runnerTestStore.Enqueue(ctx, evidence)
}

func testCacheGuardRefreshRunner(t *testing.T, store *cacheGuardRefreshProbeStore) *UpstreamCacheGuardRunner {
	t.Helper()
	r := testRunner(t, store.runnerTestStore)
	r.store = store
	r.engine = upstreamcacheguard.NewEngine(store)
	settings := upstreamcacheguard.DefaultSettings()
	settings.Mode = upstreamcacheguard.Protect
	require.NoError(t, r.engine.ApplySettings(settings))
	encoded, err := json.Marshal(settings)
	require.NoError(t, err)
	r.settings = NewSettingService(&upstreamManagementSettingRepoStub{values: map[string]string{SettingKeyUpstreamCacheGuard: string(encoded)}}, nil)
	r.secret = []byte("local-refresh-test-secret")
	t.Cleanup(r.cancelRefresh)
	return r
}

func TestCacheGuardRefreshBudgetsAreIndependentAndBackgroundOnly(t *testing.T) {
	store := &cacheGuardRefreshProbeStore{runnerTestStore: newRunnerTestStore()}
	var snapshotContext context.Context
	store.rows = func(ctx context.Context) ([]upstreamcacheguard.RuntimeRow, error) {
		snapshotContext = ctx
		deadline, bounded := ctx.Deadline()
		require.True(t, bounded)
		require.Greater(t, time.Until(deadline), 2*time.Second, "background refresh must not retain the old 750ms budget")
		require.LessOrEqual(t, time.Until(deadline), cacheGuardRefreshBudget)
		return nil, &upstreamcacheguard.RefreshStageError{Stage: "account_windows", Err: context.DeadlineExceeded}
	}
	called := false
	store.pending = func(ctx context.Context) (bool, error) {
		called = true
		require.NotSame(t, snapshotContext, ctx)
		deadline, bounded := ctx.Deadline()
		require.True(t, bounded)
		require.Greater(t, time.Until(deadline), 250*time.Millisecond)
		require.LessOrEqual(t, time.Until(deadline), cacheGuardPendingHealthBudget)
		require.NoError(t, ctx.Err())
		return true, nil
	}
	r := testCacheGuardRefreshRunner(t, store)
	r.refresh()
	require.True(t, called, "snapshot failure must not erase independent outbox diagnostics")
	require.True(t, r.pendingDelayed.Load())
	require.False(t, r.pendingUnknown.Load())
	require.False(t, r.available.Load())
}

func TestCacheGuardRefreshPendingFailureDoesNotWithdrawSnapshot(t *testing.T) {
	store := &cacheGuardRefreshProbeStore{runnerTestStore: newRunnerTestStore()}
	store.pending = func(context.Context) (bool, error) { return false, context.DeadlineExceeded }
	r := testCacheGuardRefreshRunner(t, store)
	r.refresh()
	require.True(t, r.available.Load())
	require.True(t, r.pendingUnknown.Load())
	require.True(t, r.engine.CachedSnapshot(1).Available)
}

func TestCacheGuardRefreshCloseCancelsQueryWithoutCancelingEvidenceDelivery(t *testing.T) {
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	store := &cacheGuardRefreshProbeStore{runnerTestStore: newRunnerTestStore()}
	queryEntered, queryCanceled := make(chan struct{}), make(chan struct{})
	deliveryEntered, deliveryRelease := make(chan context.Context, 1), make(chan struct{})
	store.rows = func(ctx context.Context) ([]upstreamcacheguard.RuntimeRow, error) {
		close(queryEntered)
		<-ctx.Done()
		close(queryCanceled)
		return nil, ctx.Err()
	}
	store.enqueue = func(ctx context.Context, evidence upstreamcacheguard.Evidence) error {
		deliveryEntered <- ctx
		<-deliveryRelease
		return store.runnerTestStore.Enqueue(ctx, evidence)
	}
	r := testCacheGuardRefreshRunner(t, store)
	r.retrying.Add(1)
	r.queue <- upstreamcacheguard.Evidence{ID: "shutdown-evidence"}
	go r.run()
	select {
	case <-queryEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("refresh did not enter the query barrier")
	}
	var deliveryContext context.Context
	select {
	case deliveryContext = <-deliveryEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("evidence delivery did not enter its barrier")
	}
	closed := make(chan struct{})
	go func() { r.Close(); close(closed) }()
	select {
	case <-queryCanceled:
	case <-time.After(time.Second):
		t.Fatal("Close did not promptly cancel the background query")
	}
	require.NoError(t, deliveryContext.Err(), "shutdown must preserve the separate evidence delivery budget")
	close(deliveryRelease)
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not finish after evidence delivery was released")
	}
	store.mu.Lock()
	_, delivered := store.events["shutdown-evidence"]
	store.mu.Unlock()
	require.True(t, delivered)
	require.False(t, r.available.Load())
	require.NotContains(t, logs.String(), "upstream_cache_guard_refresh_failed", "expected Close cancellation must stay silent")
}

func TestCacheGuardRefreshWaitsOneSecondAfterSlowRound(t *testing.T) {
	store := &cacheGuardRefreshProbeStore{runnerTestStore: newRunnerTestStore()}
	entered, release, second := make(chan struct{}), make(chan struct{}), make(chan time.Time, 1)
	var calls, inFlight, peak atomic.Int32
	store.rows = func(ctx context.Context) ([]upstreamcacheguard.RuntimeRow, error) {
		active := inFlight.Add(1)
		defer inFlight.Add(-1)
		for maximum := peak.Load(); active > maximum; maximum = peak.Load() {
			if peak.CompareAndSwap(maximum, active) {
				break
			}
		}
		if calls.Add(1) == 1 {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		} else {
			second <- time.Now()
		}
		return nil, nil
	}
	r := testCacheGuardRefreshRunner(t, store)
	go r.run()
	t.Cleanup(r.Close)
	<-entered
	// This deliberate slow-query barrier exceeds the scheduling interval;
	// a ticker would leave a pending tick and start the second round too soon.
	<-time.After(1100 * time.Millisecond)
	released := time.Now()
	close(release)
	select {
	case at := <-second:
		require.GreaterOrEqual(t, at.Sub(released), time.Second)
	case <-time.After(3 * time.Second):
		t.Fatal("second refresh did not run")
	}
	require.Equal(t, int32(1), peak.Load(), "the worker must never overlap refresh queries")
}

func TestCacheGuardRefreshRealBudgetExpiryKeepsSnapshotAndNextRoundRecovers(t *testing.T) {
	store := &cacheGuardRefreshProbeStore{runnerTestStore: newRunnerTestStore()}
	r := testCacheGuardRefreshRunner(t, store)
	require.NoError(t, store.TransactAccount(context.Background(), 0, 2, 3, func(tx upstreamcacheguard.Tx) error {
		_, err := tx.(upstreamcacheguard.AccountTx).WriteAccount(2, 3, upstreamcacheguard.AccountState{AccountID: 2, UpstreamKeyID: 3, Phase: upstreamcacheguard.Healthy})
		return err
	}))
	r.refresh()
	require.True(t, r.available.Load())
	before, available := r.engine.CachedSharedAccount(2, 3)
	require.True(t, available)
	require.False(t, before.LoadedAt.IsZero())
	entered := make(chan context.Context, 1)
	observed := make(chan error, 1)
	store.rows = func(ctx context.Context) ([]upstreamcacheguard.RuntimeRow, error) {
		entered <- ctx
		<-ctx.Done()
		observed <- ctx.Err()
		return nil, ctx.Err()
	}
	finished := make(chan time.Time, 1)
	started := time.Now()
	go func() { r.refresh(); finished <- time.Now() }()
	var queryContext context.Context
	select {
	case queryContext = <-entered:
	case <-time.After(time.Second):
		t.Fatal("refresh never entered the blocking-query barrier")
	}
	deadline, bounded := queryContext.Deadline()
	require.True(t, bounded)
	var completedAt time.Time
	select {
	case completedAt = <-finished:
		require.False(t, completedAt.Before(deadline), "refresh must not complete before its actual context budget expired")
	case <-queryContext.Done():
	case <-time.After(4 * time.Second):
		t.Fatal("the real refresh budget failed to cancel its query")
	}
	require.ErrorIs(t, queryContext.Err(), context.DeadlineExceeded)
	require.ErrorIs(t, <-observed, context.DeadlineExceeded)
	if completedAt.IsZero() {
		select {
		case completedAt = <-finished:
		case <-time.After(time.Second):
			t.Fatal("refresh did not return after its query observed the real deadline")
		}
	}
	require.False(t, completedAt.Before(deadline))
	require.GreaterOrEqual(t, completedAt.Sub(started), 2900*time.Millisecond, "this test must exercise the real 3s budget, not synthesize a timeout error")
	require.False(t, r.available.Load())
	afterFailure, available := r.engine.CachedSharedAccount(2, 3)
	require.True(t, available, "the last usable account snapshot must survive the failed round")
	require.Equal(t, before.LoadedAt, afterFailure.LoadedAt)
	r.refreshDiagnostics.mu.Lock()
	failedState := r.refreshDiagnostics.states["refresh"]
	_, classified := failedState.lastLogged["active_runtime_rows:deadline_exceeded"]
	r.refreshDiagnostics.mu.Unlock()
	require.Equal(t, uint64(1), failedState.failures)
	require.True(t, classified)
	store.rows = nil // The timed-out goroutine has fully exited before recovery.
	r.refresh()
	require.True(t, r.available.Load())
	afterRecovery, available := r.engine.CachedSharedAccount(2, 3)
	require.True(t, available)
	require.True(t, afterRecovery.LoadedAt.After(before.LoadedAt))
	require.Equal(t, before.ValidRequests, afterRecovery.ValidRequests)
}

func TestCacheGuardRefreshDiagnosticsThrottleRecoverAndRedactErrors(t *testing.T) {
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	var diagnostics cacheGuardRefreshDiagnostics
	now := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	err := &upstreamcacheguard.RefreshStageError{Stage: "account_windows", Err: errors.New("SECRET SQL evidence_json request_body")}
	for _, seconds := range []int{0, 1, 59, 60} {
		diagnostics.recordAt(now.Add(time.Duration(seconds)*time.Second), "refresh", err, time.Second, cacheGuardRefreshBudget, map[string]float64{"account_windows": 999})
	}
	diagnostics.recordAt(now.Add(61*time.Second), "refresh", nil, time.Millisecond, cacheGuardRefreshBudget, nil)
	diagnostics.recordAt(now.Add(62*time.Second), "refresh", nil, time.Millisecond, cacheGuardRefreshBudget, nil)
	diagnostics.recordAt(now.Add(63*time.Second), "refresh", context.DeadlineExceeded, time.Second, cacheGuardRefreshBudget, nil)
	require.NotContains(t, logs.String(), "SECRET")
	require.NotContains(t, logs.String(), "request_body")
	decoder := json.NewDecoder(&logs)
	var entries []map[string]any
	for decoder.More() {
		var entry map[string]any
		require.NoError(t, decoder.Decode(&entry))
		entries = append(entries, entry)
	}
	require.Len(t, entries, 4)
	require.Equal(t, "account_windows", entries[0]["stage"])
	require.Equal(t, float64(4), entries[1]["consecutive_failures"])
	require.Equal(t, "upstream_cache_guard_refresh_recovered", entries[2]["msg"])
	require.Equal(t, "deadline_exceeded", entries[3]["reason"])
	require.Equal(t, float64(1000), entries[3]["last_success_age_ms"])
}

func TestCacheGuardRefreshDiagnosticsThrottleEachFailureClass(t *testing.T) {
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	var diagnostics cacheGuardRefreshDiagnostics
	now := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	for index, stage := range []string{"account_windows", "identity_versions", "account_windows"} {
		err := &upstreamcacheguard.RefreshStageError{Stage: stage, Err: context.DeadlineExceeded}
		diagnostics.recordAt(now.Add(time.Duration(index)*time.Second), "refresh", err, time.Second, cacheGuardRefreshBudget, nil)
	}
	require.Equal(t, 2, bytes.Count(logs.Bytes(), []byte("upstream_cache_guard_refresh_failed")), "alternating failure classes must not bypass the per-class throttle")
}

func TestCacheGuardRefreshStageClassifiesPostgresContextCancellation(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		name := "deadline_exceeded"
		if canceled {
			name = "canceled"
		}
		t.Run(name, func(t *testing.T) {
			var logs bytes.Buffer
			old := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(old) })
			var ctx context.Context
			var cancel context.CancelFunc
			if canceled {
				ctx, cancel = context.WithCancel(context.Background())
			} else {
				ctx, cancel = context.WithTimeout(context.Background(), 25*time.Millisecond)
			}
			defer cancel()
			driverErr := &pq.Error{Code: "57014", Message: "SECRET SQL request_body"}
			err := cacheGuardRefreshStage(ctx, "settings", func() error {
				if canceled {
					cancel()
				}
				<-ctx.Done()
				return driverErr
			})
			require.ErrorIs(t, err, ctx.Err())
			var pgErr *pq.Error
			require.ErrorAs(t, err, &pgErr)
			require.Same(t, driverErr, pgErr)
			var diagnostics cacheGuardRefreshDiagnostics
			diagnostics.recordAt(time.Now(), "refresh", err, time.Second, cacheGuardRefreshBudget, nil)
			var entry map[string]any
			require.NoError(t, json.Unmarshal(logs.Bytes(), &entry))
			require.Equal(t, name, entry["reason"])
			require.Equal(t, "settings", entry["stage"])
			require.NotContains(t, logs.String(), "SECRET")
		})
	}
}

func TestCacheGuardRefreshPendingDeadlineDoesNotReclassifyCompletedRefresh(t *testing.T) {
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	store := &cacheGuardRefreshProbeStore{runnerTestStore: newRunnerTestStore()}
	var refreshContext context.Context
	store.rows = func(ctx context.Context) ([]upstreamcacheguard.RuntimeRow, error) {
		refreshContext = ctx
		// Finish the real refresh with headroom, then let the independent health
		// budget cross its expired deadline. Only the health query failed.
		select {
		case <-time.After(2600 * time.Millisecond):
			return nil, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	store.pending = func(ctx context.Context) (bool, error) {
		<-ctx.Done()
		return false, &pq.Error{Code: "57014", Message: "SECRET canceled pending statement"}
	}
	r := testCacheGuardRefreshRunner(t, store)
	r.refresh()
	require.ErrorIs(t, refreshContext.Err(), context.DeadlineExceeded)
	require.True(t, r.available.Load(), "a later health failure must not change a completed snapshot result")
	require.True(t, r.pendingUnknown.Load())
	require.NotContains(t, logs.String(), "SECRET")
	decoder := json.NewDecoder(&logs)
	var entries []map[string]any
	for decoder.More() {
		var entry map[string]any
		require.NoError(t, decoder.Decode(&entry))
		entries = append(entries, entry)
	}
	require.Len(t, entries, 1)
	require.Equal(t, "pending_health", entries[0]["source"])
	require.Equal(t, "deadline_exceeded", entries[0]["reason"])
	r.refreshDiagnostics.mu.Lock()
	state := r.refreshDiagnostics.states["refresh"]
	r.refreshDiagnostics.mu.Unlock()
	require.Zero(t, state.failures)
	require.False(t, state.lastSuccess.IsZero())
}
