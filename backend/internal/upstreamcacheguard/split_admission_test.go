package upstreamcacheguard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// This store implements the production split contract. The old memoryStore is
// deliberately left untouched: it remains an independent legacy-state oracle.
// Every transaction clones both domains and publishes neither after a failure.
type splitMemoryStore struct {
	*memoryStore
	admissions                  map[string]Admission
	countOverride               *int
	failWrite                   bool
	reads, writes, pendingReads int
}

type splitMemoryTx struct {
	*memoryTx
	owner      *splitMemoryStore
	admissions map[string]Admission
}

func newSplitMemoryStore() *splitMemoryStore {
	return &splitMemoryStore{memoryStore: newMemoryStore(), admissions: map[string]Admission{}}
}

func (s *splitMemoryStore) Transact(ctx context.Context, group int64, fn func(Tx) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	copy := newMemoryStore()
	for k, v := range s.rows {
		copy.rows[k] = v
	}
	for k, v := range s.accounts {
		copy.accounts[k] = v
	}
	for k, v := range s.facts {
		copy.facts[k] = v
	}
	for k, v := range s.processed {
		copy.processed[k] = v
	}
	admissions := make(map[string]Admission, len(s.admissions))
	for k, v := range s.admissions {
		admissions[k] = v
	}
	if err := fn(&splitMemoryTx{memoryTx: &memoryTx{s: copy, group: group}, owner: s, admissions: admissions}); err != nil {
		return err
	}
	s.rows, s.accounts, s.facts, s.processed = copy.rows, copy.accounts, copy.facts, copy.processed
	s.admissions = admissions
	return nil
}

func (s *splitMemoryStore) TransactAccount(ctx context.Context, group, _, _ int64, fn func(Tx) error) error {
	return s.Transact(ctx, group, fn)
}

func (t *splitMemoryTx) Write(kind, key string, value any) (int64, error) {
	if kind == SessionKind {
		raw, err := json.Marshal(value)
		if err != nil {
			return 0, err
		}
		var session SessionState
		if err := json.Unmarshal(raw, &session); err != nil {
			return 0, err
		}
		if len(session.Admissions) != 0 {
			return 0, errors.New("split session leaked admission history")
		}
	}
	return t.memoryTx.Write(kind, key, value)
}

func (t *splitMemoryTx) ReadAdmission(session, keyID string) (Admission, bool, error) {
	t.owner.reads++
	a, ok := t.admissions[key(t.group, session, keyID)]
	return a, ok, nil
}

func (t *splitMemoryTx) WriteAdmission(session, keyID string, a Admission) error {
	t.owner.writes++
	if t.owner.failWrite {
		return errors.New("injected admission write failure")
	}
	storageKey := key(t.group, session, keyID)
	if old, exists := t.admissions[storageKey]; exists {
		if a.Completed && !old.Completed {
			old.Completed = true
			t.admissions[storageKey] = old
		}
	} else {
		t.admissions[storageKey] = a
	}
	return nil
}

func (t *splitMemoryTx) PendingAdmissions(session string, account int64, route string) ([]Admission, error) {
	t.owner.pendingReads++
	var out []Admission
	for _, a := range t.admissions {
		if a.GroupID == t.group && a.SessionKey == session && !a.Completed && a.AccountID == account && (a.RouteKey == route || a.RouteKey == "") {
			out = append(out, a)
		}
	}
	return out, nil
}

func (t *splitMemoryTx) RetainedAdmissionCount(session string, since time.Time) (int, error) {
	if t.owner.countOverride != nil {
		return *t.owner.countOverride, nil
	}
	n := 0
	for _, a := range t.admissions {
		if a.GroupID == t.group && a.SessionKey == session && (!a.Completed || !a.StartedAt.Before(since)) {
			n++
		}
	}
	return n, nil
}

func (s *splitMemoryStore) AdmissionHealth(ctx context.Context, cutoff time.Time, failures []AdmissionFailure) (AdmissionHealth, error) {
	health, err := s.memoryStore.AdmissionHealth(ctx, cutoff, nil)
	if err != nil {
		return health, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return AdmissionHealth{}, err
	}
	for storageKey, a := range s.admissions {
		if a.Completed {
			for _, f := range failures {
				if f.AccountID == a.AccountID && f.UpstreamKeyID == a.UpstreamKeyID && strings.HasSuffix(storageKey, ":"+f.Key) {
					health.Acknowledged = append(health.Acknowledged, f)
				}
			}
		}
	}
	return health, nil
}

