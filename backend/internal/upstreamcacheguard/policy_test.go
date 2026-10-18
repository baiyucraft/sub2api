package upstreamcacheguard

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/upstreamcacheusage"
)

func TestDefaultEvidenceExpiryPreservesReleasedPolicyIdentity(t *testing.T) {
	e, _ := testEngine(t)
	s := e.Settings()
	legacy := s
	legacy.ProtectionEvidenceExpiryMinutes = 0
	signature := digest(fmt.Sprintf("cache-policy-v1:%g:%g:%d", s.ThresholdPercent, s.RecoveryThresholdPercent, s.MinValidRequests))
	if PolicySignature(s) != signature || PolicySignature(legacy) != signature || !projectionSettingsCompatible(s, legacy) {
		t.Fatal("default expiry changed released policy identity")
	}
	anchor := testNow.Add(-time.Minute)
	state := AccountState{AccountID: 2, UpstreamKeyID: 3, Policies: map[string]PolicyProjection{
		signature: {Settings: legacy, State: AccountState{AccountID: 2, UpstreamKeyID: 3, Phase: Trial,
			PolicySignature: signature, CohortID: "released-cohort", LastEvidenceAt: anchor}},
	}}
	e.preparePolicies(&state)
	if p := state.Policies[signature]; p.State.Phase != Trial || p.State.CohortID != "released-cohort" {
		t.Fatalf("upgrade replaced released trial: %+v", p)
	}
	// A persisted projection without the new field must expire on read too.
	legacySnapshot := AccountSnapshot{AccountState: AccountState{Policies: map[string]PolicyProjection{
		signature: {Settings: legacy, State: AccountState{Phase: Cooling, LastEvidenceAt: testNow.Add(-16 * time.Minute)}},
	}}, LoadedAt: testNow}
	got := projectAccountSnapshot(e.accountSnapshotAtLocked("2:3", legacySnapshot, testNow), s)
	if got.Phase != Unknown {
		t.Fatalf("legacy projection zero expiry skipped expiration: %+v", got)
	}
	custom := s
	custom.ProtectionEvidenceExpiryMinutes = 2
	if PolicySignature(custom) == signature {
		t.Fatal("different expiry reused incompatible policy identity")
	}
}

func TestPolicySharedFactsSeparateThresholdsAndDisabledSampling(t *testing.T) {
	e, _ := testEngine(t)
	global := e.Settings()
	global.MinValidRequests = 2
	if err := e.ApplySettings(global); err != nil {
		t.Fatal(err)
	}
	loose := global
	loose.ThresholdPercent = 50
	disabled := global
	disabled.Mode = Disabled
	if err := e.ApplyPolicySettings(map[int64]Settings{2: loose, 3: disabled}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		r := request(fmt.Sprint(i), "passive", testNow.Add(-time.Minute+time.Duration(i)*time.Second))
		r.GroupID = 3
		a := admit(t, e, r)
		if a.ProtectedAtStart || a.Trial || !a.Sample {
			t.Fatalf("disabled group admission=%+v", a)
		}
		a.WarmAtStart = true
		complete(t, e, r, a, 60, true, upstreamcacheusage.Known)
	}
	if err := e.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	strict, _ := e.CachedAccountForGroup(1, 2, 3)
	looseView, _ := e.CachedAccountForGroup(2, 2, 3)
	shared, _ := e.CachedSharedAccount(2, 3)
	if strict.Phase != Cooling || looseView.Phase != Healthy || strict.ValidRequests != 3 || looseView.ValidRequests != 3 || shared.CachePercent != 60 {
		t.Fatalf("strict=%+v loose=%+v shared=%+v", strict, looseView, shared)
	}
	rotated, _ := e.CachedAccountForGroup(1, 2, 4)
	if rotated.Phase != Healthy || rotated.ValidRequests != 0 {
		t.Fatalf("new Key reused evidence=%+v", rotated)
	}
}

func TestCustomPolicyProtectsWhenGlobalDisabled(t *testing.T) {
	e, _ := testEngine(t)
	global := e.Settings()
	global.Mode = Disabled
	global.MinValidRequests = 1
	if err := e.ApplySettings(global); err != nil {
		t.Fatal(err)
	}
	custom := global
	custom.Mode = Protect
	if err := e.ApplyPolicySettings(map[int64]Settings{2: custom}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		r := request(fmt.Sprint(i), "other-group", testNow.Add(-time.Minute+time.Duration(i)*time.Second))
		a := admit(t, e, r)
		a.WarmAtStart = true
		complete(t, e, r, a, 0, true, upstreamcacheusage.Known)
	}
	if err := e.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	view := e.CachedSnapshot(2)
	if !view.Available || view.Mode != Protect || len(view.Accounts) != 1 || view.Accounts[0].Phase != Cooling {
		t.Fatalf("custom global-disabled=%+v", view)
	}
	if e.ResolvedSettings(1).Mode != Disabled {
		t.Fatal("inherit group unexpectedly protects")
	}
}

