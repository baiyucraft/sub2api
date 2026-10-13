package upstreamcacheguard

import (
	"context"
	"testing"
	"time"
)

func TestCachedSnapshotProjectsLinkedProtectionWithoutCopyingNativeEvidence(t *testing.T) {
	e, _ := testEngine(t)
	source := cascadeTestSnapshot(testNow, 2, Cooling)
	source.Accounts[0].AccountID, source.Accounts[0].UpstreamKeyID = 2, 3
	target := cascadeTestSnapshot(testNow, 1, Healthy)
	target.Accounts[0].AccountID, target.Accounts[0].UpstreamKeyID = 2, 3
	target.Accounts[0].CachePercent, target.Accounts[0].ValidRequests = 95, 42
	e.groups = map[int64]Snapshot{1: target, 2: source}
	e.loadedAt = testNow
	view := e.CachedSnapshot(1)
	if len(view.Accounts) != 1 || view.Accounts[0].DerivedProtection == nil || !view.Accounts[0].DerivedProtection.Protected {
		t.Fatalf("missing foreign source in target view: %+v", view)
	}
	account := view.Accounts[0]
	if account.Phase != Healthy || account.CachePercent != 95 || account.ValidRequests != 42 || account.DerivedProtection.PrimarySource.GroupID != 2 {
		t.Fatalf("derived protection changed native target evidence: %+v", account)
	}
	if e.groups[1].Accounts[0].DerivedProtection != nil || e.groups[2].Accounts[0].DerivedProtection != nil {
		t.Fatal("view derivation mutated shared native snapshots")
	}
	empty := e.CachedSnapshot(3)
	if len(empty.Accounts) != 1 || empty.Accounts[0].Phase != Healthy || empty.Accounts[0].ValidRequests != 0 || empty.Accounts[0].CachePercent != 0 || empty.Accounts[0].DerivedProtection == nil {
		t.Fatalf("new group must keep its own evidence unknown: %+v", empty)
	}
	// A source's disappearance removes only the derived view, with no write to
	// the target's native phase or statistics.
	source.Accounts[0].Phase = Healthy
	e.groups[2] = source
	if next := e.CachedSnapshot(1); next.Accounts[0].DerivedProtection != nil || next.Accounts[0].CachePercent != 95 {
		t.Fatalf("source recovery did not clear only its derived cause: %+v", next)
	}
}

func TestLinkedProtectionFreezesAdmissionButDoesNotBorrowRecoveryCohort(t *testing.T) {
	e, _ := testEngine(t)
	source := cascadeTestSnapshot(testNow, 2, Trial)
	source.Accounts[0].AccountID, source.Accounts[0].UpstreamKeyID = 2, 3
	source.Accounts[0].CohortID = "source-cycle"
	source.Accounts[0].FirstTrialAt = testNow.Add(-time.Minute)
	e.groups = map[int64]Snapshot{2: source}
	e.loadedAt = testNow
	r := request("linked-target", "linked-session", testNow)
	a, err := e.Admit(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if !a.ProtectedAtStart || a.Trial || a.CohortID != "" || a.WarmAtStart {
		t.Fatalf("foreign reason must protect admission without owning source trial: %+v", a)
	}
	r = request("new-key", "other-session", testNow.Add(time.Second))
	r.UpstreamKeyID = 4
	a, err = e.Admit(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if a.ProtectedAtStart {
		t.Fatal("old upstream key source protected a different current key")
	}
	e.now = func() time.Time { return testNow.Add(RefreshLifetime + time.Nanosecond) }
	if view := e.CachedSnapshot(1); view.Available || len(view.Accounts) != 0 {
		t.Fatalf("stale foreign state must become neutral: %+v", view)
	}
}
