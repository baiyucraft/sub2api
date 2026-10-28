package upstreamcacheguard

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func TestRefreshStagePreservesCauseAndRejectsLateSuccess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	err := runRefreshStage(ctx, "account_windows", func() error { cancel(); return nil })
	require.ErrorIs(t, err, context.Canceled)
	var stage *RefreshStageError
	require.True(t, errors.As(err, &stage))
	require.Equal(t, "account_windows", stage.Stage)
	called := false
	err = runRefreshStage(ctx, "identity_recheck", func() error { called = true; return nil })
	require.ErrorIs(t, err, context.Canceled)
	require.False(t, called)
}

func TestRefreshStagePreservesPostgresErrorAndContextCause(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		name := "deadline"
		if canceled {
			name = "canceled"
		}
		t.Run(name, func(t *testing.T) {
			var ctx context.Context
			var cancel context.CancelFunc
			if canceled {
				ctx, cancel = context.WithCancel(context.Background())
			} else {
				ctx, cancel = context.WithTimeout(context.Background(), 25*time.Millisecond)
			}
			defer cancel()
			driverErr := &pq.Error{Code: "57014", Message: "synthetic canceled statement"}
			err := runRefreshStage(ctx, "account_windows", func() error {
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
		})
	}
}

func TestRefreshCancellationBeforePublishNeverRenewsSnapshot(t *testing.T) {
	e, _ := testEngine(t)
	r := request("before-canceled-refresh", "existing-session", testNow.Add(-time.Minute))
	a := admit(t, e, r)
	complete(t, e, r, a, 90, true, "known")
	require.NoError(t, e.Refresh(context.Background()))
	before, ok := e.CachedAccount(r.AccountID, r.UpstreamKeyID)
	require.True(t, ok)
	e.now = func() time.Time { return testNow.Add(time.Second) }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = WithStageObserver(ctx, func(stage string, _ time.Duration) {
		if stage == "account_windows" {
			cancel()
		}
	})
	err := e.Refresh(ctx)
	require.ErrorIs(t, err, context.Canceled)
	var stage *RefreshStageError
	require.True(t, errors.As(err, &stage))
	require.Equal(t, "publish", stage.Stage)
	after, ok := e.CachedAccount(r.AccountID, r.UpstreamKeyID)
	require.True(t, ok)
	require.Equal(t, before.LoadedAt, after.LoadedAt)
	require.Equal(t, before.ValidRequests, after.ValidRequests)
	require.NoError(t, e.Refresh(context.Background()))
	after, ok = e.CachedAccount(r.AccountID, r.UpstreamKeyID)
	require.True(t, ok)
	require.True(t, after.LoadedAt.After(before.LoadedAt))
}

func TestRefreshPolicyChangeBeforePublishDoesNotRenewSnapshot(t *testing.T) {
	e, _ := testEngine(t)
	r := request("before-policy-change", "existing-session", testNow.Add(-time.Minute))
	a := admit(t, e, r)
	complete(t, e, r, a, 90, true, "known")
	require.NoError(t, e.Refresh(context.Background()))
	before, ok := e.CachedAccount(r.AccountID, r.UpstreamKeyID)
	require.True(t, ok)
	e.now = func() time.Time { return testNow.Add(time.Second) }
	ready, resume := make(chan struct{}), make(chan struct{})
	ctx := WithStageObserver(context.Background(), func(stage string, _ time.Duration) {
		if stage == "account_windows" {
			close(ready)
			<-resume
		}
	})
	result := make(chan error, 1)
	go func() { result <- e.Refresh(ctx) }()
	select {
	case <-ready:
	case <-time.After(time.Second):
		t.Fatal("refresh did not reach pre-publication barrier")
	}
	settings := e.Settings()
	settings.MinValidRequests++
	require.NoError(t, e.ApplySettings(settings))
	close(resume)
	err := <-result
	require.ErrorIs(t, err, ErrUnavailable)
	var stage *RefreshStageError
	require.True(t, errors.As(err, &stage))
	require.Equal(t, "publish", stage.Stage)
	e.mu.RLock()
	after := e.accounts[accountIdentity(r.AccountID, r.UpstreamKeyID)]
	e.mu.RUnlock()
	require.Equal(t, before.LoadedAt, after.LoadedAt)
	require.Equal(t, before.ValidRequests, after.ValidRequests)
}

type blockingRefreshRowsStore struct {
	*memoryStore
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (s *blockingRefreshRowsStore) Rows(ctx context.Context, ids []int64) ([]RuntimeRow, error) {
	s.once.Do(func() {
		close(s.entered)
		<-s.release
	})
	return s.memoryStore.Rows(ctx, ids)
}

func TestRefreshWaitingForSerializationRespondsToCancellation(t *testing.T) {
	e, store := testEngine(t)
	blocked := &blockingRefreshRowsStore{memoryStore: store, entered: make(chan struct{}), release: make(chan struct{})}
	e.store = blocked
	first := make(chan error, 1)
	go func() { first <- e.Refresh(context.Background()) }()
	<-blocked.entered
	ctx, cancel := context.WithCancel(context.Background())
	second := make(chan error, 1)
	go func() { second <- e.Refresh(ctx) }()
	cancel()
	select {
	case err := <-second:
		require.ErrorIs(t, err, context.Canceled)
		var stage *RefreshStageError
		require.True(t, errors.As(err, &stage))
		require.Equal(t, "refresh_lock", stage.Stage)
	case <-time.After(time.Second):
		t.Fatal("refresh waited for another refresh despite cancellation")
	}
	close(blocked.release)
	require.NoError(t, <-first)
	require.NoError(t, e.Refresh(context.Background()), "a canceled waiter must not consume or leak the serialization token")
}

func TestRefreshCanceledAfterLockAcquisitionReturnsToken(t *testing.T) {
	e, _ := testEngine(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = WithStageObserver(ctx, func(stage string, _ time.Duration) {
		if stage == "refresh_lock" {
			cancel()
		}
	})
	require.ErrorIs(t, e.Refresh(ctx), context.Canceled)
	require.NoError(t, e.Refresh(context.Background()), "cancellation immediately after lock acquisition must release the token")
}

func TestRefreshConfigurationCapturesOnePublishedGeneration(t *testing.T) {
	e, _ := testEngine(t)
	settings := e.Settings()
	settings.MinValidRequests++
	e.mu.Lock()
	// Stop the writer between its immutable pointer publication and the epoch
	// advance. A matching reader must wait rather than pair these generations.
	e.settings.Store(&settings)
	started := make(chan struct{})
	type capture struct {
		settings Settings
		epoch    uint64
		policies map[string]Settings
	}
	result := make(chan capture, 1)
	go func() {
		close(started)
		s, epoch, policies := e.refreshConfiguration()
		result <- capture{s, epoch, policies}
	}()
	<-started
	select {
	case <-result:
		e.mu.Unlock()
		t.Fatal("refresh read configuration before its writer published the matching epoch")
	case <-time.After(25 * time.Millisecond):
	}
	wantEpoch := e.policyEpoch.Add(1)
	e.mu.Unlock()
	select {
	case got := <-result:
		require.Equal(t, settings, got.settings)
		require.Equal(t, wantEpoch, got.epoch)
		require.Equal(t, settings.MinValidRequests, got.policies[PolicySignature(settings)].MinValidRequests)
	case <-time.After(time.Second):
		t.Fatal("configuration capture remained blocked after its writer completed")
	}
}
