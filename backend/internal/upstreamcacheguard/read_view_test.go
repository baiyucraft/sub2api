package upstreamcacheguard

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAdmissionReadViewClockFollowsPublicationLock(t *testing.T) {
	e, _ := testEngine(t)
	var ticks atomic.Int64
	e.now = func() time.Time { return testNow.Add(time.Duration(ticks.Load())) }
	r := request("view", "session", testNow)
	e.mu.Lock()
	started := make(chan struct{})
	result := make(chan AdmissionReadView, 1)
	go func() {
		close(started)
		result <- e.CachedAdmissionReadView(r)
	}()
	<-started
	published := testNow.Add(time.Second)
	ticks.Store(int64(time.Second))
	e.loadedAt = published
	e.accounts[accountIdentity(r.AccountID, r.UpstreamKeyID)] = AccountSnapshot{
		AccountState: AccountState{AccountID: r.AccountID, UpstreamKeyID: r.UpstreamKeyID, Phase: Healthy}, LoadedAt: published,
	}
	e.mu.Unlock()
	view := <-result
	require.True(t, view.Consistent)
	require.Equal(t, published, view.EvaluatedAt)
	require.True(t, view.Metadata.Fresh)
	require.True(t, view.Account.Fresh)
	rollback := e.CachedAdmissionReadViewAt(r, testNow)
	require.True(t, rollback.Consistent)
	require.False(t, rollback.Metadata.Available)
	require.False(t, rollback.Account.Available, "a genuinely earlier evaluation remains unavailable")
}

func TestAdmissionReadViewRetainsExactAgeBoundaries(t *testing.T) {
	e, _ := testEngine(t)
	r := request("view", "session", testNow)
	e.loadedAt = testNow
	e.accounts[accountIdentity(r.AccountID, r.UpstreamKeyID)] = AccountSnapshot{
		AccountState: AccountState{AccountID: r.AccountID, UpstreamKeyID: r.UpstreamKeyID, Phase: Healthy}, LoadedAt: testNow,
	}
	for _, tc := range []struct {
		age              time.Duration
		fresh, available bool
	}{
		{-time.Nanosecond, false, false},
		{5 * time.Second, true, true},
		{5*time.Second + time.Nanosecond, false, true},
		{30 * time.Second, false, true},
		{30*time.Second + time.Nanosecond, false, false},
	} {
		view := e.CachedAdmissionReadViewAt(r, testNow.Add(tc.age))
		require.True(t, view.Consistent)
		require.Equal(t, tc.fresh, view.Metadata.Fresh)
		require.Equal(t, tc.fresh, view.Account.Fresh)
		require.Equal(t, tc.available, view.Metadata.Available)
		require.Equal(t, tc.available, view.Account.Available)
	}
}

type readViewSessionStore struct {
	*memoryStore
	load func() (*SessionState, int64, error)
}

func (s *readViewSessionStore) LoadSession(context.Context, int64, string) (*SessionState, int64, error) {
	return s.load()
}

func TestSessionReadViewReevaluatesAfterColdLoadPublication(t *testing.T) {
	e, memory := testEngine(t)
	r := request("cold-view", "session", testNow)
	at := testNow
	e.now = func() time.Time { return at }
	e.loadedAt = at
	e.accounts[accountIdentity(r.AccountID, r.UpstreamKeyID)] = AccountSnapshot{
		AccountState: AccountState{AccountID: r.AccountID, UpstreamKeyID: r.UpstreamKeyID, Phase: Healthy}, LoadedAt: at,
	}
	routeKey := WarmRouteKey(r)
	store := &readViewSessionStore{memoryStore: memory}
	store.load = func() (*SessionState, int64, error) {
		e.mu.Lock()
		at = testNow.Add(2 * time.Second)
		e.loadedAt = at
		a := e.accounts[accountIdentity(r.AccountID, r.UpstreamKeyID)]
		a.LoadedAt = at
		e.accounts[accountIdentity(r.AccountID, r.UpstreamKeyID)] = a
		e.mu.Unlock()
		return &SessionState{Key: SessionKey(r), LastAccountID: r.AccountID, LastModel: r.Model, LastProtocol: r.Protocol,
			LastRouteKey: routeKey, Routes: map[string]SessionRoute{routeKey: {AccountID: r.AccountID, UpstreamKeyID: r.UpstreamKeyID, LastSuccessfulAt: testNow.Add(-time.Second)}}}, 1, nil
	}
	e.store = store
	calls := 0
	view, err := e.SessionReadViewWithDatabaseContext(context.Background(), r, func(ctx context.Context) (context.Context, context.CancelFunc) {
		calls++
		return context.WithCancel(ctx)
	})
	require.NoError(t, err)
	require.True(t, view.Consistent)
	require.Equal(t, testNow.Add(2*time.Second), view.EvaluatedAt)
	require.True(t, view.Metadata.Fresh)
	require.True(t, view.Account.Fresh)
	require.True(t, view.Session.Known)
	require.True(t, view.Session.Fresh)
	require.Equal(t, r.AccountID, view.Session.PreferredAccountID)
	require.Equal(t, 1, calls)
	_, err = e.SessionReadViewWithDatabaseContext(context.Background(), r, func(context.Context) (context.Context, context.CancelFunc) {
		t.Fatal("a resident session must not start another database budget")
		return nil, nil
	})
	require.NoError(t, err)
}

