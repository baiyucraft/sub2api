package service

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/upstreamcacheguard"
)

type boundedCompletionStore struct {
	*runnerTestStore
	workerMu           sync.Mutex
	active, total, max int
	accounts           map[int64]int
	overlap            bool
	started            chan int64
	release            chan struct{}
}

func (s *boundedCompletionStore) TransactAccount(ctx context.Context, group, account, key int64, fn func(upstreamcacheguard.Tx) error) error {
	s.workerMu.Lock()
	s.active++
	s.total++
	s.accounts[account]++
	if s.accounts[account] > 1 {
		s.overlap = true
	}
	if s.active > s.max {
		s.max = s.active
	}
	s.workerMu.Unlock()
	defer func() { s.workerMu.Lock(); s.active--; s.accounts[account]--; s.workerMu.Unlock() }()
	s.started <- account
	select {
	case <-s.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return s.runnerTestStore.TransactAccount(ctx, group, account, key, fn)
}

func TestCacheGuardCompletionWorkersBoundedAndAccountSerial(t *testing.T) {
	store := &boundedCompletionStore{runnerTestStore: newRunnerTestStore(), accounts: map[int64]int{}, started: make(chan int64, 16), release: make(chan struct{})}
	now := time.Now().UTC()
	for account := int64(1); account <= 6; account++ {
		for n := 0; n < 2; n++ {
			id := fmt.Sprintf("%d-%d", account, n)
			r := upstreamcacheguard.Request{ID: id, LogicalSessionDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", APIKeyID: 1, GroupID: 1, AccountID: account, UpstreamKeyID: 1, Model: "test", Protocol: "openai", StartedAt: now.Add(-time.Second)}
			a := upstreamcacheguard.Admission{RequestID: id, SessionKey: upstreamcacheguard.SessionKey(r), GroupID: 1, AccountID: account, UpstreamKeyID: 1, Mode: upstreamcacheguard.Disabled}
			store.events[id] = upstreamcacheguard.Evidence{ID: id, Request: r, Admission: a, CompletedAt: now}
		}
	}
	runner := &UpstreamCacheGuardRunner{engine: upstreamcacheguard.NewEngine(store), store: store, owner: "worker-test", wake: make(chan struct{}, 1)}
	done := make(chan struct{})
	go func() { runner.processPending(); close(done) }()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for n := 0; n < 4; n++ {
		select {
		case <-store.started:
		case <-timer.C:
			close(store.release)
			t.Fatal("different accounts did not make progress concurrently")
		}
	}
	// The release channel freezes all four workers at the transaction boundary.
	// A fifth observed entry here would prove that the configured bound was lost.
	select {
	case <-store.started:
		close(store.release)
		t.Fatal("more than four completion workers entered")
	default:
	}
	close(store.release)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("completion batch did not drain")
	}
	store.workerMu.Lock()
	defer store.workerMu.Unlock()
	if store.max != 4 || store.overlap || store.total != 12 {
		t.Fatalf("max=%d overlap=%v completed=%d", store.max, store.overlap, store.total)
	}
	if pending, _ := store.HasPending(context.Background()); pending {
		t.Fatal("successful completion remained pending")
	}
}
