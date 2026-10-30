package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type userRateInvalidationRepoStub struct {
	authInvalidationRepoStub
	events  []UserGroupRateInvalidationEvent
	ack     []UserGroupRateInvalidationEvent
	listErr error
	ackErr  error
	limit   int
}

func (r *userRateInvalidationRepoStub) ListUserGroupRateInvalidations(_ context.Context, limit int) ([]UserGroupRateInvalidationEvent, error) {
	r.limit = limit
	return r.events, r.listErr
}

func (r *userRateInvalidationRepoStub) DeleteUserGroupRateInvalidation(_ context.Context, id, revision int64) error {
	r.ack = append(r.ack, UserGroupRateInvalidationEvent{UserID: id, Revision: revision})
	return r.ackErr
}

type userRateInvalidationCacheStub struct {
	authInvalidationCacheStub
	mu    sync.Mutex
	users []int64
	err   error
	subFn func(context.Context, func(int64)) error
}

func (c *userRateInvalidationCacheStub) PublishUserGroupRateInvalidation(_ context.Context, userID int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.users = append(c.users, userID)
	return c.err
}

func (c *userRateInvalidationCacheStub) SubscribeUserGroupRateInvalidation(ctx context.Context, handle func(int64)) error {
	if c.subFn != nil {
		return c.subFn(ctx, handle)
	}
	<-ctx.Done()
	return ctx.Err()
}

func TestUserGroupRateInvalidationWorkerPublishesThenAcknowledgesRevision(t *testing.T) {
	old := 95.0
	rateRepo := &userGroupRateResolverRepoStub{percent: &old}
	resolver := newUserGroupRateResolver(rateRepo, nil, time.Minute, nil, "service.test")
	require.Equal(t, .95, resolver.Resolve(context.Background(), 135, 8, 1))
	newPercent := 50.0
	rateRepo.percent = &newPercent
	event := UserGroupRateInvalidationEvent{UserID: 135, Revision: 202}
	repo := &userRateInvalidationRepoStub{events: []UserGroupRateInvalidationEvent{event}}
	cache := &userRateInvalidationCacheStub{}
	worker := NewAuthCacheInvalidationWorker(repo, cache)
	require.NoError(t, worker.processUserGroupRateInvalidations(context.Background()))
	require.Equal(t, 100, repo.limit)
	require.Equal(t, []int64{135}, cache.users)
	require.Equal(t, []UserGroupRateInvalidationEvent{event}, repo.ack)
	require.Empty(t, cache.deleted, "rate invalidations do not delete arbitrary auth keys")
	require.Empty(t, cache.published, "rate invalidations never extend auth message grammar")
	require.Equal(t, .5, resolver.Resolve(context.Background(), 135, 8, 1))
}

func TestUserGroupRateInvalidationWorkerRetainsFailedEvents(t *testing.T) {
	for _, mode := range []string{"list", "publish", "ack", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			repo := &userRateInvalidationRepoStub{events: []UserGroupRateInvalidationEvent{{UserID: 135, Revision: 3}}}
			cache := &userRateInvalidationCacheStub{}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			sentinel := errors.New("test failure")
			switch mode {
			case "list":
				repo.listErr = sentinel
			case "publish":
				cache.err = sentinel
			case "ack":
				repo.ackErr = sentinel
			case "cancel":
				cancel()
			}
			worker := NewAuthCacheInvalidationWorker(repo, cache)
			err := worker.processUserGroupRateInvalidations(ctx)
			require.Error(t, err)
			if mode != "ack" {
				require.Empty(t, repo.ack)
			}
			if mode == "cancel" {
				require.ErrorIs(t, err, context.Canceled)
			} else {
				require.ErrorIs(t, err, sentinel)
			}
		})
	}
}

func TestUserGroupRateSubscriberReconnectClearsCachedMissingRatesAndStops(t *testing.T) {
	rateRepo := &userGroupRateResolverRepoStub{}
	resolver := newUserGroupRateResolver(rateRepo, nil, time.Minute, nil, "service.test")
	require.Equal(t, 1.0, resolver.Resolve(context.Background(), 135, 8, 1))
	percent := 95.0
	rateRepo.percent = &percent
	ready := make(chan struct{})
	cache := &userRateInvalidationCacheStub{subFn: func(ctx context.Context, _ func(int64)) error {
		NotifyAuthCacheSubscriptionReady(ctx)
		close(ready)
		<-ctx.Done()
		return ctx.Err()
	}}
	svc := &APIKeyService{}
	ctx, cancel := context.WithCancel(context.Background())
	svc.authInvalidationWG.Add(1)
	go svc.runUserGroupRateInvalidationSubscriber(ctx, cache)
	select {
	case <-ready:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("rate subscriber did not become ready")
	}
	require.Equal(t, .95, resolver.Resolve(context.Background(), 135, 8, 1))
	cancel()
	stopped := make(chan struct{})
	go func() { svc.authInvalidationWG.Wait(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("rate subscriber did not stop")
	}
}

func TestUserGroupRateSubscriberRunsWithAuthCachesDisabled(t *testing.T) {
	ready := make(chan struct{})
	cache := &userRateInvalidationCacheStub{
		authInvalidationCacheStub: authInvalidationCacheStub{subscribeFn: func(ctx context.Context, _ func(string)) error {
			<-ctx.Done()
			return ctx.Err()
		}},
		subFn: func(ctx context.Context, _ func(int64)) error {
			NotifyAuthCacheSubscriptionReady(ctx)
			close(ready)
			<-ctx.Done()
			return ctx.Err()
		},
	}
	svc := &APIKeyService{cache: cache}
	require.Nil(t, svc.authCacheL1)
	require.Nil(t, svc.authNegativeCacheL1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	svc.StartAuthCacheInvalidationSubscriber(ctx)
	select {
	case <-ready:
	case <-time.After(time.Second):
		t.Fatal("rate subscriber was disabled with auth caching")
	}
	stopped := make(chan struct{})
	go func() { svc.StopAuthCacheInvalidationSubscriber(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("service Stop did not stop rate subscriber")
	}
}
