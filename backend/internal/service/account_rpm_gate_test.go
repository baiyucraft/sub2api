package service

import (
	"context"
	"errors"
	"testing"
	"time"
)

type accountRPMGateTestCache struct {
	counts       map[int64]int
	batchErr     error
	getErr       error
	acquireErr   error
	acquireCalls int
	limit        int
}

func (c *accountRPMGateTestCache) IncrementRPM(_ context.Context, accountID int64) (int, error) {
	c.counts[accountID]++
	return c.counts[accountID], nil
}

func (c *accountRPMGateTestCache) GetRPM(_ context.Context, accountID int64) (int, error) {
	if c.getErr != nil {
		return 0, c.getErr
	}
	return c.counts[accountID], nil
}

func (c *accountRPMGateTestCache) GetRPMBatch(_ context.Context, accountIDs []int64) (map[int64]int, error) {
	if c.batchErr != nil {
		return nil, c.batchErr
	}
	out := make(map[int64]int, len(accountIDs))
	for _, id := range accountIDs {
		out[id] = c.counts[id]
	}
	return out, nil
}

func (c *accountRPMGateTestCache) TryAcquireRPM(_ context.Context, accountID int64, limit int) (bool, int, time.Duration, error) {
	c.acquireCalls++
	if c.acquireErr != nil {
		return false, c.counts[accountID], 9 * time.Second, c.acquireErr
	}
	if c.counts[accountID] >= limit {
		return false, c.counts[accountID], 7 * time.Second, nil
	}
	c.counts[accountID]++
	c.limit = limit
	return true, c.counts[accountID], 0, nil
}

func TestAccountRPMGatePrefetchAndFailOpen(t *testing.T) {
	cache := &accountRPMGateTestCache{counts: map[int64]int{1: 3}}
	ctx := withAccountRPMPrefetch(context.Background(), cache, []Account{{ID: 1, RPMLimit: 3}, {ID: 2}})
	if IsAccountSchedulableForRPM(ctx, cache, &Account{ID: 1, RPMLimit: 3}) {
		t.Fatal("account at the prefetched limit must be filtered")
	}
	if !IsAccountSchedulableForRPM(ctx, cache, &Account{ID: 2, RPMLimit: 0}) {
		t.Fatal("unlimited account must remain schedulable")
	}
	failing := &accountRPMGateTestCache{counts: map[int64]int{}, batchErr: errors.New("redis down"), getErr: errors.New("redis down")}
	if !IsAccountSchedulableForRPM(context.Background(), failing, &Account{ID: 1, RPMLimit: 1}) {
		t.Fatal("prefetch/read failures must fail open")
	}
}

func TestTryAcquireAccountRPMIsAtomicAndFailOpen(t *testing.T) {
	cache := &accountRPMGateTestCache{counts: map[int64]int{1: 1}}
	account := &Account{ID: 1, RPMLimit: 1}
	allowed, retryAfter, err := TryAcquireAccountRPM(context.Background(), cache, account)
	if allowed || err != nil || retryAfter != 7*time.Second {
		t.Fatalf("limit denial = allowed=%v retry=%s err=%v", allowed, retryAfter, err)
	}
	if cache.acquireCalls != 1 {
		t.Fatalf("expected one atomic acquire, got %d", cache.acquireCalls)
	}
	cache.acquireErr = errors.New("redis down")
	allowed, _, err = TryAcquireAccountRPM(context.Background(), cache, &Account{ID: 2, RPMLimit: 1})
	if !allowed || err == nil {
		t.Fatalf("redis failure must fail open, got allowed=%v err=%v", allowed, err)
	}
	allowed, _, err = TryAcquireAccountRPM(context.Background(), cache, &Account{ID: 3, RPMLimit: 0})
	if !allowed || err != nil || cache.acquireCalls != 2 {
		t.Fatalf("unlimited account must bypass limiter, got allowed=%v err=%v calls=%d", allowed, err, cache.acquireCalls)
	}
}

func TestAccountRPMRetryAfterKeepsShortestWindow(t *testing.T) {
	ctx := RecordAccountRPMRetryAfter(context.Background(), 12*time.Second)
	ctx = RecordAccountRPMRetryAfter(ctx, 4*time.Second)
	ctx = RecordAccountRPMRetryAfter(ctx, 9*time.Second)
	if got := AccountRPMRetryAfter(ctx); got != 4 {
		t.Fatalf("shortest retry-after = %d, want 4", got)
	}
}