func splitTestEngine(t *testing.T) (*Engine, *splitMemoryStore) {
	t.Helper()
	s := newSplitMemoryStore()
	e := NewEngine(s)
	e.now = func() time.Time { return testNow }
	e.bornAt = testNow.Add(-time.Hour)
	settings := DefaultSettings()
	settings.Mode = Protect
	if err := e.ApplySettings(settings); err != nil {
		t.Fatal(err)
	}
	return e, s
}

func readSplitSession(t *testing.T, s *splitMemoryStore, r Request) SessionState {
	t.Helper()
	var session SessionState
	if err := s.Transact(context.Background(), r.GroupID, func(tx Tx) error { _, err := tx.Read(SessionKind, SessionKey(r), &session); return err }); err != nil {
		t.Fatal(err)
	}
	if len(session.Admissions) != 0 {
		t.Fatal("admissions remain in session body")
	}
	return session
}

func TestSplitAdmissionColdFreezeAndDuplicateCompletion(t *testing.T) {
	e, s := splitTestEngine(t)
	first := request("first", "parallel", testNow.Add(-time.Minute))
	second := request("second", "parallel", first.StartedAt.Add(time.Second))
	a, b := admit(t, e, first), admit(t, e, second)
	if a.WarmAtStart || b.WarmAtStart {
		t.Fatal("parallel initial requests must both freeze cold")
	}
	// Finish the newer call first; neither completion may retroactively warm b.
	complete(t, e, second, b, 90, true, "known")
	v := complete(t, e, first, a, 90, true, "known")
	if err := e.Complete(context.Background(), v); err != nil {
		t.Fatal(err)
	}
	b.Completed = true
	if got := admit(t, e, second); !reflect.DeepEqual(got, b) {
		t.Fatalf("retry changed frozen classification: %+v", got)
	}
	third := request("third", "parallel", testNow.Add(-30*time.Second))
	c := admit(t, e, third)
	if !c.WarmAtStart {
		t.Fatal("successful completed route did not warm next admission")
	}
	readSplitSession(t, s, third)
	if len(s.facts) != 2 || len(s.admissions) != 3 || s.reads == 0 || s.writes == 0 || s.pendingReads == 0 {
		t.Fatalf("split path was not used or idempotency failed: facts=%d admissions=%d reads/writes/pending=%d/%d/%d", len(s.facts), len(s.admissions), s.reads, s.writes, s.pendingReads)
	}
}

func TestSplitAdmissionUnfinishedAttemptRemainsLowStreakBarrier(t *testing.T) {
	e, s := splitTestEngine(t)
	seedCooling(t, e)
	base := testNow.Add(-10 * time.Minute)
	seed := request("barrier-seed", "barrier", base)
	complete(t, e, seed, admit(t, e, seed), 90, true, "known")
	lowOne := request("barrier-low-one", "barrier", base.Add(time.Minute))
	complete(t, e, lowOne, admit(t, e, lowOne), 20, true, "known")
	pending := request("barrier-pending", "barrier", base.Add(2*time.Minute))
	pendingAdmission := admit(t, e, pending)
	lowTwo := request("barrier-low-two", "barrier", base.Add(3*time.Minute))
	complete(t, e, lowTwo, admit(t, e, lowTwo), 20, true, "known")
	if err := e.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	next := request("barrier-next", "barrier", base.Add(4*time.Minute))
	if read := e.CachedSessionRead(next); read.PendingLow {
		t.Fatal("unfinished split admission failed to interrupt low streak")
	}
	if account, available := e.CachedAccount(pending.AccountID, pending.UpstreamKeyID); !available || !account.Fresh {
		t.Fatalf("unfinished ordering barrier revoked account snapshot: %+v available=%v", account, available)
	}
	complete(t, e, pending, pendingAdmission, 0, false, "absent")
	if err := e.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if read := e.CachedSessionRead(next); read.PendingLow {
		t.Fatal("late failure erased the intervening-attempt barrier")
	}
	if s.pendingReads < 4 {
		t.Fatal("completion bypassed split pending index")
	}
}

