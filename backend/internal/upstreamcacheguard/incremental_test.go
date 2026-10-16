package upstreamcacheguard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"testing"
	"time"
)

type incrementalMemoryStore struct {
	*memoryStore
	loads, windows int
	loadErr        error
	directoryHook  func()
	factReads      int
	windowIDs      []int64
}

type revisionMemoryTx struct {
	AccountTx
	store *memoryStore
	reads *int
}

func (tx *revisionMemoryTx) FactVersion(account, key int64) (int64, error) {
	var version int64
	for id, f := range tx.store.facts {
		if f.Request.AccountID == account && f.Request.UpstreamKeyID == key {
			version++
			if tx.store.processed[id] {
				version++
			}
		}
	}
	return version, nil
}
func (tx *revisionMemoryTx) AccountFacts(account, key int64, since time.Time) ([]Evidence, error) {
	*tx.reads++
	return tx.AccountTx.AccountFacts(account, key, since)
}
func (s *incrementalMemoryStore) TransactAccount(ctx context.Context, group, account, key int64, fn func(Tx) error) error {
	return s.memoryStore.TransactAccount(ctx, group, account, key, func(tx Tx) error {
		return fn(&revisionMemoryTx{AccountTx: tx.(AccountTx), store: tx.(*memoryTx).s, reads: &s.factReads})
	})
}

