package service

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type qualityFlightRepo struct {
	UsageLogRepository
	calls   atomic.Int32
	started chan struct{}
	release chan struct{}
}

func (r *qualityFlightRepo) GetAccountQualityStatsBatch(ctx context.Context, ids []int64, _, _, _ time.Time) (map[int64]AccountQualitySamples, error) {
	r.calls.Add(1)
	if r.started != nil {
		r.started <- struct{}{}
	}
	select {
	case <-r.release:
		return map[int64]AccountQualitySamples{ids[0]: {SuccessfulRequests1h: 3}}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestAccountQualitySharedExactWindowIndependentCancellationAndNoTTL(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 1, time.UTC)
	r := &qualityFlightRepo{started: make(chan struct{}, 4), release: make(chan struct{})}
	s := &AccountUsageService{usageLogRepo: r}
	base := WithAccountListReadAuthorization(context.Background(), 811, "admin")
	firstCtx, cancel := context.WithCancel(base)
	first := make(chan error, 1)
	go func() { _, err := s.GetAccountQualityStatsBatch(firstCtx, []int64{3, 1, 3}, now); first <- err }()
	<-r.started
	joined := make(chan struct{})
	second := make(chan error, 1)
	go func() {
		_, err := s.GetAccountQualityStatsBatch(&accountListJoinedContext{Context: base, joined: joined}, []int64{1, 3}, now)
		second <- err
	}()
	<-joined
	cancel()
	require.ErrorIs(t, <-first, context.Canceled)
	close(r.release)
	require.NoError(t, <-second)
	require.Equal(t, int32(1), r.calls.Load())
	_, err := s.GetAccountQualityStatsBatch(base, []int64{1, 3}, now)
	require.NoError(t, err)
	require.Equal(t, int32(2), r.calls.Load(), "even the exact same completed window must be re-read")
}

func TestAccountQualitySharedReadDoesNotRoundTimeOrShareAuthorization(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 1, time.UTC)
	r := &qualityFlightRepo{started: make(chan struct{}, 4), release: make(chan struct{})}
	s := &AccountUsageService{usageLogRepo: r}
	done := make(chan error, 4)
	for _, entry := range []struct {
		user int64
		now  time.Time
		ids  []int64
	}{
		{1, now, []int64{1}}, {2, now, []int64{1}},
		{1, now.Add(time.Nanosecond), []int64{1}}, {1, now, []int64{2}},
	} {
		go func() {
			_, err := s.GetAccountQualityStatsBatch(WithAccountListReadAuthorization(context.Background(), entry.user, "admin"), entry.ids, entry.now)
			done <- err
		}()
	}
	for range 4 {
		<-r.started
	}
	require.Equal(t, int32(4), r.calls.Load())
	close(r.release)
	for range 4 {
		require.NoError(t, <-done)
	}
}
