package upstreamcacheguard

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/upstreamcacheusage"
)

func routeFenceComplete(t *testing.T, e *Engine, r Request, a Admission, at time.Time, success bool) Evidence {
	t.Helper()
	v := Evidence{ID: "route-fence-" + r.ID, Request: r, Admission: a, Success: success,
		RawUsage: RawUsage{Status: upstreamcacheusage.Known, TotalInputTokens: 100, CacheReadTokens: 95}, CompletedAt: at}
	if err := e.Complete(context.Background(), v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestSessionRouteFenceLateSourceKeepsPhysicalFactsWithoutReclaimingOwner(t *testing.T) {
	e, store := testEngine(t)
	source := request("source-first", "route-fence", testNow.Add(-30*time.Second))
	first := admit(t, e, source)
	routeFenceComplete(t, e, source, first, testNow.Add(-29*time.Second), true)
	if err := store.Transact(context.Background(), source.GroupID, func(tx Tx) error {
		var session SessionState
		if _, err := tx.Read(SessionKind, SessionKey(source), &session); err != nil {
			return err
		}
		session.CohortID, session.TrialAccountID = "existing-cohort", source.AccountID
		session.TrialModel, session.TrialProtocol = source.Model, source.Protocol
		_, err := tx.Write(SessionKind, SessionKey(source), session)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	old := source
	old.ID, old.StartedAt = "source-in-flight", testNow.Add(-20*time.Second)
	oldAdmission := admit(t, e, old)
	target := old
	target.ID, target.AccountID, target.UpstreamKeyID = "target", 4, 5
	target.StartedAt = testNow.Add(-15 * time.Second)
	targetAdmission := admit(t, e, target)
	if targetAdmission.RouteGeneration != oldAdmission.RouteGeneration+1 || targetAdmission.AffinityGeneration != oldAdmission.AffinityGeneration {
		t.Fatalf("ordinary change did not create its own route fence: old=%+v target=%+v", oldAdmission, targetAdmission)
	}
	routeFenceComplete(t, e, target, targetAdmission, testNow.Add(-10*time.Second), true)
	before := captureSession(t, store, target)
	late := routeFenceComplete(t, e, old, oldAdmission, testNow, true)
	after := captureSession(t, store, target)
	if after.LastAccountID != target.AccountID || after.LastRouteKey != WarmRouteKey(target) || after.LastCompletedAt != before.LastCompletedAt || after.ResidenceUntil != before.ResidenceUntil {
		t.Fatalf("late source reclaimed owner or extended residence: before=%+v after=%+v", before, after)
	}
	if after.Routes[WarmRouteKey(old)].LastSuccessfulAt != late.CompletedAt || !after.Admissions[admissionKey(old)].Completed {
		t.Fatal("late source's physical warmth or completion was discarded")
	}
	if _, exists := store.facts[key(old.GroupID, "fact", late.ID)]; !exists {
		t.Fatal("late source's raw account fact was discarded")
	}
	if after.NaturalAccountID != before.NaturalAccountID || after.NaturalRequestID != before.NaturalRequestID || after.NaturalTrialOrdinal != before.NaturalTrialOrdinal || after.CohortID != before.CohortID {
		t.Fatal("route fence reset natural identity or trial cohort")
	}
}

func TestSessionRouteFenceIdentityAndSameTimestamp(t *testing.T) {
	for _, identity := range []string{"account", "key", "model", "protocol"} {
		t.Run(identity, func(t *testing.T) {
			e, store := testEngine(t)
			source := request("old-"+identity, "identity-fence", testNow.Add(-10*time.Second))
			a := admit(t, e, source)
			target := source
			target.ID = "new-" + identity
			switch identity {
			case "account":
				target.AccountID++
			case "key":
				target.UpstreamKeyID++
			case "model":
				target.Model = "another-model"
			case "protocol":
				target.Protocol = "openai_ws"
			}
			b := admit(t, e, target)
			if b.RouteGeneration != a.RouteGeneration+1 {
				t.Fatalf("%s change did not fence a same-timestamp request: old=%+v new=%+v", identity, a, b)
			}
			routeFenceComplete(t, e, target, b, testNow.Add(-time.Second), true)
			routeFenceComplete(t, e, source, a, testNow, true)
			got := captureSession(t, store, target)
			if got.LastRouteKey != WarmRouteKey(target) || got.AcceptedRouteKey != WarmRouteKey(target) {
				t.Fatalf("same-timestamp old completion replaced accepted %s: %+v", identity, got)
			}
		})
	}
}

func TestSessionRouteFenceRestartReplayAndCaptureOnly(t *testing.T) {
	e, store := testEngine(t)
	a := request("first", "replay-fence", testNow.Add(-20*time.Second))
	first := admit(t, e, a)
	b := a
	b.ID, b.AccountID, b.UpstreamKeyID, b.StartedAt = "second", 4, 5, testNow.Add(-10*time.Second)
	second := admit(t, e, b)
	v := routeFenceComplete(t, e, b, second, testNow.Add(-5*time.Second), true)
	before := captureSession(t, store, b)
	restarted := NewEngine(store)
	restarted.now = e.now
	if err := restarted.ApplySettings(e.Settings()); err != nil {
		t.Fatal(err)
	}
	if replay := admit(t, restarted, a); !reflect.DeepEqual(first, replay) {
		t.Fatalf("old admission replay changed its frozen generation: %+v", replay)
	}
	if err := restarted.Complete(context.Background(), v); err != nil {
		t.Fatal(err)
	}
	if got := captureSession(t, store, b); !reflect.DeepEqual(before, got) {
		t.Fatal("restart replay changed accepted route or completion")
	}
	c := a
	c.ID, c.AccountID, c.StartedAt = "capture-other", 8, testNow.Add(-3*time.Second)
	ca := captureAdmission(t, restarted, c)
	if ca.RouteGeneration != second.RouteGeneration {
		t.Fatalf("capture did not freeze the existing fence: %+v", ca)
	}
	routeFenceComplete(t, restarted, c, ca, testNow, true)
	after := captureSession(t, store, b)
	if after.RouteGeneration != before.RouteGeneration || after.AcceptedRouteKey != before.AcceptedRouteKey || after.LastRouteKey != before.LastRouteKey || after.ResidenceUntil != before.ResidenceUntil {
		t.Fatal("capture-only created a routing decision")
	}
	if after.Routes[WarmRouteKey(c)].LastSuccessfulAt != testNow {
		t.Fatal("capture-only physical warmth was lost")
	}
}

func TestSessionRouteFenceFailedTargetCanReturnToWarmSource(t *testing.T) {
	e, store := testEngine(t)
	source := request("warm-source", "return-fence", testNow.Add(-30*time.Second))
	first := admit(t, e, source)
	routeFenceComplete(t, e, source, first, testNow.Add(-29*time.Second), true)
	target := source
	target.ID, target.AccountID, target.UpstreamKeyID, target.StartedAt = "failed-target", 4, 5, testNow.Add(-20*time.Second)
	failed := admit(t, e, target)
	late := target
	late.ID = "old-target-also-in-flight"
	lateAdmission := admit(t, e, late)
	routeFenceComplete(t, e, target, failed, testNow.Add(-19*time.Second), false)
	if got := captureSession(t, store, target); got.LastAccountID != source.AccountID {
		t.Fatal("failed target became successful session owner")
	}
	fallback := source
	fallback.ID, fallback.StartedAt = "legal-return", testNow.Add(-10*time.Second)
	returned := admit(t, e, fallback)
	if !returned.WarmAtStart || returned.RouteGeneration != failed.RouteGeneration+1 || returned.NaturalNew || returned.AffinityGeneration != first.AffinityGeneration {
		t.Fatalf("legal fallback lost warmth or reclaimed natural quota: %+v", returned)
	}
	if err := e.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if read := e.CachedSessionRead(fallback); !read.Known || read.PreferredAccountID != 0 || read.WarmAtStart {
		t.Fatalf("historical source warmth confirmed a new route generation: %+v", read)
	}
	routeFenceComplete(t, e, fallback, returned, testNow.Add(-5*time.Second), true)
	routeFenceComplete(t, e, late, lateAdmission, testNow, true)
	if got := captureSession(t, store, fallback); got.LastAccountID != source.AccountID || got.RouteGeneration != returned.RouteGeneration {
		t.Fatalf("old target reclaimed a valid fallback: %+v", got)
	}
}

func TestSessionRouteFenceOlderPhysicalWarmthCannotSuppressAcceptedReturn(t *testing.T) {
	e, store := testEngine(t)
	source := request("return-source", "physical-return-fence", testNow.Add(-30*time.Second))
	first := admit(t, e, source)
	routeFenceComplete(t, e, source, first, testNow.Add(-29*time.Second), true)
	oldSource := source
	oldSource.ID, oldSource.StartedAt = "very-slow-source", testNow.Add(-25*time.Second)
	old := admit(t, e, oldSource)
	target := source
	target.ID, target.AccountID, target.StartedAt = "intermediate-target", 4, testNow.Add(-20*time.Second)
	b := admit(t, e, target)
	routeFenceComplete(t, e, target, b, testNow.Add(-19*time.Second), true)
	returned := source
	returned.ID, returned.StartedAt = "accepted-return", testNow.Add(-10*time.Second)
	a := admit(t, e, returned)
	routeFenceComplete(t, e, oldSource, old, testNow, true)
	routeFenceComplete(t, e, returned, a, testNow.Add(-5*time.Second), true)
	got := captureSession(t, store, returned)
	if got.LastAccountID != source.AccountID || got.LastCompletedAt != testNow.Add(-5*time.Second) || got.Routes[WarmRouteKey(source)].LastSuccessfulAt != testNow {
		t.Fatalf("physical and accepted-route clocks were conflated: %+v", got)
	}
}

func TestSessionRouteFenceConcurrentAdmissionsAndRollback(t *testing.T) {
	e, store := testEngine(t)
	requests := []Request{request("parallel-a", "parallel-fence", testNow.Add(-10*time.Second)), request("parallel-b", "parallel-fence", testNow.Add(-10*time.Second))}
	requests[1].AccountID++
	var wg sync.WaitGroup
	admissions := make([]Admission, len(requests))
	errs := make([]error, len(requests))
	for i := range requests {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			admissions[i], errs[i] = e.Admit(context.Background(), requests[i])
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	winner, loser := 0, 1
	if admissions[1].RouteGeneration > admissions[0].RouteGeneration {
		winner, loser = 1, 0
	}
	if admissions[winner].RouteGeneration != 2 || admissions[loser].RouteGeneration != 1 {
		t.Fatalf("same-clock decisions did not serialize: %+v", admissions)
	}
	routeFenceComplete(t, e, requests[winner], admissions[winner], testNow.Add(-time.Second), true)
	routeFenceComplete(t, e, requests[loser], admissions[loser], testNow, true)
	before := captureSession(t, store, requests[winner])
	if before.LastAccountID != requests[winner].AccountID {
		t.Fatal("completion order overrode serialized admission order")
	}
	e.store = affinityRollbackStore{store}
	r := requests[loser]
	r.ID = "rollback-route"
	if _, err := e.Admit(context.Background(), r); err == nil {
		t.Fatal("expected admission rollback")
	}
	if got := captureSession(t, store, r); !reflect.DeepEqual(before, got) {
		t.Fatal("rolled-back selection advanced the route fence")
	}
}

func TestSessionRouteFenceLegacyJSONCannotBorrowNewGeneration(t *testing.T) {
	e, store := testEngine(t)
	r := request("legacy-request", "legacy-fence", testNow.Add(-20*time.Second))
	legacy := Admission{RequestID: r.ID, SessionKey: SessionKey(r), GroupID: r.GroupID, AccountID: r.AccountID, UpstreamKeyID: r.UpstreamKeyID, StartedAt: r.StartedAt, Mode: Protect, Sample: true, RouteKey: WarmRouteKey(r)}
	if err := store.Transact(context.Background(), r.GroupID, func(tx Tx) error {
		// Old JSON has neither a route generation nor an accepted route.
		raw := fmt.Sprintf(`{"key":%q,"first_seen_at":%q,"last_started_at":%q,"admissions":{}}`, SessionKey(r), r.StartedAt.Format(time.RFC3339Nano), r.StartedAt.Format(time.RFC3339Nano))
		var session SessionState
		if err := json.Unmarshal([]byte(raw), &session); err != nil {
			return err
		}
		session.Admissions[admissionKey(r)] = legacy
		_, err := tx.Write(SessionKind, SessionKey(r), session)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	target := r
	target.ID, target.AccountID, target.StartedAt = "new-after-upgrade", 4, testNow.Add(-10*time.Second)
	fresh := admit(t, e, target)
	if fresh.RouteGeneration != 1 || fresh.NaturalNew {
		t.Fatalf("legacy route not upgraded without new quota: %+v", fresh)
	}
	routeFenceComplete(t, e, target, fresh, testNow.Add(-time.Second), true)
	routeFenceComplete(t, e, r, legacy, testNow, true)
	if got := captureSession(t, store, target); got.LastAccountID != target.AccountID || got.RouteGeneration != 1 {
		t.Fatalf("legacy zero generation borrowed the new decision: %+v", got)
	}
}

func TestSessionRouteFencePendingOwnerWaitsForCurrentSuccessfulCompletion(t *testing.T) {
	e, store := testEngine(t)
	source := request("established-owner", "pending-owner-fence", testNow.Add(-30*time.Second))
	a := admit(t, e, source)
	routeFenceComplete(t, e, source, a, testNow.Add(-29*time.Second), true)
	late := source
	late.ID, late.StartedAt = "late-pending-source", testNow.Add(-20*time.Second)
	lateAdmission := admit(t, e, late)
	target := source
	target.ID, target.AccountID, target.UpstreamKeyID, target.StartedAt = "accepted-in-flight", 4, 5, testNow.Add(-10*time.Second)
	b := admit(t, e, target)
	if b.WarmAtStart {
		t.Fatal("a new accepted route invented physical warmness")
	}
	pending := captureSession(t, store, target)
	if pending.OwnerRouteGeneration != a.RouteGeneration || pending.RouteGeneration != b.RouteGeneration {
		t.Fatal("admission published an owner without a successful completion")
	}
	if err := e.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	next := target
	next.ID, next.AccountID, next.UpstreamKeyID, next.StartedAt = "next-read", 0, 0, testNow
	read := e.CachedSessionRead(next)
	if !read.Known || read.PreferredAccountID != 0 || read.WarmAtStart || read.Protected || read.Trial || !read.ResidenceUntil.IsZero() || !read.LastSuccessfulAt.IsZero() {
		t.Fatalf("pending admission projected an unconfirmed or former owner: %+v", read)
	}
	continued := target
	continued.ID, continued.StartedAt = "stale-target-continuation", testNow
	if _, err := e.AdmitExisting(context.Background(), continued); err == nil {
		t.Fatal("pending target acquired stale existing-owner admission")
	}
	old := source
	old.ID, old.StartedAt = "stale-old-continuation", testNow
	if _, err := e.AdmitExisting(context.Background(), old); err == nil {
		t.Fatal("stale former owner bypassed the accepted route fence")
	}
	if replay := admit(t, e, target); !reflect.DeepEqual(replay, b) {
		t.Fatalf("pending replay changed the frozen admission: %+v", replay)
	}
	if got := captureSession(t, store, target); !reflect.DeepEqual(got, pending) {
		t.Fatal("pending reads, existing-only rejection or replay mutated durable identity")
	}
	// A restart uses only durable JSON, not a process-local owner cache.
	restarted := NewEngine(store)
	restarted.now = e.now
	if err := restarted.ApplySettings(e.Settings()); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if read := restarted.CachedSessionRead(next); !read.Known || read.PreferredAccountID != 0 || read.WarmAtStart || read.Trial {
		t.Fatalf("restart published pending owner identity: %+v", read)
	}
	// An old source success is still a physical fact, not current-route proof.
	routeFenceComplete(t, restarted, late, lateAdmission, testNow, true)
	if got := captureSession(t, store, target); got.OwnerRouteGeneration == b.RouteGeneration {
		t.Fatal("old source completion confirmed the pending target")
	}
	routeFenceComplete(t, restarted, target, b, testNow.Add(-5*time.Second), true)
	if err := restarted.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if read := restarted.CachedSessionRead(next); !read.Known || read.PreferredAccountID != target.AccountID || !read.WarmAtStart {
		t.Fatalf("current successful completion did not establish owner: %+v", read)
	}
	if admitted, err := restarted.AdmitExisting(context.Background(), continued); err != nil || admitted.RouteGeneration != b.RouteGeneration {
		t.Fatalf("proven current owner could not continue: %+v %v", admitted, err)
	}
}

func TestSessionRouteFenceFailedOrUnknownCompletionDoesNotConfirmPendingOwner(t *testing.T) {
	for _, kind := range []string{"failed", "unknown", "capture"} {
		t.Run(kind, func(t *testing.T) {
			e, store := testEngine(t)
			r := request("pending-"+kind, "pending-failure-fence", testNow.Add(-time.Second))
			a := admit(t, e, r)
			if !a.NaturalNew {
				t.Fatal("fixture did not allocate its original natural classification")
			}
			before := captureSession(t, store, r)
			v := Evidence{ID: "pending-proof-" + kind, Request: r, Admission: a, Success: true,
				RawUsage: RawUsage{Status: upstreamcacheusage.Known, TotalInputTokens: 100, CacheReadTokens: 95}, CompletedAt: testNow}
			switch kind {
			case "failed":
				v.Success = false
			case "unknown":
				v.RawUsage = RawUsage{Status: upstreamcacheusage.Absent}
			case "capture":
				capture := r
				capture.ID = "capture-pending"
				v.Request, v.Admission = capture, captureAdmission(t, e, capture)
			}
			if err := e.Complete(context.Background(), v); err != nil {
				t.Fatal(err)
			}
			if err := e.Refresh(context.Background()); err != nil {
				t.Fatal(err)
			}
			next := r
			next.ID, next.AccountID, next.UpstreamKeyID, next.StartedAt = "after-pending-"+kind, 0, 0, testNow
			if read := e.CachedSessionRead(next); !read.Known || read.PreferredAccountID != 0 || read.WarmAtStart || read.Trial || read.Protected {
				t.Fatalf("%s completion established a pending owner: %+v", kind, read)
			}
			after := captureSession(t, store, r)
			if after.OwnerRouteGeneration != 0 || after.RouteGeneration != a.RouteGeneration || after.NaturalRequestID != before.NaturalRequestID || after.NaturalTrialOrdinal != before.NaturalTrialOrdinal {
				t.Fatal("unconfirmed completion changed route proof or natural identity")
			}
			continued := r
			continued.ID, continued.StartedAt = "ordinary-after-pending-"+kind, testNow
			if nextAdmission := admit(t, e, continued); nextAdmission.NaturalNew || nextAdmission.RouteGeneration != a.RouteGeneration {
				t.Fatalf("pending identity reclaimed natural quota or changed route: %+v", nextAdmission)
			}
		})
	}
}

func TestSessionRouteFenceIdleClearDoesNotResurrectAcceptedOwner(t *testing.T) {
	e, store := testEngine(t)
	old := request("accepted-before-idle", "idle-pending-fence", testNow.Add(-2*time.Hour))
	a := admit(t, e, old)
	if err := e.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	next := old
	next.ID, next.StartedAt = "after-idle-capture", testNow
	if read := e.CachedSessionRead(next); !read.AffinityExpired || read.PreferredAccountID != 0 {
		t.Fatalf("idle accepted route remained selected: %+v", read)
	}
	captureAdmission(t, e, next)
	if state := captureSession(t, store, next); state.AcceptedRouteKey != "" || state.RouteGeneration != a.RouteGeneration {
		t.Fatalf("idle capture retained or rewound accepted routing fence: %+v", state)
	}
	fresh := next
	fresh.ID, fresh.AccountID = "fresh-after-idle", 4
	b := admit(t, e, fresh)
	if b.RouteGeneration != a.RouteGeneration+1 {
		t.Fatal("new idle-route admission reused a discarded route generation")
	}
}

func TestSessionRouteFenceSerialDecisionsSurviveClockRollback(t *testing.T) {
	e, store := testEngine(t)
	source := request("before-clock-rollback", "clock-route-fence", testNow.Add(-5*time.Second))
	a := admit(t, e, source)
	routeFenceComplete(t, e, source, a, testNow, true)
	target := source
	target.ID, target.AccountID, target.StartedAt = "after-clock-rollback", 4, testNow.Add(-10*time.Second)
	b := admit(t, e, target)
	routeFenceComplete(t, e, target, b, testNow.Add(-2*time.Second), true)
	if got := captureSession(t, store, target); got.LastAccountID != target.AccountID || got.RouteGeneration != b.RouteGeneration {
		t.Fatalf("wall-clock ordering overrode the new serialized decision: %+v", got)
	}
	sameRoute := target
	sameRoute.ID = "same-route-another-request"
	if same := admit(t, e, sameRoute); same.RouteGeneration != b.RouteGeneration {
		t.Fatalf("same route consumed another owner generation: %+v", same)
	}
}

func TestSessionRouteFenceCurrentReturnProofSurvivesOlderCompletionClock(t *testing.T) {
	e, store := testEngine(t)
	source := request("before-return-clock-rollback", "return-clock-fence", testNow.Add(-30*time.Second))
	a := admit(t, e, source)
	routeFenceComplete(t, e, source, a, testNow, true)
	target := source
	target.ID, target.AccountID, target.StartedAt = "pending-clock-target", 4, testNow.Add(-20*time.Second)
	admit(t, e, target)
	returned := source
	returned.ID, returned.StartedAt = "current-return-clock-rollback", testNow.Add(-10*time.Second)
	b := admit(t, e, returned)
	routeFenceComplete(t, e, returned, b, testNow.Add(-2*time.Second), true)
	got := captureSession(t, store, returned)
	if got.OwnerRouteGeneration != b.RouteGeneration || got.LastCompletedAt != testNow || got.Routes[WarmRouteKey(source)].LastSuccessfulAt != testNow {
		t.Fatalf("current-generation proof borrowed the historical route clock: %+v", got)
	}
	if err := e.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	next := returned
	next.StartedAt = testNow
	if read := e.CachedSessionRead(next); !read.Known || read.PreferredAccountID != source.AccountID || !read.WarmAtStart {
		t.Fatalf("real current completion did not confirm returning owner: %+v", read)
	}
}
