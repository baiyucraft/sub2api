package upstreamcacheguard

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/upstreamcacheusage"
)

var ordinaryTestGeneration = strings.Repeat("a", 64)

func ordinaryTestSettings() Settings {
	s := DefaultSettings()
	s.Mode, s.MinValidRequests = Protect, 5
	s.RecoveryThresholdPercent = 85
	return s
}

func ordinaryTestState(s Settings) AccountState {
	return AccountState{AccountID: 2, UpstreamKeyID: 3, Phase: Cooling,
		DegradedAt: testNow.Add(-20 * time.Minute), CooldownUntil: testNow.Add(-10 * time.Minute), PolicySignature: PolicySignature(s)}
}

func ordinaryTestFact(i int, at time.Time, read int64) Evidence {
	r := request(fmt.Sprintf("ordinary-%d-%d", i, at.UnixNano()), fmt.Sprintf("session-%d", i%3), at.Add(-time.Second))
	r.OrdinaryRecoverySessionID = fmt.Sprintf("%064x", i%3+1)
	r.OrdinaryRecoveryIdentityGeneration = ordinaryTestGeneration
	a := Admission{Sample: true, WarmAtStart: true, RequestID: r.ID, GroupID: r.GroupID, AccountID: r.AccountID,
		UpstreamKeyID: r.UpstreamKeyID, SessionKey: SessionKey(r), StartedAt: r.StartedAt,
		OrdinaryRecoverySessionID: r.OrdinaryRecoverySessionID, OrdinaryRecoveryIdentityGeneration: ordinaryTestGeneration}
	return Evidence{ID: r.ID, Request: r, Admission: a, Success: true, CompletedAt: at,
		RawUsage: RawUsage{Status: upstreamcacheusage.Known, TotalInputTokens: 100, CacheReadTokens: read}}
}

func advanceTestOrdinary(t *testing.T, a *AccountState, s Settings, facts []Evidence, current Evidence, now time.Time, gap bool) {
	t.Helper()
	if err := RecomputeAccount(a, facts, s, now, false); err != nil {
		t.Fatal(err)
	}
	if err := advanceOrdinaryRecovery(a, s, facts, current, now, ordinaryTestGeneration, "stable-policy", gap); err != nil {
		t.Fatal(err)
	}
}

func qualifyingOrdinaryFacts(at time.Time) []Evidence {
	var facts []Evidence
	for i := 0; i < 6; i++ {
		facts = append(facts, ordinaryTestFact(i, at.Add(time.Duration(i-6)*time.Second), 95))
	}
	return facts
}

func TestOrdinaryRecoveryNeedsFullObservationAndFreshCompletion(t *testing.T) {
	s := ordinaryTestSettings()
	a := ordinaryTestState(s)
	facts := qualifyingOrdinaryFacts(testNow)
	advanceTestOrdinary(t, &a, s, facts, facts[5], testNow, false)
	if a.Phase != Cooling || !a.OrdinaryRecovery.QualifiedSince.Equal(testNow) || a.OrdinaryRecovery.ValidRequests != 6 || a.OrdinaryRecovery.Sessions != 3 {
		t.Fatalf("initial qualification: %+v", a)
	}
	before := *a.OrdinaryRecovery
	view := OrdinaryRecoveryStatusAt(a, s, testNow.Add(10*time.Minute))
	if a.Phase != Cooling || !reflect.DeepEqual(before, *a.OrdinaryRecovery) || view.Reason != "awaiting_fresh_evidence" {
		t.Fatalf("read mutated/finished recovery: %+v %+v", a, view)
	}
	// A newly delivered late fact cannot turn elapsed time into fresh evidence.
	late := ordinaryTestFact(100, testNow.Add(-30*time.Second), 95)
	facts = append(facts, late)
	advanceTestOrdinary(t, &a, s, facts, late, testNow.Add(10*time.Minute), false)
	if a.Phase != Cooling || !a.OrdinaryRecovery.QualifiedSince.Equal(testNow) {
		t.Fatalf("late evidence recovered/reset qualification: %+v", a)
	}
	fresh := ordinaryTestFact(101, testNow.Add(10*time.Minute), 95)
	facts = append(facts, fresh)
	advanceTestOrdinary(t, &a, s, facts, fresh, testNow.Add(10*time.Minute), false)
	if a.Phase != Healthy || a.OrdinaryRecovery != nil || !a.QualitySince.Equal(testNow.Add(10*time.Minute)) || a.ValidRequests != 0 {
		t.Fatalf("ordinary completion failed recovery: %+v", a)
	}
	for _, f := range facts {
		if f.Admission.Trial || f.Admission.CohortID != "" {
			t.Fatal("ordinary recovery changed trial classification")
		}
	}
}

