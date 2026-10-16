package service

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAccountListSharedReadIndependentCancellationAndNoCompletedCache(t *testing.T) {
	base := WithAccountListReadAuthorization(context.Background(), 101, "admin")
	ctx, cancel := context.WithCancel(base)
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	read := func(ctx context.Context) (int, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		select {
		case <-release:
			return 42, nil
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	first := make(chan error, 1)
	go func() { _, err := accountListSharedRead(ctx, t.Name(), "test", "query", read); first <- err }()
	<-started
	second := make(chan int, 1)
	joined := make(chan struct{})
	secondContext := &accountListJoinedContext{Context: base, joined: joined}
	go func() {
		value, err := accountListSharedRead(secondContext, t.Name(), "test", "query", read)
		if err != nil {
			second <- -1
		} else {
			second <- value
		}
	}()
	// Wait for the second caller to join the existing flight before cancelling
	// the first. Cancellation itself must not propagate to the database context.
	<-joined
	cancel()
	require.ErrorIs(t, <-first, context.Canceled)
	close(release)
	require.Equal(t, 42, <-second)
	_, err := accountListSharedRead(base, t.Name(), "test", "query", read)
	require.NoError(t, err)
	require.Equal(t, int32(2), calls.Load())
}

type accountListJoinedContext struct {
	context.Context
	joined chan struct{}
	once   sync.Once
}

func (ctx *accountListJoinedContext) Done() <-chan struct{} {
	ctx.once.Do(func() { close(ctx.joined) })
	return ctx.Context.Done()
}

func TestAccountListSharedReadAuthorizationAndQueryIsolation(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	started := make(chan struct{}, 4)
	read := func(context.Context) (int, error) { calls.Add(1); started <- struct{}{}; <-release; return 1, nil }
	done := make(chan struct{}, 4)
	for _, query := range []struct {
		user        int64
		role, query string
	}{{1, "admin", "a"}, {2, "admin", "a"}, {1, "operator", "a"}, {1, "admin", "b"}} {
		go func() {
			defer func() { done <- struct{}{} }()
			_, _ = accountListSharedRead(WithAccountListReadAuthorization(context.Background(), query.user, query.role), t.Name(), "read", query.query, read)
		}()
	}
	for range 4 {
		<-started
	}
	close(release)
	for range 4 {
		<-done
	}
	require.Equal(t, int32(4), calls.Load())
}

func TestAccountListSharedReadFinalCancellationReleasesUnderlyingRead(t *testing.T) {
	ctx, cancel := context.WithCancel(WithAccountListReadAuthorization(context.Background(), 3, "admin"))
	started, released := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := accountListSharedRead(ctx, t.Name(), "read", "query", func(shared context.Context) (int, error) {
			close(started)
			<-shared.Done()
			close(released)
			return 0, shared.Err()
		})
		done <- err
	}()
	<-started
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	select {
	case <-released:
	case <-time.After(time.Second):
		t.Fatal("final cancelled caller did not release database read")
	}
	accountListRawFlights.Lock()
	defer accountListRawFlights.Unlock()
	require.Empty(t, accountListRawFlights.active)
}

func TestAccountListSharedReadErrorsAreNotCached(t *testing.T) {
	ctx := WithAccountListReadAuthorization(context.Background(), 4, "admin")
	var calls int
	read := func(context.Context) (int, error) {
		calls++
		if calls == 1 {
			return 0, errors.New("temporary")
		}
		return 2, nil
	}
	_, err := accountListSharedRead(ctx, t.Name(), "read", "query", read)
	require.Error(t, err)
	got, err := accountListSharedRead(ctx, t.Name(), "read", "query", read)
	require.NoError(t, err)
	require.Equal(t, 2, got)
	require.Equal(t, 2, calls)
}

func TestAccountListSharedReadIncludesContextFilters(t *testing.T) {
	base := WithAccountListReadAuthorization(context.Background(), 5, "admin")
	config, key := int64(11), int64(12)
	contexts := []context.Context{base, WithAccountListPreferred(base, true), WithAccountListUpstreamIDs(base, AccountListUpstreamIDs{ConfigID: &config}), WithAccountListUpstreamIDs(base, AccountListUpstreamIDs{KeyID: &key})}
	started, release, done := make(chan struct{}, 4), make(chan struct{}), make(chan struct{}, 4)
	var calls atomic.Int32
	for _, ctx := range contexts {
		go func() {
			defer func() { done <- struct{}{} }()
			_, _ = accountListSharedRead(ctx, t.Name(), "read", "same query", func(context.Context) (int, error) { calls.Add(1); started <- struct{}{}; <-release; return 1, nil })
		}()
	}
	for range 4 {
		<-started
	}
	close(release)
	for range 4 {
		<-done
	}
	require.Equal(t, int32(4), calls.Load())
}
