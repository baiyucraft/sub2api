//go:build userdefaultpg

package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

// This suite deliberately fails without a positively identified isolated test
// database. It never falls back to application or production configuration.
func userDefaultPG(t *testing.T) (*sql.DB, *sql.Conn, string) {
	t.Helper()
	dsn := os.Getenv("SUB2API_USER_DEFAULT_RATE_TEST_DSN")
	require.NotEmpty(t, dsn, "SUB2API_USER_DEFAULT_RATE_TEST_DSN is required")
	require.Equal(t, "1", os.Getenv("SUB2API_USER_DEFAULT_RATE_TEST_ISOLATED"), "isolated PostgreSQL consent is required")
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var database string
	require.NoError(t, db.QueryRowContext(ctx, "SELECT current_database()").Scan(&database))
	require.Contains(t, strings.ToLower(database), "test", "database name must identify a test database")
	schema := fmt.Sprintf("user_default_rate_test_%d", time.Now().UnixNano())
	_, err = db.ExecContext(ctx, "CREATE SCHEMA "+schema)
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, err := db.ExecContext(cleanup, "DROP SCHEMA "+schema+" CASCADE")
		require.NoError(t, err)
	})
	conn := userDefaultPGConn(t, db, schema)
	_, err = conn.ExecContext(ctx, `
CREATE TABLE users (id BIGINT PRIMARY KEY, note TEXT NOT NULL DEFAULT '');
CREATE TABLE groups (id BIGINT PRIMARY KEY, rate_multiplier DECIMAL(20,10) NOT NULL);
CREATE TABLE user_group_rate_multipliers (
 user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 group_id BIGINT NOT NULL REFERENCES groups(id),
 rate_multiplier DECIMAL(20,10), rpm_override INTEGER,
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), PRIMARY KEY(user_id,group_id)
);
CREATE TABLE auth_cache_invalidation_outbox (id BIGINT PRIMARY KEY, event_type TEXT NOT NULL);
INSERT INTO users (id) VALUES (1),(2),(3);
INSERT INTO groups (id,rate_multiplier) VALUES (1,2),(2,3);
INSERT INTO user_group_rate_multipliers (user_id,group_id,rate_multiplier) VALUES (1,1,1.9);
INSERT INTO auth_cache_invalidation_outbox VALUES (9,'old-event');`)
	require.NoError(t, err)
	for _, name := range []string{"277_user_group_rate_percent.sql", "307_user_default_group_rate_percent.sql"} {
		raw, err := FS.ReadFile(name)
		require.NoError(t, err)
		_, err = conn.ExecContext(ctx, string(raw))
		require.NoError(t, err, name)
	}
	return db, conn, schema
}

func userDefaultPGConn(t *testing.T, db *sql.DB, schema string) *sql.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := db.Conn(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, conn.Close()) })
	_, err = conn.ExecContext(ctx, "SET search_path = "+schema+", pg_catalog; SET statement_timeout = '5s'; SET lock_timeout = '3s'")
	require.NoError(t, err)
	return conn
}

func userDefaultPGExec(t *testing.T, conn *sql.Conn, query string, args ...any) {
	t.Helper()
	_, err := conn.ExecContext(context.Background(), query, args...)
	require.NoError(t, err)
}