func TestOrdinaryRecoveryStrictCountsReliableSessionsAndSharedRate(t *testing.T) {
	s := ordinaryTestSettings()
	for _, tc := range []struct {
		name string
		edit func([]Evidence) []Evidence
		want string
	}{
		{"strict count", func(f []Evidence) []Evidence { return f[:5] }, "waiting_samples"},
		{"one identity across groups", func(f []Evidence) []Evidence {
			for i := range f {
				f[i].Request.GroupID = int64(i + 1)
				f[i].Request.OrdinaryRecoverySessionID = strings.Repeat("b", 64)
				f[i].Admission.OrdinaryRecoverySessionID = f[i].Request.OrdinaryRecoverySessionID
			}
			return f
		}, "waiting_sessions"},
		{"legacy identity", func(f []Evidence) []Evidence {
			for i := range f {
				f[i].Request.OrdinaryRecoverySessionID = ""
				f[i].Admission.OrdinaryRecoverySessionID = ""
			}
			return f
		}, "waiting_samples"},
		{"identity mismatch", func(f []Evidence) []Evidence {
			for i := range f {
				f[i].Admission.OrdinaryRecoverySessionID = strings.Repeat("c", 64)
			}
			return f
		}, "waiting_samples"},
		{"ordinary low", func(f []Evidence) []Evidence {
			for i := range f {
				f[i].RawUsage.CacheReadTokens = 84
			}
			return f
		}, "below_recovery_threshold"},
		{"bad trial not hidden", func(f []Evidence) []Evidence {
			bad := ordinaryTestFact(100, testNow, 0)
			bad.Admission.Trial = true
			bad.RawUsage.TotalInputTokens = 10000
			return append(f, bad)
		}, "shared_below_recovery_threshold"},
		{"capture only", func(f []Evidence) []Evidence {
			for i := range f {
				f[i].Admission.CaptureOnly = true
			}
			return f
		}, "waiting_samples"},
		{"paused", func(f []Evidence) []Evidence {
			for i := range f {
				f[i].ProtectionPaused = true
			}
			return f
		}, "waiting_samples"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := ordinaryTestState(s)
			facts := tc.edit(qualifyingOrdinaryFacts(testNow))
			current := ordinaryTestFact(200, testNow, 95)
			advanceTestOrdinary(t, &a, s, facts, current, testNow, false)
			if a.Phase != Cooling || !a.OrdinaryRecovery.QualifiedSince.IsZero() || a.OrdinaryRecovery.Reason != tc.want {
				t.Fatalf("got %+v", a.OrdinaryRecovery)
			}
		})
	}
}

func TestOrdinaryRecoveryFiveMinuteWindowCanQualifyForTenMinutes(t *testing.T) {
	s := ordinaryTestSettings()
	s.WindowMinutes = 5
	a := ordinaryTestState(s)
	var facts []Evidence
	for minute := 0; minute <= 10; minute++ {
		at := testNow.Add(time.Duration(minute) * time.Minute)
		for i := 0; i < 6; i++ {
			facts = append(facts, ordinaryTestFact(minute*6+i, at.Add(time.Duration(i-6)*time.Second), 95))
		}
		current := facts[len(facts)-1]
		// At the last instant require an actual completion beyond ten minutes.
		if minute == 10 {
			current = ordinaryTestFact(1000, at, 95)
			facts = append(facts, current)
		}
		advanceTestOrdinary(t, &a, s, facts, current, at, false)
		if minute < 10 && (a.Phase != Cooling || !a.OrdinaryRecovery.QualifiedSince.Equal(testNow)) {
			t.Fatalf("minute %d lost qualification: %+v", minute, a)
		}
	}
	if a.Phase != Healthy {
		t.Fatalf("five-minute window never recovered: %+v", a)
	}
}

