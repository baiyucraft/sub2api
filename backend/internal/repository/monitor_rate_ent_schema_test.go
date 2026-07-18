package repository

import (
	"testing"

	entschema "entgo.io/ent/dialect/sql/schema"
	entmigrate "github.com/Wei-Shaw/sub2api/ent/migrate"
	"github.com/stretchr/testify/require"
)

func TestMonitorRateEntSchemaMatchesMigration195(t *testing.T) {
	requireEntIndex(t, entmigrate.APIKeysTable, "idx_api_keys_purpose")
	requireEntIndex(t, entmigrate.APIKeysTable, "idx_api_keys_managed_monitor_id")
	requireEntIndex(t, entmigrate.ChannelMonitorsTable, "idx_channel_monitors_group_id")
	requireEntIndex(t, entmigrate.ChannelMonitorsTable, "idx_channel_monitors_managed_api_key_id")
	requireEntIndex(t, entmigrate.GroupRateSnapshotsTable, "idx_group_rate_snapshots_group_effective")

	requireEntForeignKey(t, entmigrate.ChannelMonitorsTable, "channel_monitors_group_id_fkey", entschema.SetNull)
	requireEntForeignKey(t, entmigrate.ChannelMonitorsTable, "channel_monitors_managed_api_key_id_fkey", entschema.SetNull)
	requireEntForeignKey(t, entmigrate.GroupRateSnapshotsTable, "group_rate_snapshots_group_id_fkey", entschema.Cascade)
}

func requireEntIndex(t *testing.T, table *entschema.Table, name string) {
	t.Helper()
	for _, idx := range table.Indexes {
		if idx.Name == name {
			return
		}
	}
	require.Failf(t, "missing Ent index", "%s.%s", table.Name, name)
}

func requireEntForeignKey(t *testing.T, table *entschema.Table, symbol string, onDelete entschema.ReferenceOption) {
	t.Helper()
	for _, fk := range table.ForeignKeys {
		if fk.Symbol == symbol {
			require.Equal(t, onDelete, fk.OnDelete)
			return
		}
	}
	require.Failf(t, "missing Ent foreign key", "%s.%s", table.Name, symbol)
}
