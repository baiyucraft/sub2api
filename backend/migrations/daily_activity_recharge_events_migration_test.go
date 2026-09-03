package migrations

import (
	"strings"
	"testing"
)

func TestDailyActivityRechargeEventsMigrationKeepsAdminOperationExplicit(t *testing.T) {
	sqlBytes, err := FS.ReadFile("261_daily_activity_recharge_events.sql")
	if err != nil {
		t.Fatal(err)
	}
	normalized := strings.ToLower(string(sqlBytes))
	for _, fragment := range []string{
		"create table if not exists activity_recharge_events",
		"unique(source_type, source_key)",
		"check (amount > 0)",
		"idx_activity_recharge_events_user_time",
	} {
		if !strings.Contains(normalized, fragment) {
			t.Fatalf("migration missing %q", fragment)
		}
	}
}
