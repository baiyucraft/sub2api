//go:build cacheguardpg

package repository

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/upstreamcacheguard"
	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

// The legacy fixture mirrors the observed 1,650-admission session without
// containing user content, session values or credentials.
func TestCacheGuardAdmissionSplitPostgres(t *testing.T) {
	db, ctx := cacheGuardAdmissionPostgres(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	sessionKey := strings.Repeat("a", 64)
	routeKey := strings.Repeat("b", 64)
	legacy := upstreamcacheguard.SessionState{
		Key: sessionKey, FirstSeenAt: now.Add(-72 * time.Hour), NaturalRequestID: "first", NaturalTrialOrdinal: 17,
		LastAccountID: 2, LastRouteKey: routeKey, LastCompletedAt: now.Add(-time.Minute),
		Routes:     map[string]upstreamcacheguard.SessionRoute{routeKey: {AccountID: 2, UpstreamKeyID: 5, LastSuccessfulAt: now.Add(-time.Minute)}},
		Admissions: make(map[string]upstreamcacheguard.Admission, 1650),
	}
	for i := 0; i < 1650; i++ {
		key := fmt.Sprintf("%064d", i)
		legacy.Admissions[key] = upstreamcacheguard.Admission{
			RequestID: fmt.Sprintf("synthetic-%04d-%s", i, strings.Repeat("x", 150)), SessionKey: sessionKey,
			GroupID: 9, AccountID: 2, UpstreamKeyID: 5, StartedAt: now.Add(-time.Duration(i+1) * time.Second),
			Sample: true, WarmAtStart: i > 0, Completed: true, RouteKey: routeKey,
			PolicySignature: strings.Repeat("c", 64), CohortID: strings.Repeat("d", 64), Mode: upstreamcacheguard.Observe,
		}
	}
	// A pre-account-scope record must use its frozen route, while an unknown
	// legacy route remains key=0 rather than borrowing today's account Key.
	legacy.Admissions["route-provenance"] = upstreamcacheguard.Admission{RequestID: "provenance", SessionKey: sessionKey, GroupID: 9, AccountID: 2, StartedAt: now.Add(-10 * time.Minute), RouteKey: routeKey}
	legacy.Admissions["unknown-provenance"] = upstreamcacheguard.Admission{RequestID: "unknown", SessionKey: sessionKey, GroupID: 9, AccountID: 2, StartedAt: now.Add(-10 * time.Minute), RouteKey: "missing-route"}
	raw, err := json.Marshal(legacy)
	require.NoError(t, err)
	require.Greater(t, len(raw), 900000)
	_, err = db.ExecContext(ctx, `INSERT INTO upstream_cache_guard_runtime(group_id,state_kind,state_key,version,state_json,updated_at) VALUES(9,'session',$1,7,$2,$3)`, sessionKey, string(raw), now.Add(-time.Minute))
	require.NoError(t, err)
	migration, err := migrations.FS.ReadFile("301_upstream_cache_guard_admissions.sql")
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, string(migration))
	require.NoError(t, err)
	store := NewUpstreamCacheGuardRepository(db).(*upstreamCacheGuardRepository)

	t.Run("migration preserves identity and frozen classifications", func(t *testing.T) {
		var count, version, bytes int
		var hasLedger, hasPending bool
		var updated time.Time
		require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM upstream_cache_guard_admissions`).Scan(&count))
		require.Equal(t, 1652, count)
		require.NoError(t, db.QueryRowContext(ctx, `SELECT version,octet_length(state_json::text),state_json ? 'admissions',has_pending_admissions,updated_at FROM upstream_cache_guard_runtime WHERE group_id=9 AND state_kind='session' AND state_key=$1`, sessionKey).Scan(&version, &bytes, &hasLedger, &hasPending, &updated))
		require.Equal(t, 8, version)
		require.False(t, hasLedger)
		require.False(t, hasPending)
		require.True(t, updated.Equal(now.Add(-time.Minute)), "migration must not rewrite business update time")
		require.Less(t, bytes, 10000)
		loaded, _, err := store.LoadSession(ctx, 9, sessionKey)
		require.NoError(t, err)
		require.Equal(t, legacy.NaturalTrialOrdinal, loaded.NaturalTrialOrdinal)
		require.Equal(t, legacy.NaturalRequestID, loaded.NaturalRequestID)
		require.True(t, loaded.FirstSeenAt.Equal(legacy.FirstSeenAt))
		require.Empty(t, loaded.Admissions)
		require.Equal(t, legacy.Routes, loaded.Routes)
		require.NoError(t, store.TransactAccount(ctx, 9, 2, 5, func(rawTx upstreamcacheguard.Tx) error {
			tx := rawTx.(upstreamcacheguard.AdmissionTx)
			got, found, err := tx.ReadAdmission(sessionKey, fmt.Sprintf("%064d", 4))
			if err != nil || !found || !got.Sample || !got.WarmAtStart || !got.Completed {
				return fmt.Errorf("migrated admission mismatch: found=%v admission=%+v err=%w", found, got, err)
			}
			got, found, err = tx.ReadAdmission(sessionKey, "route-provenance")
			if err != nil || !found || got.UpstreamKeyID != 5 {
				return fmt.Errorf("route provenance mismatch: found=%v Key=%d err=%w", found, got.UpstreamKeyID, err)
			}
			got, found, err = tx.ReadAdmission(sessionKey, "unknown-provenance")
			if err != nil || !found || got.UpstreamKeyID != 0 {
				return fmt.Errorf("unknown Key was guessed: found=%v Key=%d err=%w", found, got.UpstreamKeyID, err)
			}
			return nil
		}))
		t.Logf("synthetic legacy session bytes=%d lightweight body bytes=%d admissions=%d", len(raw), bytes, count)
	})

	t.Run("pending barriers count retention and immutable completion", func(t *testing.T) {
		boundary := now.Add(-48 * time.Hour)
		entries := map[string]upstreamcacheguard.Admission{
			"expired-complete":     {StartedAt: boundary.Add(-time.Microsecond), Completed: true},
			"boundary-complete":    {StartedAt: boundary, Completed: true},
			"ancient-pending":      {StartedAt: now.Add(-72 * time.Hour)},
			"five-minute-boundary": {StartedAt: now.Add(-5 * time.Minute)},
		}
		require.NoError(t, store.TransactAccount(ctx, 9, 2, 5, func(rawTx upstreamcacheguard.Tx) error {
			tx := rawTx.(upstreamcacheguard.AdmissionTx)
			for key, a := range entries {
				a.RequestID, a.SessionKey, a.GroupID, a.AccountID, a.UpstreamKeyID, a.RouteKey = key, sessionKey, 9, 2, 5, routeKey
				a.Sample, a.WarmAtStart = true, true
				if err := tx.WriteAdmission(sessionKey, key, a); err != nil {
					return err
				}
			}
			count, err := tx.RetainedAdmissionCount(sessionKey, boundary)
			if err != nil || count != 1655 {
				return fmt.Errorf("retained count=%d want=1655: %w", count, err)
			}
			pending, err := tx.PendingAdmissions(sessionKey, 2, routeKey)
			if err != nil || len(pending) != 3 {
				return fmt.Errorf("pending route barriers=%d want=3: %w", len(pending), err)
			}
			original, _, err := tx.ReadAdmission(sessionKey, "ancient-pending")
			if err != nil {
				return err
			}
			mutated := original
			mutated.Completed, mutated.Sample, mutated.WarmAtStart = true, false, false
			if err := tx.WriteAdmission(sessionKey, "ancient-pending", mutated); err != nil {
				return err
			}
			if err := tx.WriteAdmission(sessionKey, "ancient-pending", original); err != nil {
				return err
			}
			completed, _, err := tx.ReadAdmission(sessionKey, "ancient-pending")
			if err != nil || !completed.Completed || !completed.Sample || !completed.WarmAtStart {
				return fmt.Errorf("classification/completion was overwritten: %+v: %w", completed, err)
			}
			return nil
		}))
		failures := []upstreamcacheguard.AdmissionFailure{{AccountID: 2, UpstreamKeyID: 5, Key: "ancient-pending"}, {AccountID: 2, UpstreamKeyID: 5, Key: "five-minute-boundary"}, {AccountID: 2, UpstreamKeyID: 5, Key: "does-not-exist"}}
		health, err := store.AdmissionHealth(ctx, now.Add(-upstreamcacheguard.RefreshLifetime), failures)
		require.NoError(t, err)
		require.Len(t, health.Acknowledged, 1)
		require.Equal(t, "ancient-pending", health.Acknowledged[0].Key)
		// An unfinished request remains a route barrier and an immutable ledger
		// entry, but it has no completed usage to hold an account snapshot back.
		require.Empty(t, health.PendingFacts)
	})

	t.Run("atomic rollback and cross account session serialization", func(t *testing.T) {
		sentinel := errors.New("rollback requested")
		err := store.TransactAccount(ctx, 9, 2, 5, func(rawTx upstreamcacheguard.Tx) error {
			a := upstreamcacheguard.Admission{RequestID: "rolled-back", GroupID: 9, AccountID: 2, UpstreamKeyID: 5, SessionKey: sessionKey, StartedAt: now}
			if err := rawTx.(upstreamcacheguard.AdmissionTx).WriteAdmission(sessionKey, "rolled-back", a); err != nil {
				return err
			}
			return sentinel
		})
		require.ErrorIs(t, err, sentinel)
		var count int
		require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM upstream_cache_guard_admissions WHERE admission_key='rolled-back'`).Scan(&count))
		require.Zero(t, count)
		var wg sync.WaitGroup
		errCh := make(chan error, 2)
		for _, account := range []int64{2, 3} {
			wg.Add(1)
			go func(account int64) {
				defer wg.Done()
				errCh <- store.TransactAccount(ctx, 9, account, 5, func(rawTx upstreamcacheguard.Tx) error {
					var state upstreamcacheguard.SessionState
					if _, err := rawTx.Read(upstreamcacheguard.SessionKind, sessionKey, &state); err != nil {
						return err
					}
					a := upstreamcacheguard.Admission{RequestID: fmt.Sprintf("parallel-%d", account), GroupID: 9, AccountID: account, UpstreamKeyID: 5, SessionKey: sessionKey, StartedAt: now}
					if err := rawTx.(upstreamcacheguard.AdmissionTx).WriteAdmission(sessionKey, a.RequestID, a); err != nil {
						return err
					}
					state.LastAccountID = account
					_, err := rawTx.Write(upstreamcacheguard.SessionKind, sessionKey, &state)
					return err
				})
			}(account)
		}
		wg.Wait()
		close(errCh)
		for err := range errCh {
			require.NoError(t, err)
		}
		require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM upstream_cache_guard_admissions WHERE admission_key LIKE 'parallel-%'`).Scan(&count))
		require.Equal(t, 2, count)
	})

	t.Run("bounded cleanup preserves unfinished and session identity", func(t *testing.T) {
		before := now.Add(-48 * time.Hour)
		deleted, err := store.CleanupBatch(ctx, before, 1)
		require.NoError(t, err)
		require.EqualValues(t, 1, deleted)
		deleted, err = store.CleanupBatch(ctx, before, 1000)
		require.NoError(t, err)
		require.EqualValues(t, 1, deleted)
		var count int
		require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM upstream_cache_guard_admissions WHERE admission_key='boundary-complete'`).Scan(&count))
		require.Equal(t, 1, count)
		require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM upstream_cache_guard_admissions WHERE completed=FALSE`).Scan(&count))
		require.Equal(t, 5, count)
		loaded, _, err := store.LoadSession(ctx, 9, sessionKey)
		require.NoError(t, err)
		require.EqualValues(t, 17, loaded.NaturalTrialOrdinal)
		require.Equal(t, "first", loaded.NaturalRequestID)
		require.Len(t, loaded.Routes, 1)
	})

	t.Run("synthetic persistence hot path comparison", func(t *testing.T) {
		// This compares the session persistence segment only. Both paths hold
		// account -> session locks, inspect idempotence, retain the 48h bound and
		// save the same classification. Account policy calculations are excluded.
		// No application latency or production performance claim follows from it.
		_, err := db.ExecContext(ctx, `INSERT INTO upstream_cache_guard_runtime(group_id,state_kind,state_key,state_json) VALUES(9,'session','legacy-profile',$1)`, string(raw))
		require.NoError(t, err)
		const iterations = 20
		for round := 0; round < 3; round++ {
			legacyDurations, splitDurations := make([]time.Duration, 0, iterations), make([]time.Duration, 0, iterations)
			for _, split := range []bool{false, true} {
				for i := 0; i < iterations; i++ {
					key := fmt.Sprintf("profile-%d-%d", round, i)
					a := upstreamcacheguard.Admission{RequestID: key, GroupID: 9, AccountID: 2, UpstreamKeyID: 5, SessionKey: sessionKey, StartedAt: now, RouteKey: routeKey, Sample: true, WarmAtStart: true}
					start := time.Now()
					err := store.TransactAccount(ctx, 9, 2, 5, func(rawTx upstreamcacheguard.Tx) error {
						if split {
							var state upstreamcacheguard.SessionState
							if _, err := rawTx.Read(upstreamcacheguard.SessionKind, sessionKey, &state); err != nil {
								return err
							}
							tx := rawTx.(upstreamcacheguard.AdmissionTx)
							if _, _, err := tx.ReadAdmission(sessionKey, key); err != nil {
								return err
							}
							if _, err := tx.RetainedAdmissionCount(sessionKey, now.Add(-48*time.Hour)); err != nil {
								return err
							}
							if err := tx.WriteAdmission(sessionKey, key, a); err != nil {
								return err
							}
							state.LastStartedAt = now
							_, err := rawTx.Write(upstreamcacheguard.SessionKind, sessionKey, &state)
							return err
						}
						// The pre-split implementation necessarily decodes and rewrites
						// the whole ledger on every request, including its pending trigger.
						tx := rawTx.(*upstreamCacheGuardTx)
						if err := tx.lockState(upstreamcacheguard.SessionKind, "legacy-profile"); err != nil {
							return err
						}
						var body []byte
						if err := tx.tx.QueryRowContext(ctx, `SELECT state_json FROM upstream_cache_guard_runtime WHERE group_id=9 AND state_kind='session' AND state_key='legacy-profile'`).Scan(&body); err != nil {
							return err
						}
						var state upstreamcacheguard.SessionState
						if err := json.Unmarshal(body, &state); err != nil {
							return err
						}
						if _, exists := state.Admissions[key]; exists {
							return errors.New("synthetic admission unexpectedly repeated")
						}
						for key, admission := range state.Admissions {
							if admission.Completed && now.Sub(admission.StartedAt) > 48*time.Hour {
								delete(state.Admissions, key)
							}
						}
						if len(state.Admissions) >= 100000 {
							return errors.New("synthetic admission bound exceeded")
						}
						state.Admissions[key], state.LastStartedAt = a, now
						body, err := json.Marshal(state)
						if err != nil {
							return err
						}
						_, err = tx.tx.ExecContext(ctx, `UPDATE upstream_cache_guard_runtime SET state_json=$1,version=version+1,updated_at=CURRENT_TIMESTAMP WHERE group_id=9 AND state_kind='session' AND state_key='legacy-profile'`, string(body))
						return err
					})
					elapsed := time.Since(start)
					require.NoError(t, err)
					if split {
						splitDurations = append(splitDurations, elapsed)
					} else {
						legacyDurations = append(legacyDurations, elapsed)
					}
				}
			}
			for _, result := range []struct {
				name   string
				values []time.Duration
			}{{"legacy", legacyDurations}, {"split", splitDurations}} {
				sort.Slice(result.values, func(i, j int) bool { return result.values[i] < result.values[j] })
				var sum time.Duration
				for _, value := range result.values {
					sum += value
				}
				t.Logf("session persistence synthetic round=%d path=%s n=%d mean=%s p50=%s p95=%s max=%s", round+1, result.name, len(result.values), sum/time.Duration(len(result.values)), result.values[9], result.values[18], result.values[19])
			}
		}
	})

	t.Run("retained count caps oversized sessions", func(t *testing.T) {
		seed := upstreamcacheguard.Admission{RequestID: "synthetic-bound", GroupID: 9, AccountID: 2, UpstreamKeyID: 5, SessionKey: "bound-profile", StartedAt: now, Sample: true}
		payload, err := json.Marshal(seed)
		require.NoError(t, err)
		_, err = db.ExecContext(ctx, `INSERT INTO upstream_cache_guard_admissions(group_id,session_key,admission_key,account_id,upstream_key_id,started_at,completed,admission_json)
			SELECT 9,'bound-profile',n::TEXT,2,5,$1,FALSE,$2 FROM generate_series(1,100001) n`, now, string(payload))
		require.NoError(t, err)
		require.NoError(t, store.TransactAccount(ctx, 9, 2, 5, func(rawTx upstreamcacheguard.Tx) error {
			count, err := rawTx.(upstreamcacheguard.AdmissionTx).RetainedAdmissionCount("bound-profile", now.Add(-48*time.Hour))
			if err != nil || count != 100000 {
				return fmt.Errorf("bounded count=%d want=100000: %w", count, err)
			}
			return nil
		}))
	})
}

func TestCacheGuardAdmissionHealthPostgres(t *testing.T) {
	db, ctx := cacheGuardAdmissionPostgres(t)
	migration, err := migrations.FS.ReadFile("301_upstream_cache_guard_admissions.sql")
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, string(migration))
	require.NoError(t, err)
	writer := NewUpstreamCacheGuardRepository(db).(*upstreamCacheGuardRepository)
	now := time.Now().UTC().Truncate(time.Microsecond)
	cutoff := now.Add(-upstreamcacheguard.RefreshLifetime)

	// Another application instance has an independent database pool. Its birth
	// time cannot tell whether either of these attempts is still running.
	var schema string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT current_schema()`).Scan(&schema))
	readerDB, err := sql.Open("postgres", cacheGuardScopedDSN(os.Getenv("SUB2API_CACHE_GUARD_TEST_DSN"), schema))
	require.NoError(t, err)
	defer readerDB.Close()
	reader := NewUpstreamCacheGuardRepository(readerDB).(*upstreamCacheGuardRepository)
	for _, admission := range []struct {
		key string
		at  time.Time
	}{{"orphaned-before-restart", now.Add(-72 * time.Hour)}, {"live-on-another-instance", now.Add(-time.Second)}} {
		require.NoError(t, writer.TransactAccount(ctx, 9, 2, 5, func(rawTx upstreamcacheguard.Tx) error {
			return rawTx.(upstreamcacheguard.AdmissionTx).WriteAdmission("health-session", admission.key, upstreamcacheguard.Admission{
				RequestID: admission.key, GroupID: 9, AccountID: 2, UpstreamKeyID: 5, SessionKey: "health-session", StartedAt: admission.at, RouteKey: "health-route", Sample: true,
			})
		}))
	}
	health, err := reader.AdmissionHealth(ctx, cutoff, nil)
	require.NoError(t, err)
	require.Empty(t, health.PendingFacts, "an unfinished ledger entry alone is not missing completed evidence")
	var unfinished int
	require.NoError(t, readerDB.QueryRowContext(ctx, `SELECT count(*) FROM upstream_cache_guard_admissions WHERE completed=FALSE`).Scan(&unfinished))
	require.Equal(t, 2, unfinished, "ignoring account-wide gaps must neither complete nor remove unfinished attempts")

	cases := []struct {
		id      string
		group   int64
		account int64
		key     int64
		at      time.Time
	}{
		{"same-account-first", 9, 2, 5, cutoff.Add(-time.Minute)},
		{"same-account-second", 10, 2, 5, cutoff.Add(-time.Second)},
		{"previous-key", 9, 2, 4, cutoff.Add(-2 * time.Second)},
		{"unknown-key", 9, 2, 0, cutoff.Add(-3 * time.Second)},
		{"another-account", 9, 3, 5, cutoff.Add(-4 * time.Second)},
		{"strict-cutoff", 9, 3, 6, cutoff},
		{"after-cutoff", 9, 3, 7, cutoff.Add(time.Microsecond)},
		{"already-processed", 9, 3, 8, cutoff.Add(-time.Second)},
	}
	facts := make(map[string]upstreamcacheguard.Evidence, len(cases))
	for _, tc := range cases {
		fact := upstreamcacheguard.Evidence{ID: tc.id, Request: upstreamcacheguard.Request{ID: tc.id, GroupID: tc.group, AccountID: tc.account, UpstreamKeyID: tc.key}, CompletedAt: tc.at}
		facts[tc.id] = fact
		require.NoError(t, writer.Enqueue(ctx, fact))
	}
	require.NoError(t, writer.TransactAccount(ctx, 9, 3, 8, func(tx upstreamcacheguard.Tx) error {
		inserted, err := tx.InsertFact(facts["already-processed"])
		if err != nil || !inserted {
			return fmt.Errorf("processed fixture inserted=%v: %w", inserted, err)
		}
		return nil
	}))
	// A live consumer lease is about processing ownership, not completeness of
	// the statistical window. A different reader still sees all delayed facts.
	claimed, err := writer.ClaimPending(ctx, "other-instance-consumer", 32, time.Minute)
	require.NoError(t, err)
	require.Len(t, claimed, len(cases)-1)
	health, err = reader.AdmissionHealth(ctx, cutoff, nil)
	require.NoError(t, err)
	expected := []upstreamcacheguard.PendingFact{
		{AccountID: 2, UpstreamKeyID: 0, CompletedAt: cutoff.Add(-3 * time.Second)},
		{AccountID: 2, UpstreamKeyID: 4, CompletedAt: cutoff.Add(-2 * time.Second)},
		{AccountID: 2, UpstreamKeyID: 5, CompletedAt: cutoff.Add(-time.Minute)},
		{AccountID: 3, UpstreamKeyID: 5, CompletedAt: cutoff.Add(-4 * time.Second)},
	}
	require.Len(t, health.PendingFacts, len(expected), "deduplicate exact account/Key identities without borrowing the current Key or excluding live leases")
	for i, fact := range health.PendingFacts {
		require.Equal(t, expected[i].AccountID, fact.AccountID)
		require.Equal(t, expected[i].UpstreamKeyID, fact.UpstreamKeyID)
		require.True(t, expected[i].CompletedAt.Equal(fact.CompletedAt), "completion instant must match independently of the database session time zone")
	}
	// Equality stays inside the tolerance; one PostgreSQL precision step later
	// exposes that identity, while the next newer fact remains inside it.
	health, err = reader.AdmissionHealth(ctx, cutoff.Add(time.Microsecond), nil)
	require.NoError(t, err)
	require.Len(t, health.PendingFacts, 5)
	require.Equal(t, int64(3), health.PendingFacts[4].AccountID)
	require.Equal(t, int64(6), health.PendingFacts[4].UpstreamKeyID)
	require.True(t, cutoff.Equal(health.PendingFacts[4].CompletedAt))

	// Completing one identity on one instance removes only its delayed fact
	// gap; other Key identities and the durable unfinished ledger stay intact.
	for _, id := range []string{"same-account-first", "same-account-second"} {
		fact := facts[id]
		require.NoError(t, writer.TransactAccount(ctx, fact.Request.GroupID, 2, 5, func(tx upstreamcacheguard.Tx) error {
			inserted, err := tx.InsertFact(fact)
			if err != nil || !inserted {
				return fmt.Errorf("acknowledge delayed fact inserted=%v: %w", inserted, err)
			}
			return nil
		}))
	}
	health, err = reader.AdmissionHealth(ctx, cutoff, nil)
	require.NoError(t, err)
	require.Len(t, health.PendingFacts, 3)
	for _, pending := range health.PendingFacts {
		require.False(t, pending.AccountID == 2 && pending.UpstreamKeyID == 5)
	}
	require.NoError(t, readerDB.QueryRowContext(ctx, `SELECT count(*) FROM upstream_cache_guard_admissions WHERE completed=FALSE`).Scan(&unfinished))
	require.Equal(t, 2, unfinished)
}

