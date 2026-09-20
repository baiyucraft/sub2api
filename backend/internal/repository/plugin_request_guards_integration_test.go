//go:build integration

package repository

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

type pluginRequestGuardFixture struct {
	ctx      context.Context
	repo     *pluginRepository
	id       int64
	sha      string
	revision uint64
}

func newPluginRequestGuardFixture(t *testing.T) pluginRequestGuardFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	var suffix [16]byte
	_, err := rand.Read(suffix[:])
	require.NoError(t, err)
	key := "test.request-guard." + hex.EncodeToString(suffix[:])
	f := pluginRequestGuardFixture{ctx: ctx, repo: &pluginRepository{db: integrationDB}, sha: strings.Repeat("a", 64), revision: 7}
	err = integrationDB.QueryRowContext(ctx, `
		INSERT INTO sub2api_plugin_installations
		(plugin_key, name, version, artifact_path, install_path, binary_path, binary_sha256, state, config_revision)
		VALUES ($1, 'request guard test', '1', '', '', '', $2, 'enabled', $3) RETURNING id
	`, key, f.sha, f.revision).Scan(&f.id)
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_, err := integrationDB.ExecContext(cleanup, `DELETE FROM sub2api_plugin_installations WHERE id=$1`, f.id)
		require.NoError(t, err)
	})
	_, err = integrationDB.ExecContext(ctx, `
		INSERT INTO sub2api_plugin_bindings (plugin_id, capability, platform, account_type, enabled)
		VALUES ($1, $2, 'openai', 'oauth', TRUE)
	`, f.id, key)
	require.NoError(t, err)
	return f
}

func requirePluginRequestCount(t *testing.T, f pluginRequestGuardFixture, expected int64) {
	t.Helper()
	count, err := f.repo.PluginRequestsInFlight(f.ctx, f.id)
	require.NoError(t, err)
	require.Equal(t, expected, count)
}

func TestPluginRequestGuardsPostgresAdmissionPredicates(t *testing.T) {
	for _, condition := range []string{"enabled", "disabled", "starting", "error", "incompatible", "upgrading", "wrong_sha", "wrong_revision", "no_binding", "disabled_binding", "missing_plugin"} {
		t.Run(condition, func(t *testing.T) {
			f := newPluginRequestGuardFixture(t)
			id, sha, revision := f.id, f.sha, f.revision
			switch condition {
			case "wrong_sha":
				sha = strings.Repeat("b", 64)
			case "wrong_revision":
				revision++
			case "no_binding":
				_, err := integrationDB.ExecContext(f.ctx, `DELETE FROM sub2api_plugin_bindings WHERE plugin_id=$1`, f.id)
				require.NoError(t, err)
			case "disabled_binding":
				_, err := integrationDB.ExecContext(f.ctx, `UPDATE sub2api_plugin_bindings SET enabled=FALSE WHERE plugin_id=$1`, f.id)
				require.NoError(t, err)
			case "missing_plugin":
				id = -1
			default:
				_, err := integrationDB.ExecContext(f.ctx, `UPDATE sub2api_plugin_installations SET state=$2 WHERE id=$1`, f.id, condition)
				require.NoError(t, err)
			}
			err := f.repo.BeginPluginRequest(f.ctx, id, sha, revision, "request-a")
			if condition == "enabled" {
				require.NoError(t, err)
				requirePluginRequestCount(t, f, 1)
			} else {
				require.ErrorIs(t, err, service.ErrPluginStateChanged)
				requirePluginRequestCount(t, f, 0)
			}
		})
	}
}

