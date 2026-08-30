package migrations

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEnableBalanceNotificationsMigrationContract(t *testing.T) {
	sql := normalizedMigrationSQL(t, "254_enable_balance_notifications_for_existing_users.sql")

	require.Contains(t, sql, "UPDATE users")
	require.Contains(t, sql, "SET balance_notify_enabled = TRUE")
	require.Contains(t, sql, "WHERE deleted_at IS NULL")
	require.Contains(t, sql, "AND balance_notify_enabled = FALSE")
	require.Contains(t, sql, "RETURNING id")
	require.Contains(t, sql, "INSERT INTO auth_cache_invalidation_outbox (cache_key)")
	require.Contains(t, sql, "encode(sha256(convert_to(k.key, 'UTF8')), 'hex')")
	require.Contains(t, sql, "JOIN enabled_users AS u ON u.id = k.user_id")
	require.Contains(t, sql, "k.deleted_at IS NULL")
	require.Contains(t, sql, "CREATE OR REPLACE FUNCTION enqueue_user_auth_cache_invalidation()")
	require.Contains(t, sql, "OLD.email IS NOT DISTINCT FROM NEW.email")
	require.Contains(t, sql, "OLD.balance_notify_enabled IS NOT DISTINCT FROM NEW.balance_notify_enabled")
	require.Contains(t, sql, "OLD.balance_notify_threshold_type IS NOT DISTINCT FROM NEW.balance_notify_threshold_type")
	require.Contains(t, sql, "OLD.balance_notify_threshold IS NOT DISTINCT FROM NEW.balance_notify_threshold")
	require.Contains(t, sql, "OLD.balance_notify_extra_emails IS NOT DISTINCT FROM NEW.balance_notify_extra_emails")
	require.NotContains(t, sql, "SELECT k.key")
}
