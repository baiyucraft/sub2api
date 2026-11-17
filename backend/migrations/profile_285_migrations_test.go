package migrations

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProfile285OfficialUltrafastAndNonblockingRollupMigrationContract(t *testing.T) {
	cases := []struct {
		name    string
		digest  string
		markers []string
	}{
		{
			name:   "311_group_force_openai_ultrafast.sql",
			digest: "f91352f2d3b1bffbbea5f6b7fc17e37c5ad64f04c3426cb7168778d10135a7bf",
			markers: []string{
				"ADD COLUMN IF NOT EXISTS force_openai_ultrafast BOOLEAN NOT NULL DEFAULT FALSE",
				"COMMENT ON COLUMN groups.force_openai_ultrafast",
			},
		},
		{
			name:   "312_group_usage_rollup_nonblocking_build.sql",
			digest: "da504646104cec14aa7bcf5a0af6f06f5cd40883459379ae915db27c1d4cec8b",
			markers: []string{
				"SET LOCAL lock_timeout = '5s'",
				"ADD COLUMN IF NOT EXISTS expired_before TIMESTAMPTZ NOT NULL",
				"ADD COLUMN IF NOT EXISTS building_until DATE",
				"ADD COLUMN IF NOT EXISTS build_dirty_from DATE",
				"CREATE OR REPLACE FUNCTION apply_group_usage_rollup_invalidation",
				"CREATE OR REPLACE FUNCTION invalidate_group_usage_rollups_at",
				"FOR KEY SHARE",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := FS.ReadFile(tc.name)
			require.NoError(t, err)
			require.Equal(t, tc.digest, fmt.Sprintf("%x", sha256.Sum256(raw)))
			sql := strings.Join(strings.Fields(string(raw)), " ")
			for _, marker := range tc.markers {
				require.Contains(t, sql, marker)
			}
		})
	}
	for _, name := range []string{"251_group_force_openai_ultrafast.sql", "251_group_usage_rollup_nonblocking_build.sql"} {
		_, err := FS.ReadFile(name)
		require.ErrorIs(t, err, fs.ErrNotExist)
	}
}