func (s *incrementalMemoryStore) IdentityVersions(ctx context.Context) ([]IdentityVersion, error) {
	if s.directoryHook != nil {
		s.directoryHook()
	}
	rows, err := s.AccountRows(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]IdentityVersion, 0, len(rows))
	for _, row := range rows {
		v := IdentityVersion{AccountID: row.AccountID, UpstreamKeyID: row.UpstreamKeyID, Version: row.Version}
		for key, f := range s.facts {
			if f.Request.AccountID == row.AccountID && f.Request.UpstreamKeyID == row.UpstreamKeyID {
				v.FactVersion++
				if s.processed[key] {
					v.FactVersion++
				}
			}
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AccountID < out[j].AccountID })
	return out, nil
}
func (s *incrementalMemoryStore) ActiveRuntimeRows(ctx context.Context, active []SessionIdentity, since time.Time) ([]RuntimeRow, error) {
	rows, err := s.Rows(ctx, nil)
	if err != nil {
		return nil, err
	}
	out := []RuntimeRow{}
	versions := map[string]int64{}
	for _, v := range active {
		versions[sessionCacheKey(v.GroupID, v.Key)] = v.Version
	}
	for _, row := range rows {
		if row.Kind == SessionKind {
			if versions[sessionCacheKey(row.GroupID, row.Key)] == row.Version {
				continue
			}
		}
		out = append(out, row)
	}
	return out, nil
}
func (s *incrementalMemoryStore) AccountRowsFor(ctx context.Context, ids []int64) ([]AccountRuntimeRow, error) {
	rows, err := s.AccountRows(ctx)
	out := []AccountRuntimeRow{}
	for _, row := range rows {
		for _, id := range ids {
			if id == row.AccountID {
				out = append(out, row)
				break
			}
		}
	}
	return out, err
}
func (s *incrementalMemoryStore) AccountWindowsFor(ctx context.Context, ids []int64, since, until time.Time) (map[string]AccountWindow, error) {
	out := map[string]AccountWindow{}
	if len(ids) == 0 {
		return out, nil
	}
	s.windows++
	s.windowIDs = append([]int64(nil), ids...)
	windows, err := s.AccountWindows(ctx, since, until)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, w := range windows {
		for _, id := range ids {
			if w.AccountID != id {
				continue
			}
			for factKey, f := range s.facts {
				if !s.processed[factKey] || !f.ValidSample() || f.Request.AccountID != id || f.Request.UpstreamKeyID != w.UpstreamKeyID || f.CompletedAt.Before(since) {
					continue
				}
				boundary := f.CompletedAt
				if !boundary.After(until) {
					boundary = boundary.Add(until.Sub(since)).Add(time.Nanosecond)
				}
				if w.NextBoundary.IsZero() || boundary.Before(w.NextBoundary) {
					w.NextBoundary = boundary
				}
			}
			out[key] = w
			break
		}
	}
	return out, nil
}
func (s *incrementalMemoryStore) LoadSession(ctx context.Context, group int64, key string) (*SessionState, int64, error) {
	s.loads++
	if s.loadErr != nil {
		return nil, 0, s.loadErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.rows[fmt.Sprintf("%d:%s:%s", group, SessionKind, key)]
	if !ok {
		return nil, 0, nil
	}
	var v SessionState
	err := json.Unmarshal(row.Payload, &v)
	return &v, row.Version, err
}

func TestIncrementalRefreshMatchesExactBaseline(t *testing.T) {
	old, _ := testEngine(t)
	optimizedStore := &incrementalMemoryStore{memoryStore: newMemoryStore()}
	optimized := NewEngine(optimizedStore)
	if err := optimized.ApplySettings(old.Settings()); err != nil {
		t.Fatal(err)
	}
	at := testNow
	old.now = func() time.Time { return at }
	optimized.now = old.now
	for i := 0; i < 24; i++ {
		r := request(fmt.Sprintf("event-%d", i), fmt.Sprintf("session-%d", i%4), testNow.Add(-time.Minute).Add(time.Duration(i)*time.Second))
		for _, engine := range []*Engine{old, optimized} {
			a := admit(t, engine, r)
			complete(t, engine, r, a, int64((i*17)%101), true, "known")
		}
		for _, engine := range []*Engine{old, optimized} {
			if err := engine.Refresh(context.Background()); err != nil {
				t.Fatal(err)
			}
		}
		a, _ := old.CachedSharedAccount(2, 3)
		b, _ := optimized.CachedSharedAccount(2, 3)
		if !reflect.DeepEqual(a.AccountState, b.AccountState) {
			t.Fatalf("event %d differs\nold=%+v\nnew=%+v", i, a, b)
		}
	}
	before := optimizedStore.windows
	at = at.Add(time.Second)
	if err := optimized.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if optimizedStore.windows != before {
		t.Fatal("unchanged identity recomputed window")
	}
	for _, offset := range []time.Duration{time.Hour - time.Minute, time.Hour - time.Minute + 24*time.Second, time.Hour + time.Nanosecond} {
		at = testNow.Add(offset)
		for _, engine := range []*Engine{old, optimized} {
			if err := engine.Refresh(context.Background()); err != nil {
				t.Fatal(err)
			}
		}
		a, _ := old.CachedSharedAccount(2, 3)
		b, _ := optimized.CachedSharedAccount(2, 3)
		if !reflect.DeepEqual(a.AccountState, b.AccountState) {
			t.Fatalf("window boundary %s differs", offset)
		}
	}
}

func TestSessionColdLoadPreservesIdentityAndFailureIsUnknown(t *testing.T) {
	store := &incrementalMemoryStore{memoryStore: newMemoryStore()}
	e := NewEngine(store)
	s := DefaultSettings()
	s.Mode = Protect
	e.ApplySettings(s)
	e.now = func() time.Time { return testNow }
	r := request("old", "old-session", testNow.Add(-time.Hour))
	admit(t, e, r)
	if err := e.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	e.sessions = map[string]SessionState{}
	e.mu.Unlock()
	read, err := e.SessionRead(context.Background(), r)
	if err != nil || !read.Known || store.loads != 1 {
		t.Fatalf("cold load lost identity: %+v %v", read, err)
	}
	e.mu.Lock()
	e.sessions = map[string]SessionState{}
	e.mu.Unlock()
	store.loadErr = errors.New("database timeout")
	read, err = e.SessionRead(context.Background(), r)
	if err == nil || read.Known {
		t.Fatalf("failed read became a new session: %+v %v", read, err)
	}
}

func TestCandidateProjectionCopiesOnlyRequestedPolicy(t *testing.T) {
	e, _ := testEngine(t)
	r := request("a", "s", testNow.Add(-time.Minute))
	admit(t, e, r)
	e.Refresh(context.Background())
	snapshot := e.CachedCandidates(1, []Candidate{{AccountID: 2, UpstreamKeyID: 3}})
	if len(snapshot.Accounts) != 1 || snapshot.Accounts[0].Policies != nil {
		t.Fatalf("unexpected candidate view: %+v", snapshot)
	}
	if len(e.CachedMetadata(1).Accounts) != 0 {
		t.Fatal("metadata enumerated accounts")
	}
}

func TestColdSessionMissAndFailureDoNotRetainAccessKeys(t *testing.T) {
	store := &incrementalMemoryStore{memoryStore: newMemoryStore()}
	e := NewEngine(store)
	s := DefaultSettings()
	s.Mode = Protect
	e.ApplySettings(s)
	e.now = func() time.Time { return testNow }
	e.loadedAt = testNow
	for _, failure := range []bool{false, true} {
		if failure {
			store.loadErr = errors.New("down")
		}
		for i := 0; i < 100; i++ {
			r := request(fmt.Sprint(i), fmt.Sprint(i), testNow)
			_, _ = e.SessionRead(context.Background(), r)
		}
	}
	if len(e.sessions) != 0 || len(e.sessionAccess) != 0 || len(e.sessionVersions) != 0 {
		t.Fatalf("miss/error retained identities: %d %d %d", len(e.sessions), len(e.sessionAccess), len(e.sessionVersions))
	}
}

func TestAdmissionReusesOnlyMatchingFactsStatePolicyAndTimeBoundary(t *testing.T) {
	store := &incrementalMemoryStore{memoryStore: newMemoryStore()}
	e := NewEngine(store)
	s := DefaultSettings()
	s.Mode = Protect
	e.ApplySettings(s)
	e.now = func() time.Time { return testNow }
	r := request("first", "session", testNow.Add(-time.Minute))
	a := admit(t, e, r)
	complete(t, e, r, a, 90, true, "known")
	if err := e.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	r2 := request("reuse", "session", testNow)
	before := store.factReads
	a2 := admit(t, e, r2)
	if store.factReads != before {
		t.Fatal("verified unchanged admission replayed all facts")
	}
	if err := e.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := store.Enqueue(context.Background(), Evidence{ID: "pending", Request: r2, Admission: a2, CompletedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	before = store.factReads
	admit(t, e, request("revision-change", "session", testNow))
	if store.factReads <= before {
		t.Fatal("pending fact revision reused old projection")
	}
	if err := e.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.nextBoundaries[accountIdentity(2, 3)] = testNow
	before = store.factReads
	admit(t, e, request("at-boundary", "session", testNow))
	if store.factReads <= before {
		t.Fatal("exact time boundary reused prior projection")
	}
	if err := e.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.ThresholdPercent = 70
	if err := e.ApplySettings(s); err != nil {
		t.Fatal(err)
	}
	before = store.factReads
	admit(t, e, request("policy-change", "session", testNow))
	if store.factReads <= before {
		t.Fatal("new policy reused old projection")
	}
}

func TestSessionPublicationIsVersionedAndCapacityBounded(t *testing.T) {
	e := NewEngine(nil)
	e.cacheSessionLocked("same", SessionState{Key: "new"}, 5, testNow)
	e.cacheSessionLocked("same", SessionState{Key: "old"}, 4, testNow)
	if e.sessions["same"].Key != "new" {
		t.Fatal("late publication replaced newer session")
	}
	for i := 0; i < MaxActiveSessions+10; i++ {
		key := fmt.Sprint(i)
		e.cacheSessionLocked(key, SessionState{Key: key}, 1, testNow)
	}
	if len(e.sessions) > MaxActiveSessions || len(e.sessionAccess) > MaxActiveSessions || len(e.sessionVersions) > MaxActiveSessions {
		t.Fatal("active session indices exceeded capacity")
	}
}

func TestIncrementalConfirmationRejectsChangedFactsWithoutPriorSnapshot(t *testing.T) {
	store := &incrementalMemoryStore{memoryStore: newMemoryStore()}
	e := NewEngine(store)
	s := DefaultSettings()
	s.Mode = Protect
	e.ApplySettings(s)
	e.now = func() time.Time { return testNow }
	r := request("seed", "session", testNow.Add(-time.Minute))
	a := admit(t, e, r)
	complete(t, e, r, a, 90, true, "known")
	calls := 0
	store.directoryHook = func() {
		calls++
		if calls == 2 {
			store.mu.Lock()
			pending := Evidence{ID: "late-pending", Request: r, Admission: a, CompletedAt: testNow}
			store.facts["late-pending"] = pending
			store.mu.Unlock()
		}
	}
	if err := e.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, available := e.CachedSharedAccount(2, 3); available {
		t.Fatal("unconfirmed first snapshot was published")
	}
}

func TestIncrementalConfirmationDoesNotResurrectRemovedIdentity(t *testing.T) {
	store := &incrementalMemoryStore{memoryStore: newMemoryStore()}
	e := NewEngine(store)
	s := DefaultSettings()
	s.Mode = Protect
	e.ApplySettings(s)
	e.now = func() time.Time { return testNow }
	r := request("seed", "session", testNow.Add(-time.Minute))
	a := admit(t, e, r)
	complete(t, e, r, a, 90, true, "known")
	if err := e.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	calls := 0
	store.directoryHook = func() {
		calls++
		if calls == 2 {
			store.mu.Lock()
			delete(store.accounts, accountIdentity(2, 3))
			store.mu.Unlock()
		}
	}
	if err := e.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(e.CachedSnapshot(1).Accounts) != 0 {
		t.Fatal("removed directory identity retained its old snapshot")
	}
}

func BenchmarkCacheMetadataAndCandidateRead(b *testing.B) {
	for _, count := range []int{100, 1000, 10000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			e := NewEngine(nil)
			s := DefaultSettings()
			s.Mode = Protect
			e.ApplySettings(s)
			at := time.Now()
			e.now = func() time.Time { return at }
			e.loadedAt = at
			for i := 1; i <= count; i++ {
				e.accounts[accountIdentity(int64(i), 1)] = AccountSnapshot{AccountState: AccountState{AccountID: int64(i), UpstreamKeyID: 1, Phase: Healthy}, LoadedAt: at}
			}
			candidates := []Candidate{{AccountID: 1, UpstreamKeyID: 1}, {AccountID: 2, UpstreamKeyID: 1}}
			b.Run("baseline-full", func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					e.CachedSnapshot(1)
				}
			})
			b.Run("metadata", func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					e.CachedMetadata(1)
				}
			})
			b.Run("two-candidates", func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					e.CachedCandidates(1, candidates)
				}
			})
		})
	}
}