func TestOrdinaryRecoveryFindsActualFirstLosingBoundary(t *testing.T) {
	s := ordinaryTestSettings()
	s.WindowMinutes = 5
	a := ordinaryTestState(s)
	p := &OrdinaryRecoveryState{IdentityGeneration: ordinaryTestGeneration, BarrierAt: a.CooldownUntil}
	facts := qualifyingOrdinaryFacts(testNow)
	facts = append([]Evidence{ordinaryTestFact(90, testNow.Add(-time.Minute), 95)}, facts...)
	_, boundary, _, err := ordinaryWindow(a, s, p, facts, testNow)
	if err != nil {
		t.Fatal(err)
	}
	// The extra oldest request can expire without losing the six required ones.
	want := facts[1].CompletedAt.Add(5 * time.Minute)
	if !boundary.Equal(want) {
		t.Fatalf("boundary=%s want=%s", boundary, want)
	}
	advanceTestOrdinary(t, &a, s, facts, facts[len(facts)-1], testNow, false)
	newNow := testNow.Add(6 * time.Minute)
	facts = qualifyingOrdinaryFacts(newNow)
	advanceTestOrdinary(t, &a, s, facts, facts[len(facts)-1], newNow, false)
	if !a.OrdinaryRecovery.QualifiedSince.Equal(newNow) {
		t.Fatal("qualification crossed an unobserved empty window")
	}
}

func TestOrdinaryRecoveryBoundaryIncludesBothRatesAndSessionExpiry(t *testing.T) {
	s := ordinaryTestSettings()
	s.WindowMinutes = 5
	for _, kind := range []string{"ordinary rate", "shared rate", "session"} {
		t.Run(kind, func(t *testing.T) {
			a := ordinaryTestState(s)
			p := &OrdinaryRecoveryState{IdentityGeneration: ordinaryTestGeneration, BarrierAt: a.CooldownUntil}
			facts := qualifyingOrdinaryFacts(testNow)
			// Keep enough recent requests; expiry must be caused by the named
			// rate/session constraint rather than the strict request threshold.
			for i := 6; i < 12; i++ {
				facts = append(facts, ordinaryTestFact(i, testNow.Add(-time.Second), 95))
			}
			earliest := ordinaryTestFact(90, testNow.Add(-time.Minute), 100)
			switch kind {
			case "ordinary rate":
				for i := range facts {
					facts[i].RawUsage.CacheReadTokens = 80
				}
				earliest.RawUsage.TotalInputTokens = 10000
				earliest.RawUsage.CacheReadTokens = 10000
			case "shared rate":
				earliest.Admission.Trial = true
				earliest.RawUsage.TotalInputTokens = 20000
				earliest.RawUsage.CacheReadTokens = 20000
				bad := ordinaryTestFact(99, testNow, 0)
				bad.Admission.Trial = true
				bad.RawUsage.TotalInputTokens = 3000
				facts = append(facts, bad)
			case "session":
				for i := range facts {
					facts[i].Request.OrdinaryRecoverySessionID = fmt.Sprintf("%064x", i%2+1)
					facts[i].Admission.OrdinaryRecoverySessionID = facts[i].Request.OrdinaryRecoverySessionID
				}
				earliest.Request.OrdinaryRecoverySessionID = fmt.Sprintf("%064x", 3)
				earliest.Admission.OrdinaryRecoverySessionID = earliest.Request.OrdinaryRecoverySessionID
			}
			facts = append(facts, earliest)
			w, until, _, err := ordinaryWindow(a, s, p, facts, testNow)
			if err != nil {
				t.Fatal(err)
			}
			if w.reason(s) != "" || !until.Equal(earliest.CompletedAt.Add(5*time.Minute)) {
				t.Fatalf("%s reason=%s until=%s", kind, w.reason(s), until)
			}
		})
	}
}

