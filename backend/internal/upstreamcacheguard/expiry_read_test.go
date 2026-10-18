package upstreamcacheguard

import (
	"testing"
	"time"
)

func TestEvidenceExpiryReadCopiesOnlyExpiredPolicyMap(t *testing.T) {
	e, _ := testEngine(t)
	s := e.Settings()
	sig := PolicySignature(s)
	anchor := testNow
	a := AccountSnapshot{AccountState: AccountState{AccountID: 2, UpstreamKeyID: 3,
		GroupIDs: []int64{1, 2}, Policies: map[string]PolicyProjection{
			sig: {Settings: s, State: AccountState{Phase: Trial, LastEvidenceAt: testNow, LastDegradedEvidenceAt: &anchor, CohortID: "kept", GroupIDs: []int64{1, 2}}},
		}}, LoadedAt: testNow}
	if allocations := testing.AllocsPerRun(100, func() {
		_ = e.accountSnapshotAtLocked("2:3", a, testNow)
	}); allocations != 0 {
		t.Fatalf("unchanged read allocated %.0f times", allocations)
	}
	expired := e.accountSnapshotAtLocked("2:3", a, testNow.Add(16*time.Minute))
	if expired.Policies[sig].State.Phase != Unknown || a.Policies[sig].State.Phase != Trial || a.Policies[sig].State.CohortID != "kept" {
		t.Fatal("read expiry changed the published policy")
	}
	// Public batch reads must still own nested data after leaving the lock.
	e.accounts["2:3"] = a
	views := e.CachedSharedAccountsAt([]Candidate{{AccountID: 2, UpstreamKeyID: 3}}, testNow)
	p := views[2].Policies[sig]
	p.State.GroupIDs[0] = 99
	if e.accounts["2:3"].Policies[sig].State.GroupIDs[0] == 99 {
		t.Fatal("public batch read exposed published nested data")
	}
	*p.State.LastDegradedEvidenceAt = testNow.Add(time.Hour)
	if !e.accounts["2:3"].Policies[sig].State.LastDegradedEvidenceAt.Equal(testNow) {
		t.Fatal("public batch read exposed published evidence anchor")
	}
}

func TestExpiredAccountSessionReadKeepsIdentityWithoutRecoveryControls(t *testing.T) {
	e, _ := testEngine(t)
	s := e.Settings()
	sig := PolicySignature(s)
	r := request("after-expiry", "same-session", testNow)
	routeKey := WarmRouteKey(r)
	e.loadedAt = testNow
	e.accounts["2:3"] = AccountSnapshot{AccountState: AccountState{AccountID: 2, UpstreamKeyID: 3,
		Policies: map[string]PolicyProjection{sig: {Settings: s, State: AccountState{Phase: Trial,
			LastEvidenceAt: testNow.Add(-16 * time.Minute), CohortID: "expired"}}}}, LoadedAt: testNow}
	e.sessions[sessionCacheKey(r.GroupID, SessionKey(r))] = SessionState{
		Key: SessionKey(r), LastAccountID: r.AccountID, LastModel: r.Model, LastProtocol: r.Protocol,
		LastRouteKey: routeKey, TrialAccountID: r.AccountID, TrialModel: r.Model, TrialProtocol: r.Protocol,
		PolicySignature: sig, CohortID: "expired", ResidenceUntil: testNow.Add(time.Minute),
		Routes: map[string]SessionRoute{routeKey: {AccountID: r.AccountID, UpstreamKeyID: r.UpstreamKeyID,
			PolicySignature: sig, LastSuccessfulAt: testNow.Add(-time.Minute), HighCacheSeen: true, PendingLow: true}},
	}
	got := e.CachedSessionReadAt(r, testNow)
	if !got.Known || got.PreferredAccountID != r.AccountID || !got.WarmAtStart || got.LastSuccessfulAt.IsZero() {
		t.Fatalf("expired account lost physical/durable identity: %+v", got)
	}
	if got.Trial || got.CohortID != "" || got.Protected || got.PendingLow {
		t.Fatalf("expired account retained recovery controls: %+v", got)
	}
}
