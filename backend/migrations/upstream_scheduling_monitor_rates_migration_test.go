package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUpstreamSchedulingMonitorRatesMigrationContract(t *testing.T) {
	content, err := FS.ReadFile("195_upstream_scheduling_monitor_rates.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "scheduling_enabled BOOLEAN NOT NULL DEFAULT TRUE")
	require.Contains(t, sql, "upstream_source_rate_multiplier DECIMAL(20,10)")
	require.Contains(t, sql, "credential_mode VARCHAR(32) NOT NULL DEFAULT 'manual'")
	require.Contains(t, sql, "max_probe_attempts INT NOT NULL DEFAULT 3")
	require.Contains(t, sql, "CHECK (max_probe_attempts BETWEEN 1 AND 5)")
	require.Contains(t, sql, "'unknown'")
	require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS group_rate_snapshots")
	require.Contains(t, sql, "CREATE TRIGGER trg_record_group_rate_snapshot")
	require.Contains(t, sql, "idx_api_keys_purpose")
	require.Contains(t, sql, "idx_api_keys_managed_monitor_id")
	require.Contains(t, sql, "idx_channel_monitors_group_id")
	require.Contains(t, sql, "idx_channel_monitors_managed_api_key_id")
	require.Contains(t, sql, "idx_group_rate_snapshots_group_effective")
	require.Contains(t, sql, "conrelid = 'channel_monitors'::regclass")
	require.Contains(t, sql, "contype = 'f'")
	require.Contains(t, sql, "NULLIF(current_setting('TIMEZONE', TRUE), '')")
	require.Contains(t, sql, "CEIL((source_rate_multiplier * c.recharge_rate) * 100) / 100")
	require.Contains(t, sql, "derived_priority := CEIL(key_actual_rate * 100)::INTEGER")
	require.Contains(t, sql, "NEW.upstream_source_rate_multiplier := key_source_rate")
	require.Contains(t, sql, "concurrency, schedulable, deleted_at")
	require.Contains(t, sql, "UPDATE accounts SET rate_multiplier = rate_multiplier, updated_at = NOW() WHERE upstream_key_id IS NOT NULL")
	require.NotContains(t, sql, "ROUND(source_rate_multiplier * c.recharge_rate, 4)")
	require.Contains(t, sql, "'account_bulk_changed'")

	triggerAt := strings.Index(sql, "CREATE TRIGGER trg_validate_account_upstream_key_binding")
	recalculateAt := strings.Index(sql, "UPDATE accounts SET rate_multiplier = rate_multiplier")
	outboxAt := strings.Index(sql, "INSERT INTO scheduler_outbox")
	require.Greater(t, recalculateAt, triggerAt, "existing accounts must be recalculated by the new trigger")
	require.Greater(t, outboxAt, recalculateAt, "scheduler cache invalidation must follow account recalculation")
}
