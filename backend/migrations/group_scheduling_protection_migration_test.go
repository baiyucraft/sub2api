package migrations

import (
	"strings"
	"testing"
)

func TestGroupSchedulingProtectionMigrationUsesExplicitWhitelistColumn(t *testing.T) {
	raw, err := FS.ReadFile("298_group_scheduling_protection_policies.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(raw)
	// An unqualified `id` resolves to the outer groups.id before the set-returning
	// function's table alias, silently making every group match the whitelist.
	for _, clause := range []string{"AS selected(group_id_text)", "selected.group_id_text::bigint = g.id", "THEN 'inherit' ELSE 'disabled'", "ON CONFLICT (group_id) DO NOTHING"} {
		if !strings.Contains(sql, clause) {
			t.Errorf("missing migration clause %q", clause)
		}
	}
	if strings.Contains(sql, "WHERE id::bigint = g.id") || strings.Contains(sql, "UPDATE settings") || strings.Contains(sql, "DELETE FROM settings") {
		t.Fatal("ambiguous whitelist or legacy rollback configuration mutation")
	}
}