func TestOrdinaryRecoveryFutureFactDoesNotAdvanceWatermark(t *testing.T) {
	s := ordinaryTestSettings()
	a := ordinaryTestState(s)
	facts := qualifyingOrdinaryFacts(testNow)
	future := ordinaryTestFact(100, testNow.Add(time.Minute), 95)
	facts = append(facts, future)
	advanceTestOrdinary(t, &a, s, facts, future, testNow, false)
	if !a.OrdinaryRecovery.QualifiedSince.IsZero() || a.OrdinaryRecovery.LastEvidenceAt.After(testNow) {
		t.Fatal("future fact advanced recovery")
	}
	current := ordinaryTestFact(101, testNow, 95)
	facts = append(facts, current)
	advanceTestOrdinary(t, &a, s, facts, current, testNow, false)
	if !a.OrdinaryRecovery.QualifiedSince.Equal(testNow) {
		t.Fatal("future fact stranded current progress")
	}
}

func TestOrdinaryRecoveryGapPauseRulesAndClockResetProgress(t *testing.T) {
	s := ordinaryTestSettings()
	for _, kind := range []string{"gap", "paused", "capture", "rule", "secret", "rollback", "low late"} {
		t.Run(kind, func(t *testing.T) {
			a := ordinaryTestState(s)
			facts := qualifyingOrdinaryFacts(testNow)
			advanceTestOrdinary(t, &a, s, facts, facts[5], testNow, false)
			now := testNow.Add(time.Minute)
			current := ordinaryTestFact(500, now, 95)
			facts = append(facts, current)
			identity, policy := ordinaryTestGeneration, "stable-policy"
			gap := false
			switch kind {
			case "gap":
				gap = true
			case "paused":
				current.ProtectionPaused = true
			case "capture":
				current.Admission.CaptureOnly = true
			case "rule":
				policy = "changed"
			case "secret":
				identity = strings.Repeat("d", 64)
			case "rollback":
				now = testNow.Add(-time.Second)
			case "low late":
				current = ordinaryTestFact(500, testNow.Add(-30*time.Second), 0)
				current.RawUsage.TotalInputTokens = 100000
				facts = append(facts, current)
			}
			if err := advanceOrdinaryRecovery(&a, s, facts, current, now, identity, policy, gap); err != nil {
				t.Fatal(err)
			}
			if a.Phase != Cooling || !a.OrdinaryRecovery.QualifiedSince.IsZero() {
				t.Fatalf("%s preserved progress: %+v", kind, a.OrdinaryRecovery)
			}
		})
	}
}

type ordinaryMemoryTx struct {
	AccountTx
	gap bool
}

func (t *ordinaryMemoryTx) AccountRecoveryFacts(account, key int64, since, at time.Time) (AccountRecoveryFacts, error) {
	facts, err := t.AccountFacts(account, key, since)
	return AccountRecoveryFacts{Facts: facts, HasPendingGap: t.gap}, err
}

type ordinaryMemoryStore struct {
	*memoryStore
	gap bool
}

func (s *ordinaryMemoryStore) TransactAccount(ctx context.Context, group, account, key int64, fn func(Tx) error) error {
	return s.memoryStore.TransactAccount(ctx, group, account, key, func(tx Tx) error { return fn(&ordinaryMemoryTx{AccountTx: tx.(AccountTx), gap: s.gap}) })
}

