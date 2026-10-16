package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/upstreamcacheusage"
	"github.com/Wei-Shaw/sub2api/internal/upstreamcacheguard"
	"github.com/stretchr/testify/require"
)

type runnerTestStore struct {
	mu                                               sync.Mutex
	events                                           map[string]upstreamcacheguard.Evidence
	processed                                        map[string]bool
	rows                                             map[string]json.RawMessage
	enqueueDown, completeDown, freshnessDown, leased bool
}

func newRunnerTestStore() *runnerTestStore {
	return &runnerTestStore{events: map[string]upstreamcacheguard.Evidence{}, processed: map[string]bool{}, rows: map[string]json.RawMessage{}}
}

type runnerTestTx struct{ s *runnerTestStore }

func (s *runnerTestStore) Transact(ctx context.Context, _ int64, fn func(upstreamcacheguard.Tx) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.completeDown {
		return errors.New("db interrupted")
	}
	return fn(&runnerTestTx{s})
}
func (t *runnerTestTx) Read(kind, k string, out any) (int64, error) {
	raw := t.s.rows[kind+":"+k]
	if raw == nil {
		return 0, nil
	}
	return 1, json.Unmarshal(raw, out)
}
func (t *runnerTestTx) Write(kind, k string, v any) (int64, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return 0, err
	}
	t.s.rows[kind+":"+k] = raw
	return 1, nil
}
func (t *runnerTestTx) InsertFact(e upstreamcacheguard.Evidence) (bool, error) {
	if t.s.processed[e.ID] {
		return false, nil
	}
	t.s.processed[e.ID] = true
	t.s.events[e.ID] = e
	return true, nil
}
func (t *runnerTestTx) Facts(account int64, since time.Time) ([]upstreamcacheguard.Evidence, error) {
	var out []upstreamcacheguard.Evidence
	for id, e := range t.s.events {
		if t.s.processed[id] && e.Request.AccountID == account && !e.CompletedAt.Before(since) {
			out = append(out, e)
		}
	}
	return out, nil
}
func (s *runnerTestStore) Rows(context.Context, []int64) ([]upstreamcacheguard.RuntimeRow, error) {
	return nil, nil
}
func (s *runnerTestStore) Cleanup(context.Context, time.Time) error { return nil }
func (s *runnerTestStore) Enqueue(ctx context.Context, e upstreamcacheguard.Evidence) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.enqueueDown {
		return errors.New("db interrupted")
	}
	if _, ok := s.events[e.ID]; !ok {
		s.events[e.ID] = e
	}
	return nil
}
func (s *runnerTestStore) ClaimPending(_ context.Context, _ string, limit int, _ time.Duration) ([]upstreamcacheguard.Evidence, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.leased {
		return nil, nil
	}
	var out []upstreamcacheguard.Evidence
	for id, e := range s.events {
		if !s.processed[id] {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CompletedAt.Equal(out[j].CompletedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CompletedAt.Before(out[j].CompletedAt)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func (s *runnerTestStore) HasPending(context.Context) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id := range s.events {
		if !s.processed[id] {
			return true, nil
		}
	}
	return false, nil
}

func (s *runnerTestStore) HasPendingBefore(ctx context.Context, cutoff time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if s.freshnessDown {
		return false, errors.New("freshness query unavailable")
	}
	for id, e := range s.events {
		if !s.processed[id] && e.CompletedAt.Before(cutoff) {
			return true, nil
		}
	}
	return false, nil
}
func testRunner(t *testing.T, s *runnerTestStore) *UpstreamCacheGuardRunner {
	t.Helper()
	r := &UpstreamCacheGuardRunner{engine: upstreamcacheguard.NewEngine(s), store: s, owner: "test", queue: make(chan upstreamcacheguard.Evidence, 1), wake: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{})}
	settings := upstreamcacheguard.DefaultSettings()
	settings.Mode = upstreamcacheguard.Protect
	if err := r.engine.ApplySettings(settings); err != nil {
		t.Fatal(err)
	}
	r.available.Store(true)
	old := installedCacheGuardRunner.Swap(r)
	t.Cleanup(func() { installedCacheGuardRunner.Store(old) })
	return r
}
func testRunnerAdmission(t *testing.T, r *UpstreamCacheGuardRunner) *UpstreamCacheGuardAdmission {
	t.Helper()
	req := upstreamcacheguard.Request{ID: newCacheGuardID(), LogicalSessionDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", APIKeyID: 1, GroupID: 1, AccountID: 2, UpstreamKeyID: 3, Model: "gpt-test", Protocol: "openai", StartedAt: time.Now().UTC().Add(-time.Second)}
	a, err := r.engine.Admit(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	return &UpstreamCacheGuardAdmission{Request: req, Admission: a}
}

func TestUpstreamCacheGuardQueueFullPersistsBeforeReturn(t *testing.T) {
	s := newRunnerTestStore()
	r := testRunner(t, s)
	a := testRunnerAdmission(t, r)
	r.queue <- upstreamcacheguard.Evidence{ID: "occupied"}
	raw := UpstreamCacheUsageEvidence{Status: upstreamcacheusage.Known, TotalInputTokens: 100, CacheReadTokens: 90}
	ObserveUpstreamCacheGuardResult(a, &raw, true, time.Now().UTC())
	if pending, err := s.HasPending(context.Background()); err != nil || !pending {
		t.Fatalf("full queue lost completion: %v %v", pending, err)
	}
	if r.lost.Load() || r.retrying.Load() != 0 {
		t.Fatal("healthy durable delivery was treated as queue loss")
	}
	r.processPending()
	if pending, _ := s.HasPending(context.Background()); pending {
		t.Fatal("worker did not acknowledge durable completion")
	}
}

func TestUpstreamCacheGuardDatabaseShortOutageRetry(t *testing.T) {
	s := newRunnerTestStore()
	r := testRunner(t, s)
	a := testRunnerAdmission(t, r)
	s.enqueueDown = true
	ObserveUpstreamCacheGuardResult(a, nil, false, time.Now().UTC())
	if r.available.Load() || r.retrying.Load() != 1 || !r.pendingFailure.Load() {
		t.Fatal("outage did not withdraw preferences")
	}
	s.enqueueDown = false
	v := <-r.queue
	if !r.retryEnqueue(v) {
		t.Fatal("retry failed after db recovery")
	}
	if r.retrying.Load() != 0 {
		t.Fatal("retry remained unresolved")
	}
	s.completeDown = true
	r.processPending()
	if !r.pendingFailure.Load() {
		t.Fatal("failed state transaction cleared failure latch")
	}
	s.completeDown = false
	s.leased = true
	r.processPending()
	if !r.pendingFailure.Load() {
		t.Fatal("leased pending was mistaken for empty outbox")
	}
	s.leased = false
	r.processPending()
	if r.pendingFailure.Load() {
		t.Fatal("fully acknowledged outbox did not clear transient failure")
	}
	if pending, _ := s.HasPending(context.Background()); pending {
		t.Fatal("retry did not persist outcome")
	}
}

func TestUpstreamCacheGuardOverflowLatchesSamplingLoss(t *testing.T) {
	s := newRunnerTestStore()
	r := testRunner(t, s)
	a := testRunnerAdmission(t, r)
	s.enqueueDown = true
	r.queue <- upstreamcacheguard.Evidence{ID: "full"}
	ObserveUpstreamCacheGuardResult(a, nil, false, time.Now().UTC())
	if !r.lost.Load() || r.available.Load() {
		t.Fatal("overflow did not latch fail-open")
	}
	s.enqueueDown = false
	r.processPending()
	if !r.lost.Load() {
		t.Fatal("refreshable db erased sampling loss")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var session upstreamcacheguard.SessionState
	raw := s.rows[upstreamcacheguard.SessionKind+":"+a.Admission.SessionKey]
	if err := json.Unmarshal(raw, &session); err != nil {
		t.Fatal(err)
	}
	incomplete := false
	for _, admission := range session.Admissions {
		if !admission.Completed {
			incomplete = true
		}
	}
	if !incomplete {
		t.Fatal("lost completion had no durable missing-admission marker")
	}
}

func TestUpstreamCacheGuardCloseRestartKeepsPending(t *testing.T) {
	s := newRunnerTestStore()
	r := testRunner(t, s)
	a := testRunnerAdmission(t, r)
	s.completeDown = true
	ObserveUpstreamCacheGuardResult(a, nil, false, time.Now().UTC())
	go r.run()
	r.Close()
	if pending, _ := s.HasPending(context.Background()); !pending {
		t.Fatal("close discarded durable pending")
	}
	s.completeDown = false
	r2 := testRunner(t, s)
	r2.processPending()
	if pending, _ := s.HasPending(context.Background()); pending {
		t.Fatal("restart failed to finish retained outcome")
	}
	r2.processPending()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.events) != 1 {
		t.Fatal("restart duplicated outcome")
	}
}

func TestUpstreamCacheGuardLogicalIdentityModelInvariant(t *testing.T) {
	s := newRunnerTestStore()
	r := testRunner(t, s)
	r.secret = []byte("local-test-secret")
	ctx := WithUpstreamCacheGuardScheduling(context.Background(), 7)
	group := int64(3)
	a, ok := r.logicalRequest(ctx, &group, "logical-session", "gpt-a", "openai")
	if !ok {
		t.Fatal("identity missing")
	}
	b, ok := r.logicalRequest(ctx, &group, "logical-session", "gpt-b", "anthropic")
	if !ok || a.LogicalSessionDigest != b.LogicalSessionDigest || a.ID != b.ID {
		t.Fatal("model/protocol change acquired new logical identity")
	}
	c, _ := r.logicalRequest(WithUpstreamCacheGuardScheduling(context.Background(), 8), &group, "logical-session", "gpt-a", "openai")
	if a.LogicalSessionDigest == c.LogicalSessionDigest {
		t.Fatal("API key scope missing")
	}
	stableCtx := context.WithValue(ctx, cacheGuardLogicalSessionKey{}, "stable-cli-identity")
	x, ok := r.logicalRequest(stableCtx, &group, "body-hash-before", "gpt-a", "openai")
	if !ok {
		t.Fatal("stable identity missing")
	}
	y, ok := r.logicalRequest(stableCtx, &group, "body-hash-after", "gpt-b", "anthropic")
	if !ok || x.LogicalSessionDigest != y.LogicalSessionDigest {
		t.Fatal("body-derived routing hash changed reliable logical identity")
	}
}

func TestUpstreamCacheGuardBeginRequiresExistingIdentity(t *testing.T) {
	s := newRunnerTestStore()
	r := testRunner(t, s)
	r.secret = []byte("test-only")
	if err := r.engine.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	group, upstream, configID := int64(1), int64(3), int64(4)
	account := &Account{ID: 2, Platform: PlatformOpenAI, UpstreamConfigID: &configID, UpstreamKeyID: &upstream}
	if a := BeginUpstreamCacheGuardRequest(context.Background(), 7, &group, "session", "gpt-test", "openai", account); a != nil {
		t.Fatal("invented identity for unclassified request")
	}
	ctx := WithUpstreamCacheGuardScheduling(context.Background(), 8)
	if a := BeginUpstreamCacheGuardRequest(ctx, 7, &group, "session", "gpt-test", "openai", account); a != nil {
		t.Fatal("accepted mismatched API key identity")
	}
	ctx = WithUpstreamCacheGuardScheduling(context.Background(), 7)
	ordinary := &Account{ID: 2, Platform: PlatformOpenAI, UpstreamKeyID: &upstream}
	if a := BeginUpstreamCacheGuardRequest(ctx, 7, &group, "session", "gpt-test", "openai", ordinary); a != nil {
		t.Fatal("sampled ordinary account with an upstream key but no upstream config")
	}
	if a := BeginUpstreamCacheGuardRequest(ctx, 7, &group, "session", "gpt-test", "openai", account); a == nil {
		t.Fatal("reliable classified identity was not admitted")
	}
}

func TestUpstreamCacheGuardBurstDrainsBeyondOneWakeBatch(t *testing.T) {
	s := newRunnerTestStore()
	r := testRunner(t, s)
	at := time.Now().UTC().Add(-time.Second)
	for i := 0; i < 1000; i++ {
		id := fmt.Sprintf("burst-%04d", i)
		req := upstreamcacheguard.Request{ID: id, LogicalSessionDigest: fmt.Sprintf("%064x", i+1), APIKeyID: 1, GroupID: 1,
			AccountID: 2, UpstreamKeyID: 3, Model: "gpt-test", Protocol: "openai", StartedAt: at.Add(-time.Second)}
		// Old disabled admissions still require durable acknowledgment after a
		// restart or setting change, independent of whether they affect quality.
		s.events[id] = upstreamcacheguard.Evidence{ID: id, Request: req, CompletedAt: at, Admission: upstreamcacheguard.Admission{
			RequestID: id, SessionKey: upstreamcacheguard.SessionKey(req), GroupID: 1, AccountID: 2,
			StartedAt: req.StartedAt, Mode: upstreamcacheguard.Disabled,
		}}
	}
	r.processPending()
	if len(s.processed) != 32 {
		t.Fatal("fixture did not enforce the real claim batch size")
	}
drain:
	for rounds := 0; rounds < 40; rounds++ {
		select {
		case <-r.wake:
			r.processPending()
		default:
			break drain
		}
	}
	if pending, err := s.HasPending(context.Background()); err != nil || pending || len(s.processed) != 1000 {
		t.Fatalf("coalesced burst was stranded behind timer ticks: processed=%d pending=%v err=%v", len(s.processed), pending, err)
	}
	r.processPending()
	if len(s.events) != 1000 || len(s.processed) != 1000 {
		t.Fatal("extra wake duplicated durable outcomes")
	}
}

func TestUpstreamCacheGuardOldPendingDoesNotWithdrawFreshSnapshot(t *testing.T) {
	s := newRunnerTestStore()
	r := testRunner(t, s)
	r.secret = []byte("local-test-secret")
	encoded, err := json.Marshal(r.engine.Settings())
	if err != nil {
		t.Fatal(err)
	}
	r.settings = NewSettingService(&upstreamManagementSettingRepoStub{values: map[string]string{SettingKeyUpstreamCacheGuard: string(encoded)}}, nil)
	s.events["pending"] = upstreamcacheguard.Evidence{CompletedAt: time.Now().UTC().Add(-upstreamcacheguard.RefreshLifetime - time.Second)}
	s.leased = true
	r.processPending()
	r.refresh()
	if !r.engine.CachedSnapshot(1).Available {
		t.Fatal("test did not produce a newly loaded snapshot")
	}
	if !r.available.Load() {
		t.Fatal("fresh row reload was withdrawn by a completion backlog older than five seconds")
	}
	if !r.pendingDelayed.Load() || r.pendingUnknown.Load() {
		t.Fatal("delayed pending was not retained as a diagnostic")
	}
	// Query failure changes diagnostics, never account snapshot availability.
	s.processed["pending"] = true
	s.freshnessDown = true
	r.refresh()
	if !r.available.Load() {
		t.Fatal("pending-age query failure withdrew preferences")
	}
	if !r.pendingUnknown.Load() {
		t.Fatal("pending-age query failure was not retained as a diagnostic")
	}
	s.freshnessDown = false
	r.refresh()
	if !r.available.Load() {
		t.Fatal("fully processed backlog did not restore trusted observation")
	}
	s.events["recent"] = upstreamcacheguard.Evidence{CompletedAt: time.Now().UTC()}
	r.refresh()
	if !r.available.Load() {
		t.Fatal("in-budget pending completion unnecessarily disabled preferences")
	}
}

type cacheGuardSelectionDeliveryStore struct {
	*cacheGuardSelectionStore
	enqueueDown, completeDown bool
	pending                   []upstreamcacheguard.Evidence
}

func (s *cacheGuardSelectionDeliveryStore) Enqueue(_ context.Context, v upstreamcacheguard.Evidence) error {
	if s.enqueueDown {
		return errors.New("outbox unavailable")
	}
	s.pending = append(s.pending, v)
	return nil
}

func (s *cacheGuardSelectionDeliveryStore) ClaimPending(context.Context, string, int, time.Duration) ([]upstreamcacheguard.Evidence, error) {
	return s.pending, nil
}

func (s *cacheGuardSelectionDeliveryStore) HasPending(context.Context) (bool, error) {
	return len(s.pending) > 0, nil
}

func (s *cacheGuardSelectionDeliveryStore) TransactAccount(context.Context, int64, int64, int64, func(upstreamcacheguard.Tx) error) error {
	if s.completeDown {
		return errors.New("completion unavailable")
	}
	s.pending = nil
	return nil
}

func TestUpstreamCacheGuardDeliveryFailuresStayAccountLocalAndRecover(t *testing.T) {
	for _, lost := range []bool{false, true} {
		t.Run(fmt.Sprintf("lost_%v", lost), func(t *testing.T) {
			_, _, runner, _ := cacheGuardSelectionFixture(t, true, []float64{60, 90}, nil, false, time.Time{}, nil)
			store := &cacheGuardSelectionDeliveryStore{cacheGuardSelectionStore: runner.store.(*cacheGuardSelectionStore), enqueueDown: true}
			runner.store = store
			settings := runner.engine.Settings()
			runner.engine = upstreamcacheguard.NewEngine(store)
			require.NoError(t, runner.engine.ApplySettings(settings))
			require.NoError(t, runner.engine.Refresh(context.Background()))
			runner.queue = make(chan upstreamcacheguard.Evidence, 1)
			runner.wake = make(chan struct{}, 1)
			if lost {
				runner.queue <- upstreamcacheguard.Evidence{ID: "occupied"}
			}
			request := upstreamcacheguard.Request{ID: "delivery-attempt", APIKeyID: 1, LogicalSessionDigest: fmt.Sprintf("%064x", 1), GroupID: 100, AccountID: 1, UpstreamKeyID: 1, Model: "gpt-test", Protocol: "openai", StartedAt: time.Now().UTC()}
			admission := &UpstreamCacheGuardAdmission{Request: request, Admission: upstreamcacheguard.Admission{RequestID: request.ID, SessionKey: upstreamcacheguard.SessionKey(request), GroupID: 100, AccountID: 1, Mode: upstreamcacheguard.Disabled}}
			ObserveUpstreamCacheGuardResult(admission, nil, false, time.Now().UTC())
			failed, usable := runner.engine.CachedAccount(1, 1)
			require.True(t, usable)
			require.False(t, failed.Fresh)
			healthy, usable := runner.engine.CachedAccount(2, 2)
			require.True(t, usable)
			require.True(t, healthy.Fresh)
			require.Equal(t, lost, runner.lost.Load())
			if lost {
				return
			}
			store.enqueueDown = false
			require.True(t, runner.retryEnqueue(<-runner.queue))
			store.completeDown = true
			runner.processPending()
			failed, usable = runner.engine.CachedAccount(1, 1)
			require.True(t, usable)
			require.False(t, failed.Fresh)
			store.completeDown = false
			runner.processPending()
			require.NoError(t, runner.engine.Refresh(context.Background()))
			recovered, usable := runner.engine.CachedAccount(1, 1)
			require.True(t, usable)
			require.True(t, recovered.Fresh)
		})
	}
}
