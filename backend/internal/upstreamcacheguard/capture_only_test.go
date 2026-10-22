package upstreamcacheguard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/upstreamcacheusage"
)

func captureAdmission(t *testing.T, e *Engine, r Request) Admission {
	t.Helper()
	a, err := e.CaptureOnly(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if !a.CaptureOnly || !a.Sample || a.Trial || a.CohortID != "" || a.ProtectedAtStart {
		t.Fatalf("invalid capture classification: %+v", a)
	}
	return a
}

func captureSession(t *testing.T, store *memoryStore, r Request) SessionState {
	t.Helper()
	var session SessionState
	if err := store.Transact(context.Background(), r.GroupID, func(tx Tx) error {
		_, err := tx.Read(SessionKind, SessionKey(r), &session)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return session
}

func TestCaptureOnlyFreezesWarmnessWithoutCreatingAffinityOrTrials(t *testing.T) {
	e, store := testEngine(t)
	settings := e.Settings()
	settings.TrialPercent = 100
	if err := e.ApplySettings(settings); err != nil {
		t.Fatal(err)
	}
	first := request("capture-first", "capture-session", testNow.Add(-time.Minute))
	a := captureAdmission(t, e, first)
	if !a.NaturalNew || a.WarmAtStart {
		t.Fatalf("first capture: %+v", a)
	}
	if again := captureAdmission(t, e, first); !reflect.DeepEqual(a, again) {
		t.Fatal("duplicate admission changed immutable classification")
	}
	v := complete(t, e, first, a, 95, true, upstreamcacheusage.Known)
	if v.ValidSample() {
		t.Fatal("cold capture counted as a warm sample")
	}
	if err := e.Complete(context.Background(), v); err != nil {
		t.Fatal(err)
	}
	session := captureSession(t, store, first)
	if session.LastAccountID != 0 || session.LastRouteKey != "" || !session.ResidenceUntil.IsZero() || session.NaturalAccountID != 0 || session.TrialAccountID != 0 || session.CohortID != "" {
		t.Fatalf("capture created routing decisions: %+v", session)
	}
	if !session.NaturalTrialSlot || session.NaturalTrialOrdinal != 1 || !session.NaturalTrialSuppressed {
		t.Fatalf("natural identity not reserved accurately: %+v", session)
	}
	plan, err := e.PlanTrial(context.Background(), first)
	if err != nil || plan.TrialSlot || plan.TrialOrdinal != 1 {
		t.Fatalf("capture slot reused: %+v, %v", plan, err)
	}
	// A different physical account within the same logical request cannot claim
	// the reserved natural slot after the capture-only attempt.
	retry := first
	retry.AccountID++
	retry.StartedAt = first.StartedAt.Add(2 * time.Second)
	if retried := admit(t, e, retry); retried.Trial || retried.CohortID != "" {
		t.Fatalf("retry manufactured trial: %+v", retried)
	}
	second := request("capture-second", "capture-session", first.StartedAt.Add(5*time.Second))
	b := captureAdmission(t, e, second)
	if b.NaturalNew || !b.WarmAtStart {
		t.Fatalf("successful cold completion did not warm route: %+v", b)
	}
	complete(t, e, second, b, 90, true, upstreamcacheusage.Known)
	// An older completion cannot regress the physical route clock.
	late := request("capture-late", "capture-session", first.StartedAt.Add(2*time.Second))
	lateAdmission := captureAdmission(t, e, late)
	if lateAdmission.WarmAtStart {
		t.Fatal("future physical success became historical warm evidence")
	}
	complete(t, e, late, lateAdmission, 20, true, upstreamcacheusage.Known)
	session = captureSession(t, store, first)
	route := session.Routes[WarmRouteKey(first)]
	if !route.LastSuccessfulAt.Equal(second.StartedAt.Add(time.Second)) || route.HighCacheSeen || route.PendingLow || route.LowStreak != 0 {
		t.Fatalf("capture changed interpreted route state: %+v", route)
	}
	var group GroupState
	if err := store.Transact(context.Background(), first.GroupID, func(tx Tx) error {
		_, err := tx.Read(GroupKind, "quota", &group)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if group.NaturalNewCount != 1 || group.TrialCount != 0 {
		t.Fatalf("capture changed trial execution quota: %+v", group)
	}
}

func TestCaptureOnlyCompletionPreservesExistingPreferenceAndRecovery(t *testing.T) {
	for _, phase := range []Phase{Trial, Unknown, Healthy} {
		t.Run(string(phase), func(t *testing.T) {
			e, store := testEngine(t)
			settings := e.Settings()
			settings.MinValidRequests = 1
			if err := e.ApplySettings(settings); err != nil {
				t.Fatal(err)
			}
			r := request("capture-preserve", "preserve-session", testNow.Add(-time.Second))
			sig := PolicySignature(settings)
			anchor := testNow.Add(-5 * time.Minute)
			state := AccountState{AccountID: r.AccountID, UpstreamKeyID: r.UpstreamKeyID, Phase: phase, CohortID: "existing-cohort", PolicySignature: sig, FirstTrialAt: testNow.Add(-11 * time.Minute), LastEvidenceAt: anchor, LastDegradedEvidenceAt: &anchor}
			state.Policies = map[string]PolicyProjection{sig: {Settings: settings, State: state}}
			previous := SessionState{Key: SessionKey(r), FirstSeenAt: testNow.Add(-time.Hour), LastAccountID: 99, LastRouteKey: "previous-route", LastCompletedAt: anchor, ResidenceUntil: testNow.Add(time.Minute), TrialAccountID: 99, CohortID: "previous-cohort", Routes: map[string]SessionRoute{WarmRouteKey(r): {AccountID: r.AccountID, UpstreamKeyID: r.UpstreamKeyID, LastSuccessfulAt: testNow.Add(-time.Minute), PolicySignature: "previous-policy", HighCacheSeen: true, PendingLow: true, LowStreak: 2}}}
			if err := store.Transact(context.Background(), r.GroupID, func(tx Tx) error {
				if _, err := tx.(AccountTx).WriteAccount(r.AccountID, r.UpstreamKeyID, &state); err != nil {
					return err
				}
				if _, err := tx.Write(SessionKind, SessionKey(r), &previous); err != nil {
					return err
				}
				for i := 0; i < 3; i++ {
					f := Evidence{ID: fmt.Sprintf("existing-trial-%d", i), Request: r, Admission: Admission{Sample: true, WarmAtStart: true, Trial: true, CohortID: state.CohortID, PolicySignature: sig, SessionKey: fmt.Sprint(i)}, Success: true, RawUsage: RawUsage{Status: upstreamcacheusage.Known, TotalInputTokens: 100, CacheReadTokens: 90}, CompletedAt: testNow.Add(-time.Duration(i+2) * time.Second)}
					if phase == Healthy {
						f.RawUsage.CacheReadTokens = 0
					}
					if _, err := tx.InsertFact(f); err != nil {
						return err
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			a := captureAdmission(t, e, r)
			if !a.WarmAtStart {
				t.Fatal("durable physical route was ignored")
			}
			read := int64(90)
			if phase == Healthy {
				read = 0
			}
			complete(t, e, r, a, read, true, upstreamcacheusage.Known)
			got := captureSession(t, store, r)
			beforeRoute, afterRoute := previous.Routes[WarmRouteKey(r)], got.Routes[WarmRouteKey(r)]
			beforeRoute.LastSuccessfulAt, beforeRoute.LastCachePercent = afterRoute.LastSuccessfulAt, afterRoute.LastCachePercent
			if !reflect.DeepEqual(beforeRoute, afterRoute) || got.LastAccountID != previous.LastAccountID || got.LastRouteKey != previous.LastRouteKey || got.CohortID != previous.CohortID || got.TrialAccountID != previous.TrialAccountID || !got.ResidenceUntil.Equal(previous.ResidenceUntil) || !got.LastCompletedAt.Equal(previous.LastCompletedAt) {
				t.Fatalf("capture changed affinity or interpretation: %+v", got)
			}
			var account AccountState
			if err := store.Transact(context.Background(), r.GroupID, func(tx Tx) error {
				_, err := tx.(AccountTx).ReadAccount(r.AccountID, r.UpstreamKeyID, &account)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			projected := account.Policies[sig].State
			wantPhase := phase
			if phase == Healthy {
				wantPhase = Cooling
			}
			if projected.Phase != wantPhase || projected.ValidRequests != 4 || (phase == Trial && projected.TrialValidRequests != 3) {
				t.Fatalf("capture statistics or decision incorrect: %+v", projected)
			}
		})
	}
}

func TestCaptureOnlyAdmissionJSONCompatibilityAndCancellation(t *testing.T) {
	var legacy Admission
	if err := json.Unmarshal([]byte(`{"sample":true,"warm_at_start":true}`), &legacy); err != nil || legacy.CaptureOnly {
		t.Fatalf("legacy admission changed meaning: %+v, %v", legacy, err)
	}
	e, store := testEngine(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.CaptureOnly(ctx, request("cancelled", "cancelled", testNow)); err == nil {
		t.Fatal("cancelled capture was committed")
	}
	if len(store.rows) != 0 || len(store.accounts) != 0 {
		t.Fatal("cancelled capture changed durable state")
	}
}

type captureCommitFailureStore struct {
	*memoryStore
	fail bool
}

func (s *captureCommitFailureStore) TransactAccount(ctx context.Context, groupID, accountID, keyID int64, fn func(Tx) error) error {
	return s.Transact(ctx, groupID, func(tx Tx) error {
		if err := fn(tx); err != nil {
			return err
		}
		if s.fail {
			return errors.New("injected commit failure")
		}
		return nil
	})
}

func TestCompleteWithResultCountsOnlyCommittedNewFacts(t *testing.T) {
	e, memory := testEngine(t)
	store := &captureCommitFailureStore{memoryStore: memory}
	e.store = store
	r := request("result-capture", "result-capture", testNow.Add(-time.Minute))
	a := captureAdmission(t, e, r)
	v := Evidence{ID: "result-capture", Request: r, Admission: a, Success: true, CompletedAt: r.StartedAt.Add(time.Second), RawUsage: RawUsage{Status: upstreamcacheusage.Known, TotalInputTokens: 100, CacheReadTokens: 90}, CompletionReason: "success"}
	store.fail = true
	if inserted, err := e.CompleteWithResult(context.Background(), v); err == nil || inserted || len(memory.facts) != 0 {
		t.Fatalf("rolled-back completion was counted: inserted=%v err=%v facts=%d", inserted, err, len(memory.facts))
	}
	store.fail = false
	if inserted, err := e.CompleteWithResult(context.Background(), v); err != nil || !inserted {
		t.Fatalf("committed completion was not counted: inserted=%v err=%v", inserted, err)
	}
	if inserted, err := e.CompleteWithResult(context.Background(), v); err != nil || inserted {
		t.Fatalf("duplicate completion was counted again: inserted=%v err=%v", inserted, err)
	}
	for _, fact := range memory.facts {
		if !fact.Admission.CaptureOnly || fact.CompletionReason != v.CompletionReason {
			t.Fatalf("completion classification was not persisted: %+v", fact)
		}
	}
}

func TestCaptureOnlySplitStoreRestartAndConcurrentNaturalPlanning(t *testing.T) {
	e, _ := testEngine(t)
	store := newSplitMemoryStore()
	e.store = store
	r := request("split-capture", "split-capture", testNow.Add(-time.Minute))
	var wg sync.WaitGroup
	errors := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, err := e.CaptureOnly(context.Background(), r)
		errors <- err
	}()
	go func() {
		defer wg.Done()
		_, err := e.PlanTrial(context.Background(), r)
		errors <- err
	}()
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	a := captureAdmission(t, e, r)
	v := complete(t, e, r, a, 95, true, upstreamcacheusage.Known)
	if len(store.admissions) != 1 {
		t.Fatalf("concurrent planning duplicated admission: %d", len(store.admissions))
	}
	for _, persisted := range store.admissions {
		if !persisted.CaptureOnly || !persisted.Completed {
			t.Fatalf("split completion erased frozen capture mode: %+v", persisted)
		}
	}
	restarted := NewEngine(store)
	restarted.now = e.now
	if err := restarted.ApplySettings(e.Settings()); err != nil {
		t.Fatal(err)
	}
	if inserted, err := restarted.CompleteWithResult(context.Background(), v); err != nil || inserted {
		t.Fatalf("restart replay was not idempotent: inserted=%v, err=%v", inserted, err)
	}
	next := r
	next.ID = "after-restart"
	next.StartedAt = r.StartedAt.Add(3 * time.Second)
	b := captureAdmission(t, restarted, next)
	if !b.WarmAtStart || b.NaturalNew {
		t.Fatalf("restart lost captured session identity: %+v", b)
	}
	var group GroupState
	if err := store.Transact(context.Background(), r.GroupID, func(tx Tx) error {
		_, err := tx.Read(GroupKind, "quota", &group)
		return err
	}); err != nil || group.NaturalNewCount != 1 || group.TrialCount != 0 {
		t.Fatalf("natural planning diverged: %+v, %v", group, err)
	}
}

func TestCaptureOnlyNewEvidenceCanDegradeUnknownButCannotRecoverInitialProjection(t *testing.T) {
	e, _ := testEngine(t)
	settings := e.Settings()
	settings.MinValidRequests = 1
	if err := e.ApplySettings(settings); err != nil {
		t.Fatal(err)
	}
	var facts []Evidence
	for i := 0; i < 2; i++ {
		facts = append(facts, Evidence{ID: fmt.Sprint(i), Request: request(fmt.Sprint(i), "capture-only", testNow.Add(-time.Minute)), Admission: Admission{Sample: true, WarmAtStart: true, CaptureOnly: true}, Success: true, CompletedAt: testNow.Add(-time.Duration(i+1) * time.Second), RawUsage: RawUsage{Status: upstreamcacheusage.Known, TotalInputTokens: 100}})
	}
	account := AccountState{Phase: Unknown}
	if err := recomputeAccountWithRecovery(&account, facts, settings, testNow, true, false); err != nil || account.Phase != Cooling || account.ValidRequests != 2 {
		t.Fatalf("new capture evidence did not degrade accurately: %+v, %v", account, err)
	}
	for i := range facts {
		facts[i].RawUsage.CacheReadTokens = 95
	}
	sig := PolicySignature(settings)
	account = AccountState{Policies: map[string]PolicyProjection{sig: {Settings: settings, State: AccountState{Phase: Unknown}}}}
	if err := e.recomputePoliciesWithRecovery(&account, facts, testNow, false, false); err != nil {
		t.Fatal(err)
	}
	if projected := account.Policies[sig].State; projected.Phase != Unknown || projected.ValidRequests != 2 {
		t.Fatalf("initial projection manufactured recovery: %+v", projected)
	}
}

func TestCaptureOnlyBackgroundRefreshCannotRecoverInitialPolicyProjection(t *testing.T) {
	for _, phase := range []Phase{Unknown, Trial, Healthy} {
		t.Run(string(phase), func(t *testing.T) {
			e, store := testEngine(t)
			settings := e.Settings()
			settings.MinValidRequests = 1
			if err := e.ApplySettings(settings); err != nil {
				t.Fatal(err)
			}
			r := request("capture-refresh", "capture-refresh", testNow.Add(-time.Minute))
			sig := PolicySignature(settings)
			state := AccountState{AccountID: r.AccountID, UpstreamKeyID: r.UpstreamKeyID, Phase: phase, CohortID: "initial-cohort", PolicySignature: sig, FirstTrialAt: testNow.Add(-11 * time.Minute)}
			state.Policies = map[string]PolicyProjection{sig: {Settings: settings, State: state}}
			facts := make([]Evidence, 0, 4)
			for i := 0; i < 4; i++ {
				f := Evidence{ID: fmt.Sprintf("initial-%d", i), Request: r, Admission: Admission{Sample: true, WarmAtStart: true, Trial: i < 3, CaptureOnly: i == 3, CohortID: state.CohortID, PolicySignature: sig, SessionKey: fmt.Sprint(i)}, Success: true, CompletedAt: testNow.Add(-time.Duration(i+1) * time.Second), RawUsage: RawUsage{Status: upstreamcacheusage.Known, TotalInputTokens: 100, CacheReadTokens: 90}}
				facts = append(facts, f)
			}
			if err := store.Transact(context.Background(), r.GroupID, func(tx Tx) error {
				if _, err := tx.(AccountTx).WriteAccount(r.AccountID, r.UpstreamKeyID, &state); err != nil {
					return err
				}
				for _, f := range facts {
					if _, err := tx.InsertFact(f); err != nil {
						return err
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if err := e.Refresh(context.Background()); err != nil {
				t.Fatal(err)
			}
			got, available := e.CachedAccountForGroup(r.GroupID, r.AccountID, r.UpstreamKeyID)
			if !available || got.Phase != phase || got.ValidRequests != len(facts) {
				t.Fatalf("background replay changed recovery state: %+v, available=%v", got, available)
			}
			// A genuinely new ordinary warm completion still permits the normal
			// recovery transition; initialization itself is not that trigger.
			ordinary := facts[0]
			ordinary.ID, ordinary.CompletedAt = "ordinary-new", testNow
			facts = append(facts, ordinary)
			if err := e.recomputePoliciesWithRecovery(&state, facts, testNow, true, true); err != nil {
				t.Fatal(err)
			}
			if state.Policies[sig].State.Phase != Healthy {
				t.Fatalf("new ordinary evidence lost recovery: %+v", state.Policies[sig].State)
			}
		})
	}
}
