package migrations

import (
	"crypto/sha256"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProfile248PreservesRenumberedOfficialRepairMigrationBytes(t *testing.T) {
	content, err := FS.ReadFile("268_group_model_allowlist_repair.sql")
	require.NoError(t, err)
	require.Equal(t, "752809cf1d3812ce241606ba8b015dac48bd6c04ab1cde9ef204d138cffb1025", fmt.Sprintf("%x", sha256.Sum256(content)))
	_, err = FS.ReadFile("236_group_model_allowlist_repair.sql")
	require.Error(t, err, "official filename must not re-enter the fork catalog")
}
