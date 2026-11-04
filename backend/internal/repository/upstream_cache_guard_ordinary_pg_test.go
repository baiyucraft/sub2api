//go:build cacheguardpg

package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/upstreamcacheusage"
	"github.com/Wei-Shaw/sub2api/internal/upstreamcacheguard"
	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

const ordinaryPGIdentity = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func ordinaryPGFixture(t *testing.T) (*sql.DB, context.Context, *upstreamCacheGuardRepository) {
	t.Helper()
	db, ctx := cacheGuardAdmissionPostgres(t)
	text, err := migrations.FS.ReadFile("301_upstream_cache_guard_admissions.sql")
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, string(text))
	require.NoError(t, err)
	return db, ctx, NewUpstreamCacheGuardRepository(db).(*upstreamCacheGuardRepository)
}

func ordinaryPGSettings() upstreamcacheguard.Settings {
	s := upstreamcacheguard.DefaultSettings()
	s.Mode, s.MinValidRequests = upstreamcacheguard.Protect, 5
	s.GroupIDs = []int64{8, 21}
	return s
}

func ordinaryPGEvidence(id int, at time.Time) upstreamcacheguard.Evidence {
	r := upstreamcacheguard.Request{ID: fmt.Sprintf("ordinary-pg-%d", id), LogicalSessionDigest: fmt.Sprintf("%064x", id%3+1), APIKeyID: 1,
		GroupID: []int64{8, 21}[id%2], AccountID: 2, UpstreamKeyID: 5, Model: "gpt-synthetic", Protocol: "openai", StartedAt: at.Add(-time.Second),
		OrdinaryRecoverySessionID: fmt.Sprintf("%064x", id%3+1), OrdinaryRecoveryIdentityGeneration: ordinaryPGIdentity}
	a := upstreamcacheguard.Admission{RequestID: r.ID, SessionKey: upstreamcacheguard.SessionKey(r), GroupID: r.GroupID, AccountID: r.AccountID,
		UpstreamKeyID: r.UpstreamKeyID, StartedAt: r.StartedAt, Sample: true, WarmAtStart: true, Mode: upstreamcacheguard.Protect,
		RouteKey: upstreamcacheguard.WarmRouteKey(r), OrdinaryRecoverySessionID: r.OrdinaryRecoverySessionID, OrdinaryRecoveryIdentityGeneration: ordinaryPGIdentity}
	return upstreamcacheguard.Evidence{ID: r.ID, Request: r, Admission: a, CompletedAt: at, Success: true,
		RawUsage: upstreamcacheusage.Evidence{Status: upstreamcacheusage.Known, TotalInputTokens: 100, CacheReadTokens: 95}}
}

func ordinaryPGInsertFact(t *testing.T, db *sql.DB, ctx context.Context, f upstreamcacheguard.Evidence, processed bool) {
	t.Helper()
	raw, err := json.Marshal(f)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO upstream_cache_guard_facts(group_id,evidence_id,account_id,upstream_key_id,completed_at,valid_sample,evidence_json,processed) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, f.Request.GroupID, f.ID, f.Request.AccountID, f.Request.UpstreamKeyID, f.CompletedAt, f.ValidSample(), string(raw), processed)
	require.NoError(t, err)
}

func ordinaryPGWriteState(t *testing.T, store *upstreamCacheGuardRepository, ctx context.Context, state upstreamcacheguard.AccountState) {
	t.Helper()
	require.NoError(t, store.TransactAccount(ctx, 8, state.AccountID, state.UpstreamKeyID, func(tx upstreamcacheguard.Tx) error {
		_, err := tx.(upstreamcacheguard.AccountTx).WriteAccount(state.AccountID, state.UpstreamKeyID, state)
		return err
	}))
}

func ordinaryPGReadState(t *testing.T, store *upstreamCacheGuardRepository, ctx context.Context) upstreamcacheguard.AccountState {
	t.Helper()
	var state upstreamcacheguard.AccountState
	require.NoError(t, store.TransactAccount(ctx, 8, 2, 5, func(tx upstreamcacheguard.Tx) error {
		_, err := tx.(upstreamcacheguard.AccountTx).ReadAccount(2, 5, &state)
		return err
	}))
	return state
}

