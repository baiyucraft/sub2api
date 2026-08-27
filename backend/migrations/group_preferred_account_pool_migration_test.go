package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGroupPreferredAccountPoolMigrationIsIdempotentAndScoped(t *testing.T) {
	sql, err := FS.ReadFile("253_group_preferred_account_pool.sql")
	require.NoError(t, err)
	normalized := strings.ToLower(string(sql))

	require.Contains(t, normalized, "alter table account_groups")
	require.Contains(t, normalized, "add column if not exists scheduler_preferred boolean not null default false")
	require.Contains(t, normalized, "create index if not exists")
	require.Contains(t, normalized, "where scheduler_preferred = true")
	require.NotContains(t, normalized, "drop table")
	require.NotContains(t, normalized, "delete from account_groups")
}