func TestSplitAdmissionTransactionFailureRollsBackAllDomains(t *testing.T) {
	e, s := splitTestEngine(t)
	r := request("rollback", "rollback", testNow)
	s.failWrite = true
	if _, err := e.Admit(context.Background(), r); err == nil {
		t.Fatal("expected admission persistence failure")
	}
	if len(s.rows) != 0 || len(s.accounts) != 0 || len(s.admissions) != 0 {
		t.Fatal("failed admission committed quota, identity or account")
	}
	s.failWrite = false
	a := admit(t, e, r)
	before := append([]byte(nil), s.rows[key(r.GroupID, SessionKind, SessionKey(r))].Payload...)
	s.failWrite = true
	v := Evidence{ID: "rollback-completion", Request: r, Admission: a, Success: true, RawUsage: RawUsage{Status: "known", TotalInputTokens: 100}, CompletedAt: r.StartedAt.Add(time.Second)}
	if err := e.Complete(context.Background(), v); err == nil {
		t.Fatal("expected completion persistence failure")
	}
	if len(s.facts) != 0 || !bytes.Equal(before, s.rows[key(r.GroupID, SessionKind, SessionKey(r))].Payload) || s.admissions[key(r.GroupID, SessionKey(r), admissionKey(r))].Completed {
		t.Fatal("completion failure partially committed")
	}
}

func TestSplitAdmissionMatchesLegacyEventSequence(t *testing.T) {
	legacy, _ := testEngine(t)
	split, store := splitTestEngine(t)
	for _, engine := range []*Engine{legacy, split} {
		s := engine.Settings()
		s.MinValidRequests, s.CooldownMinutes, s.TrialPercent = 2, 1, 100
		if err := engine.ApplySettings(s); err != nil {
			t.Fatal(err)
		}
	}
	at := testNow.Add(-30 * time.Minute)
	for _, engine := range []*Engine{legacy, split} {
		engine.now = func() time.Time { return at }
	}
	for i := 0; i < 40; i++ {
		at = at.Add(20 * time.Second)
		r := request(fmt.Sprintf("equivalence-%d", i), fmt.Sprintf("session-%d", i/8), at)
		if i%11 == 0 {
			r.AccountID = 4
			r.UpstreamKeyID = 5
		}
		if i >= 30 {
			r.UpstreamKeyID += 10
		}
		a, b := admit(t, legacy, r), admit(t, split, r)
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("event %d admission differs: legacy=%+v split=%+v", i, a, b)
		}
		read := int64(90)
		if i%8 >= 3 {
			read = 0
		}
		at = at.Add(time.Second)
		complete(t, legacy, r, a, read, i%13 != 0, "known")
		complete(t, split, r, b, read, i%13 != 0, "known")
		for _, engine := range []*Engine{legacy, split} {
			if err := engine.Refresh(context.Background()); err != nil {
				t.Fatal(err)
			}
		}
		x, xok := legacy.CachedAccount(r.AccountID, r.UpstreamKeyID)
		y, yok := split.CachedAccount(r.AccountID, r.UpstreamKeyID)
		if xok != yok || !reflect.DeepEqual(x, y) {
			t.Fatalf("event %d account differs: legacy=%+v split=%+v", i, x, y)
		}
		if x, y := legacy.CachedSessionRead(r), split.CachedSessionRead(r); !reflect.DeepEqual(x, y) {
			t.Fatalf("event %d session preference differs: legacy=%+v split=%+v", i, x, y)
		}
		readSplitSession(t, store, r)
	}
}

func TestSplitAdmissionConcurrentSameSessionAcrossAccounts(t *testing.T) {
	e, s := splitTestEngine(t)
	const workers = 24
	var wg sync.WaitGroup
	errCh := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := request(fmt.Sprintf("cross-%d", i), "one-session", testNow.Add(-time.Minute))
			r.AccountID = int64(2 + i%2)
			r.UpstreamKeyID = int64(4 + i%2)
			a, err := e.Admit(context.Background(), r)
			if err != nil {
				errCh <- err
				return
			}
			if a.WarmAtStart {
				errCh <- errors.New("no-completion concurrent request unexpectedly warm")
				return
			}
			if duplicate, err := e.Admit(context.Background(), r); err != nil {
				errCh <- err
			} else if !reflect.DeepEqual(a, duplicate) {
				errCh <- errors.New("concurrent idempotent admission changed")
			}
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
	if len(s.admissions) != workers {
		t.Fatalf("admissions=%d want %d", len(s.admissions), workers)
	}
	r := request("cross-0", "one-session", testNow.Add(-time.Minute))
	readSplitSession(t, s, r)
}

