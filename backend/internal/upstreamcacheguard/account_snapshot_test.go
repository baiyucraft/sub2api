package upstreamcacheguard

import (
	"context"
	"testing"
	"time"
)

func TestAccountSnapshotProjectsSameQualityWithoutDerivedGroupProtection(t *testing.T) {
	e, store := testEngine(t)
	state := AccountState{AccountID: 2, UpstreamKeyID: 3, Phase: Cooling, CooldownUntil: testNow.Add(time.Hour), GroupIDs: []int64{1, 2}}
	if err := store.TransactAccount(context.Background(), 0, 2, 3, func(tx Tx) error {
		_, err := tx.(AccountTx).WriteAccount(2, 3, state)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, g := range []int64{1, 2, 3} {
		view := e.CachedSnapshot(g)
		if !view.Available || len(view.Accounts) != 1 || view.Accounts[0].Phase != Cooling || view.Accounts[0].DerivedProtection != nil {
			t.Fatalf("group%d=%+v", g, view)
		}
	}
	account, ok := e.CachedAccount(2, 3)
	if !ok {
		t.Fatal("missing global snapshot")
	}
	account.GroupIDs[0] = 99
	if e.CachedAccountGroups(2, 3)[0] == 99 {
		t.Fatal("caller mutated shared group provenance")
	}
	e.now = func() time.Time { return testNow.Add(RefreshLifetime + time.Nanosecond) }
	if _, ok := e.CachedAccount(2, 3); !ok {
		t.Fatal("bounded stale account snapshot was not retained")
	}
	e.now = func() time.Time { return testNow.Add(UsableSnapshotLifetime + time.Nanosecond) }
	if _, ok := e.CachedAccount(2, 3); ok {
		t.Fatal("expired account snapshot remained available")
	}
}

func TestAccountSamplingGapDoesNotPoisonOtherAccountsOrNewKeys(t *testing.T) {
	e, store := testEngine(t)
	r := request("lost", "conversation", testNow.Add(-6*time.Minute))
	a := admit(t, e, r)
	if err := store.Enqueue(context.Background(), Evidence{ID: "unprocessed-completion", Request: r, Admission: a, CompletedAt: testNow.Add(-6 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	if err := e.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.CachedAccount(2, 3); ok {
		t.Fatal("unprocessed completion hidden")
	}
	if _, ok := e.CachedAccount(2, 4); !ok {
		t.Fatal("old key gap poisoned new key")
	}
	if _, ok := e.CachedAccount(9, 3); !ok {
		t.Fatal("foreign account gap affected quality")
	}
	if len(store.accounts) != 1 {
		t.Fatal("unexpected account identities")
	}
}

func TestAccountKeyRotationDropsOldSessionPreference(t *testing.T) {
	e, _ := testEngine(t)
	r := request("before-rotation", "conversation", testNow.Add(-time.Minute))
	a := admit(t, e, r)
	complete(t, e, r, a, 90, true, "known")
	if err := e.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	r.StartedAt = testNow
	if got := e.CachedSessionRead(r); !got.Known || got.PreferredAccountID != r.AccountID {
		t.Fatalf("current Key lost preference: %+v", got)
	}
	r.UpstreamKeyID++
	if got := e.CachedSessionRead(r); got.Known || got.PreferredAccountID != 0 {
		t.Fatalf("rotated Key inherited preference: %+v", got)
	}
}

func TestAccountKeyRotationRejectsMismatchedFrozenAdmission(t *testing.T) {
	e, _ := testEngine(t)
	r := request("frozen", "conversation", testNow.Add(-time.Minute))
	a := admit(t, e, r)
	r.UpstreamKeyID++
	if _, err := e.Admit(context.Background(), r); err == nil {
		t.Fatal("same attempt reused the previous Key admission")
	}
	if err := e.Complete(context.Background(), Evidence{ID: r.ID, Request: r, Admission: a, CompletedAt: r.StartedAt.Add(time.Second)}); err == nil {
		t.Fatal("completion attached frozen evidence to a different Key")
	}
}

func TestAccountLegacyAdmissionUsesFrozenRouteToIsolateKey(t *testing.T) {
	e, store := testEngine(t)
	r := request("legacy-admission", "conversation", testNow.Add(-time.Minute))
	a := admit(t, e, r)
	a.UpstreamKeyID = 0
	if err := store.Transact(context.Background(), r.GroupID, func(tx Tx) error {
		var session SessionState
		if _, err := tx.Read(SessionKind, a.SessionKey, &session); err != nil {
			return err
		}
		session.Admissions[admissionKey(r)] = a
		_, err := tx.Write(SessionKind, a.SessionKey, &session)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Admit(context.Background(), r); err != nil {
		t.Fatalf("matching legacy Key was not compatible: %v", err)
	}
	rotated := r
	rotated.UpstreamKeyID++
	if _, err := e.Admit(context.Background(), rotated); err == nil {
		t.Fatal("legacy admission crossed Key boundary")
	}
	complete(t, e, r, a, 90, true, "known")
}

func TestAccountExpiredWindowRefreshKeepsStableDisplayTimestamp(t *testing.T) {
	e, store := testEngine(t)
	state := AccountState{AccountID: 2, UpstreamKeyID: 3, Phase: Cooling,
		ValidRequests: 2, TotalInputTokens: 100, CacheReadTokens: 30, CachePercent: 30,
		UpdatedAt: testNow.Add(-time.Hour)}
	if err := store.TransactAccount(context.Background(), 0, 2, 3, func(tx Tx) error {
		_, err := tx.(AccountTx).WriteAccount(2, 3, state)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	first, _ := e.CachedAccount(2, 3)
	e.now = func() time.Time { return testNow.Add(time.Second) }
	if err := e.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	second, _ := e.CachedAccount(2, 3)
	if second.ValidRequests != 0 || second.Phase != Cooling || !second.UpdatedAt.Equal(first.UpdatedAt) {
		t.Fatalf("unchanged expired window invalidates ETag: first=%+v second=%+v", first, second)
	}
}