func ordinaryPGEngine(t *testing.T, store *upstreamCacheGuardRepository, s upstreamcacheguard.Settings) *upstreamcacheguard.Engine {
	t.Helper()
	e := upstreamcacheguard.NewEngine(store)
	require.NoError(t, e.ApplySettings(s))
	require.NoError(t, e.ApplyPolicySettings(map[int64]upstreamcacheguard.Settings{8: s, 21: s}))
	e.SetOrdinaryRecoveryIdentityGeneration(ordinaryPGIdentity)
	e.SetOrdinaryRecoveryPolicyGenerations(map[string]string{upstreamcacheguard.PolicySignature(s): "durable-pg-policy"})
	return e
}

func ordinaryPGProtectedState(s upstreamcacheguard.Settings, at time.Time) upstreamcacheguard.AccountState {
	p := upstreamcacheguard.AccountState{AccountID: 2, UpstreamKeyID: 5, Phase: upstreamcacheguard.Cooling,
		PolicySignature: upstreamcacheguard.PolicySignature(s), DegradedAt: at.Add(-30 * time.Minute), CooldownUntil: at.Add(-20 * time.Minute)}
	return upstreamcacheguard.AccountState{AccountID: 2, UpstreamKeyID: 5, Phase: upstreamcacheguard.Healthy,
		Policies: map[string]upstreamcacheguard.PolicyProjection{upstreamcacheguard.PolicySignature(s): {Settings: s, State: p}}}
}