func TestPolicyRecoveryKeepsSharedFactsAndOtherCohort(t *testing.T) {
	e, _ := testEngine(t)
	global := e.Settings()
	global.MinValidRequests = 2
	if err := e.ApplySettings(global); err != nil {
		t.Fatal(err)
	}
	custom := global
	custom.ThresholdPercent = 50
	custom.RecoveryThresholdPercent = 90
	if err := e.ApplyPolicySettings(map[int64]Settings{2: custom, 3: global}); err != nil {
		t.Fatal(err)
	}
	strictSig, customSig := PolicySignature(global), PolicySignature(custom)
	a := AccountState{AccountID: 2, UpstreamKeyID: 3, Policies: map[string]PolicyProjection{
		strictSig: {Settings: global, State: AccountState{AccountID: 2, UpstreamKeyID: 3, PolicySignature: strictSig, Phase: Trial, CohortID: "strict-cohort", FirstTrialAt: testNow.Add(-11 * time.Minute)}},
		customSig: {Settings: custom, State: AccountState{AccountID: 2, UpstreamKeyID: 3, PolicySignature: customSig, Phase: Trial, CohortID: "custom-cohort", FirstTrialAt: testNow.Add(-11 * time.Minute)}},
	}}
	var facts []Evidence
	for i := 0; i < 3; i++ {
		r := request(fmt.Sprint(i), fmt.Sprint(i), testNow.Add(-time.Minute))
		r.GroupID = int64(i%2*2 + 1)
		facts = append(facts, Evidence{ID: r.ID, Request: r, Admission: Admission{Sample: true, WarmAtStart: true, Trial: true, CohortID: "strict-cohort", PolicySignature: strictSig, SessionKey: fmt.Sprint(i)}, Success: true, CompletedAt: testNow.Add(-time.Second), RawUsage: RawUsage{Status: upstreamcacheusage.Known, TotalInputTokens: 100, CacheReadTokens: 85}})
	}
	if err := e.recomputePolicies(&a, facts, testNow, true); err != nil {
		t.Fatal(err)
	}
	if a.Policies[strictSig].State.Phase != Healthy || a.Policies[customSig].State.Phase != Trial || a.Policies[customSig].State.TrialValidRequests != 0 || a.ValidRequests != 3 || a.CachePercent != 85 {
		t.Fatalf("recovery contaminated shared/foreign state=%+v", a)
	}
	if a.Policies[strictSig].State.QualitySince.IsZero() || !a.Policies[customSig].State.QualitySince.IsZero() {
		t.Fatal("reset boundary leaked")
	}
	if PolicySignature(global) != PolicySignature(e.ResolvedSettings(3)) {
		t.Fatal("identical cross-group policy split")
	}
	global.Priority = "ttft_first"
	if PolicySignature(global) != strictSig {
		t.Fatal("priority reset cohort")
	}
}

func TestNewCustomPolicyDoesNotBorrowLegacyTrial(t *testing.T) {
	e, _ := testEngine(t)
	global := e.Settings()
	custom := global
	custom.ThresholdPercent = 50
	if err := e.ApplyPolicySettings(map[int64]Settings{2: custom}); err != nil {
		t.Fatal(err)
	}
	a := AccountState{AccountID: 2, UpstreamKeyID: 3, Phase: Trial, CohortID: "legacy", FirstTrialAt: testNow.Add(-time.Hour), QualitySince: testNow.Add(-time.Hour)}
	e.preparePolicies(&a)
	if a.Policies[PolicySignature(global)].State.CohortID != "legacy" || a.Policies[PolicySignature(custom)].State.CohortID != "" || a.Policies[PolicySignature(custom)].State.Phase != Healthy {
		t.Fatalf("legacy trial migration=%+v", a)
	}
}

