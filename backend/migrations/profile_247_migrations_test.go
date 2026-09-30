package migrations

import (
	"crypto/sha256"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProfile247PreservesRenumberedUpstreamAllowlistMigrationBytes(t *testing.T) {
	checksums := map[string]string{
		"267_group_model_allowlist.sql": "3479c2812ae32f1ae1fa69b1c82ef55ce0861b207066e3b621974e9f44c3f208",
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
