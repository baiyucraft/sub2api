package upstreamcacheguard

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// Simulates slow legacy materialization while keeping directory and targeted
// window reads independent, as they are in the production repository.
type boundedRefreshStore struct {
	*incrementalMemoryStore
	delay          time.Duration
	blockedAccount int64
	factOnly       bool
	directoryReads int
	failConfirm    bool
	beforeConfirm  func()
	invalidAccount int64
}

func (s *boundedRefreshStore) TransactAccount(ctx context.Context, group, account, key int64, fn func(Tx) error) error {
	if account == s.blockedAccount {
		<-ctx.Done()
		return ctx.Err()
	}
	if s.delay > 0 {
		timer := time.NewTimer(s.delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
	return s.incrementalMemoryStore.TransactAccount(ctx, group, account, key, fn)
}

func (s *boundedRefreshStore) IdentityVersions(ctx context.Context) ([]IdentityVersion, error) {
	s.directoryReads++
	if s.directoryReads%2 == 0 {
		if s.failConfirm {
			return nil, errors.New("confirmation failed")
		}
		if s.beforeConfirm != nil {
			s.beforeConfirm()
		}
	}
	out, err := s.incrementalMemoryStore.IdentityVersions(ctx)
	if s.factOnly {
		out = append(out, IdentityVersion{AccountID: 2, UpstreamKeyID: 3, Version: 0, FactVersion: 1})
	}
	return out, err
}

func (s *boundedRefreshStore) AccountRowsFor(ctx context.Context, ids []int64) ([]AccountRuntimeRow, error) {
	rows, err := s.incrementalMemoryStore.AccountRowsFor(ctx, ids)
	if err != nil {
		return rows, err
	}
	if s.invalidAccount == 0 {
		return rows, nil
	}
	for i := range rows {
		if rows[i].AccountID == s.invalidAccount {
			rows[i].Payload = []byte("invalid JSON")
		}
	}
	return rows, nil
}

func legacyRefreshEngine(t *testing.T, count int) (*Engine, *boundedRefreshStore) {
	t.Helper()
	store := &boundedRefreshStore{incrementalMemoryStore: &incrementalMemoryStore{memoryStore: newMemoryStore()}}
	for id := int64(1); id <= int64(count); id++ {
		payload, err := json.Marshal(AccountState{AccountID: id, UpstreamKeyID: 3, Phase: Healthy})
		if err != nil {
			t.Fatal(err)
		}
		store.accounts[accountIdentity(id, 3)] = AccountRuntimeRow{AccountID: id, UpstreamKeyID: 3, Version: 1, Payload: payload}
	}
	e := NewEngine(store)
	e.now = func() time.Time { return testNow }
	s := DefaultSettings()
	s.Mode = Protect
	if err := e.ApplySettings(s); err != nil {
		t.Fatal(err)
	}
	return e, store
}

func TestBoundedRefreshPublishesSuccessfulLegacySubsetAndContinues(t *testing.T) {
	e, store := legacyRefreshEngine(t, 1000)
	store.delay = 3 * time.Millisecond
	refresh := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		// Partial materialization can report degraded health while publishing
		// the independently confirmed successful identities.
		_ = e.Refresh(ctx)
	}
	refresh()
	first := len(e.accounts)
	if first == 0 || first >= 256 {
		t.Fatalf("expected bounded partial progress, got %d identities", first)
	}
	if e.reconcileCursor > first || !e.lastReconciled.IsZero() {
		t.Fatalf("deferred consistency page marked completed: cursor=%d successful=%d at=%s", e.reconcileCursor, first, e.lastReconciled)
	}
	for id, a := range e.accounts {
		if a.LoadedAt != testNow || e.refreshVersions[id].Version == 0 || e.accountPolicyEpoch[id] != e.PolicyVersion() {
			t.Fatalf("published identity not confirmed: %s %+v", id, a)
		}
	}
	if a, ok := e.CachedSharedAccount(1000, 3); ok || !a.LoadedAt.IsZero() || e.refreshVersions["1000:3"].Version != 0 {
		t.Fatalf("deferred identity borrowed global success: %+v", a)
	}
	refresh()
	if len(e.accounts) <= first {
		t.Fatalf("next refresh did not progress: %d -> %d", first, len(e.accounts))
	}
	// Near the tail, select the remaining tail before wrapping to lower IDs.
	e.refreshCursor = 998
	refresh()
	for _, id := range []int64{999, 1000} {
		if a, ok := e.CachedSharedAccount(id, 3); !ok || !a.Fresh {
			t.Fatalf("tail identity starved: %d %+v", id, a)
		}
	}
}

func TestBoundedRefreshConfirmationFailureNeverPublishesSubset(t *testing.T) {
	e, store := legacyRefreshEngine(t, 300)
	store.failConfirm = true
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := e.Refresh(ctx); err == nil {
		t.Fatal("confirmation failure ignored")
	}
	if len(e.accounts) != 0 || len(e.refreshVersions) != 0 || e.refreshCursor != 0 || !e.loadedAt.IsZero() {
		t.Fatal("unconfirmed result advanced publication state")
	}
}

func TestBoundedRefreshRotatesPastSinglePermanentlySlowAccount(t *testing.T) {
	e, store := legacyRefreshEngine(t, 2)
	store.blockedAccount = 1
	for round := 0; round < 2; round++ {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		_ = e.Refresh(ctx)
		cancel()
	}
	if a, ok := e.CachedSharedAccount(1, 3); ok || !a.LoadedAt.IsZero() {
		t.Fatalf("blocked account gained a snapshot: %+v", a)
	}
	if a, ok := e.CachedSharedAccount(2, 3); !ok || !a.Fresh {
		t.Fatalf("healthy account starved behind blocked account: %+v", a)
	}
}

func TestBoundedRefreshFactOnlyIdentityWithoutAccountRowStaysUnknown(t *testing.T) {
	e, store := legacyRefreshEngine(t, 2)
	delete(store.accounts, "2:3")
	store.factOnly = true
	if err := e.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if a, ok := e.CachedSharedAccount(2, 3); ok || !a.LoadedAt.IsZero() {
		t.Fatalf("fact-only identity borrowed shared LoadedAt: %+v", a)
	}
	if a, ok := e.CachedSharedAccount(1, 3); !ok || !a.Fresh {
		t.Fatalf("independent row was affected: %+v", a)
	}
}

func TestBoundedRefreshRejectsConcurrentFactChangeAndDeletedKey(t *testing.T) {
	e, store := legacyRefreshEngine(t, 3)
	store.beforeConfirm = func() {
		store.mu.Lock()
		defer store.mu.Unlock()
		store.facts["late"] = Evidence{Request: Request{AccountID: 1, UpstreamKeyID: 3}}
		delete(store.accounts, "2:3")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := e.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if a, ok := e.CachedSharedAccount(1, 3); ok || !a.LoadedAt.IsZero() {
		t.Fatalf("changed fact version was published: %+v", a)
	}
	if _, exists := e.accounts["2:3"]; exists || e.refreshVersions["2:3"].Version != 0 {
		t.Fatal("deleted key was published")
	}
	if a, ok := e.CachedSharedAccount(3, 3); !ok || !a.Fresh {
		t.Fatalf("independent successful identity was discarded: %+v", a)
	}
}

func TestExplicitRebuildWithoutDeadlineStillMaterializesEveryIdentity(t *testing.T) {
	e, _ := legacyRefreshEngine(t, 300)
	if err := e.Rebuild(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(e.accounts) != 300 {
		t.Fatalf("explicit rebuild truncated to %d", len(e.accounts))
	}
	for id := int64(1); id <= 300; id++ {
		if a, ok := e.CachedSharedAccount(id, 3); !ok || !a.Fresh {
			t.Fatalf("rebuild omitted %d: %+v", id, a)
		}
	}
}

func TestPolicyChangesKeepCompatibleGroupAndRawSnapshotAvailable(t *testing.T) {
	e, _ := legacyRefreshEngine(t, 1)
	a := e.Settings()
	b := a
	if err := e.ApplyPolicySettings(map[int64]Settings{1: a, 2: b}); err != nil {
		t.Fatal(err)
	}
	if err := e.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.now = func() time.Time { return testNow.Add(time.Second) }
	b.ThresholdPercent++
	if err := e.ApplyPolicySettings(map[int64]Settings{1: a, 2: b}); err != nil {
		t.Fatal(err)
	}
	raw, available := e.CachedSharedAccount(1, 3)
	if !available || raw.Fresh || raw.LoadedAt != testNow {
		t.Fatalf("unrelated policy change withdrew/renewed raw snapshot: %+v", raw)
	}
	if projection, available := e.CachedAccountForGroup(1, 1, 3); !available || projection.Fresh {
		t.Fatalf("compatible group lost its usable projection: %+v", projection)
	}
	if projection, available := e.CachedAccountForGroupAt(2, 1, 3, testNow.Add(time.Second)); available || projection.Fresh {
		t.Fatalf("new strategy borrowed an old healthy projection: %+v", projection)
	}
	if got := e.CachedCandidates(2, []Candidate{{AccountID: 1, UpstreamKeyID: 3}}); len(got.Accounts) != 0 {
		t.Fatalf("unready projection entered candidate set: %+v", got)
	}
	if len(e.CachedSnapshot(2).Accounts) != 0 {
		t.Fatal("unready projection entered full snapshot")
	}
	if err := e.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, group := range []int64{1, 2} {
		if projection, available := e.CachedAccountForGroup(group, 1, 3); !available || !projection.Fresh || projection.PolicySignature != PolicySignature(e.ResolvedSettings(group)) {
			t.Fatalf("rebuilt group %d missing exact projection: %+v", group, projection)
		}
	}
}

func TestPriorityOnlyPolicyChangeUsesLightweightConfirmation(t *testing.T) {
	e, store := legacyRefreshEngine(t, 1)
	if err := e.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	reads := store.factReads
	s := e.Settings()
	s.Priority = "ttft_first"
	if err := e.ApplySettings(s); err != nil {
		t.Fatal(err)
	}
	a, ok := e.CachedAccountForGroup(1, 1, 3)
	if !ok || a.Fresh {
		t.Fatalf("compatible policy should retain value pending confirmation: %+v", a)
	}
	e.now = func() time.Time { return testNow.Add(time.Second) }
	if err := e.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if a, ok := e.CachedAccountForGroup(1, 1, 3); !ok || !a.Fresh || a.LoadedAt != testNow.Add(time.Second) {
		t.Fatalf("lightweight policy confirmation failed: %+v", a)
	}
	if store.factReads != reads {
		t.Fatal("priority-only change rescanned facts")
	}
	for _, mutate := range []func(*Settings){
		func(s *Settings) { s.WindowMinutes++ },
		func(s *Settings) { s.CooldownMinutes++ },
		func(s *Settings) { s.TrialPercent++ },
	} {
		changed := s
		mutate(&changed)
		if err := e.ApplySettings(changed); err != nil {
			t.Fatal(err)
		}
		if a, ok := e.CachedAccountForGroup(1, 1, 3); ok || a.Fresh {
			t.Fatalf("same signature with incompatible window reused projection: %+v", a)
		}
	}
}

func TestBoundedReconcileDeferredUnchangedAccountsRemainFresh(t *testing.T) {
	e, store := legacyRefreshEngine(t, 600)
	if err := e.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	now := testNow.Add(time.Second)
	e.now = func() time.Time { return now }
	e.lastReconciled = testNow.Add(-5 * time.Minute)
	e.reconcileCursor, e.refreshCursor = 0, 0
	for round := 0; round < 3; round++ {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		err := e.Refresh(ctx)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		for id := int64(1); id <= 600; id++ {
			a, ok := e.CachedSharedAccount(id, 3)
			if !ok || !a.Fresh || a.LoadedAt != now || e.accountGaps[accountIdentity(id, 3)] {
				t.Fatalf("round %d unchanged account %d lost its confirmed snapshot: %+v", round, id, a)
			}
		}
		if round == 0 && (e.reconcileCursor != 256 || e.lastReconciled == now || len(store.windowIDs) != 256) {
			t.Fatalf("deferred reconcile advanced past precise work: cursor=%d windows=%d at=%s", e.reconcileCursor, len(store.windowIDs), e.lastReconciled)
		}
	}
	if e.reconcileCursor != 0 || e.lastReconciled != now {
		t.Fatalf("consistency work failed to catch up: cursor=%d at=%s", e.reconcileCursor, e.lastReconciled)
	}
}

func TestBoundedReconcileDoesNotRenewChangedOrFailedAccounts(t *testing.T) {
	for _, scenario := range []string{"state", "fact", "boundary", "gap", "sampling", "policy", "rollback", "decode", "confirm", "late_fact", "deleted_key"} {
		t.Run(scenario, func(t *testing.T) {
			e, store := legacyRefreshEngine(t, 600)
			if err := e.Refresh(context.Background()); err != nil {
				t.Fatal(err)
			}
			now := testNow.Add(time.Second)
			e.now = func() time.Time { return now }
			e.lastReconciled = testNow.Add(-5 * time.Minute)
			e.reconcileCursor, e.refreshCursor = 0, 0
			target := int64(600) // Outside the first bounded consistency batch.
			id := accountIdentity(target, 3)
			switch scenario {
			case "state":
				row := store.accounts[id]
				row.Version++
				store.accounts[id] = row
			case "fact":
				store.facts["new"] = Evidence{Request: Request{AccountID: target, UpstreamKeyID: 3}}
			case "boundary":
				e.nextBoundaries[id] = now
			case "gap":
				e.accountGaps[id] = true
			case "sampling":
				e.samplingFailures[id] = map[string]struct{}{"failed": {}}
			case "policy":
				s := e.Settings()
				s.ThresholdPercent++
				if err := e.ApplySettings(s); err != nil {
					t.Fatal(err)
				}
			case "rollback":
				now = testNow.Add(-time.Second)
			case "decode":
				target, id = 1, "1:3" // An actual attempted precise read fails.
				store.invalidAccount = target
			case "confirm":
				store.failConfirm = true
			case "late_fact":
				store.beforeConfirm = func() {
					store.facts["late"] = Evidence{Request: Request{AccountID: target, UpstreamKeyID: 3}}
				}
			case "deleted_key":
				store.beforeConfirm = func() { delete(store.accounts, id) }
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			err := e.Refresh(ctx)
			cancel()
			if (scenario == "decode" || scenario == "confirm") && err == nil {
				t.Fatal("actual read failure was ignored")
			}
			if scenario == "deleted_key" {
				if _, exists := e.accounts[id]; exists {
					t.Fatal("deleted identity survived final directory confirmation")
				}
				return
			}
			a, _ := e.CachedSharedAccount(target, 3)
			if a.LoadedAt != testNow {
				t.Fatalf("unverified account renewed by consistency deferral: %+v", a)
			}
			if scenario != "confirm" && a.Fresh {
				t.Fatalf("changed or failed account reported fresh: %+v", a)
			}
			if scenario != "policy" && scenario != "rollback" && scenario != "confirm" {
				if other, ok := e.CachedSharedAccount(2, 3); !ok || !other.Fresh || other.LoadedAt != now {
					t.Fatalf("independent unchanged account lost its confirmation: %+v", other)
				}
			}
		})
	}
}