func TestCacheGuardOrdinaryPostgresSQLMatchesFrozenFactEligibility(t *testing.T) {
	db, ctx, store := ordinaryPGFixture(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	s := ordinaryPGSettings()
	state := ordinaryPGProtectedState(s, now)
	sig := upstreamcacheguard.PolicySignature(s)
	p := state.Policies[sig]
	p.State.QualitySince = now.Add(-10 * time.Minute)
	p.State.LegacyQualitySince = map[int64]time.Time{8: now.Add(-2 * time.Minute)}
	p.State.OrdinaryRecovery = &upstreamcacheguard.OrdinaryRecoveryState{IdentityGeneration: ordinaryPGIdentity, BarrierAt: now.Add(-5 * time.Minute)}
	state.Policies[sig] = p
	state.LegacyQualitySince = p.State.LegacyQualitySince
	ordinaryPGWriteState(t, store, ctx, state)
	var facts []upstreamcacheguard.Evidence
	for i := 0; i < 6; i++ {
		facts = append(facts, ordinaryPGEvidence(i, now.Add(-time.Duration(i+1)*time.Second)))
	}
	edits := []func(*upstreamcacheguard.Evidence){
		func(f *upstreamcacheguard.Evidence) {
			f.Admission.OrdinaryRecoverySessionID, f.Request.OrdinaryRecoverySessionID = "", ""
		},
		func(f *upstreamcacheguard.Evidence) { f.Admission.OrdinaryRecoverySessionID = strings.Repeat("b", 64) },
		func(f *upstreamcacheguard.Evidence) {
			f.Admission.OrdinaryRecoverySessionID, f.Request.OrdinaryRecoverySessionID = strings.Repeat("z", 64), strings.Repeat("z", 64)
		},
		func(f *upstreamcacheguard.Evidence) {
			f.Request.OrdinaryRecoveryIdentityGeneration, f.Admission.OrdinaryRecoveryIdentityGeneration = strings.Repeat("b", 64), strings.Repeat("b", 64)
		},
		func(f *upstreamcacheguard.Evidence) { f.Admission.CaptureOnly = true },
		func(f *upstreamcacheguard.Evidence) { f.ProtectionPaused = true },
		func(f *upstreamcacheguard.Evidence) { f.Admission.Trial = true },
		func(f *upstreamcacheguard.Evidence) {
			f.Request.StartedAt = p.State.OrdinaryRecovery.BarrierAt.Add(-time.Microsecond)
		},
		func(f *upstreamcacheguard.Evidence) {
			f.CompletedAt = p.State.OrdinaryRecovery.BarrierAt.Add(-time.Microsecond)
		},
		func(f *upstreamcacheguard.Evidence) { f.Request.UpstreamKeyID, f.Admission.UpstreamKeyID = 6, 6 },
		func(f *upstreamcacheguard.Evidence) { f.Request.AccountID, f.Admission.AccountID = 3, 3 },
		func(f *upstreamcacheguard.Evidence) { f.CompletedAt = p.State.QualitySince },
		func(f *upstreamcacheguard.Evidence) {
			f.Request.GroupID, f.Admission.GroupID = 8, 8
			f.CompletedAt = state.LegacyQualitySince[8]
		},
		func(f *upstreamcacheguard.Evidence) { f.CompletedAt = now.Add(time.Second) },
	}
	for i, edit := range edits {
		f := ordinaryPGEvidence(100+i, now.Add(-30*time.Second))
		edit(&f)
		facts = append(facts, f)
	}
	// Exact barrier equality is eligible, while the quality reset is strict.
	equal := ordinaryPGEvidence(200, p.State.OrdinaryRecovery.BarrierAt.Add(time.Second))
	equal.Request.StartedAt = p.State.OrdinaryRecovery.BarrierAt
	equal.Request.GroupID, equal.Admission.GroupID = 21, 21
	facts = append(facts, equal)
	for _, f := range facts {
		ordinaryPGInsertFact(t, db, ctx, f, true)
	}
	windows, err := store.AccountWindowsFor(ctx, []int64{2}, now.Add(-time.Hour), now)
	require.NoError(t, err)
	w := windows["2:5"].Policies[sig]
	wantCount, wantInput, wantRead := 0, int64(0), int64(0)
	sessions := map[string]bool{}
	for _, f := range facts {
		if f.Request.AccountID != 2 || f.Request.UpstreamKeyID != 5 || f.CompletedAt.After(now) || !f.CompletedAt.After(p.State.QualitySince) || (!state.LegacyQualitySince[f.Request.GroupID].IsZero() && !f.CompletedAt.After(state.LegacyQualitySince[f.Request.GroupID])) || !upstreamcacheguard.OrdinaryRecoveryFactEligible(f, p.State.OrdinaryRecovery) {
			continue
		}
		wantCount++
		wantInput += f.RawUsage.TotalInputTokens
		wantRead += f.RawUsage.CacheReadTokens
		sessions[f.Admission.OrdinaryRecoverySessionID] = true
	}
	require.Equal(t, 7, wantCount)
	require.Equal(t, wantCount, w.OrdinaryValidRequests)
	require.Equal(t, len(sessions), w.OrdinarySessions)
	require.Equal(t, int64(3), int64(w.OrdinarySessions), "same reliable identity across groups counts once")
	require.Equal(t, wantInput, w.OrdinaryInputTokens)
	require.Equal(t, wantRead, w.OrdinaryCacheReadTokens)
}

func TestCacheGuardOrdinaryPostgresConcurrentCompletionRestartAndRollback(t *testing.T) {
	db, ctx, store := ordinaryPGFixture(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	s := ordinaryPGSettings()
	ordinaryPGWriteState(t, store, ctx, ordinaryPGProtectedState(s, now))
	for i := 0; i < 6; i++ {
		ordinaryPGInsertFact(t, db, ctx, ordinaryPGEvidence(i, now.Add(-time.Duration(i+1)*time.Second)), true)
	}
	e := ordinaryPGEngine(t, store, s)
	current := ordinaryPGEvidence(90, now)
	inserted, err := e.CompleteWithResult(ctx, current)
	require.NoError(t, err)
	require.True(t, inserted)
	state := ordinaryPGReadState(t, store, ctx)
	sig := upstreamcacheguard.PolicySignature(s)
	p := state.Policies[sig]
	require.NotNil(t, p.State.OrdinaryRecovery)
	require.False(t, p.State.OrdinaryRecovery.QualifiedSince.IsZero())
	// Preserve the exact durable metadata across a fresh engine. Advancing the
	// fixture's verified interval avoids a real ten-minute test sleep.
	p.State.OrdinaryRecovery.QualifiedSince = now.Add(-11 * time.Minute)
	p.State.OrdinaryRecovery.QualifiedUntil = now.Add(5 * time.Minute)
	state.Policies[sig] = p
	ordinaryPGWriteState(t, store, ctx, state)
	restarted := ordinaryPGEngine(t, store, s)

	const completions = 8
	var wg sync.WaitGroup
	errors := make(chan error, completions)
	for i := 0; i < completions; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			f := ordinaryPGEvidence(1000+i, time.Now().UTC().Add(-time.Millisecond))
			_, err := restarted.CompleteWithResult(ctx, f)
			errors <- err
		}(i)
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	state = ordinaryPGReadState(t, store, ctx)
	require.Equal(t, upstreamcacheguard.Healthy, state.Policies[sig].State.Phase)
	require.Nil(t, state.Policies[sig].State.OrdinaryRecovery)
	var before, trials int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*),count(*) FILTER(WHERE evidence_json->'admission'->>'trial'='true') FROM upstream_cache_guard_facts`).Scan(&before, &trials))
	require.Equal(t, 6+1+completions, before)
	require.Zero(t, trials)
	inserted, err = restarted.CompleteWithResult(ctx, current)
	require.NoError(t, err)
	require.False(t, inserted)
	var after int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM upstream_cache_guard_facts`).Scan(&after))
	require.Equal(t, before, after)

	beforeState, err := json.Marshal(state)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `CREATE FUNCTION reject_ordinary_account_write() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic rollback'; END $$; CREATE TRIGGER reject_ordinary_account_write BEFORE UPDATE ON upstream_cache_guard_accounts FOR EACH ROW EXECUTE FUNCTION reject_ordinary_account_write()`)
	require.NoError(t, err)
	failed := ordinaryPGEvidence(9999, time.Now().UTC())
	inserted, err = restarted.CompleteWithResult(ctx, failed)
	require.Error(t, err)
	require.False(t, inserted)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM upstream_cache_guard_facts`).Scan(&after))
	require.Equal(t, before, after, "fact insertion and admission acknowledgement roll back with state")
	var failedAdmissions int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM upstream_cache_guard_admissions WHERE admission_json->>'request_id'=$1`, failed.Request.ID).Scan(&failedAdmissions))
	require.Zero(t, failedAdmissions, "rolled-back completion must not leave an acknowledged admission")
	afterState, err := json.Marshal(ordinaryPGReadState(t, store, ctx))
	require.NoError(t, err)
	require.JSONEq(t, string(beforeState), string(afterState))
}