func TestPluginRequestGuardsPostgresCountAndEndIsolation(t *testing.T) {
	first := newPluginRequestGuardFixture(t)
	second := newPluginRequestGuardFixture(t)
	for _, f := range []pluginRequestGuardFixture{first, second} {
		require.NoError(t, f.repo.BeginPluginRequest(f.ctx, f.id, f.sha, f.revision, "same-request-id"))
		requirePluginRequestCount(t, f, 1)
	}
	err := first.repo.BeginPluginRequest(first.ctx, first.id, first.sha, first.revision, "same-request-id")
	var pgErr *pq.Error
	require.ErrorAs(t, err, &pgErr)
	require.Equal(t, pq.ErrorCode("23505"), pgErr.Code)
	requirePluginRequestCount(t, first, 1)

	require.NoError(t, first.repo.EndPluginRequest(first.ctx, first.id, "unknown-request"))
	requirePluginRequestCount(t, first, 1)
	require.NoError(t, first.repo.EndPluginRequest(first.ctx, first.id, "same-request-id"))
	require.NoError(t, first.repo.EndPluginRequest(first.ctx, first.id, "same-request-id"))
	requirePluginRequestCount(t, first, 0)
	requirePluginRequestCount(t, second, 1)

	// Age is never proof of completion, even while the installation is upgrading.
	_, err = integrationDB.ExecContext(second.ctx, `UPDATE sub2api_plugin_runtime_requests SET created_at=clock_timestamp()-interval '365 days' WHERE plugin_id=$1`, second.id)
	require.NoError(t, err)
	previous, err := second.repo.GetByID(second.ctx, second.id)
	require.NoError(t, err)
	require.NoError(t, second.repo.BeginPluginMaintenance(second.ctx, previous, maintenanceTestOwner, time.Minute))
	requirePluginRequestCount(t, second, 1)
	require.NoError(t, second.repo.EndPluginRequest(second.ctx, second.id, "same-request-id"))
	requirePluginRequestCount(t, second, 0)
}

func waitPluginGuardBlockedPID(t *testing.T, ctx context.Context, blockerPID int) int {
	t.Helper()
	var waitingPID int
	require.Eventually(t, func() bool {
		return integrationDB.QueryRowContext(ctx, `SELECT pid FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)) LIMIT 1`, blockerPID).Scan(&waitingPID) == nil
	}, 5*time.Second, 10*time.Millisecond, "expected an actual PostgreSQL lock wait")
	return waitingPID
}

func awaitPluginGuardResult(t *testing.T, ctx context.Context, results <-chan error) error {
	t.Helper()
	select {
	case err := <-results:
		return err
	case <-ctx.Done():
		t.Fatal(ctx.Err())
		return ctx.Err()
	}
}