func TestReconciliationPagesDoNotDelayNormalChangedIdentity(t *testing.T) {
	store := &incrementalMemoryStore{memoryStore: newMemoryStore()}
	e := NewEngine(store)
	settings := DefaultSettings()
	settings.Mode = Protect
	e.ApplySettings(settings)
	at := testNow
	e.now = func() time.Time { return at }
	e.loadedAt = at
	e.lastReconciled = at.Add(-5 * time.Minute)
	e.refreshPolicyEpoch = e.policyEpoch.Load()
	for i := 1; i <= 1200; i++ {
		id := int64(i)
		state := AccountState{AccountID: id, UpstreamKeyID: 1, Phase: Healthy}
		e.preparePolicies(&state)
		raw, _ := json.Marshal(state)
		identity := accountIdentity(id, 1)
		store.accounts[identity] = AccountRuntimeRow{AccountID: id, UpstreamKeyID: 1, Version: 1, Payload: raw}
		e.accounts[identity] = AccountSnapshot{AccountState: state, Version: 1, LoadedAt: at}
		e.refreshVersions[identity] = IdentityVersion{AccountID: id, UpstreamKeyID: 1, Version: 1}
	}
	changed := store.accounts[accountIdentity(1100, 1)]
	changed.Version = 2
	store.accounts[accountIdentity(1100, 1)] = changed
	if err := e.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.windowIDs) != 501 || e.reconcileCursor != 500 {
		t.Fatalf("first page size=%d cursor=%d", len(store.windowIDs), e.reconcileCursor)
	}
	found := false
	for _, id := range store.windowIDs {
		found = found || id == 1100
	}
	if !found {
		t.Fatal("changed identity waited behind reconciliation")
	}
	at = at.Add(time.Second)
	if err := e.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.windowIDs) != 500 || e.reconcileCursor != 1000 {
		t.Fatalf("second page size=%d cursor=%d", len(store.windowIDs), e.reconcileCursor)
	}
	at = at.Add(time.Second)
	if err := e.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.windowIDs) != 200 || e.reconcileCursor != 0 || !e.lastReconciled.Equal(at) {
		t.Fatalf("last page size=%d cursor=%d reconciled=%v", len(store.windowIDs), e.reconcileCursor, e.lastReconciled)
	}
}

