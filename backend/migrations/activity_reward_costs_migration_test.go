package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestActivityRewardCostsMigrationContract(t *testing.T) {
	content, err := FS.ReadFile("289_activity_reward_costs.sql")
	require.NoError(t, err)
	sql := strings.Join(strings.Fields(string(content)), " ")
	for _, fragment := range []string{
		"activity_reward_id BIGINT", "related_user_id BIGINT", "activity_type VARCHAR(32)",
		"UNIQUE INDEX IF NOT EXISTS extra_cost_entries_activity_reward_uq", "(activity_reward_id)",
		"CREATE OR REPLACE FUNCTION backfill_activity_reward_costs()", "status = 'credited'",
		"AT TIME ZONE 'Asia/Shanghai'", "activity-reward-cost-v1", "activity-reward:",
		"ON CONFLICT DO NOTHING", "SELECT backfill_activity_reward_costs()",
	} {
		require.Contains(t, sql, fragment)
	}
	require.NotContains(t, strings.ToUpper(sql), "UPDATE USERS")
	require.NotContains(t, strings.ToUpper(sql), "DELETE FROM")
	require.NotContains(t, strings.ToUpper(sql), "REFERENCES")
}