func TestCacheGuardOrdinaryPostgresPendingGapIsAccountScoped(t *testing.T) {
	db, ctx, store := ordinaryPGFixture(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	read := func() upstreamcacheguard.AccountRecoveryFacts {
		var view upstreamcacheguard.AccountRecoveryFacts
		require.NoError(t, store.TransactAccount(ctx, 8, 2, 5, func(tx upstreamcacheguard.Tx) error {
			var err error
			view, err = tx.(upstreamcacheguard.RecoveryFactsTx).AccountRecoveryFacts(2, 5, now.Add(-time.Hour), now)
			return err
		}))
		return view
	}
	other := ordinaryPGEvidence(1, now.Add(-time.Minute))
	other.Request.AccountID, other.Admission.AccountID = 3, 3
	ordinaryPGInsertFact(t, db, ctx, other, false)
	require.False(t, read().HasPendingGap)
	recent := ordinaryPGEvidence(2, now.Add(-time.Second))
	ordinaryPGInsertFact(t, db, ctx, recent, false)
	require.False(t, read().HasPendingGap)
	_, err := db.ExecContext(ctx, `INSERT INTO upstream_cache_guard_admissions(group_id,session_key,admission_key,account_id,upstream_key_id,started_at,admission_json) VALUES(8,'synthetic','unfinished',2,5,$1,'{}')`, now.Add(-time.Hour))
	require.NoError(t, err)
	require.False(t, read().HasPendingGap, "an unfinished upstream request is not a delayed completion")
	delayed := ordinaryPGEvidence(3, now.Add(-6*time.Second))
	ordinaryPGInsertFact(t, db, ctx, delayed, false)
	require.True(t, read().HasPendingGap)
	_, err = db.ExecContext(ctx, `UPDATE upstream_cache_guard_facts SET processed=TRUE WHERE evidence_id=$1`, delayed.ID)
	require.NoError(t, err)
	view := read()
	require.False(t, view.HasPendingGap)
	require.Len(t, view.Facts, 1)
}

func TestCacheGuardTrialPostgresFailureEndsCohortAndIgnoresLateCompletion(t *testing.T) {
	db, ctx, store := ordinaryPGFixture(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	s := ordinaryPGSettings()
	s.RecoveryMinSessions = 100
	sig := upstreamcacheguard.PolicySignature(s)
	const cohort = "current-low-trial"
	state := ordinaryPGProtectedState(s, now)
	p := state.Policies[sig]
	p.State.Phase, p.State.CohortID = upstreamcacheguard.Trial, cohort
	p.State.DegradedAt, p.State.CooldownUntil = now.Add(-2*time.Minute), now.Add(-time.Minute)
	p.State.FirstTrialAt, p.State.LastEvidenceAt = now.Add(-time.Minute), now.Add(-6*time.Second)
	state.Policies[sig] = p
	ordinaryPGWriteState(t, store, ctx, state)
	for i := 0; i < 5; i++ {
		f := ordinaryPGEvidence(1000+i, now.Add(time.Duration(i-10)*time.Second))
		f.Admission.Trial, f.Admission.CohortID, f.Admission.PolicySignature = true, cohort, sig
		f.Admission.ProtectedAtStart = true
		f.RawUsage.CacheReadTokens = 0
		ordinaryPGInsertFact(t, db, ctx, f, true)
	}

	current := ordinaryPGEvidence(1006, now.Add(-time.Second))
	current.RawUsage.CacheReadTokens = 0
	r := current.Request
	// A durable current-cohort warm session provides real continuation
	// admissions. Keep one request in flight while another ends its cohort.
	lastSuccessful := now.Add(-5 * time.Second)
	session := upstreamcacheguard.SessionState{Key: upstreamcacheguard.SessionKey(r), FirstSeenAt: now.Add(-time.Minute),
		LastAccountID: r.AccountID, LastModel: r.Model, LastProtocol: r.Protocol, LastRouteKey: upstreamcacheguard.WarmRouteKey(r), LastCompletedAt: lastSuccessful,
		TrialAccountID: r.AccountID, TrialModel: r.Model, TrialProtocol: r.Protocol, CohortID: cohort, PolicySignature: sig,
		Routes: map[string]upstreamcacheguard.SessionRoute{upstreamcacheguard.WarmRouteKey(r): {PolicySignature: sig, AccountID: r.AccountID, UpstreamKeyID: r.UpstreamKeyID, LastSuccessfulAt: lastSuccessful}}}
	require.NoError(t, store.TransactAccount(ctx, r.GroupID, r.AccountID, r.UpstreamKeyID, func(tx upstreamcacheguard.Tx) error {
		_, err := tx.Write(upstreamcacheguard.SessionKind, session.Key, session)
		return err
	}))
	e := ordinaryPGEngine(t, store, s)
	pending := r
	pending.ID, pending.StartedAt = "old-trial-inflight", now.Add(-4*time.Second)
	oldAdmission, err := e.Admit(ctx, pending)
	require.NoError(t, err)
	require.True(t, oldAdmission.Trial)
	require.True(t, oldAdmission.WarmAtStart)
	require.Equal(t, cohort, oldAdmission.CohortID)
	current.Admission, err = e.Admit(ctx, current.Request)
	require.NoError(t, err)
	require.True(t, current.Admission.Trial)
	require.True(t, current.Admission.WarmAtStart)
	before := ordinaryPGReadState(t, store, ctx).Policies[sig].State
	require.Equal(t, upstreamcacheguard.Trial, before.Phase)
	require.Equal(t, 5, before.TrialValidRequests, "the strict sample threshold must not end the trial early")

	inserted, err := e.CompleteWithResult(ctx, current)
	require.NoError(t, err)
	require.True(t, inserted)
	failed := ordinaryPGReadState(t, store, ctx).Policies[sig].State
	require.Equal(t, upstreamcacheguard.Cooling, failed.Phase, "failure does not wait for 100 sessions or the recovery timer")
	require.Empty(t, failed.CohortID)
	require.True(t, failed.FirstTrialAt.IsZero())
	require.Empty(t, failed.TrialGroupIDs)
	require.Zero(t, failed.TrialValidRequests)
	require.Zero(t, failed.TrialSessions)
	require.Zero(t, failed.TrialCachePercent)
	require.True(t, failed.CooldownUntil.After(now))

	late := current
	late.ID, late.Request, late.Admission, late.CompletedAt = "old-trial-completed-late", pending, oldAdmission, time.Now().UTC().Truncate(time.Microsecond)
	inserted, err = e.CompleteWithResult(ctx, late)
	require.NoError(t, err)
	require.True(t, inserted)
	afterLate := ordinaryPGReadState(t, store, ctx).Policies[sig].State
	require.Equal(t, upstreamcacheguard.Cooling, afterLate.Phase)
	require.Empty(t, afterLate.CohortID)
	require.True(t, failed.CooldownUntil.Equal(afterLate.CooldownUntil), "old-cohort completion must not renew the cooldown")
	inserted, err = e.CompleteWithResult(ctx, late)
	require.NoError(t, err)
	require.False(t, inserted)

	next := r
	next.ID, next.StartedAt = "after-trial-failure", time.Now().UTC().Truncate(time.Microsecond)
	admission, err := e.Admit(ctx, next)
	require.NoError(t, err)
	require.False(t, admission.Trial, "the existing session must not inherit a finished trial")
	require.Empty(t, admission.CohortID)
	afterAdmit := ordinaryPGReadState(t, store, ctx).Policies[sig].State
	require.Equal(t, upstreamcacheguard.Cooling, afterAdmit.Phase)
	require.True(t, failed.CooldownUntil.Equal(afterAdmit.CooldownUntil))
	var facts int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM upstream_cache_guard_facts WHERE account_id=2 AND upstream_key_id=5 AND processed=TRUE`).Scan(&facts))
	require.Equal(t, 7, facts, "five existing, one deciding and one late fact must persist exactly once")
}

func TestCacheGuardOrdinaryPostgresBoundedReadComparison(t *testing.T) {
	db, ctx, store := ordinaryPGFixture(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	for i := 0; i < 200; i++ {
		ordinaryPGInsertFact(t, db, ctx, ordinaryPGEvidence(i, now.Add(-time.Duration(i)*time.Second)), true)
	}
	type result struct {
		Run                  int    `json:"run"`
		Method               string `json:"method"`
		Samples              int    `json:"samples"`
		Rows                 int    `json:"rows"`
		SQLStatementsPerRead int    `json:"sql_statements_per_read"`
		P50Micros            int64  `json:"p50_us"`
		P95Micros            int64  `json:"p95_us"`
	}
	var report []result
	for run := 1; run <= 3; run++ {
		for _, method := range []string{"account_facts", "account_recovery_facts"} {
			var durations []int64
			require.NoError(t, store.TransactAccount(ctx, 8, 2, 5, func(tx upstreamcacheguard.Tx) error {
				for i := 0; i < 25; i++ {
					started := time.Now()
					var facts []upstreamcacheguard.Evidence
					var err error
					if method == "account_facts" {
						facts, err = tx.(upstreamcacheguard.AccountTx).AccountFacts(2, 5, now.Add(-time.Hour))
					} else {
						var view upstreamcacheguard.AccountRecoveryFacts
						view, err = tx.(upstreamcacheguard.RecoveryFactsTx).AccountRecoveryFacts(2, 5, now.Add(-time.Hour), now)
						facts = view.Facts
						if view.HasPendingGap {
							return fmt.Errorf("unexpected gap")
						}
					}
					if err != nil {
						return err
					}
					if len(facts) != 200 {
						return fmt.Errorf("bounded facts changed: %d", len(facts))
					}
					durations = append(durations, time.Since(started).Microseconds())
				}
				return nil
			}))
			sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
			report = append(report, result{run, method, len(durations), 200, 1, durations[len(durations)/2], durations[len(durations)*95/100]})
		}
	}
	encoded, err := json.MarshalIndent(struct {
		CheckedAt string   `json:"checked_at"`
		Note      string   `json:"note"`
		Results   []result `json:"results"`
	}{time.Now().UTC().Format(time.RFC3339Nano), "Local isolated PostgreSQL, 200 immutable facts, 25 reads per method per run, 3 runs. Timings include transport+JSON decode, exclude outer account transaction/lock. SQL statements are verified from each implementation's single QueryContext call; no end-to-end speedup claim.", report}, "", "  ")
	require.NoError(t, err)
	if path := os.Getenv("SUB2API_CACHE_GUARD_PERF_RECEIPT"); path != "" {
		require.NoError(t, os.WriteFile(path, append(encoded, '\n'), 0600))
	}
	t.Log(string(encoded))
}