func userDefaultPGCount(t *testing.T, conn *sql.Conn) int {
	t.Helper()
	var count int
	require.NoError(t, conn.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM user_group_rate_invalidation_outbox").Scan(&count))
	return count
}

func userDefaultPGRevision(t *testing.T, conn *sql.Conn, userID int64) int64 {
	t.Helper()
	var revision int64
	require.NoError(t, conn.QueryRowContext(context.Background(), "SELECT revision FROM user_group_rate_invalidation_outbox WHERE user_id=$1", userID).Scan(&revision))
	return revision
}

func TestUserDefaultRatePGConstraintsIdempotencyAndExistingRows(t *testing.T) {
	_, conn, _ := userDefaultPG(t)
	raw, err := FS.ReadFile("307_user_default_group_rate_percent.sql")
	require.NoError(t, err)
	userDefaultPGExec(t, conn, string(raw))
	var defaults int
	require.NoError(t, conn.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM users WHERE default_group_rate_percent IS NULL").Scan(&defaults))
	require.Equal(t, 3, defaults)
	var percent, legacy string
	require.NoError(t, conn.QueryRowContext(context.Background(), "SELECT rate_percent::text,rate_multiplier::text FROM user_group_rate_multipliers WHERE user_id=1").Scan(&percent, &legacy))
	require.Equal(t, "95.0000000000", percent)
	require.Equal(t, "1.9000000000", legacy)
	require.Zero(t, userDefaultPGCount(t, conn), "migration must not backfill invalidations")
	for _, tc := range []struct{ value, expected string }{
		{"0", "0.0000000000"}, {"95", "95.0000000000"},
		{"95.1256789012", "95.1256789012"}, {"1000", "1000.0000000000"},
	} {
		userDefaultPGExec(t, conn, "UPDATE users SET default_group_rate_percent=$1 WHERE id=1", tc.value)
		var saved string
		require.NoError(t, conn.QueryRowContext(context.Background(), "SELECT default_group_rate_percent::text FROM users WHERE id=1").Scan(&saved))
		require.Equal(t, tc.expected, saved)
	}
	for _, value := range []string{"-0.1", "NaN", "Infinity", "-Infinity"} {
		_, err := conn.ExecContext(context.Background(), "UPDATE users SET default_group_rate_percent=$1 WHERE id=1", value)
		require.Error(t, err, value)
	}
	userDefaultPGExec(t, conn, "UPDATE users SET default_group_rate_percent=NULL WHERE id=1")
	var oldType string
	require.NoError(t, conn.QueryRowContext(context.Background(), "SELECT event_type FROM auth_cache_invalidation_outbox WHERE id=9").Scan(&oldType))
	require.Equal(t, "old-event", oldType)
}

func TestUserDefaultRatePGDefaultTransitionsAndRollback(t *testing.T) {
	_, conn, _ := userDefaultPG(t)
	for _, value := range []any{float64(0), float64(95), nil} {
		userDefaultPGExec(t, conn, "DELETE FROM user_group_rate_invalidation_outbox")
		userDefaultPGExec(t, conn, "UPDATE users SET default_group_rate_percent=$1 WHERE id=1", value)
		require.Equal(t, 1, userDefaultPGCount(t, conn))
		revision := userDefaultPGRevision(t, conn, 1)
		userDefaultPGExec(t, conn, "UPDATE users SET default_group_rate_percent=$1,note='other' WHERE id=1", value)
		require.Equal(t, revision, userDefaultPGRevision(t, conn, 1), "same default and unrelated fields must not enqueue")
	}
	userDefaultPGExec(t, conn, "DELETE FROM user_group_rate_invalidation_outbox")
	tx, err := conn.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	_, err = tx.Exec("UPDATE users SET default_group_rate_percent=95 WHERE id=1")
	require.NoError(t, err)
	require.NoError(t, tx.Rollback())
	require.Zero(t, userDefaultPGCount(t, conn))
	tx, err = conn.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	_, err = tx.Exec("UPDATE users SET default_group_rate_percent=0 WHERE id=1")
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
	require.Equal(t, 1, userDefaultPGCount(t, conn))
	userDefaultPGExec(t, conn, "DELETE FROM user_group_rate_invalidation_outbox; INSERT INTO users(id,default_group_rate_percent) VALUES(4,NULL),(5,0),(6,95)")
	require.Equal(t, 2, userDefaultPGCount(t, conn), "inserted 0 and 95 enqueue; NULL does not")
}

func TestUserDefaultRatePGOverrideTriggerRangeAndLegacyShadow(t *testing.T) {
	cases := []struct {
		name, query string
		count       int
	}{
		{"rpm-only insert", "INSERT INTO user_group_rate_multipliers(user_id,group_id,rpm_override) VALUES(2,1,5)", 0},
		{"rpm-only update", "UPDATE user_group_rate_multipliers SET rpm_override=10 WHERE user_id=1", 0},
		{"same percent", "UPDATE user_group_rate_multipliers SET rate_percent=95 WHERE user_id=1", 0},
		{"explicit free insert", "INSERT INTO user_group_rate_multipliers(user_id,group_id,rate_percent) VALUES(2,1,0)", 1},
		{"percent update", "UPDATE user_group_rate_multipliers SET rate_percent=90 WHERE user_id=1", 1},
		{"legacy update", "UPDATE user_group_rate_multipliers SET rate_multiplier=1.8 WHERE user_id=1", 1},
		{"clear override", "UPDATE user_group_rate_multipliers SET rate_percent=NULL WHERE user_id=1", 1},
		{"delete override", "DELETE FROM user_group_rate_multipliers WHERE user_id=1", 1},
		{"move group", "UPDATE user_group_rate_multipliers SET group_id=2 WHERE user_id=1", 1},
		{"move user", "UPDATE user_group_rate_multipliers SET user_id=2 WHERE user_id=1", 2},
		{"group shadow refresh", "UPDATE groups SET rate_multiplier=4 WHERE id=1", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, conn, _ := userDefaultPG(t)
			userDefaultPGExec(t, conn, tc.query)
			require.Equal(t, tc.count, userDefaultPGCount(t, conn))
			if tc.name == "legacy update" || tc.name == "group shadow refresh" {
				var percent, shadow string
				require.NoError(t, conn.QueryRowContext(context.Background(), "SELECT rate_percent::text,rate_multiplier::text FROM user_group_rate_multipliers WHERE user_id=1").Scan(&percent, &shadow))
				if tc.name == "legacy update" {
					require.Equal(t, "90.0000000000", percent)
				} else {
					require.Equal(t, "95.0000000000", percent)
					require.Equal(t, "3.8000000000", shadow)
				}
			}
		})
	}
	t.Run("rpm-only row update move and delete", func(t *testing.T) {
		_, conn, _ := userDefaultPG(t)
		userDefaultPGExec(t, conn, "INSERT INTO user_group_rate_multipliers(user_id,group_id,rpm_override) VALUES(2,1,1); UPDATE user_group_rate_multipliers SET rpm_override=2,user_id=3,group_id=2 WHERE user_id=2; DELETE FROM user_group_rate_multipliers WHERE user_id=3")
		require.Zero(t, userDefaultPGCount(t, conn))
	})
}

func TestUserDefaultRatePGRevisionABAAndParentDelete(t *testing.T) {
	_, conn, _ := userDefaultPG(t)
	userDefaultPGExec(t, conn, "UPDATE users SET default_group_rate_percent=95 WHERE id=1")
	first := userDefaultPGRevision(t, conn, 1)
	userDefaultPGExec(t, conn, "UPDATE users SET default_group_rate_percent=96 WHERE id=1")
	second := userDefaultPGRevision(t, conn, 1)
	require.Greater(t, second, first)
	userDefaultPGExec(t, conn, "DELETE FROM user_group_rate_invalidation_outbox WHERE user_id=1 AND revision=$1", first)
	require.Equal(t, second, userDefaultPGRevision(t, conn, 1))
	userDefaultPGExec(t, conn, "DELETE FROM user_group_rate_invalidation_outbox WHERE user_id=1 AND revision=$1", second)
	userDefaultPGExec(t, conn, "UPDATE users SET default_group_rate_percent=97 WHERE id=1")
	third := userDefaultPGRevision(t, conn, 1)
	require.Greater(t, third, second, "deletion and reinsert must not reuse revision")
	userDefaultPGExec(t, conn, "DELETE FROM user_group_rate_invalidation_outbox WHERE user_id=1 AND revision=$1", second)
	require.Equal(t, third, userDefaultPGRevision(t, conn, 1))
	userDefaultPGExec(t, conn, "DELETE FROM users WHERE id=1")
	require.Zero(t, userDefaultPGCount(t, conn), "cascaded override deletion must not resurrect an outbox FK")
}

func TestUserDefaultRatePGConcurrentOldAckCannotDeleteNewEvent(t *testing.T) {
	db, writer, schema := userDefaultPG(t)
	ack := userDefaultPGConn(t, db, schema)
	userDefaultPGExec(t, writer, "UPDATE users SET default_group_rate_percent=95 WHERE id=1")
	oldRevision := userDefaultPGRevision(t, writer, 1)
	tx, err := writer.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })
	_, err = tx.Exec("UPDATE users SET default_group_rate_percent=96 WHERE id=1")
	require.NoError(t, err)
	var newer int64
	require.NoError(t, tx.QueryRow("SELECT revision FROM user_group_rate_invalidation_outbox WHERE user_id=1").Scan(&newer))
	require.Greater(t, newer, oldRevision)
	var ackPID int
	require.NoError(t, ack.QueryRowContext(context.Background(), "SELECT pg_backend_pid()").Scan(&ackPID))
	type result struct {
		rows int64
		err  error
	}
	done := make(chan result, 1)
	go func() {
		res, err := ack.ExecContext(context.Background(), "DELETE FROM user_group_rate_invalidation_outbox WHERE user_id=1 AND revision=$1", oldRevision)
		if err != nil {
			done <- result{err: err}
			return
		}
		n, err := res.RowsAffected()
		done <- result{rows: n, err: err}
	}()
	// Observe PostgreSQL's lock barrier rather than rely on a sleep: the old
	// acknowledgement must have started while the replacement row is uncommitted.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for {
		var waiting bool
		err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM pg_locks WHERE pid=$1 AND NOT granted)", ackPID).Scan(&waiting)
		require.NoError(t, err)
		if waiting {
			break
		}
		select {
		case finished := <-done:
			t.Fatalf("old ack completed before writer commit: %+v", finished)
		case <-ctx.Done():
			t.Fatal("old ack never reached the PostgreSQL lock barrier")
		case <-time.After(time.Millisecond):
		}
	}
	require.NoError(t, tx.Commit())
	select {
	case finished := <-done:
		require.NoError(t, finished.err)
		require.Zero(t, finished.rows)
	case <-time.After(5 * time.Second):
		t.Fatal("old acknowledgement did not finish after writer commit")
	}
	require.Equal(t, newer, userDefaultPGRevision(t, writer, 1))
}