func TestSplitAdmissionRetentionBoundIncludesUnfinishedAndExactBoundary(t *testing.T) {
	e, s := splitTestEngine(t)
	r := request("retention", "retention", testNow)
	seed := func(id string, at time.Time, completed bool) {
		a := Admission{RequestID: id, GroupID: r.GroupID, AccountID: r.AccountID, UpstreamKeyID: r.UpstreamKeyID, SessionKey: SessionKey(r), StartedAt: at, Completed: completed}
		s.admissions[key(r.GroupID, a.SessionKey, id)] = a
	}
	seed("older-completed", testNow.Add(-FactLifetime-time.Nanosecond), true)
	seed("exact-completed", testNow.Add(-FactLifetime), true)
	seed("older-pending", testNow.Add(-FactLifetime-time.Hour), false)
	if err := s.Transact(context.Background(), r.GroupID, func(tx Tx) error {
		n, err := tx.(AdmissionTx).RetainedAdmissionCount(SessionKey(r), testNow.Add(-FactLifetime))
		if err != nil {
			return err
		}
		if n != 2 {
			return fmt.Errorf("retained=%d want exact boundary and unfinished", n)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	bound := 100000
	s.countOverride = &bound
	if _, err := e.Admit(context.Background(), r); err == nil {
		t.Fatal("100000 retained admissions must reject atomically")
	}
	if len(s.rows) != 0 || len(s.accounts) != 0 || len(s.admissions) != 3 {
		t.Fatal("bound rejection partially committed")
	}
	bound--
	a := admit(t, e, r)
	if len(s.admissions) != 4 {
		t.Fatal("below-bound admission was not recorded")
	}
	bound++
	if got := admit(t, e, r); !reflect.DeepEqual(got, a) {
		t.Fatal("idempotent read was incorrectly rejected at retained-admission bound")
	}
}

func TestSplitAdmissionCompletionRebuildsMissingSession(t *testing.T) {
	e, s := splitTestEngine(t)
	r := request("restore", "restore", testNow.Add(-time.Minute))
	a := admit(t, e, r)
	delete(s.rows, key(r.GroupID, SessionKind, SessionKey(r)))
	complete(t, e, r, a, 90, true, "known")
	session := readSplitSession(t, s, r)
	if session.Key != a.SessionKey || !session.FirstSeenAt.Equal(r.StartedAt) {
		t.Fatal("missing session identity was not rebuilt from immutable admission")
	}
	if !s.admissions[key(r.GroupID, SessionKey(r), admissionKey(r))].Completed {
		t.Fatal("reconstruction lost completed classification")
	}
	next := request("restore-next", "restore", testNow.Add(-30*time.Second))
	if got := admit(t, e, next); got.NaturalNew || !got.WarmAtStart {
		t.Fatal("reconstructed session became naturally new or lost physical warmth")
	}
}

func TestSplitAdmissionOldUnfinishedSurvivesRestartWithoutGapAndCompletionAcknowledgesFailure(t *testing.T) {
	e, s := splitTestEngine(t)
	r := request("old-pending", "restart", testNow.Add(-72*time.Hour))
	a := admit(t, e, r)
	if err := e.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if account, available := e.CachedAccount(r.AccountID, r.UpstreamKeyID); !available || !account.Fresh || account.ValidRequests != 0 {
		t.Fatalf("three-day unfinished request blocked fresh completed-only snapshot: %+v available=%v", account, available)
	}
	// Unfinished classifications are neither completed evidence nor proof that
	// a completion was lost, even when another engine starts after admission.
	young := request("young-pending", "restart-young", testNow.Add(-time.Second))
	ya := admit(t, e, young)
	restarted := NewEngine(s)
	restarted.now = e.now
	restarted.bornAt = testNow
	if err := restarted.ApplySettings(e.Settings()); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if account, available := restarted.CachedAccount(young.AccountID, young.UpstreamKeyID); !available || !account.Fresh {
		t.Fatalf("restart treated an unfinished admission as a lost completion: %+v available=%v", account, available)
	}
	continued := request("after-restart", "restart", testNow)
	ca := admit(t, restarted, continued)
	if !ca.Sample || ca.WarmAtStart || ca.NaturalNew {
		t.Fatalf("restart did not preserve session identity and cold classification: %+v", ca)
	}
	complete(t, restarted, continued, ca, 90, true, "known")
	warm := request("warm-after-restart", "restart", testNow.Add(2*time.Second))
	if wa := admit(t, restarted, warm); !wa.Sample || !wa.WarmAtStart {
		t.Fatalf("new real success did not warm the preserved route: %+v", wa)
	}
	if old := s.admissions[key(r.GroupID, SessionKey(r), admissionKey(r))]; old.Completed || !reflect.DeepEqual(old, a) {
		t.Fatalf("new sampling rewrote old immutable admission: old=%+v frozen=%+v", old, a)
	}
	complete(t, e, r, a, 90, true, "known")
	complete(t, e, young, ya, 90, true, "known")
	e.MarkSamplingFailure(r, false)
	if err := e.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.mu.RLock()
	remaining := len(e.samplingFailures[accountIdentity(r.AccountID, r.UpstreamKeyID)])
	e.mu.RUnlock()
	if remaining != 0 {
		t.Fatal("durable split completion did not acknowledge transient failure")
	}
	if e.accountGaps[accountIdentity(r.AccountID, r.UpstreamKeyID)] {
		t.Fatal("completed split records left a false sampling gap")
	}
}

func TestSplitAdmissionQueuedCompletionSurvivesRestartAndRecoversAfterConsumption(t *testing.T) {
	e, store := splitTestEngine(t)
	seedSnapshotAccounts(t, e, store.memoryStore)
	seed := request("pre-exit-cold", "pre-exit-session", testNow.Add(-20*time.Second))
	complete(t, e, seed, admit(t, e, seed), 90, true, "known")
	r := request("pre-exit-warm", "pre-exit-session", testNow.Add(-10*time.Second))
	a := admit(t, e, r)
	if !a.WarmAtStart {
		t.Fatal("fixture did not freeze a warm admission")
	}
	v := Evidence{ID: "queued-before-process-exit", Request: r, Admission: a, CompletedAt: testNow.Add(-9 * time.Second), Success: true,
		RawUsage: RawUsage{Status: "known", TotalInputTokens: 100, CacheReadTokens: 40}}
	if err := store.Enqueue(context.Background(), v); err != nil {
		t.Fatal(err)
	}
	restarted := NewEngine(store)
	restarted.now = e.now
	restarted.bornAt = testNow
	if err := restarted.ApplySettings(e.Settings()); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, available := restarted.CachedAccount(r.AccountID, r.UpstreamKeyID); available {
		t.Fatal("restart published a snapshot missing an old durable completion")
	}
	if other, available := restarted.CachedAccount(9, 3); !available || !other.Fresh {
		t.Fatalf("queued completion affected another account: %+v available=%v", other, available)
	}
	claimed, err := store.ClaimPending(context.Background(), "replacement-worker", 32, time.Second)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("durable completion was not reclaimable: %+v err=%v", claimed, err)
	}
	if err := restarted.Complete(context.Background(), claimed[0]); err != nil {
		t.Fatal(err)
	}
	if _, available := restarted.CachedAccount(r.AccountID, r.UpstreamKeyID); available {
		t.Fatal("consumption without refresh revived an unverified snapshot")
	}
	if err := restarted.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Complete(context.Background(), v); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	account, available := restarted.CachedAccount(r.AccountID, r.UpstreamKeyID)
	if !available || !account.Fresh || account.ValidRequests != 1 || account.CachePercent != 40 || account.Phase != Cooling {
		t.Fatalf("consumed completion changed classification, duplicated a sample or fabricated recovery: %+v available=%v", account, available)
	}
	if !store.admissions[key(r.GroupID, SessionKey(r), admissionKey(r))].Completed {
		t.Fatal("consumed fact did not acknowledge its original admission")
	}
}

func TestSplitAdmissionUnfinishedAloneCannotRestoreProtectedAccount(t *testing.T) {
	e, store := splitTestEngine(t)
	r := request("unfinished-before-protection", "idle-protection", testNow.Add(-72*time.Hour))
	admission := admit(t, e, r)
	if err := store.TransactAccount(context.Background(), r.GroupID, r.AccountID, r.UpstreamKeyID, func(tx Tx) error {
		_, err := tx.(AccountTx).WriteAccount(r.AccountID, r.UpstreamKeyID, AccountState{AccountID: r.AccountID, UpstreamKeyID: r.UpstreamKeyID,
			Phase: Cooling, LastEvidenceAt: testNow, DegradedAt: testNow, CooldownUntil: testNow.Add(10 * time.Minute)})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		age   time.Duration
		phase Phase
	}{{time.Minute, Cooling}, {16 * time.Minute, Unknown}, {72 * time.Hour, Unknown}} {
		now := testNow.Add(tc.age)
		e.now = func() time.Time { return now }
		if err := e.Refresh(context.Background()); err != nil {
			t.Fatal(err)
		}
		account, available := e.CachedAccount(r.AccountID, r.UpstreamKeyID)
		if !available || !account.Fresh || account.Phase != tc.phase || account.ValidRequests != 0 || account.TrialValidRequests != 0 {
			t.Fatalf("time without completed evidence changed recovery: %+v available=%v", account, available)
		}
	}
	if got := store.admissions[key(r.GroupID, SessionKey(r), admissionKey(r))]; !reflect.DeepEqual(got, admission) {
		t.Fatalf("time passage changed the unfinished classification: got=%+v want=%+v", got, admission)
	}
}

func longSessionFixture() (SessionState, Admission) {
	r := request(strings.Repeat("r", 175), "long-history", testNow)
	session := SessionState{Key: SessionKey(r), FirstSeenAt: testNow.Add(-time.Hour), Routes: map[string]SessionRoute{}, Admissions: map[string]Admission{}}
	var selected Admission
	for i := 0; i < 1650; i++ {
		id := fmt.Sprintf("%04d-%s", i, r.ID)
		a := Admission{RequestID: id, SessionKey: session.Key, GroupID: 1, AccountID: 2, UpstreamKeyID: 3, StartedAt: testNow.Add(time.Duration(i-1650) * time.Second), Sample: true, Completed: true, RouteKey: strings.Repeat("a", 64), PolicySignature: strings.Repeat("b", 64), CohortID: strings.Repeat("c", 64), Mode: Protect}
		session.Admissions[id] = a
		selected = a
	}
	return session, selected
}

func TestSplitAdmissionLongSessionKeepsIdentitySmall(t *testing.T) {
	e, s := splitTestEngine(t)
	fixture, _ := longSessionFixture()
	r := request("after-history", "long-history", testNow)
	full, _ := json.Marshal(fixture)
	if len(full) < 1000000 {
		t.Fatalf("fixture must reproduce megabyte session; got %d bytes", len(full))
	}
	for k, a := range fixture.Admissions {
		s.admissions[key(r.GroupID, fixture.Key, k)] = a
	}
	fixture.Admissions = nil
	if err := s.Transact(context.Background(), r.GroupID, func(tx Tx) error { _, err := tx.Write(SessionKind, fixture.Key, &fixture); return err }); err != nil {
		t.Fatal(err)
	}
	a := admit(t, e, r)
	if a.NaturalNew || len(s.admissions) != 1651 {
		t.Fatal("long session identity or admissions lost")
	}
	row := s.rows[key(r.GroupID, SessionKind, fixture.Key)]
	if len(row.Payload) > 4096 {
		t.Fatalf("long admission history bloated session body to %d bytes", len(row.Payload))
	}
	if got := readSplitSession(t, s, r); !got.FirstSeenAt.Equal(fixture.FirstSeenAt) {
		t.Fatal("first session identity changed")
	}
	t.Logf("history=%d bytes, lightweight session=%d bytes; admission history=%d records", len(full), len(row.Payload), len(s.admissions))
}

// Compare only the serialization/read-modify-write operation removed from the
// SQL hot path. Neither side clones fake transaction maps or performs unrelated
// account recomputation. These numbers are not end-to-end database latency.
func BenchmarkLongSessionAdmissionCodec(b *testing.B) {
	full, selected := longSessionFixture()
	legacy, _ := json.Marshal(full)
	full.Admissions = nil
	body, _ := json.Marshal(full)
	admission, _ := json.Marshal(selected)
	b.Run("legacy-full-session-1650", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(legacy)))
		for i := 0; i < b.N; i++ {
			var s SessionState
			if err := json.Unmarshal(legacy, &s); err != nil {
				b.Fatal(err)
			}
			s.LastStartedAt = testNow
			if _, err := json.Marshal(s); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("split-session-and-one-admission", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(body) + len(admission)))
		for i := 0; i < b.N; i++ {
			var s SessionState
			var a Admission
			if err := json.Unmarshal(body, &s); err != nil {
				b.Fatal(err)
			}
			if err := json.Unmarshal(admission, &a); err != nil {
				b.Fatal(err)
			}
			s.LastStartedAt = testNow
			if _, err := json.Marshal(s); err != nil {
				b.Fatal(err)
			}
			if _, err := json.Marshal(a); err != nil {
				b.Fatal(err)
			}
		}
	})
}