func TestPluginRequestGuardsPostgresUpgradeWaitsForAtomicAdmission(t *testing.T) {
	f := newPluginRequestGuardFixture(t)
	ctx, cancel := context.WithCancel(f.ctx)
	defer cancel()
	barrier, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = barrier.Rollback() }()
	lockID := int64(0x5052470000000000) + f.id
	_, err = barrier.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, lockID)
	require.NoError(t, err)
	var barrierPID int
	require.NoError(t, barrier.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&barrierPID))

	// Pause the real BeginPluginRequest between its locked SELECT and INSERT
	// completion. This proves an upgrade cannot slip into that exact interval.
	name := pq.QuoteIdentifier(fmt.Sprintf("test_plugin_admission_%d", f.id))
	_, err = integrationDB.ExecContext(ctx, fmt.Sprintf(`
		CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.plugin_id = %d THEN PERFORM pg_advisory_xact_lock(%d); END IF;
			RETURN NEW;
		END; $$;
		CREATE TRIGGER %s BEFORE INSERT ON sub2api_plugin_runtime_requests
		FOR EACH ROW EXECUTE FUNCTION %s();
	`, name, f.id, lockID, name, name))
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_, err := integrationDB.ExecContext(cleanup, fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON sub2api_plugin_runtime_requests; DROP FUNCTION IF EXISTS %s()`, name, name))
		require.NoError(t, err)
	})
	admitted := make(chan error, 1)
	go func() { admitted <- f.repo.BeginPluginRequest(ctx, f.id, f.sha, f.revision, "request-a") }()
	admissionPID := waitPluginGuardBlockedPID(t, ctx, barrierPID)
	requirePluginRequestCount(t, f, 0)
	previous, err := f.repo.GetByID(ctx, f.id)
	require.NoError(t, err)
	upgraded := make(chan error, 1)
	go func() {
		upgraded <- f.repo.BeginPluginMaintenance(ctx, previous, maintenanceTestOwner, time.Minute)
	}()
	waitPluginGuardBlockedPID(t, ctx, admissionPID)
	require.NoError(t, barrier.Commit())
	require.NoError(t, awaitPluginGuardResult(t, ctx, admitted))
	require.NoError(t, awaitPluginGuardResult(t, ctx, upgraded))
	requirePluginRequestCount(t, f, 1)
	require.ErrorIs(t, f.repo.BeginPluginRequest(ctx, f.id, f.sha, f.revision, "late-request"), service.ErrPluginStateChanged)
	requirePluginRequestCount(t, f, 1)
	require.NoError(t, f.repo.EndPluginRequest(ctx, f.id, "request-a"))
	requirePluginRequestCount(t, f, 0)
}

func TestPluginRequestGuardsPostgresAdmissionWaitsForUpgrade(t *testing.T) {
	for _, commit := range []bool{true, false} {
		t.Run(map[bool]string{true: "upgrade_commits", false: "upgrade_rolls_back"}[commit], func(t *testing.T) {
			f := newPluginRequestGuardFixture(t)
			ctx, cancel := context.WithCancel(f.ctx)
			defer cancel()
			tx, err := integrationDB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
			require.NoError(t, err)
			defer func() { _ = tx.Rollback() }()
			_, err = tx.ExecContext(ctx, `UPDATE sub2api_plugin_installations SET state='upgrading' WHERE id=$1`, f.id)
			require.NoError(t, err)
			var upgradePID int
			require.NoError(t, tx.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&upgradePID))
			admitted := make(chan error, 1)
			go func() { admitted <- f.repo.BeginPluginRequest(ctx, f.id, f.sha, f.revision, "request-a") }()
			waitPluginGuardBlockedPID(t, ctx, upgradePID)
			requirePluginRequestCount(t, f, 0)
			if commit {
				require.NoError(t, tx.Commit())
				require.ErrorIs(t, awaitPluginGuardResult(t, ctx, admitted), service.ErrPluginStateChanged)
				requirePluginRequestCount(t, f, 0)
				// This fixture used raw SQL without a journal; only fixture SQL may
				// undo it. Public state writes cannot bypass maintenance anymore.
				require.ErrorIs(t, f.repo.UpdateState(ctx, f.id, service.PluginStateEnabled, "", nil, f.sha, service.PluginStateUpgrading), service.ErrPluginStateChanged)
				_, err = integrationDB.ExecContext(ctx, `UPDATE sub2api_plugin_installations SET state='enabled' WHERE id=$1`, f.id)
				require.NoError(t, err)
				require.NoError(t, f.repo.BeginPluginRequest(ctx, f.id, f.sha, f.revision, "request-a"))
			} else {
				require.NoError(t, tx.Rollback())
				require.NoError(t, awaitPluginGuardResult(t, ctx, admitted))
			}
			requirePluginRequestCount(t, f, 1)
		})
	}
}

func TestPluginRequestGuardsPostgresInstallationDeleteCascades(t *testing.T) {
	f := newPluginRequestGuardFixture(t)
	require.NoError(t, f.repo.BeginPluginRequest(f.ctx, f.id, f.sha, f.revision, "request-a"))
	_, err := integrationDB.ExecContext(f.ctx, `DELETE FROM sub2api_plugin_installations WHERE id=$1`, f.id)
	require.NoError(t, err)
	requirePluginRequestCount(t, f, 0)
	require.NoError(t, f.repo.EndPluginRequest(f.ctx, f.id, "request-a"))
}
