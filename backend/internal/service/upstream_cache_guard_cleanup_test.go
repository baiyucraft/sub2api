package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/upstreamcacheguard"
	"github.com/stretchr/testify/require"
)

type timedCleanupStore struct {
	*runnerTestStore
	mu               sync.Mutex
	remaining, calls int
	limits           []int
	cutoffs          []time.Time
	fail             bool
}

func (s *timedCleanupStore) CleanupBatch(ctx context.Context, before time.Time, limit int) (int64, error) {
	s.mu.Lock()
	s.calls++
	s.limits = append(s.limits, limit)
	s.cutoffs = append(s.cutoffs, before)
	fail := s.fail
	s.mu.Unlock()
	if fail {
		return 0, errors.New("cleanup unavailable")
	}
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-time.After(600 * time.Millisecond):
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	n := min(limit, s.remaining)
	s.remaining -= n
	return int64(n), nil
}

func (s *timedCleanupStore) snapshot() (int, int, []int, []time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls, s.remaining, append([]int(nil), s.limits...), append([]time.Time(nil), s.cutoffs...)
}

func TestCacheGuardCleanupMinuteCadenceDeadlineCatchupAndFailureIsolation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &timedCleanupStore{runnerTestStore: newRunnerTestStore(), remaining: 4500}
		runner := testRunner(t, store.runnerTestStore)
		runner.store = store
		runner.engine = upstreamcacheguard.NewEngine(store)
		settings := upstreamcacheguard.DefaultSettings()
		settings.Mode = upstreamcacheguard.Protect
		require.NoError(t, runner.engine.ApplySettings(settings))
		testRunnerAdmission(t, runner)
		require.NoError(t, runner.engine.Refresh(context.Background()))
		start := time.Now()
		go runner.run()
		defer runner.Close()
		time.Sleep(time.Minute - time.Nanosecond)
		synctest.Wait()
		calls, _, _, _ := store.snapshot()
		require.Zero(t, calls, "cleanup must not run before its minute ticker")
		time.Sleep(2*time.Second + time.Nanosecond)
		synctest.Wait()
		calls, remaining, limits, cutoffs := store.snapshot()
		require.Equal(t, 4, calls, "three complete batches and one canceled batch share the same 2-second deadline")
		require.Equal(t, 1500, remaining)
		for _, limit := range limits {
			require.Equal(t, 1000, limit)
		}
		for _, cutoff := range cutoffs {
			require.True(t, start.Add(time.Minute-48*time.Hour).Equal(cutoff), "all batches must share the round's retention cutoff")
		}
		time.Sleep(58*time.Second + 1200*time.Millisecond)
		synctest.Wait()
		calls, remaining, _, _ = store.snapshot()
		require.Equal(t, 6, calls, "the next minute resumes the backlog rather than waiting a full round per batch")
		require.Zero(t, remaining)
		// A fresh successful snapshot remains readable despite cleanup failure.
		require.NoError(t, runner.engine.Refresh(context.Background()))
		prior, ok := runner.engine.CachedAccountForGroupAt(1, 2, 3, time.Now())
		require.True(t, ok)
		store.mu.Lock()
		store.fail = true
		store.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		require.Error(t, runner.engine.Cleanup(ctx))
		cancel()
		after, ok := runner.engine.CachedAccountForGroupAt(1, 2, 3, time.Now())
		require.True(t, ok, "maintenance failure cannot withdraw a verified account snapshot")
		require.Equal(t, prior.AccountState, after.AccountState)
		require.Equal(t, prior.LoadedAt, after.LoadedAt)
	})
}