func TestPausedCompletionKeepsTerminalAndSharedFact(t *testing.T) {
	e, store := testEngine(t)
	r := request("paused", "paused-session", testNow.Add(-time.Second))
	a := admit(t, e, r)
	v := Evidence{ID: "paused-evidence", Request: r, Admission: a, ProtectionPaused: true, Success: true, CompletedAt: testNow, RawUsage: RawUsage{Status: upstreamcacheusage.Known, TotalInputTokens: 1000, CacheReadTokens: 900}}
	if err := e.Complete(context.Background(), v); err != nil {
		t.Fatal(err)
	}
	if err := store.TransactAccount(context.Background(), r.GroupID, r.AccountID, r.UpstreamKeyID, func(tx Tx) error {
		var session SessionState
		if _, err := tx.Read(SessionKind, a.SessionKey, &session); err != nil {
			return err
		}
		if !session.Admissions[admissionKey(r)].Completed {
			t.Fatal("scope pause left a durable incomplete admission")
		}
		facts, err := tx.(AccountTx).AccountFacts(r.AccountID, r.UpstreamKeyID, testNow.Add(-time.Minute))
		if err != nil {
			return err
		}
		if len(facts) != 1 || !facts[0].ProtectionPaused || facts[0].RawUsage.TotalInputTokens != 1000 {
			t.Fatalf("paused terminal fact was lost: %+v", facts)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPausedTrialFactsRemainRawAndCannotRecoverAfterReselection(t *testing.T) {
	s := DefaultSettings()
	s.Mode, s.MinValidRequests = Protect, 2
	a := AccountState{AccountID: 2, UpstreamKeyID: 3, Phase: Trial, CohortID: "cohort", PolicySignature: PolicySignature(s), FirstTrialAt: testNow.Add(-11 * time.Minute)}
	var facts []Evidence
	for i := 0; i < 3; i++ {
		r := request(fmt.Sprint(i), fmt.Sprint(i), testNow.Add(-time.Minute))
		facts = append(facts, Evidence{ID: r.ID, Request: r, Admission: Admission{Sample: true, WarmAtStart: true, Trial: true, CohortID: "cohort", PolicySignature: a.PolicySignature, SessionKey: fmt.Sprint(i)}, ProtectionPaused: true, Success: true, CompletedAt: testNow.Add(-time.Second), RawUsage: RawUsage{Status: upstreamcacheusage.Known, TotalInputTokens: 1000, CacheReadTokens: 900}})
	}
	if err := RecomputeAccount(&a, facts, s, testNow, true); err != nil {
		t.Fatal(err)
	}
	if a.Phase != Trial || a.ValidRequests != 3 || a.CachePercent != 90 || a.TrialValidRequests != 0 || a.TrialSessions != 0 {
		t.Fatalf("paused raw facts entered recovery: %+v", a)
	}
}

func TestSamePolicyGroupsShareRecoveryAdmissions(t *testing.T) {
	e, store := testEngine(t)
	s := e.Settings()
	s.TrialPercent = 100
	if err := e.ApplySettings(s); err != nil {
		t.Fatal(err)
	}
	if err := e.ApplyPolicySettings(map[int64]Settings{2: s}); err != nil {
		t.Fatal(err)
	}
	if err := store.TransactAccount(context.Background(), 1, 2, 3, func(tx Tx) error {
		_, err := tx.(AccountTx).WriteAccount(2, 3, AccountState{AccountID: 2, UpstreamKeyID: 3, Phase: Cooling, CooldownUntil: testNow.Add(-time.Second)})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	first := request("trial-first", "first", testNow)
	second := request("trial-second", "second", testNow.Add(time.Second))
	second.GroupID = 2
	a, b := admit(t, e, first), admit(t, e, second)
	if !a.Trial || !b.Trial || a.CohortID == "" || a.CohortID != b.CohortID || a.PolicySignature != b.PolicySignature {
		t.Fatalf("same strategy trial split: %+v %+v", a, b)
	}
}

func TestSessionPolicyChangeDoesNotReuseAffinityOrLowStreak(t *testing.T) {
	for _, prior := range []struct {
		name          string
		high, pending bool
		threshold     float64
	}{
		{name: "stricter does not reuse earlier high cache", high: true, threshold: 90},
		{name: "looser does not reuse earlier low streak", pending: true, threshold: 50},
	} {
		t.Run(prior.name, func(t *testing.T) {
			e, store := testEngine(t)
			old := e.Settings()
			r := request("current", "stable", testNow)
			routeKey := WarmRouteKey(r)
			session := SessionState{Key: SessionKey(r), FirstSeenAt: testNow.Add(-time.Minute), LastAccountID: r.AccountID, LastModel: r.Model, LastProtocol: r.Protocol, LastRouteKey: routeKey,
				Routes: map[string]SessionRoute{routeKey: {AccountID: r.AccountID, UpstreamKeyID: r.UpstreamKeyID, PolicySignature: PolicySignature(old), LastSuccessfulAt: testNow.Add(-time.Second), HighCacheSeen: prior.high, PendingLow: prior.pending, LowStreak: 2}}}
			if err := store.Transact(context.Background(), r.GroupID, func(tx Tx) error {
				_, err := tx.Write(SessionKind, session.Key, session)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if err := e.Refresh(context.Background()); err != nil {
				t.Fatal(err)
			}
			changed := old
			changed.ThresholdPercent, changed.RecoveryThresholdPercent = prior.threshold, 95
			if err := e.ApplyPolicySettings(map[int64]Settings{r.GroupID: changed}); err != nil {
				t.Fatal(err)
			}
			read := e.CachedSessionRead(r)
			if !read.Known || !read.WarmAtStart || read.PendingLow || read.Protected || read.PreferredAccountID != r.AccountID {
				t.Fatalf("strategy reused old interpretation or lost physical warmth: %+v", read)
			}
			// A switch in ranking preference alone keeps the same quality rule.
			changed = old
			changed.Priority = "ttft_first"
			if err := e.ApplyPolicySettings(map[int64]Settings{r.GroupID: changed}); err != nil {
				t.Fatal(err)
			}
			read = e.CachedSessionRead(r)
			if read.PendingLow != prior.pending || read.Protected != (prior.high && !prior.pending) {
				t.Fatalf("priority change discarded affinity: %+v", read)
			}
		})
	}
}