func TestOrdinaryRecoveryCompletionIntegrationAndDuplicate(t *testing.T) {
	s := ordinaryTestSettings()
	store := &ordinaryMemoryStore{memoryStore: newMemoryStore()}
	now := testNow
	e := NewEngine(store)
	if err := e.ApplySettings(s); err != nil {
		t.Fatal(err)
	}
	e.now = func() time.Time { return now }
	e.SetOrdinaryRecoveryIdentityGeneration(ordinaryTestGeneration)
	e.SetOrdinaryRecoveryPolicyGenerations(map[string]string{PolicySignature(s): "stable-policy"})
	state := ordinaryTestState(s)
	if err := store.TransactAccount(context.Background(), 1, 2, 3, func(tx Tx) error { _, err := tx.(AccountTx).WriteAccount(2, 3, state); return err }); err != nil {
		t.Fatal(err)
	}
	var last Evidence
	for i := 0; i < 6; i++ {
		last = ordinaryTestFact(i, testNow.Add(time.Duration(i-6)*time.Second), 95)
		if err := e.Complete(context.Background(), last); err != nil {
			t.Fatal(err)
		}
	}
	now = testNow.Add(10 * time.Minute)
	if inserted, err := e.CompleteWithResult(context.Background(), last); err != nil || inserted {
		t.Fatalf("duplicate inserted=%v err=%v", inserted, err)
	}
	last = ordinaryTestFact(100, now, 95)
	if err := e.Complete(context.Background(), last); err != nil {
		t.Fatal(err)
	}
	if err := store.TransactAccount(context.Background(), 1, 2, 3, func(tx Tx) error {
		var got AccountState
		if _, err := tx.(AccountTx).ReadAccount(2, 3, &got); err != nil {
			return err
		}
		if got.Policies[PolicySignature(s)].State.Phase != Healthy {
			t.Fatalf("complete did not recover: %+v", got.Policies)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestOrdinaryRecoverySnapshotsOwnProgress(t *testing.T) {
	s := ordinaryTestSettings()
	a := ordinaryTestState(s)
	a.OrdinaryRecovery = &OrdinaryRecoveryState{Reason: "waiting_samples"}
	a.Policies = map[string]PolicyProjection{"policy": {State: AccountState{OrdinaryRecovery: &OrdinaryRecoveryState{Reason: "waiting_sessions"}}}}
	b := cloneAccountSnapshot(AccountSnapshot{AccountState: a})
	b.OrdinaryRecovery.Reason = "changed"
	p := b.Policies["policy"]
	p.State.OrdinaryRecovery.Reason = "changed"
	if a.OrdinaryRecovery.Reason != "waiting_samples" || a.Policies["policy"].State.OrdinaryRecovery.Reason != "waiting_sessions" {
		t.Fatal("cloned snapshot exposes ordinary progress")
	}
}

func TestOrdinaryRecoveryReadReclassifiesExpiredWindowBeforeQualification(t *testing.T) {
	s := ordinaryTestSettings()
	s.WindowMinutes = 5
	for _, persistedReason := range []string{"waiting_sessions", "below_recovery_threshold", "shared_below_recovery_threshold", "waiting_samples"} {
		t.Run(persistedReason, func(t *testing.T) {
			a := ordinaryTestState(s)
			a.TotalInputTokens, a.CacheReadTokens = 600, 570
			a.OrdinaryRecovery = &OrdinaryRecoveryState{Version: 1, ValidRequests: 6, Sessions: 2,
				TotalInputTokens: 600, CacheReadTokens: 570, Reason: persistedReason, LastVerifiedAt: testNow}
			published := *a.OrdinaryRecovery
			// The SQL window has naturally expired; applying its current metrics
			// must not start or persist a qualification clock.
			at := testNow.Add(5*time.Minute + time.Nanosecond)
			(AccountWindow{}).Apply(&a, at)
			beforeRead := *a.OrdinaryRecovery
			view := OrdinaryRecoveryStatusAt(a, s, at)
			if view.Reason != "waiting_samples" || view.ValidRequests != 0 || view.Sessions != 0 || view.StartedAt != nil || view.ReadyAt != nil {
				t.Fatalf("stale statistical reason survived expiry: %+v", view)
			}
			if !reflect.DeepEqual(beforeRead, *a.OrdinaryRecovery) || !a.OrdinaryRecovery.QualifiedSince.IsZero() || !a.OrdinaryRecovery.LastVerifiedAt.Equal(published.LastVerifiedAt) {
				t.Fatal("read/window projection advanced persistent recovery")
			}
		})
	}
	for _, control := range []string{"sampling_gap", "paused", "waiting_identity", "identity_changed", "rule_changed", "cooldown"} {
		a := ordinaryTestState(s)
		if control == "cooldown" {
			a.CooldownUntil = testNow.Add(time.Minute)
		}
		a.OrdinaryRecovery = &OrdinaryRecoveryState{Version: 1, Reason: control}
		(AccountWindow{}).Apply(&a, testNow)
		if got := OrdinaryRecoveryStatusAt(a, s, testNow); got.Reason != control {
			t.Fatalf("control barrier %s replaced by metrics: %+v", control, got)
		}
	}
}

func TestOrdinaryRecoveryRestartAndSameTimestampCannotFinish(t *testing.T) {
	s := ordinaryTestSettings()
	a := ordinaryTestState(s)
	facts := qualifyingOrdinaryFacts(testNow)
	advanceTestOrdinary(t, &a, s, facts, facts[5], testNow, false)
	// Pending a full observation, normal restart retains the durable clock.
	raw, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	var restored AccountState
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	if !restored.OrdinaryRecovery.QualifiedSince.Equal(testNow) {
		t.Fatal("restart lost clock")
	}
	for minute := 1; minute <= 9; minute++ {
		current := ordinaryTestFact(100+minute, testNow.Add(time.Duration(minute)*time.Minute), 95)
		facts = append(facts, current)
		advanceTestOrdinary(t, &restored, s, facts, current, current.CompletedAt, false)
	}
	same := ordinaryTestFact(999, testNow.Add(9*time.Minute), 95)
	facts = append(facts, same)
	advanceTestOrdinary(t, &restored, s, facts, same, testNow.Add(10*time.Minute), false)
	if restored.Phase != Cooling {
		t.Fatal("same completed_at finished recovery")
	}
	current := ordinaryTestFact(1000, testNow.Add(10*time.Minute), 95)
	facts = append(facts, current)
	advanceTestOrdinary(t, &restored, s, facts, current, current.CompletedAt, false)
	if restored.Phase != Healthy {
		t.Fatalf("restart never recovered: %+v", restored)
	}
}

func TestOrdinaryRecoveryDifferentPoliciesUseSharedFactsIndependently(t *testing.T) {
	s := ordinaryTestSettings()
	strict := s
	strict.RecoveryThresholdPercent = 98
	looseState, strictState := ordinaryTestState(s), ordinaryTestState(strict)
	facts := qualifyingOrdinaryFacts(testNow)
	advanceTestOrdinary(t, &looseState, s, facts, facts[5], testNow, false)
	advanceTestOrdinary(t, &strictState, strict, facts, facts[5], testNow, false)
	if looseState.OrdinaryRecovery.QualifiedSince.IsZero() || !strictState.OrdinaryRecovery.QualifiedSince.IsZero() {
		t.Fatal("policies shared recovery clock")
	}
	current := ordinaryTestFact(100, testNow.Add(10*time.Minute), 95)
	facts = append(facts, current)
	advanceTestOrdinary(t, &looseState, s, facts, current, current.CompletedAt, false)
	advanceTestOrdinary(t, &strictState, strict, facts, current, current.CompletedAt, false)
	if looseState.Phase != Healthy || strictState.Phase != Cooling {
		t.Fatalf("loose=%s strict=%s", looseState.Phase, strictState.Phase)
	}
}

func TestOrdinaryRecoveryOptionalStoreAndObserveKeepOriginalTrialContract(t *testing.T) {
	s := ordinaryTestSettings()
	e := NewEngine(newMemoryStore())
	if err := e.ApplySettings(s); err != nil {
		t.Fatal(err)
	}
	e.SetOrdinaryRecoveryIdentityGeneration(ordinaryTestGeneration)
	e.SetOrdinaryRecoveryPolicyGenerations(map[string]string{PolicySignature(s): "stable-policy"})
	state := ordinaryTestState(s)
	account := AccountState{AccountID: 2, UpstreamKeyID: 3, Policies: map[string]PolicyProjection{PolicySignature(s): {Settings: s, State: state}}}
	facts := qualifyingOrdinaryFacts(testNow)
	if err := e.advanceOrdinaryPolicies(&account, facts, facts[5], testNow, false, false); err != nil {
		t.Fatal(err)
	}
	if account.Policies[PolicySignature(s)].State.OrdinaryRecovery != nil {
		t.Fatal("legacy store created ordinary progress")
	}
	s.Mode = Observe
	advanceTestOrdinary(t, &state, s, facts, facts[5], testNow, false)
	if state.Phase != Cooling || state.OrdinaryRecovery != nil || OrdinaryRecoveryStatusAt(state, s, testNow) != nil {
		t.Fatal("observe created recovery")
	}
}

func TestOrdinaryGenerationSettersInvalidateOnlyOnChange(t *testing.T) {
	e := NewEngine(newMemoryStore())
	before := e.PolicyVersion()
	e.SetOrdinaryRecoveryIdentityGeneration(ordinaryTestGeneration)
	if e.PolicyVersion() != before+1 {
		t.Fatal("identity change did not invalidate projections")
	}
	before = e.PolicyVersion()
	e.SetOrdinaryRecoveryIdentityGeneration(ordinaryTestGeneration)
	if e.PolicyVersion() != before {
		t.Fatal("identical identity invalidated projections")
	}
	e.SetOrdinaryRecoveryPolicyGenerations(map[string]string{"x": "one"})
	before = e.PolicyVersion()
	e.SetOrdinaryRecoveryPolicyGenerations(map[string]string{"x": "one"})
	if e.PolicyVersion() != before {
		t.Fatal("identical policy generation invalidated projections")
	}
	e.SetOrdinaryRecoveryPolicyGenerations(map[string]string{"x": "two"})
	if e.PolicyVersion() != before+1 {
		t.Fatal("policy generation change did not invalidate projections")
	}
}

type recoveryCommitGateStore struct {
	*memoryStore
	callbackDone chan struct{}
	allowCommit  chan struct{}
	committed    chan int
	engine       *Engine
	once         sync.Once
}

func (s *recoveryCommitGateStore) TransactAccount(ctx context.Context, group, account, key int64, fn func(Tx) error) error {
	err := s.memoryStore.TransactAccount(ctx, group, account, key, func(tx Tx) error {
		if err := fn(tx); err != nil {
			return err
		}
		s.once.Do(func() { close(s.callbackDone) })
		select {
		case <-s.allowCommit:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	if err == nil {
		s.committed <- s.engine.Settings().WindowMinutes
	}
	return err
}

func TestOrdinaryConfigurationLockIncludesStoreCommit(t *testing.T) {
	for _, kind := range []string{"bundle", "settings", "group policies"} {
		t.Run(kind, func(t *testing.T) {
			store := &recoveryCommitGateStore{memoryStore: newMemoryStore(), callbackDone: make(chan struct{}), allowCommit: make(chan struct{}), committed: make(chan int, 1)}
			e := NewEngine(store)
			store.engine = e
			e.now = func() time.Time { return testNow }
			s := ordinaryTestSettings()
			s.WindowMinutes = 5
			if err := e.ApplyRecoveryConfiguration(s, map[int64]Settings{1: s}, map[string]string{PolicySignature(s): "old"}); err != nil {
				t.Fatal(err)
			}
			before := e.PolicyVersion()
			completed := make(chan error, 1)
			go func() { completed <- e.Complete(context.Background(), ordinaryTestFact(1, testNow, 95)) }()
			select {
			case <-store.callbackDone:
			case <-time.After(2 * time.Second):
				t.Fatal("completion never reached commit gate")
			}
			changed := s
			changed.WindowMinutes = 10
			published := make(chan error, 1)
			started := make(chan struct{})
			go func() {
				close(started)
				switch kind {
				case "bundle":
					published <- e.ApplyRecoveryConfiguration(changed, map[int64]Settings{1: changed}, map[string]string{PolicySignature(changed): "new"})
				case "settings":
					published <- e.ApplySettings(changed)
				case "group policies":
					published <- e.ApplyPolicySettings(map[int64]Settings{1: changed})
				}
			}()
			<-started
			select {
			case err := <-published:
				close(store.allowCommit)
				t.Fatalf("configuration published before COMMIT: %v", err)
			case <-time.After(15 * time.Millisecond):
			}
			if e.PolicyVersion() != before || e.Settings().WindowMinutes != 5 {
				close(store.allowCommit)
				t.Fatal("partial configuration escaped commit gate")
			}
			close(store.allowCommit)
			if err := <-completed; err != nil {
				t.Fatal(err)
			}
			if got := <-store.committed; got != 5 {
				t.Fatalf("store committed with mixed settings: %d", got)
			}
			if err := <-published; err != nil {
				t.Fatal(err)
			}
			if e.PolicyVersion() != before+1 {
				t.Fatal("publication did not advance exactly one epoch")
			}
		})
	}
}

func TestOrdinaryConfigurationBundleIsAtomicOwnedAndNoOp(t *testing.T) {
	e := NewEngine(newMemoryStore())
	s := ordinaryTestSettings()
	s.WindowMinutes = 5
	policies := map[int64]Settings{1: s}
	generations := map[string]string{PolicySignature(s): "generation-5"}
	if err := e.ApplyRecoveryConfiguration(s, policies, generations); err != nil {
		t.Fatal(err)
	}
	before := e.PolicyVersion()
	if err := e.ApplyRecoveryConfiguration(s, policies, generations); err != nil {
		t.Fatal(err)
	}
	if e.PolicyVersion() != before {
		t.Fatal("identical bundle invalidated observations")
	}
	policies[1] = DefaultSettings()
	generations[PolicySignature(s)] = "mutated"
	_, generation := e.ordinaryGenerations(PolicySignature(s))
	if generation != "generation-5" || e.ResolvedSettings(1).WindowMinutes != 5 {
		t.Fatal("publication borrowed mutable caller maps")
	}
	invalid := s
	invalid.MinValidRequests = 0
	if err := e.ApplyRecoveryConfiguration(s, map[int64]Settings{1: invalid}, nil); err == nil {
		t.Fatal("invalid group accepted")
	}
	if e.PolicyVersion() != before {
		t.Fatal("invalid bundle partially published")
	}
	failed := make(chan string, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			current := s
			current.WindowMinutes = 5 + (i%2)*5
			if err := e.ApplyRecoveryConfiguration(current, map[int64]Settings{1: current}, map[string]string{PolicySignature(current): fmt.Sprintf("generation-%d", current.WindowMinutes)}); err != nil {
				failed <- err.Error()
				return
			}
		}
	}()
	for {
		e.ordinaryConfigMu.RLock()
		global, group := e.Settings(), e.ResolvedSettings(1)
		_, generation := e.ordinaryGenerations(PolicySignature(group))
		if global.WindowMinutes != group.WindowMinutes || generation != fmt.Sprintf("generation-%d", global.WindowMinutes) {
			e.ordinaryConfigMu.RUnlock()
			t.Fatalf("mixed publication: %d/%d/%s", global.WindowMinutes, group.WindowMinutes, generation)
		}
		e.ordinaryConfigMu.RUnlock()
		select {
		case <-done:
			select {
			case err := <-failed:
				t.Fatal(err)
			default:
			}
			return
		default:
		}
	}
}

func BenchmarkOrdinaryRecoveryWindow(b *testing.B) {
	s := ordinaryTestSettings()
	a := ordinaryTestState(s)
	p := &OrdinaryRecoveryState{IdentityGeneration: ordinaryTestGeneration, BarrierAt: a.CooldownUntil}
	facts := make([]Evidence, 1000)
	for i := range facts {
		facts[i] = ordinaryTestFact(i, testNow.Add(-time.Duration(i)*time.Millisecond), 95)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, _, err := ordinaryWindow(a, s, p, facts, testNow); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkOrdinaryRecoveryCompletionComparison(b *testing.B) {
	for _, size := range []int{100, 1000, 10000} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			s := ordinaryTestSettings()
			state := ordinaryTestState(s)
			facts := make([]Evidence, size)
			for i := range facts {
				facts[i] = ordinaryTestFact(i, testNow.Add(-time.Duration(size-i)*time.Millisecond), 95)
			}
			b.Run("trial_only", func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					a := state
					if err := RecomputeAccount(&a, facts, s, testNow, true); err != nil {
						b.Fatal(err)
					}
				}
			})
			b.Run("with_ordinary", func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					a := state
					if err := RecomputeAccount(&a, facts, s, testNow, true); err != nil {
						b.Fatal(err)
					}
					if err := advanceOrdinaryRecovery(&a, s, facts, facts[len(facts)-1], testNow, ordinaryTestGeneration, "stable-policy", false); err != nil {
						b.Fatal(err)
					}
				}
			})
		})
	}
}
