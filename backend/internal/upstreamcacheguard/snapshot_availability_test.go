package upstreamcacheguard

import (
	"context"
	"errors"
	"testing"
	"time"
)

type snapshotFailureStore struct {
	*memoryStore
	queryFailure   bool
	accountFailure bool
}

func (s *snapshotFailureStore) AccountWindows(ctx context.Context, since, until time.Time) (map[string]AccountWindow, error) {
	if s.queryFailure {
		return nil, errors.New("temporary query failure")
	}
	windows, err := s.memoryStore.AccountWindows(ctx, since, until)
	if s.accountFailure {
		w := windows["2:3"]
		w.Err = errors.New("account projection failed")
		windows["2:3"] = w
	}
	return windows, err
}

func seedSnapshotAccounts(t *testing.T, e *Engine, store *memoryStore) {
	t.Helper()
	for _, id := range []int64{2, 9} {
		if err := store.TransactAccount(context.Background(), 0, id, 3, func(tx Tx) error {
			_, err := tx.(AccountTx).WriteAccount(id, 3, AccountState{AccountID: id, UpstreamKeyID: 3, Phase: Cooling})
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotAvailabilityBoundariesAndGlobalRefreshFailure(t *testing.T) {
	e, memory := testEngine(t)
	store := &snapshotFailureStore{memoryStore: memory}
	e.store = store
	if _, available := e.CachedSharedAccount(2, 3); available {
		t.Fatal("never loaded snapshot is available")
	}
	seedSnapshotAccounts(t, e, memory)
	store.queryFailure = true
	for _, tc := range []struct {
		age              time.Duration
		fresh, available bool
	}{
		{5 * time.Second, true, true},
		{5*time.Second + time.Nanosecond, false, true},
		{30 * time.Second, false, true},
		{30*time.Second + time.Nanosecond, false, false},
	} {
		e.now = func() time.Time { return testNow.Add(tc.age) }
		if err := e.Refresh(context.Background()); err == nil {
			t.Fatal("test did not fail refresh")
		}
		for _, id := range []int64{2, 9} {
			a, available := e.CachedAccount(id, 3)
			if available != tc.available || a.Fresh != tc.fresh || !a.LoadedAt.Equal(testNow) || a.Phase != Cooling {
				t.Fatalf("age=%v account=%d snapshot=%+v available=%v", tc.age, id, a, available)
			}
		}
	}
}

func TestAccountRefreshFailureDoesNotRenewOrPoisonOtherAccounts(t *testing.T) {
	e, memory := testEngine(t)
	store := &snapshotFailureStore{memoryStore: memory}
	e.store = store
	seedSnapshotAccounts(t, e, memory)
	store.accountFailure = true
	for _, age := range []time.Duration{time.Second, 6 * time.Second, 30 * time.Second, 31 * time.Second} {
		e.now = func() time.Time { return testNow.Add(age) }
		if err := e.Refresh(context.Background()); err == nil {
			t.Fatal("account failure did not report degraded refresh health")
		}
		failed, available := e.CachedSharedAccount(2, 3)
		healthy, healthyAvailable := e.CachedSharedAccount(9, 3)
		if available != (age <= UsableSnapshotLifetime) || failed.Fresh || !failed.LoadedAt.Equal(testNow) {
			t.Fatalf("failed account renewed or lost last value: %+v usable=%v", failed, available)
		}
		if !healthyAvailable || !healthy.Fresh || !healthy.LoadedAt.Equal(testNow.Add(age)) {
			t.Fatalf("healthy account affected: %+v", healthy)
		}
	}
	store.accountFailure = false
	if err := e.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if a, available := e.CachedSharedAccount(2, 3); !available || !a.Fresh {
		t.Fatalf("recovered account unavailable: %+v", a)
	}
}

func TestAccountVersionRegressionCannotOverwriteLatestSnapshot(t *testing.T) {
	e, store := testEngine(t)
	seedSnapshotAccounts(t, e, store)
	before, _ := e.CachedSharedAccount(2, 3)
	store.mu.Lock()
	row := store.accounts["2:3"]
	row.Version = before.Version - 1
	store.accounts["2:3"] = row
	store.mu.Unlock()
	e.now = func() time.Time { return testNow.Add(time.Second) }
	if err := e.Refresh(context.Background()); err == nil {
		t.Fatal("version regression was accepted")
	}
	after, available := e.CachedSharedAccount(2, 3)
	if !available || after.Version != before.Version || !after.LoadedAt.Equal(before.LoadedAt) || after.Fresh {
		t.Fatalf("regressed version overwrote last success: before=%+v after=%+v", before, after)
	}
}

func TestAccountSamplingGapRetainsLastSuccessWithoutExtendingAge(t *testing.T) {
	e, store := testEngine(t)
	seedSnapshotAccounts(t, e, store)
	r := request("lost", "conversation", testNow.Add(-6*time.Minute))
	admit(t, e, r)
	for _, age := range []time.Duration{time.Second, 30 * time.Second, 31 * time.Second} {
		e.now = func() time.Time { return testNow.Add(age) }
		if err := e.Refresh(context.Background()); err != nil {
			t.Fatal(err)
		}
		a, available := e.CachedSharedAccount(2, 3)
		if available != (age <= UsableSnapshotLifetime) || a.Fresh || !a.LoadedAt.Equal(testNow) {
			t.Fatalf("gap did not retain bounded last success: %+v available=%v", a, available)
		}
		if other, available := e.CachedSharedAccount(9, 3); !available || !other.Fresh {
			t.Fatalf("gap affected another account: %+v", other)
		}
	}
}

func TestSamplingDeliveryFailureIsAccountLocalAndRecoveryRequiresRefresh(t *testing.T) {
	for _, lost := range []bool{false, true} {
		t.Run(map[bool]string{false: "retry", true: "discarded"}[lost], func(t *testing.T) {
			e, store := testEngine(t)
			seedSnapshotAccounts(t, e, store)
			r := request("failed-delivery", "conversation", testNow)
			e.MarkSamplingFailure(r, lost)
			if a, available := e.CachedSharedAccount(2, 3); !available || a.Fresh {
				t.Fatalf("failure not stale: %+v", a)
			}
			e.now = func() time.Time { return testNow.Add(31 * time.Second) }
			if err := e.Refresh(context.Background()); err != nil {
				t.Fatal(err)
			}
			if a, available := e.CachedSharedAccount(2, 3); available || !a.LoadedAt.Equal(testNow) {
				t.Fatalf("failed delivery renewed: %+v", a)
			}
			if a, available := e.CachedSharedAccount(9, 3); !available || !a.Fresh {
				t.Fatalf("foreign delivery affected account: %+v", a)
			}
			e.ClearSamplingFailure(r)
			if _, available := e.CachedSharedAccount(2, 3); available {
				t.Fatal("clear without refresh revived old snapshot")
			}
			if err := e.Refresh(context.Background()); err != nil {
				t.Fatal(err)
			}
			if a, available := e.CachedSharedAccount(2, 3); available == lost || a.Fresh == lost {
				t.Fatalf("lost=%v recovery state=%+v available=%v", lost, a, available)
			}
		})
	}
}

func TestSamplingFailureClearsAfterAnotherWorkerCompletesAdmission(t *testing.T) {
	e, store := testEngine(t)
	r := request("remote-completion", "conversation", testNow.Add(-time.Second))
	a := admit(t, e, r)
	if err := e.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.MarkSamplingFailure(r, false)
	remote := NewEngine(store)
	remote.now = e.now
	if err := remote.ApplySettings(e.Settings()); err != nil {
		t.Fatal(err)
	}
	complete(t, remote, r, a, 90, true, "known")
	e.now = func() time.Time { return testNow.Add(time.Second) }
	if err := e.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if a, available := e.CachedSharedAccount(2, 3); !available || !a.Fresh {
		t.Fatalf("remote acknowledgment did not release retry marker: %+v", a)
	}
}

func TestStaleAdmissionCannotAllocateNaturalTrialOrRecreateMissingSession(t *testing.T) {
	e, store := testEngine(t)
	r := request("first", "conversation", testNow.Add(-time.Second))
	a := admit(t, e, r)
	complete(t, e, r, a, 90, true, "known")
	if err := e.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.now = func() time.Time { return testNow.Add(6 * time.Second) }
	r.ID, r.StartedAt = "continued", testNow.Add(6*time.Second)
	continued, err := e.AdmitExisting(context.Background(), r)
	if err != nil || !continued.Sample || continued.NaturalNew || continued.Trial {
		t.Fatalf("existing-only admission allocated new trial: %+v %v", continued, err)
	}
	// Even if the caller has just read a known cached session, a changed
	// durable session must fail open rather than allocate a new natural slot.
	store.mu.Lock()
	delete(store.rows, key(r.GroupID, SessionKind, SessionKey(r)))
	store.mu.Unlock()
	r.ID = "after-delete"
	if _, err := e.AdmitExisting(context.Background(), r); err == nil {
		t.Fatal("missing session was recreated")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, ok := store.rows[key(r.GroupID, SessionKind, SessionKey(r))]; ok {
		t.Fatal("failed continuation wrote a session")
	}
}

func TestSamplingFailureAcknowledgmentDoesNotClearAnotherAttempt(t *testing.T) {
	e, store := testEngine(t)
	seedSnapshotAccounts(t, e, store)
	first := request("first-delivery", "conversation", testNow)
	second := first
	second.ID = "second-delivery"
	e.MarkSamplingFailure(first, false)
	e.MarkSamplingFailure(second, false)
	e.ClearSamplingFailure(first)
	e.now = func() time.Time { return testNow.Add(31 * time.Second) }
	if err := e.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, available := e.CachedSharedAccount(2, 3); available {
		t.Fatal("one acknowledgment cleared another failed attempt")
	}
	e.ClearSamplingFailure(second)
	if err := e.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if a, available := e.CachedSharedAccount(2, 3); !available || !a.Fresh {
		t.Fatalf("all delivered attempts did not recover: %+v", a)
	}
}
