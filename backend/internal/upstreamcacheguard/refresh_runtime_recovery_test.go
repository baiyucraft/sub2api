package upstreamcacheguard

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A failed runtime read prevents the entire incremental refresh from reaching
// its publication boundary, even when the completion consumer remains healthy.
type runtimeRefreshFailureStore struct {
	*incrementalMemoryStore
	runtimeErr error
}

func (s *runtimeRefreshFailureStore) ActiveRuntimeRows(ctx context.Context, active []SessionIdentity, since time.Time) ([]RuntimeRow, error) {
	if s.runtimeErr != nil {
		return nil, s.runtimeErr
	}
	return s.incrementalMemoryStore.ActiveRuntimeRows(ctx, active, since)
}

func TestIncrementalRuntimeRefreshFailurePreservesProcessedGapUntilVerifiedRecovery(t *testing.T) {
	e, memory := testEngine(t)
	store := &runtimeRefreshFailureStore{incrementalMemoryStore: &incrementalMemoryStore{memoryStore: memory}}
	e.store = store
	at := testNow
	e.now = func() time.Time { return at }
	seedSnapshotAccounts(t, e, memory)

	// Establish a real warm route, then leave its next valid completion in the
	// outbox long enough for a successful refresh to publish an account gap.
	first := request("runtime-gap-warmup", "runtime-gap-session", testNow.Add(-time.Minute))
	complete(t, e, first, admit(t, e, first), 90, true, "known")
	require.NoError(t, e.Refresh(context.Background()))
	before, available := e.CachedSharedAccount(2, 3)
	require.True(t, available)

	r := request("runtime-gap-pending", "runtime-gap-session", testNow.Add(-10*time.Second))
	a := admit(t, e, r)
	fact := Evidence{ID: "runtime-gap-completion", Request: r, Admission: a,
		RawUsage: RawUsage{Status: "known", TotalInputTokens: 100, CacheReadTokens: 90},
		Success:  true, CompletedAt: testNow.Add(-9 * time.Second)}
	require.True(t, fact.ValidSample(), "the pending record must represent a real valid warm completion")
	require.NoError(t, memory.Enqueue(context.Background(), fact))
	require.NoError(t, e.Refresh(context.Background()))
	require.True(t, e.accountGaps[accountIdentity(2, 3)])

	// Completion succeeds independently; it cannot itself clear a previously
	// published read-side gap or renew the last verified account snapshot.
	require.NoError(t, e.Complete(context.Background(), fact))
	pending, err := memory.HasPending(context.Background())
	require.NoError(t, err)
	require.False(t, pending)
	require.True(t, e.accountGaps[accountIdentity(2, 3)])
	store.runtimeErr = context.DeadlineExceeded
	for _, tc := range []struct {
		age              time.Duration
		otherFresh       bool
		accountAvailable bool
	}{
		{5 * time.Second, true, true},
		{5*time.Second + time.Nanosecond, false, true},
		{30 * time.Second, false, true},
		{30*time.Second + time.Nanosecond, false, false},
	} {
		at = testNow.Add(tc.age)
		err = e.Refresh(context.Background())
		require.ErrorIs(t, err, context.DeadlineExceeded)
		var stage *RefreshStageError
		require.True(t, errors.As(err, &stage))
		require.Equal(t, "active_runtime_rows", stage.Stage)
		require.Equal(t, testNow, e.loadedAt)
		require.True(t, e.accountGaps[accountIdentity(2, 3)])
		account, ok := e.CachedSharedAccount(2, 3)
		require.Equal(t, tc.accountAvailable, ok)
		require.Equal(t, before.LoadedAt, account.LoadedAt)
		require.False(t, account.Fresh, "processed outbox facts alone cannot make a failed refresh fresh")
		other, ok := e.CachedSharedAccount(9, 3)
		require.Equal(t, tc.accountAvailable, ok)
		require.Equal(t, tc.otherFresh, other.Fresh)
		require.Equal(t, before.LoadedAt, other.LoadedAt)
	}

	store.runtimeErr = nil
	at = testNow.Add(31 * time.Second)
	require.NoError(t, e.Refresh(context.Background()))
	recoveredAt := at
	recovered, available := e.CachedSharedAccount(2, 3)
	require.True(t, available)
	require.True(t, recovered.Fresh)
	require.False(t, e.accountGaps[accountIdentity(2, 3)])
	require.Equal(t, recoveredAt, recovered.LoadedAt)
	require.Equal(t, 1, recovered.ValidRequests, "the processed completion must enter the recovered exact window once")
	require.Equal(t, before.Phase, recovered.Phase, "refresh recovery must preserve the underlying protection decision")
	for _, tc := range []struct {
		age              time.Duration
		fresh, available bool
	}{
		{5 * time.Second, true, true},
		{5*time.Second + time.Nanosecond, false, true},
		{30 * time.Second, false, true},
		{30*time.Second + time.Nanosecond, false, false},
	} {
		at = recoveredAt.Add(tc.age)
		account, ok := e.CachedSharedAccount(2, 3)
		require.Equal(t, tc.available, ok)
		require.Equal(t, tc.fresh, account.Fresh)
		require.Equal(t, recoveredAt, account.LoadedAt)
	}
}

func TestIncrementalRefreshCompletionDuringReadRetainsPriorSnapshotUntilStableRound(t *testing.T) {
	e, memory := testEngine(t)
	store := &incrementalMemoryStore{memoryStore: memory}
	e.store = store
	at := testNow
	e.now = func() time.Time { return at }
	seedSnapshotAccounts(t, e, memory)
	first := request("version-race-warmup", "version-race-session", testNow.Add(-time.Minute))
	complete(t, e, first, admit(t, e, first), 90, true, "known")
	require.NoError(t, e.Refresh(context.Background()))
	before, available := e.CachedSharedAccount(2, 3)
	require.True(t, available)

	r := request("version-race-completion", "version-race-session", testNow.Add(-10*time.Second))
	a := admit(t, e, r)
	calls := 0
	store.directoryHook = func() {
		calls++
		if calls == 2 {
			// A normal successful completion may race a refresh after its window
			// read. Final version confirmation must reject the now-stale window.
			complete(t, e, r, a, 90, true, "known")
		}
	}
	at = testNow.Add(time.Second)
	require.NoError(t, e.Refresh(context.Background()))
	account, available := e.CachedSharedAccount(2, 3)
	require.True(t, available)
	require.False(t, account.Fresh)
	require.True(t, e.accountGaps[accountIdentity(2, 3)])
	require.Equal(t, before.LoadedAt, account.LoadedAt)
	require.Equal(t, before.ValidRequests, account.ValidRequests)
	other, available := e.CachedSharedAccount(9, 3)
	require.True(t, available)
	require.True(t, other.Fresh)
	require.Equal(t, at, other.LoadedAt, "a version race must remain account-local")
	pending, err := memory.HasPending(context.Background())
	require.NoError(t, err)
	require.False(t, pending, "even a processed completion still requires stable refresh confirmation")

	store.directoryHook = nil
	at = at.Add(time.Second)
	require.NoError(t, e.Refresh(context.Background()))
	account, available = e.CachedSharedAccount(2, 3)
	require.True(t, available)
	require.True(t, account.Fresh)
	require.False(t, e.accountGaps[accountIdentity(2, 3)])
	require.Equal(t, at, account.LoadedAt)
	require.Equal(t, before.ValidRequests+1, account.ValidRequests)
	require.Equal(t, before.Phase, account.Phase)
}
