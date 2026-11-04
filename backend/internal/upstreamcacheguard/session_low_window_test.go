package upstreamcacheguard

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/upstreamcacheusage"
)

func lowWindowFact(i int, at time.Time, r Request, sig string) Evidence {
	r.ID = "low-window-" + string(rune('a'+i))
	r.StartedAt = at.Add(-time.Second)
	return Evidence{ID: r.ID, Request: r, Admission: Admission{
		RequestID: r.ID, SessionKey: SessionKey(r), AccountID: r.AccountID, GroupID: r.GroupID,
		UpstreamKeyID: r.UpstreamKeyID, StartedAt: r.StartedAt, PolicySignature: sig,
		Sample: true, WarmAtStart: true, ProtectedAtStart: true,
	}, Success: true, CompletedAt: at, RawUsage: RawUsage{Status: upstreamcacheusage.Known, TotalInputTokens: 100, CacheReadTokens: 10}}
}

// Exercise the persisted route, not just the window's pure arithmetic. Low
// outcomes carry much larger inputs, so alternating high requests interrupt
// the old streak rule while the token-weighted window remains low.
func TestSessionLowWindowCompletionPersistsAndRevokesHighCacheAffinity(t *testing.T) {
	e, store, now := lowWindowEngine(t, nil)
	for i := 0; i < 8; i++ {
		input, read := int64(1000), int64(10)
		if i%2 == 1 {
			input, read = 100, 100
		}
		completeLowWindowRequest(t, e, now, fmt.Sprintf("weighted-%d", i), input, read)
	}
	r := request("read-weighted", "weighted", *now)
	var persisted SessionState
	if err := store.Transact(context.Background(), 1, func(tx Tx) error {
		_, err := tx.Read(SessionKind, SessionKey(r), &persisted)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	route := persisted.Routes[WarmRouteKey(r)]
	if route.LowWindow == nil || route.PendingLow || route.LowStreak != 0 || !route.HighCacheSeen {
		t.Fatalf("weighted proof lost across persistence: %+v", route)
	}
	read := e.CachedSessionRead(r)
	if !read.Known || !read.WarmAtStart || !read.PendingLow || read.Protected {
		t.Fatalf("window did not join the migration predicate: %+v", read)
	}
	// Rebuilding from the durable JSON keeps the same proof and decision.
	restarted := NewEngine(store)
	restarted.now = func() time.Time { return *now }
	if err := restarted.ApplySettings(e.Settings()); err != nil {
		t.Fatal(err)
	}
	restarted.SetOrdinaryRecoveryPolicyGenerations(map[string]string{PolicySignature(e.Settings()): "window-generation"})
	if err := restarted.Rebuild(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := restarted.CachedSessionRead(r); !got.PendingLow || got.Protected {
		t.Fatalf("restart changed route window: %+v", got)
	}
}

func lowWindowEngine(t *testing.T, threshold *float64) (*Engine, *memoryStore, *time.Time) {
	t.Helper()
	e, store := testEngine(t)
	now := testNow
	e.now = func() time.Time { return now }
	s := e.Settings()
	s.MinValidRequests, s.WindowMinutes = 5, 5
	if threshold == nil {
		value := float64(25)
		threshold = &value
	}
	s.SessionWindowMigrationThresholdPercent = threshold
	if err := e.ApplySettings(s); err != nil {
		t.Fatal(err)
	}
	e.SetOrdinaryRecoveryPolicyGenerations(map[string]string{PolicySignature(s): "window-generation"})
	state := AccountState{AccountID: 2, UpstreamKeyID: 3, Phase: Cooling, DegradedAt: testNow.Add(-time.Minute), LastEvidenceAt: testNow.Add(-time.Minute), CooldownUntil: testNow.Add(time.Hour)}
	if err := store.TransactAccount(context.Background(), 1, 2, 3, func(tx Tx) error {
		_, err := tx.(AccountTx).WriteAccount(2, 3, state)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// Establish physical warmness and the existing high-cache affinity.
	completeLowWindowRequest(t, e, &now, "weighted-cold", 100, 100)
	return e, store, &now
}

func completeLowWindowRequest(t *testing.T, e *Engine, now *time.Time, id string, input, read int64) Evidence {
	t.Helper()
	*now = now.Add(2 * time.Second)
	r := request(id, "weighted", *now)
	a := admit(t, e, r)
	*now = now.Add(time.Second)
	v := Evidence{ID: id, Request: r, Admission: a, Success: true, CompletedAt: *now, RawUsage: RawUsage{Status: upstreamcacheusage.Known, TotalInputTokens: input, CacheReadTokens: read}}
	if err := e.Complete(context.Background(), v); err != nil {
		t.Fatal(err)
	}
	if err := e.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestSessionLowWindowZeroLeavesTwoLowRuleActive(t *testing.T) {
	zero := float64(0)
	e, _, now := lowWindowEngine(t, &zero)
	for i := 0; i < 8; i++ {
		read := int64(10)
		if i%2 == 1 {
			read = 100
		}
		completeLowWindowRequest(t, e, now, fmt.Sprintf("zero-alternating-%d", i), 100, read)
	}
	r := request("zero-read", "weighted", *now)
	if got := e.CachedSessionRead(r); got.PendingLow || !got.Protected {
		t.Fatalf("zero unexpectedly used the added window: %+v", got)
	}
	completeLowWindowRequest(t, e, now, "zero-two-low-1", 100, 0)
	completeLowWindowRequest(t, e, now, "zero-two-low-2", 100, 0)
	r.StartedAt = *now
	if got := e.CachedSessionRead(r); !got.PendingLow || got.Protected {
		t.Fatalf("zero disabled the original streak rule: %+v", got)
	}
	e.mu.RLock()
	route := e.sessions[sessionCacheKey(1, SessionKey(r))].Routes[WarmRouteKey(r)]
	e.mu.RUnlock()
	if route.LowWindow != nil || !route.PendingLow {
		t.Fatalf("zero proof or streak persisted incorrectly: %+v", route)
	}
}

func TestSessionLowWindowProofInvalidationIdentityAndClock(t *testing.T) {
	s := DefaultSettings()
	s.Mode, s.MinValidRequests = Protect, 1
	a := AccountState{AccountID: 2, UpstreamKeyID: 3, Phase: Cooling, DegradedAt: testNow.Add(-time.Minute)}
	r := request("proof-id", "proof-session", testNow)
	facts := []Evidence{lowWindowFact(0, testNow.Add(-time.Second), r, PolicySignature(s)), lowWindowFact(1, testNow, r, PolicySignature(s))}
	proof, err := sessionLowWindow(a, s, "stable", SessionKey(r), WarmRouteKey(r), facts, testNow)
	if err != nil || proof == nil {
		t.Fatalf("proof=%+v err=%v", proof, err)
	}
	for _, tc := range []struct {
		name   string
		change func(*AccountState, *Settings, *string, *time.Time)
	}{
		{"expired", func(_ *AccountState, _ *Settings, _ *string, at *time.Time) {
			*at = proof.ValidUntil.Add(time.Nanosecond)
		}},
		{"clock-back", func(_ *AccountState, _ *Settings, _ *string, at *time.Time) { *at = testNow.Add(-time.Nanosecond) }},
		{"key", func(a *AccountState, _ *Settings, _ *string, _ *time.Time) { a.UpstreamKeyID++ }},
		{"account", func(a *AccountState, _ *Settings, _ *string, _ *time.Time) { a.AccountID++ }},
		{"policy", func(_ *AccountState, s *Settings, _ *string, _ *time.Time) { s.ThresholdPercent-- }},
		{"threshold", func(_ *AccountState, s *Settings, _ *string, _ *time.Time) {
			value := float64(1)
			s.SessionWindowMigrationThresholdPercent = &value
		}},
		{"generation", func(_ *AccountState, _ *Settings, generation *string, _ *time.Time) { *generation = "changed" }},
		{"protection-cycle", func(a *AccountState, _ *Settings, _ *string, _ *time.Time) {
			a.DegradedAt = a.DegradedAt.Add(time.Second)
		}},
		{"evidence-boundary", func(a *AccountState, _ *Settings, _ *string, _ *time.Time) { a.QualitySince = testNow }},
		{"mode", func(_ *AccountState, s *Settings, _ *string, _ *time.Time) { s.Mode = Observe }},
		{"healthy", func(a *AccountState, _ *Settings, _ *string, _ *time.Time) { a.Phase = Healthy }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account, settings, generation, at := a, s, "stable", testNow
			tc.change(&account, &settings, &generation, &at)
			if sessionLowWindowValid(proof, account, settings, generation, at) {
				t.Fatal("stale proof remained eligible")
			}
		})
	}
}

func TestRecoverySessionLegacyZeroNormalizesWithoutVersionChange(t *testing.T) {
	e, _ := testEngine(t)
	before := e.PolicyVersion()
	s := e.Settings()
	s.RecoveryMinSessions = 0
	if err := e.ApplySettings(s); err != nil {
		t.Fatal(err)
	}
	if e.Settings().RecoveryMinSessions != 3 || e.PolicyVersion() != before {
		t.Fatal("legacy zero changed the effective global configuration")
	}
	if err := e.ApplyPolicySettings(map[int64]Settings{1: s}); err != nil {
		t.Fatal(err)
	}
	before = e.PolicyVersion()
	s.RecoveryMinSessions = 3
	if err := e.ApplyPolicySettings(map[int64]Settings{1: s}); err != nil {
		t.Fatal(err)
	}
	if e.ResolvedSettings(1).RecoveryMinSessions != 3 || e.PolicyVersion() != before {
		t.Fatal("legacy zero changed the effective group configuration")
	}
}

func TestTrialFailureDoesNotContinueOldCohortOrRenewCooldown(t *testing.T) {
	e, store := testEngine(t)
	now := testNow
	e.now = func() time.Time { return now }
	s := e.Settings()
	s.MinValidRequests, s.TrialPercent, s.CooldownMinutes = 1, 100, 1
	if err := e.ApplySettings(s); err != nil {
		t.Fatal(err)
	}
	state := AccountState{AccountID: 2, UpstreamKeyID: 3, Phase: Cooling, DegradedAt: testNow.Add(-time.Minute), CooldownUntil: testNow.Add(-time.Second), LastEvidenceAt: testNow.Add(-time.Minute)}
	if err := store.TransactAccount(context.Background(), 1, 2, 3, func(tx Tx) error { _, err := tx.(AccountTx).WriteAccount(2, 3, state); return err }); err != nil {
		t.Fatal(err)
	}
	first := request("trial-cold", "weighted", now)
	firstAdmission := admit(t, e, first)
	if !firstAdmission.Trial {
		t.Fatal("natural trial was not created")
	}
	now = now.Add(time.Second)
	complete(t, e, first, firstAdmission, 0, true, upstreamcacheusage.Known)
	// Freeze an old-cohort in-flight admission before its cohort fails.
	pending := request("trial-in-flight", "weighted", now.Add(time.Second))
	oldAdmission := admit(t, e, pending)
	for i := 0; i < 2; i++ {
		now = now.Add(3 * time.Second)
		r := request(fmt.Sprintf("trial-low-%d", i), "weighted", now)
		a := admit(t, e, r)
		now = now.Add(time.Second)
		complete(t, e, r, a, 0, true, upstreamcacheusage.Known)
	}
	failed := account(t, e)
	if failed.Phase != Cooling || failed.CohortID != "" || failed.TrialValidRequests != 0 {
		t.Fatalf("trial did not fail promptly: %+v", failed)
	}
	deadline := failed.CooldownUntil
	now = now.Add(time.Second)
	v := Evidence{ID: "old-cohort-finished", Request: pending, Admission: oldAdmission, Success: true, CompletedAt: now, RawUsage: RawUsage{Status: upstreamcacheusage.Known, TotalInputTokens: 100, CacheReadTokens: 0}}
	if err := e.Complete(context.Background(), v); err != nil {
		t.Fatal(err)
	}
	if got := account(t, e); got.Phase != Cooling || !got.CooldownUntil.Equal(deadline) || got.CohortID != "" {
		t.Fatalf("old cohort renewed failed trial: %+v", got)
	}
	next := request("after-failure", "weighted", now.Add(time.Second))
	if got := admit(t, e, next); got.Trial || got.CohortID != "" {
		t.Fatalf("admission continued dead cohort: %+v", got)
	}
	before := account(t, e)
	for i := 0; i < 3; i++ {
		_ = e.CachedSessionRead(next)
		_ = e.CachedSnapshot(1)
		if err := e.Refresh(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if inserted, err := e.CompleteWithResult(context.Background(), v); err != nil || inserted {
		t.Fatalf("duplicate completion=%v err=%v", inserted, err)
	}
	if got := account(t, e); !reflect.DeepEqual(before, got) {
		t.Fatalf("read or duplicate changed the failed trial: before=%+v after=%+v", before, got)
	}
}

func TestSessionLowWindowInvalidCompletionCannotCreateOrExtendProof(t *testing.T) {
	for _, tc := range []struct {
		name     string
		validity func(*Evidence)
	}{
		{"failed", func(v *Evidence) { v.Success = false }},
		{"unknown-usage", func(v *Evidence) { v.RawUsage.Status = upstreamcacheusage.Absent }},
		{"cold", func(v *Evidence) { v.Admission.WarmAtStart = false }},
		{"unprotected", func(v *Evidence) { v.Admission.ProtectedAtStart = false }},
		{"paused", func(v *Evidence) { v.ProtectionPaused = true }},
		{"capture-only", func(v *Evidence) { v.Admission.CaptureOnly = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, existing := range []bool{false, true} {
				t.Run(fmt.Sprintf("existing-%v", existing), func(t *testing.T) {
					e, _, now := lowWindowEngine(t, nil)
					if !existing {
						e.SetOrdinaryRecoveryPolicyGenerations(nil)
					}
					for i := 0; i < 8; i++ {
						input, read := int64(1000), int64(10)
						if i%2 == 1 {
							input, read = 100, 100
						}
						completeLowWindowRequest(t, e, now, fmt.Sprintf("invalid-seed-%d", i), input, read)
					}
					if tc.name == "paused" || tc.name == "capture-only" {
						completeLowWindowRequest(t, e, now, "invalid-streak-anchor", 1000, 10)
					}
					e.SetOrdinaryRecoveryPolicyGenerations(map[string]string{PolicySignature(e.Settings()): "window-generation"})
					*now = now.Add(2 * time.Second)
					r := request("invalid-window-completion", "weighted", *now)
					a := admit(t, e, r)
					e.mu.RLock()
					beforeRoute := e.sessions[sessionCacheKey(1, SessionKey(r))].Routes[WarmRouteKey(r)]
					e.mu.RUnlock()
					*now = now.Add(time.Second)
					v := Evidence{ID: r.ID, Request: r, Admission: a, Success: true, CompletedAt: *now, RawUsage: RawUsage{Status: upstreamcacheusage.Known, TotalInputTokens: 100, CacheReadTokens: 100}}
					tc.validity(&v)
					if err := e.Complete(context.Background(), v); err != nil {
						t.Fatal(err)
					}
					if err := e.Refresh(context.Background()); err != nil {
						t.Fatal(err)
					}
					e.mu.RLock()
					route := e.sessions[sessionCacheKey(1, SessionKey(r))].Routes[WarmRouteKey(r)]
					e.mu.RUnlock()
					if route.LowWindow != nil {
						t.Fatalf("invalid current evidence created or extended a proof: %+v", route.LowWindow)
					}
					if tc.name == "paused" || tc.name == "capture-only" {
						if beforeRoute.LowStreak != 1 || route.LowStreak != beforeRoute.LowStreak || route.PendingLow != beforeRoute.PendingLow || route.HighCacheSeen != beforeRoute.HighCacheSeen {
							t.Fatalf("paused/capture changed the original affinity: before=%+v after=%+v", beforeRoute, route)
						}
					}
				})
			}
		})
	}
}

func TestSessionLowWindowRejectsPriorProtectionCycleInFlightCompletion(t *testing.T) {
	e, store, now := lowWindowEngine(t, nil)
	*now = now.Add(2 * time.Second)
	pending := request("prior-cycle-inflight", "weighted", *now)
	old := admit(t, e, pending)
	if !old.ProtectedAtStart || !old.WarmAtStart {
		t.Fatal("test did not freeze a protected warm in-flight request")
	}
	*now = now.Add(time.Second)
	cycle := *now
	if err := store.TransactAccount(context.Background(), 1, 2, 3, func(tx Tx) error {
		var state AccountState
		if _, err := tx.(AccountTx).ReadAccount(2, 3, &state); err != nil {
			return err
		}
		for sig, projection := range state.Policies {
			projection.State.DegradedAt = cycle
			state.Policies[sig] = projection
		}
		state.DegradedAt = cycle
		_, err := tx.(AccountTx).WriteAccount(2, 3, state)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// Five current-cycle outcomes cannot meet the strict >5 requirement. The
	// latest high breaks the independent two-low predicate.
	for i := 0; i < 5; i++ {
		input, read := int64(100), int64(100)
		if i%2 == 1 {
			input, read = 1000, 10
		}
		completeLowWindowRequest(t, e, now, fmt.Sprintf("current-cycle-%d", i), input, read)
	}
	*now = now.Add(time.Second)
	v := Evidence{ID: pending.ID, Request: pending, Admission: old, Success: true, CompletedAt: *now, RawUsage: RawUsage{Status: upstreamcacheusage.Known, TotalInputTokens: 1000, CacheReadTokens: 10}}
	if err := e.Complete(context.Background(), v); err != nil {
		t.Fatal(err)
	}
	if err := e.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	r := request("prior-cycle-read", "weighted", *now)
	if got := e.CachedSessionRead(r); got.PendingLow {
		t.Fatalf("prior-cycle completion manufactured current-cycle quorum: %+v", got)
	}
	// A real sixth outcome in this cycle is sufficient; the old outcome is not
	// required and cannot enter the persisted proof.
	completeLowWindowRequest(t, e, now, "current-cycle-sixth", 1000, 10)
	r.StartedAt = *now
	if got := e.CachedSessionRead(r); !got.PendingLow || got.Protected {
		t.Fatalf("current cycle could not establish its own proof: %+v", got)
	}
}

func TestRecoveryRequirementChangeClearsOnlyProjectedOrdinaryProgress(t *testing.T) {
	s := ordinaryTestSettings()
	a := ordinaryTestState(s)
	a.Phase, a.CohortID, a.FirstTrialAt = Trial, "unchanged", testNow.Add(-time.Minute)
	a.OrdinaryRecovery = &OrdinaryRecoveryState{QualifiedSince: testNow, QualifiedUntil: testNow.Add(time.Hour), Sessions: 3}
	snapshot := AccountSnapshot{AccountState: AccountState{AccountID: 2, UpstreamKeyID: 3, Policies: map[string]PolicyProjection{PolicySignature(s): {Settings: s, State: a}}}, Available: true, Fresh: true}
	changed := s
	changed.RecoveryMinSessions = 100
	got := ProjectAccountSnapshot(snapshot, changed)
	if !got.Available || got.OrdinaryRecovery != nil || got.CohortID != a.CohortID || !got.FirstTrialAt.Equal(a.FirstTrialAt) {
		t.Fatalf("rule change borrowed ordinary clock or altered trial: %+v", got)
	}
	if snapshot.Policies[PolicySignature(s)].State.OrdinaryRecovery == nil {
		t.Fatal("projection mutated durable ordinary progress")
	}
	same := s
	same.RecoveryMinSessions = 0
	if got := ProjectAccountSnapshot(snapshot, same); got.OrdinaryRecovery == nil || !got.OrdinaryRecovery.QualifiedSince.Equal(testNow) {
		t.Fatal("legacy zero invalidated equivalent progress")
	}
}

func TestOrdinaryCompletionAndReadCannotFinishTrialFromAggregate(t *testing.T) {
	e, store := testEngine(t)
	s := e.Settings()
	s.MinValidRequests = 1
	if err := e.ApplySettings(s); err != nil {
		t.Fatal(err)
	}
	state := AccountState{AccountID: 2, UpstreamKeyID: 3, Phase: Trial, CohortID: "current", FirstTrialAt: testNow.Add(-time.Minute), DegradedAt: testNow.Add(-2 * time.Minute)}
	if err := store.TransactAccount(context.Background(), 1, 2, 3, func(tx Tx) error {
		if _, err := tx.(AccountTx).WriteAccount(2, 3, state); err != nil {
			return err
		}
		for i := 0; i < 2; i++ {
			r := request(fmt.Sprintf("trial-aggregate-%d", i), "ordinary-route", testNow.Add(-time.Duration(4-i)*time.Second))
			f := Evidence{ID: r.ID, Request: r, Admission: Admission{RequestID: r.ID, GroupID: 1, AccountID: 2, UpstreamKeyID: 3, SessionKey: SessionKey(r), StartedAt: r.StartedAt, Sample: true, WarmAtStart: true, Trial: true, ProtectedAtStart: true, CohortID: "current", PolicySignature: PolicySignature(s)}, Success: true, CompletedAt: r.StartedAt.Add(time.Second), RawUsage: RawUsage{Status: upstreamcacheusage.Known, TotalInputTokens: 100, CacheReadTokens: 0}}
			if _, err := tx.InsertFact(f); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	r := request("ordinary-does-not-fail-trial", "ordinary-route", testNow.Add(-time.Second))
	a := admit(t, e, r)
	if a.Trial {
		t.Fatal("unrelated ordinary session became trial")
	}
	v := complete(t, e, r, a, 0, true, upstreamcacheusage.Known)
	before := account(t, e)
	if before.Phase != Trial || before.TrialValidRequests != 2 {
		t.Fatalf("ordinary completion ended aggregate trial: %+v", before)
	}
	for i := 0; i < 2; i++ {
		_ = e.CachedSnapshot(1)
		_ = e.CachedSessionRead(r)
		if err := e.Refresh(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if inserted, err := e.CompleteWithResult(context.Background(), v); err != nil || inserted {
		t.Fatalf("duplicate inserted=%v err=%v", inserted, err)
	}
	if got := account(t, e); !reflect.DeepEqual(before, got) {
		t.Fatalf("read or duplicate finished a trial: before=%+v after=%+v", before, got)
	}
}

func TestTrialRecoverySessionRangeUsesConfiguredRequirement(t *testing.T) {
	for _, sessions := range []int{1, 100} {
		t.Run(fmt.Sprint(sessions), func(t *testing.T) {
			s := DefaultSettings()
			s.Mode, s.MinValidRequests, s.RecoveryMinSessions = Protect, 5, sessions
			if err := s.Validate(); err != nil {
				t.Fatal(err)
			}
			a := AccountState{AccountID: 2, UpstreamKeyID: 3, Phase: Trial, CohortID: "current", FirstTrialAt: testNow.Add(-RecoveryLifetime), TrialValidRequests: 6, TrialSessions: sessions - 1, TrialCachePercent: 90}
			r := request("range-trial", "range-session", testNow.Add(-time.Second))
			f := Evidence{ID: r.ID, Request: r, Admission: Admission{Sample: true, WarmAtStart: true, Trial: true, CohortID: "current", PolicySignature: PolicySignature(s)}, Success: true, CompletedAt: testNow, RawUsage: RawUsage{Status: upstreamcacheusage.Known, TotalInputTokens: 100, CacheReadTokens: 90}}
			advanceTrialCompletion(&a, s, f, testNow)
			if a.Phase != Trial {
				t.Fatal("recovered below configured session count")
			}
			a.TrialSessions++
			advanceTrialCompletion(&a, s, f, testNow)
			if a.Phase != Healthy {
				t.Fatalf("did not recover at configured session count: %+v", a)
			}
		})
	}
}

func TestOrdinaryRecoverySessionRangeAndRuleChangeResetObservation(t *testing.T) {
	for _, sessions := range []int{1, 100} {
		t.Run(fmt.Sprintf("range-%d", sessions), func(t *testing.T) {
			s := ordinaryTestSettings()
			s.RecoveryMinSessions = sessions
			a := ordinaryTestState(s)
			count := sessions
			if count < 6 {
				count = 6
			}
			var facts []Evidence
			for i := 0; i < count; i++ {
				f := ordinaryTestFact(i, testNow.Add(time.Duration(i-count)*time.Second), 95)
				f.Request.OrdinaryRecoverySessionID = fmt.Sprintf("%064x", i%sessions+1)
				f.Admission.OrdinaryRecoverySessionID = f.Request.OrdinaryRecoverySessionID
				facts = append(facts, f)
			}
			advanceTestOrdinary(t, &a, s, facts, facts[len(facts)-1], testNow, false)
			if a.OrdinaryRecovery.Sessions != sessions || !a.OrdinaryRecovery.QualifiedSince.Equal(testNow) {
				t.Fatalf("range did not qualify: %+v", a.OrdinaryRecovery)
			}
			if v := OrdinaryRecoveryStatusAt(a, s, testNow); v.RequiredSessions != sessions {
				t.Fatalf("view uses different requirement: %+v", v)
			}
			fresh := ordinaryTestFact(1000, testNow.Add(RecoveryLifetime), 95)
			fresh.Request.OrdinaryRecoverySessionID = facts[0].Request.OrdinaryRecoverySessionID
			fresh.Admission.OrdinaryRecoverySessionID = fresh.Request.OrdinaryRecoverySessionID
			facts = append(facts, fresh)
			advanceTestOrdinary(t, &a, s, facts, fresh, fresh.CompletedAt, false)
			if a.Phase != Healthy {
				t.Fatalf("range did not recover: %+v", a)
			}
		})
	}

	s := ordinaryTestSettings()
	a := ordinaryTestState(s)
	facts := qualifyingOrdinaryFacts(testNow)
	advanceTestOrdinary(t, &a, s, facts, facts[len(facts)-1], testNow, false)
	for i, sessions := range []int{100, 3} {
		s.RecoveryMinSessions = sessions
		at := testNow.Add(time.Duration(i+2) * time.Minute)
		fresh := ordinaryTestFact(200+i, at, 95)
		facts = append(facts, fresh)
		advanceTestOrdinary(t, &a, s, facts, fresh, at, false)
		if !a.OrdinaryRecovery.QualifiedSince.IsZero() || a.OrdinaryRecovery.Reason != "rule_changed" || !a.OrdinaryRecovery.BarrierAt.Equal(at) {
			t.Fatalf("configuration reused old observation: %+v", a.OrdinaryRecovery)
		}
	}
	restartedAt := testNow.Add(3*time.Minute + time.Second)
	newFacts := qualifyingOrdinaryFacts(restartedAt.Add(6 * time.Second))
	facts = append(facts, newFacts...)
	restartedAt = newFacts[len(newFacts)-1].CompletedAt
	advanceTestOrdinary(t, &a, s, facts, newFacts[len(newFacts)-1], restartedAt, false)
	if !a.OrdinaryRecovery.QualifiedSince.Equal(restartedAt) {
		t.Fatalf("new observation did not restart: %+v", a.OrdinaryRecovery)
	}
	fresh := ordinaryTestFact(300, testNow.Add(RecoveryLifetime), 95)
	facts = append(facts, fresh)
	advanceTestOrdinary(t, &a, s, facts, fresh, fresh.CompletedAt, false)
	if a.Phase != Cooling {
		t.Fatal("restored setting reused the original ten-minute clock")
	}
	fresh = ordinaryTestFact(301, restartedAt.Add(RecoveryLifetime), 95)
	facts = append(facts, fresh)
	advanceTestOrdinary(t, &a, s, facts, fresh, fresh.CompletedAt, false)
	if a.Phase != Healthy {
		t.Fatalf("fresh full observation did not recover: %+v", a)
	}
}

func TestSessionLowWindowUsesBoundedFactsAndExpiresAtFirstLoss(t *testing.T) {
	s := DefaultSettings()
	s.Mode = Protect
	s.MinValidRequests = 5
	a := AccountState{AccountID: 2, UpstreamKeyID: 3, Phase: Cooling, DegradedAt: testNow.Add(-time.Minute)}
	sig := PolicySignature(s)
	r := request("low-window-seed", "low-window-session", testNow)
	facts := make([]Evidence, 0, 6)
	for i := 0; i < 6; i++ {
		facts = append(facts, lowWindowFact(i, testNow.Add(-time.Duration(5-i)*time.Second), r, sig))
	}
	proof, err := sessionLowWindow(a, s, "stable-generation", SessionKey(r), WarmRouteKey(r), facts, testNow)
	if err != nil || proof == nil {
		t.Fatalf("proof=%+v err=%v", proof, err)
	}
	if !proof.ValidUntil.Equal(facts[0].CompletedAt.Add(time.Duration(s.WindowMinutes) * time.Minute)) {
		t.Fatalf("valid until=%s first=%s", proof.ValidUntil, facts[0].CompletedAt)
	}
	if !sessionLowWindowValid(proof, a, s, "stable-generation", proof.ValidUntil) || sessionLowWindowValid(proof, a, s, "stable-generation", proof.ValidUntil.Add(time.Nanosecond)) {
		t.Fatal("low window expiry boundary is not strict")
	}

	// A configured zero disables this additional migration signal while leaving
	// the original two-low route rule independent.
	zero := float64(0)
	s.SessionWindowMigrationThresholdPercent = &zero
	if got, err := sessionLowWindow(a, s, "stable-generation", SessionKey(r), WarmRouteKey(r), facts, testNow); err != nil || got != nil {
		t.Fatalf("explicit zero should disable proof: %+v %v", got, err)
	}
}

func TestAdvanceTrialCompletionSeparatesFailureFromRecoverySessions(t *testing.T) {
	s := DefaultSettings()
	s.Mode, s.MinValidRequests = Protect, 5
	a := AccountState{AccountID: 2, UpstreamKeyID: 3, Phase: Trial, CohortID: "cohort", FirstTrialAt: testNow.Add(-time.Minute), TrialValidRequests: 6, TrialSessions: 2, TrialCachePercent: 1}
	r := request("trial-failure", "trial-session", testNow)
	f := Evidence{Request: r, Admission: Admission{Sample: true, WarmAtStart: true, Trial: true, CohortID: "cohort", PolicySignature: PolicySignature(s)}, Success: true, CompletedAt: testNow, RawUsage: RawUsage{Status: upstreamcacheusage.Known, TotalInputTokens: 100, CacheReadTokens: 1}}
	advanceTrialCompletion(&a, s, f, testNow)
	if a.Phase != Cooling || a.CohortID != "" {
		t.Fatalf("low trial did not end promptly: %+v", a)
	}

	s.RecoveryMinSessions = 2
	a = AccountState{AccountID: 2, UpstreamKeyID: 3, Phase: Trial, CohortID: "cohort", FirstTrialAt: testNow.Add(-RecoveryLifetime), TrialValidRequests: 6, TrialSessions: 2, TrialCachePercent: 90}
	f.RawUsage.CacheReadTokens = 90
	advanceTrialCompletion(&a, s, f, testNow)
	if a.Phase != Healthy {
		t.Fatalf("configured recovery sessions did not apply: %+v", a)
	}
}
