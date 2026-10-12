package migrations

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// This is a static migration contract check, not PostgreSQL execution evidence.
func TestUserInvoiceAnnotationsMigrationPreservesLedgersAndHistory(t *testing.T) {
	content, err := FS.ReadFile("291_user_invoice_annotations.sql")
	require.NoError(t, err)
	comments := regexp.MustCompile(`(?m)--[^\n]*`)
	sql := strings.Join(strings.Fields(comments.ReplaceAllString(string(content), "")), " ")
	for _, fragment := range []string{
		"CREATE TABLE IF NOT EXISTS user_invoice_annotations",
		"CREATE TABLE IF NOT EXISTS user_invoice_annotation_events",
		"amount DECIMAL(20, 8) NOT NULL CHECK (amount > 0 AND amount < 1000000000000)",
		"UNIQUE (user_id, created_by, operation_id)",
		"REFERENCES user_invoice_annotations(id) ON DELETE RESTRICT",
		"before_value JSONB",
		"after_value JSONB NOT NULL",
		"(action = 'create' AND before_value IS NULL)",
		"(action IN ('update', 'void') AND before_value IS NOT NULL)",
		"ON user_invoice_annotations (user_id, created_at DESC, id DESC)",
		"ON user_invoice_annotation_events (invoice_id, created_at DESC, id DESC)",
	} {
		require.Contains(t, sql, fragment)
	}
	// A nullable actor only checked with > 0 yields UNKNOWN for NULL, which
	// PostgreSQL CHECK accepts. The explicit null test is part of the contract.
	require.Contains(t, sql, "status = 'voided' AND char_length(void_reason) > 0 AND voided_by IS NOT NULL AND voided_by > 0 AND voided_at IS NOT NULL")
	require.Contains(t, sql, "status = 'active' AND void_reason = '' AND voided_by IS NULL AND voided_at IS NULL")
	for _, forbidden := range []string{"ALTER TABLE", "DROP TABLE", "UPDATE users", "REFERENCES users", "currency", "redeem_codes", "payment_orders"} {
		require.NotContains(t, sql, forbidden)
	}
}