func TestCandidatesReadViewColdLoadRefreshesAllCandidateMetrics(t *testing.T) {
	e, memory := testEngine(t)
	r := request("candidates-view", "session", testNow)
	at := testNow
	e.now = func() time.Time { return at }
	e.loadedAt = at
	candidates := []Candidate{{AccountID: r.AccountID, UpstreamKeyID: r.UpstreamKeyID}, {AccountID: 9, UpstreamKeyID: 3}}
	for _, c := range candidates {
		e.accounts[accountIdentity(c.AccountID, c.UpstreamKeyID)] = AccountSnapshot{
			AccountState: AccountState{AccountID: c.AccountID, UpstreamKeyID: c.UpstreamKeyID, Phase: Healthy, CachePercent: 10}, LoadedAt: at,
		}
	}
	store := &readViewSessionStore{memoryStore: memory}
	store.load = func() (*SessionState, int64, error) {
		e.mu.Lock()
		defer e.mu.Unlock()
		at = testNow.Add(time.Second)
		e.loadedAt = at
		for _, c := range candidates {
			a := e.accounts[accountIdentity(c.AccountID, c.UpstreamKeyID)]
			a.LoadedAt, a.CachePercent = at, 90
			e.accounts[accountIdentity(c.AccountID, c.UpstreamKeyID)] = a
		}
		return &SessionState{Key: SessionKey(r)}, 1, nil
	}
	e.store = store
	view, err := e.CandidatesReadViewWithDatabaseContext(context.Background(), r, candidates, nil)
	require.NoError(t, err)
	require.True(t, view.Consistent)
	require.Equal(t, e.PolicyVersion(), view.PolicyVersion)
	require.Equal(t, at, view.EvaluatedAt)
	require.Equal(t, at, view.Snapshot.LoadedAt)
	require.True(t, view.Snapshot.Fresh)
	require.Len(t, view.Snapshot.Accounts, len(candidates))
	for _, account := range view.Snapshot.Accounts {
		require.Equal(t, at, account.LoadedAt)
		require.Equal(t, float64(90), account.CachePercent)
		require.True(t, account.Fresh)
	}
}

func TestReadViewDiagnosticsDistinguishesAccountFailuresWithoutIdentifiers(t *testing.T) {
	e, _ := testEngine(t)
	r := request("secret-request", "secret-session", testNow)
	e.loadedAt = testNow
	id := accountIdentity(r.AccountID, r.UpstreamKeyID)
	e.accounts[id] = AccountSnapshot{AccountState: AccountState{AccountID: r.AccountID, UpstreamKeyID: r.UpstreamKeyID, Phase: Healthy}, LoadedAt: testNow}
	view := e.CachedAdmissionReadView(r)
	require.True(t, view.Diagnostics.Available)
	require.True(t, view.Diagnostics.Fresh)
	require.Empty(t, view.Diagnostics.UnavailableReason)
	e.accountGaps[id] = true
	view = e.CachedAdmissionReadView(r)
	require.True(t, view.Diagnostics.Available, "a gap retains the thirty-second usable snapshot")
	require.False(t, view.Diagnostics.Fresh)
	require.True(t, view.Diagnostics.HasPendingGap)
	require.Equal(t, "account_gap", view.Diagnostics.UnavailableReason)
	e.samplingFailures[id] = map[string]struct{}{"secret-admission": {}}
	view = e.CachedAdmissionReadView(r)
	require.True(t, view.Diagnostics.HasSamplingFailure)
	require.Equal(t, "sampling_failure", view.Diagnostics.UnavailableReason)
	raw, err := json.Marshal(view.Diagnostics)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "secret-")
	require.NotContains(t, string(raw), "route")
}
