//go:build integration

package repository

import (
	"context"
	"database/sql"
	"testing"

	dbmigrations "github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

// The integration harness has already applied all migrations to public. Shadow
// the three source tables in a rolled-back schema to exercise the legacy shape.
func TestMigration284UnifiesProxyBindingsAndReplays(t *testing.T) {
	ctx := context.Background()
	tx := testTx(t)
	_, err := tx.ExecContext(ctx, `CREATE SCHEMA migration_284_contract`)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `SET LOCAL search_path TO migration_284_contract, public`)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `
CREATE SEQUENCE proxies_id_seq START WITH 1;
CREATE TABLE proxies (id BIGINT PRIMARY KEY DEFAULT nextval('proxies_id_seq'));
CREATE TABLE proxy_ip_groups (id BIGSERIAL PRIMARY KEY);
CREATE TABLE accounts (
    id BIGSERIAL PRIMARY KEY,
    proxy_id BIGINT REFERENCES proxies(id),
    proxy_ip_group_id BIGINT REFERENCES proxy_ip_groups(id),
    CONSTRAINT accounts_proxy_source_exclusive CHECK (proxy_id IS NULL OR proxy_ip_group_id IS NULL)
);
INSERT INTO proxies (id) VALUES (1), (5);
INSERT INTO proxy_ip_groups (id) VALUES (1), (2);
SELECT setval('proxy_ip_groups_id_seq', 2, true);
INSERT INTO accounts (id, proxy_id) VALUES (1, 5);
INSERT INTO accounts (id, proxy_ip_group_id) VALUES (2, 1);
`)
	require.NoError(t, err)

	migrationSQL, err := dbmigrations.FS.ReadFile("284_unified_proxy_bindings.sql")
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(migrationSQL))
	require.NoError(t, err)

	var groupOneBinding, groupTwoBinding int64
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT binding_id FROM proxy_ip_groups WHERE id = 1`).Scan(&groupOneBinding))
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT binding_id FROM proxy_ip_groups WHERE id = 2`).Scan(&groupTwoBinding))
	require.Equal(t, int64(6), groupOneBinding)
	require.Equal(t, int64(7), groupTwoBinding)
	requireBinding284(t, ctx, tx, 1, "proxy", sql.NullInt64{Int64: 1, Valid: true}, sql.NullInt64{})
	requireBinding284(t, ctx, tx, 5, "proxy", sql.NullInt64{Int64: 5, Valid: true}, sql.NullInt64{})
	requireBinding284(t, ctx, tx, groupOneBinding, "proxy_ip_group", sql.NullInt64{}, sql.NullInt64{Int64: 1, Valid: true})
	requireBinding284(t, ctx, tx, groupTwoBinding, "proxy_ip_group", sql.NullInt64{}, sql.NullInt64{Int64: 2, Valid: true})

	var accountProxyID int64
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT proxy_id FROM accounts WHERE id = 1`).Scan(&accountProxyID))
	require.Equal(t, int64(5), accountProxyID)
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT proxy_id FROM accounts WHERE id = 2`).Scan(&accountProxyID))
	require.Equal(t, groupOneBinding, accountProxyID)
	var legacyColumnCount int
	require.NoError(t, tx.QueryRowContext(ctx, `
SELECT COUNT(*) FROM information_schema.columns
WHERE table_schema = 'migration_284_contract' AND table_name = 'accounts' AND column_name = 'proxy_ip_group_id'
`).Scan(&legacyColumnCount))
	require.Zero(t, legacyColumnCount)

	var newProxyID, newGroupID, newGroupBinding int64
	require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO proxies DEFAULT VALUES RETURNING id`).Scan(&newProxyID))
	require.Equal(t, int64(8), newProxyID)
	require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO proxy_ip_groups DEFAULT VALUES RETURNING id`).Scan(&newGroupID))
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT binding_id FROM proxy_ip_groups WHERE id = $1`, newGroupID).Scan(&newGroupBinding))
	require.Equal(t, int64(9), newGroupBinding)
	requireBinding284(t, ctx, tx, newProxyID, "proxy", sql.NullInt64{Int64: newProxyID, Valid: true}, sql.NullInt64{})

	_, err = tx.ExecContext(ctx, `DELETE FROM proxies WHERE id = $1`, newProxyID)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `DELETE FROM proxy_ip_groups WHERE id = $1`, newGroupID)
	require.NoError(t, err)
	requireBinding284(t, ctx, tx, newProxyID, "proxy", sql.NullInt64{Int64: newProxyID, Valid: true}, sql.NullInt64{})
	requireBinding284(t, ctx, tx, newGroupBinding, "proxy_ip_group", sql.NullInt64{}, sql.NullInt64{Int64: newGroupID, Valid: true})

	var nextProxyID, nextGroupID, nextGroupBinding int64
	require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO proxies DEFAULT VALUES RETURNING id`).Scan(&nextProxyID))
	require.Greater(t, nextProxyID, newGroupBinding)
	require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO proxy_ip_groups DEFAULT VALUES RETURNING id`).Scan(&nextGroupID))
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT binding_id FROM proxy_ip_groups WHERE id = $1`, nextGroupID).Scan(&nextGroupBinding))
	require.Greater(t, nextGroupBinding, nextProxyID)

	_, err = tx.ExecContext(ctx, string(migrationSQL))
	require.NoError(t, err)
	var bindingCount int
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM proxy_bindings`).Scan(&bindingCount))
	require.Equal(t, 8, bindingCount)
	requireBinding284(t, ctx, tx, groupOneBinding, "proxy_ip_group", sql.NullInt64{}, sql.NullInt64{Int64: 1, Valid: true})
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT proxy_id FROM accounts WHERE id = 2`).Scan(&accountProxyID))
	require.Equal(t, groupOneBinding, accountProxyID)
}

func requireBinding284(t *testing.T, ctx context.Context, tx *sql.Tx, id int64, kind string, proxyID, groupID sql.NullInt64) {
	t.Helper()
	var actualKind string
	var actualProxyID, actualGroupID sql.NullInt64
	require.NoError(t, tx.QueryRowContext(ctx, `
SELECT binding_type, proxy_id, proxy_ip_group_id FROM proxy_bindings WHERE id = $1
`, id).Scan(&actualKind, &actualProxyID, &actualGroupID))
	require.Equal(t, kind, actualKind)
	require.Equal(t, proxyID, actualProxyID)
	require.Equal(t, groupID, actualGroupID)
}
