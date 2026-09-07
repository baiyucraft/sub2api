package migrations

import (
	"crypto/sha256"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProfile247PreservesRenumberedUpstreamAllowlistMigrationBytes(t *testing.T) {
	checksums := map[string]string{
		"267_group_model_allowlist.sql": "34dc807ff3b951533f54b3f575d2b6af7ece851150e95216b887be4d6755cd96",
	}
	for filename, expected := range checksums {
		t.Run(filename, func(t *testing.T) {
			content, err := FS.ReadFile(filename)
			require.NoError(t, err)
			require.Equal(t, expected, fmt.Sprintf("%x", sha256.Sum256(content)))
		})
	}
	_, err := FS.ReadFile("235_group_model_allowlist.sql")
	require.Error(t, err, "upstream filename must not re-enter the fork catalog: 235_group_model_allowlist.sql")
}

func TestProfile247AdditiveSchemaContract(t *testing.T) {
	sql := normalizedMigrationSQL(t, "267_group_model_allowlist.sql")
	require.Contains(t, sql, "RENAME COLUMN models_list_config TO model_allowlist")
	require.Contains(t, sql, "COMMENT ON COLUMN groups.model_allowlist")
	require.NotContains(t, sql, "DROP COLUMN")
}