// The fixture applies the published ancestors unchanged, then lets the test
// seed a legacy session before the new migration runs.
func cacheGuardAdmissionPostgres(t *testing.T) (*sql.DB, context.Context) {
	t.Helper()
	dsn := os.Getenv("SUB2API_CACHE_GUARD_TEST_DSN")
	if dsn == "" {
		t.Skip("disposable PostgreSQL DSN required")
	}
	require.Equal(t, "1", os.Getenv("SUB2API_CACHE_GUARD_TEST_ISOLATED"))
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	admin, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = admin.Close() })
	var database string
	require.NoError(t, admin.QueryRowContext(ctx, `SELECT current_database()`).Scan(&database))
	require.Contains(t, strings.ToLower(database), "test", "refuse a non-test database")
	var nonce [8]byte
	_, err = rand.Read(nonce[:])
	require.NoError(t, err)
	schema := "cache_guard_admission_test_" + hex.EncodeToString(nonce[:])
	_, err = admin.ExecContext(ctx, `CREATE SCHEMA `+schema)
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_, err := admin.ExecContext(cleanup, `DROP SCHEMA `+schema+` CASCADE`)
		require.NoError(t, err)
	})
	db, err := sql.Open("postgres", cacheGuardScopedDSN(dsn, schema))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(4)
	_, err = db.ExecContext(ctx, `CREATE TABLE accounts(id BIGINT PRIMARY KEY,upstream_key_id BIGINT,deleted_at TIMESTAMPTZ); INSERT INTO accounts(id,upstream_key_id) VALUES(2,5),(3,5)`)
	require.NoError(t, err)
	for _, name := range []string{"296_upstream_cache_guard.sql", "297_upstream_cache_guard_account_scope.sql", "299_upstream_cache_guard_revisions.sql"} {
		text, err := migrations.FS.ReadFile(name)
		require.NoError(t, err)
		_, err = db.ExecContext(ctx, string(text))
		require.NoError(t, err, name)
	}
	return db, ctx
}