func TestPolicyVersionAdvancesOnlyForChangedContent(t *testing.T) {
	e := NewEngine(nil)
	initial := e.PolicyVersion()
	settings := e.Settings()
	if err := e.ApplySettings(settings); err != nil {
		t.Fatal(err)
	}
	if e.PolicyVersion() != initial {
		t.Fatal("equal global settings advanced policy version")
	}
	settings.Mode = Protect
	if err := e.ApplySettings(settings); err != nil {
		t.Fatal(err)
	}
	changed := e.PolicyVersion()
	if changed <= initial {
		t.Fatal("changed global settings did not advance version")
	}
	values := map[int64]Settings{1: settings}
	if err := e.ApplyPolicySettings(values); err != nil {
		t.Fatal(err)
	}
	published := e.PolicyVersion()
	if published <= changed {
		t.Fatal("new group projection did not advance version")
	}
	if err := e.ApplyPolicySettings(values); err != nil {
		t.Fatal(err)
	}
	if e.PolicyVersion() != published {
		t.Fatal("equal group projections advanced version")
	}
	settings.ThresholdPercent--
	values[1] = settings
	if err := e.ApplyPolicySettings(values); err != nil {
		t.Fatal(err)
	}
	if e.PolicyVersion() <= published {
		t.Fatal("changed projection did not advance version")
	}
}
