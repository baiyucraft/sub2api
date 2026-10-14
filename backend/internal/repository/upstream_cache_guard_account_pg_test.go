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
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/upstreamcacheusage"
	"github.com/Wei-Shaw/sub2api/internal/upstreamcacheguard"
	"github.com/Wei-Shaw/sub2api/migrations"
	_ "github.com/lib/pq"
)

// Run only against a disposable PostgreSQL database whose name contains "test":
// SUB2API_CACHE_GUARD_TEST_DSN=... SUB2API_CACHE_GUARD_TEST_ISOLATED=1
// go test -tags cacheguardpg ./internal/repository -run TestCacheGuardAccountPostgres
func TestCacheGuardAccountPostgres(t *testing.T) {
	dsn := os.Getenv("SUB2API_CACHE_GUARD_TEST_DSN")
	if dsn == "" {
		t.Skip("SUB2API_CACHE_GUARD_TEST_DSN is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	adminDB, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adminDB.Close() })
	var database string
	if err := adminDB.QueryRowContext(ctx, `SELECT current_database()`).Scan(&database); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(database), "test") {
		t.Fatalf("refusing non-test PostgreSQL database %q", database)
	}
	if os.Getenv("SUB2API_CACHE_GUARD_TEST_ISOLATED") != "1" {
		t.Fatal("SUB2API_CACHE_GUARD_TEST_ISOLATED=1 must acknowledge the disposable test database")
	}
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	schema := "cache_guard_test_" + hex.EncodeToString(nonce[:])
	if _, err := adminDB.ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if _, err := adminDB.ExecContext(cleanupCtx, `DROP SCHEMA `+schema+` CASCADE`); err != nil {
			t.Errorf("remove isolated cache guard schema: %v", err)
		}
	})

	scopedDB, err := sql.Open("postgres", cacheGuardScopedDSN(dsn, schema))
	if err != nil {
		t.Fatal(err)
	}
	defer scopedDB.Close()
	scopedDB.SetMaxOpenConns(4)
	if _, err := scopedDB.ExecContext(ctx, `CREATE TABLE accounts(id BIGINT PRIMARY KEY, upstream_key_id BIGINT, deleted_at TIMESTAMPTZ)`); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"296_upstream_cache_guard.sql"} {
		sqlText, err := migrations.FS.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := scopedDB.ExecContext(ctx, string(sqlText)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := scopedDB.ExecContext(ctx, `INSERT INTO accounts(id,upstream_key_id) VALUES(2,5),(3,6)`); err != nil {
		t.Fatal(err)
	}
	legacy := upstreamcacheguard.AccountState{AccountID: 3, UpstreamKeyID: 6, Phase: upstreamcacheguard.Degraded, CooldownUntil: time.Now().UTC().Add(time.Hour)}
	legacyJSON, _ := json.Marshal(legacy)
	if _, err := scopedDB.ExecContext(ctx, `INSERT INTO upstream_cache_guard_runtime(group_id,state_kind,state_key,state_json) VALUES(9,'account','old', $1)`, string(legacyJSON)); err != nil {
		t.Fatal(err)
	}
	oldFact := upstreamcacheguard.Evidence{ID: "before-migration", Request: upstreamcacheguard.Request{GroupID: 9, AccountID: 2, UpstreamKeyID: 5}, CompletedAt: time.Now().UTC()}
	oldJSON, _ := json.Marshal(oldFact)
	if _, err := scopedDB.ExecContext(ctx, `INSERT INTO upstream_cache_guard_facts(group_id,evidence_id,account_id,completed_at,valid_sample,evidence_json,processed) VALUES(9,$1,2,$2,TRUE,$3,TRUE)`, oldFact.ID, oldFact.CompletedAt, string(oldJSON)); err != nil {
		t.Fatal(err)
	}
	newMigration, err := migrations.FS.ReadFile("297_upstream_cache_guard_account_scope.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scopedDB.ExecContext(ctx, string(newMigration)); err != nil {
		t.Fatal(err)
	}
	var migratedKey int64
	if err := scopedDB.QueryRowContext(ctx, `SELECT upstream_key_id FROM upstream_cache_guard_facts WHERE evidence_id=$1`, oldFact.ID).Scan(&migratedKey); err != nil || migratedKey != 5 {
		t.Fatalf("migration key=%d err=%v", migratedKey, err)
	}

	repo := NewUpstreamCacheGuardRepository(scopedDB).(*upstreamCacheGuardRepository)
	rows, err := repo.AccountRows(ctx)
	if err != nil || len(rows) != 1 || rows[0].AccountID != 3 || rows[0].UpstreamKeyID != 6 || rows[0].Version != 0 {
		t.Fatalf("legacy-only current-Key rows=%+v err=%v", rows, err)
	}
	if err := repo.TransactAccount(ctx, 0, 3, 6, func(rawTx upstreamcacheguard.Tx) error {
		tx := rawTx.(upstreamcacheguard.AccountTx)
		states, err := tx.LegacyAccounts(3, 6)
		if err != nil || len(states) != 1 || states[0].GroupID != 9 || states[0].Phase != upstreamcacheguard.Degraded {
			return fmt.Errorf("legacy protection=%+v: %w", states, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Two groups commit facts for the same identity concurrently. The second
	// writer must observe the first state and cannot lose its sample.
	var wg sync.WaitGroup
	errCh := make(chan error, 2)
	for _, groupID := range []int64{9, 10} {
		wg.Add(1)
		go func(groupID int64) {
			defer wg.Done()
			e := upstreamcacheguard.Evidence{ID: fmt.Sprintf("group-%d", groupID), Request: upstreamcacheguard.Request{GroupID: groupID, AccountID: 2, UpstreamKeyID: 5}, CompletedAt: time.Now().UTC()}
			errCh <- repo.TransactAccount(ctx, groupID, 2, 5, func(rawTx upstreamcacheguard.Tx) error {
				tx := rawTx.(upstreamcacheguard.AccountTx)
				var state upstreamcacheguard.AccountState
				if _, err := tx.ReadAccount(2, 5, &state); err != nil {
					return err
				}
				inserted, err := tx.InsertFact(e)
				if err != nil || !inserted {
					return fmt.Errorf("insert fact=%v: %w", inserted, err)
				}
				state.AccountID, state.UpstreamKeyID = 2, 5
				state.Phase = upstreamcacheguard.Healthy
				state.ValidRequests++
				_, err = tx.WriteAccount(2, 5, state)
				return err
			})
		}(groupID)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.TransactAccount(ctx, 0, 2, 5, func(rawTx upstreamcacheguard.Tx) error {
		tx := rawTx.(upstreamcacheguard.AccountTx)
		var state upstreamcacheguard.AccountState
		version, err := tx.ReadAccount(2, 5, &state)
		if err != nil || version != 2 || state.ValidRequests != 2 {
			return fmt.Errorf("concurrent state version=%d state=%+v: %w", version, state, err)
		}
		facts, err := tx.AccountFacts(2, 5, oldFact.CompletedAt.Add(-time.Second))
		if err != nil || len(facts) != 3 {
			return fmt.Errorf("cross-group facts=%d: %w", len(facts), err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	rollbackFact := upstreamcacheguard.Evidence{ID: "rollback", Request: upstreamcacheguard.Request{GroupID: 9, AccountID: 2, UpstreamKeyID: 5}, CompletedAt: time.Now().UTC()}
	if err := repo.Enqueue(ctx, rollbackFact); err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("rollback requested")
	err = repo.TransactAccount(ctx, 9, 2, 5, func(rawTx upstreamcacheguard.Tx) error {
		tx := rawTx.(upstreamcacheguard.AccountTx)
		inserted, err := tx.InsertFact(rollbackFact)
		if err != nil || !inserted {
			return fmt.Errorf("pending fact claim=%v: %w", inserted, err)
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("rollback error=%v", err)
	}
	var processed bool
	if err := scopedDB.QueryRowContext(ctx, `SELECT processed FROM upstream_cache_guard_facts WHERE evidence_id=$1`, rollbackFact.ID).Scan(&processed); err != nil || processed {
		t.Fatalf("rolled-back fact processed=%v err=%v", processed, err)
	}
	if err := repo.TransactAccount(ctx, 9, 2, 5, func(rawTx upstreamcacheguard.Tx) error {
		tx := rawTx.(upstreamcacheguard.AccountTx)
		inserted, err := tx.InsertFact(rollbackFact)
		if err != nil || !inserted {
			return fmt.Errorf("retry fact claim=%v: %w", inserted, err)
		}
		inserted, err = tx.InsertFact(rollbackFact)
		if err != nil || inserted {
			return fmt.Errorf("duplicate fact claim=%v: %w", inserted, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	windowNow := time.Now().UTC()
	if err := repo.TransactAccount(ctx, 0, 2, 5, func(rawTx upstreamcacheguard.Tx) error {
		tx := rawTx.(upstreamcacheguard.AccountTx)
		var state upstreamcacheguard.AccountState
		if _, err := tx.ReadAccount(2, 5, &state); err != nil {
			return err
		}
		state.QualitySince = windowNow.Add(-4 * time.Minute)
		state.LegacyQualitySince = map[int64]time.Time{10: windowNow.Add(-2 * time.Minute)}
		state.CohortID = "cohort-A"
		_, err := tx.WriteAccount(2, 5, &state)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	windowFacts := []struct {
		id, session, cohort  string
		groupID, input, read int64
		at                   time.Time
		trial                bool
	}{
		{"before-reset", "old", "", 9, 500, 300, windowNow.Add(-5 * time.Minute), false},
		{"window-group-9", "warm", "", 9, 100, 20, windowNow.Add(-3 * time.Minute), false},
		{"group-10-before-reset", "warm", "", 10, 900, 800, windowNow.Add(-3 * time.Minute), false},
		{"trial-10", "session-1", "cohort-A", 10, 300, 180, windowNow.Add(-time.Minute), true},
		{"trial-9", "session-2", "cohort-A", 9, 100, 100, windowNow.Add(-30 * time.Second), true},
		{"wrong-cohort", "session-3", "old-cohort", 9, 100, 50, windowNow.Add(-20 * time.Second), true},
		{"ordinary-10", "warm", "", 10, 100, 0, windowNow.Add(-10 * time.Second), false},
	}
	for _, item := range windowFacts {
		e := upstreamcacheguard.Evidence{ID: item.id,
			Request: upstreamcacheguard.Request{GroupID: item.groupID, AccountID: 2, UpstreamKeyID: 5},
			Admission: upstreamcacheguard.Admission{SessionKey: item.session, WarmAtStart: true, Sample: true,
				Trial: item.trial, CohortID: item.cohort},
			RawUsage: upstreamcacheusage.Evidence{Status: upstreamcacheusage.Known, TotalInputTokens: item.input,
				CacheReadTokens: item.read},
			Success: true, CompletedAt: item.at}
		if err := repo.TransactAccount(ctx, item.groupID, 2, 5, func(tx upstreamcacheguard.Tx) error {
			inserted, err := tx.InsertFact(e)
			if err != nil || !inserted {
				return fmt.Errorf("window fact %s inserted=%v: %w", item.id, inserted, err)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	windows, err := repo.AccountWindows(ctx, windowNow.Add(-6*time.Minute), windowNow)
	if err != nil {
		t.Fatal(err)
	}
	window := windows["2:5"]
	if window.ValidRequests != 5 || window.TotalInputTokens != 700 || window.CacheReadTokens != 350 ||
		window.TrialValidRequests != 2 || window.TrialInputTokens != 400 || window.TrialCacheReadTokens != 280 ||
		window.TrialSessions != 2 || len(window.GroupIDs) != 2 || len(window.TrialGroupIDs) != 2 {
		t.Fatalf("account-scoped window ignores reset or cohort: %+v", window)
	}
	engine := upstreamcacheguard.NewEngine(NewUpstreamCacheGuardRepository(scopedDB))
	settings := upstreamcacheguard.DefaultSettings()
	settings.Mode, settings.WindowMinutes = upstreamcacheguard.Observe, 10
	if err := engine.ApplySettings(settings); err != nil {
		t.Fatal(err)
	}
	if err := engine.Refresh(ctx); err != nil {
		t.Fatalf("engine refresh including legacy bootstrap: %v", err)
	}
	account, fresh := engine.CachedAccount(2, 5)
	if !fresh || account.ValidRequests != 5 || account.CachePercent != 50 || account.Phase != upstreamcacheguard.Healthy {
		t.Fatalf("engine account window=%+v fresh=%v", account, fresh)
	}
	legacyAccount, fresh := engine.CachedAccount(3, 6)
	if !fresh || legacyAccount.Phase != upstreamcacheguard.Cooling || legacyAccount.CooldownUntil.IsZero() {
		t.Fatalf("legacy protection lifted by bootstrap: %+v fresh=%v", legacyAccount, fresh)
	}
	custom := settings
	custom.ThresholdPercent, custom.RecoveryThresholdPercent, custom.MinValidRequests = 60, 90, 2
	if err := engine.ApplyPolicySettings(map[int64]upstreamcacheguard.Settings{10: custom}); err != nil {
		t.Fatal(err)
	}
	if err := engine.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if err := repo.TransactAccount(ctx, 0, 2, 5, func(rawTx upstreamcacheguard.Tx) error {
		tx := rawTx.(upstreamcacheguard.AccountTx)
		var a upstreamcacheguard.AccountState
		if _, err := tx.ReadAccount(2, 5, &a); err != nil {
			return err
		}
		p := a.Policies[upstreamcacheguard.PolicySignature(custom)]
		p.State.Phase, p.State.CohortID = upstreamcacheguard.Trial, "separate-custom-cohort"
		p.State.FirstTrialAt = windowNow.Add(-11 * time.Minute)
		a.Policies[upstreamcacheguard.PolicySignature(custom)] = p
		_, err := tx.WriteAccount(2, 5, a)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	windows, err = repo.AccountWindows(ctx, windowNow.Add(-6*time.Minute), windowNow)
	if err != nil {
		t.Fatal(err)
	}
	window = windows["2:5"]
	globalWindow := window.Policies[upstreamcacheguard.PolicySignature(settings)]
	customWindow := window.Policies[upstreamcacheguard.PolicySignature(custom)]
	if window.ValidRequests != 6 || window.TotalInputTokens != 1200 || globalWindow.ValidRequests != 5 || globalWindow.TrialValidRequests != 2 || customWindow.ValidRequests != 6 || customWindow.TrialValidRequests != 0 {
		t.Fatalf("shared facts/policy reset/cohort isolation: shared=%+v global=%+v custom=%+v", window, globalWindow, customWindow)
	}
	if _, err := scopedDB.ExecContext(ctx, `UPDATE accounts SET upstream_key_id=7 WHERE id=2`); err != nil {
		t.Fatal(err)
	}
	rows, err = repo.AccountRows(ctx)
	if err != nil || len(rows) != 1 || rows[0].AccountID != 3 {
		t.Fatalf("old Key state leaked after rotation: rows=%+v err=%v", rows, err)
	}
	if err := repo.TransactAccount(ctx, 0, 2, 5, func(rawTx upstreamcacheguard.Tx) error {
		tx := rawTx.(upstreamcacheguard.AccountTx)
		legacy, err := tx.LegacyAccounts(2, 5)
		if err != nil || len(legacy) != 0 {
			return fmt.Errorf("old Key legacy=%+v: %w", legacy, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	windows, err = repo.AccountWindows(ctx, windowNow.Add(-6*time.Minute), windowNow)
	if err != nil || len(windows) != 1 || windows["3:6"].AccountID != 3 {
		t.Fatalf("old-Key window survived rotation: %+v err=%v", windows, err)
	}
	if err := engine.Refresh(ctx); err != nil {
		t.Fatalf("engine refresh after Key rotation: %v", err)
	}
	if snapshot := engine.CachedSnapshot(9); len(snapshot.Accounts) != 1 || snapshot.Accounts[0].AccountID != 3 {
		t.Fatalf("old-Key account still in scheduler snapshot: %+v", snapshot.Accounts)
	}
}

func cacheGuardScopedDSN(dsn, schema string) string {
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err == nil {
			q := u.Query()
			q.Set("search_path", schema)
			// Exercise JSON timestamps in a non-UTC session, including empty
			// policy windows whose zero instant has a historical second offset.
			q.Set("TimeZone", "Asia/Shanghai")
			u.RawQuery = q.Encode()
			return u.String()
		}
	}
	return dsn + " search_path=" + schema + " TimeZone=Asia/Shanghai"
}
