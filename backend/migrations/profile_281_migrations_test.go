package migrations

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProfile281OfficialMigrationContracts(t *testing.T) {
	cases := []struct {
		filename string
		rawHash  string
		markers  []string
	}{
		{
			filename: "305_scoped_admin_api_keys.sql",
			rawHash:  "0c4c22f564f276fb305a54cf2d6c4b1a6179d748d5d8b74a3b99676282355bf6",
			markers: []string{
				"CREATE TABLE IF NOT EXISTS scoped_admin_api_keys",
				"id VARCHAR(24) PRIMARY KEY",
				"key_hash VARCHAR(64) NOT NULL",
				"allowed_ips JSONB NOT NULL DEFAULT '[]'",
				"policy JSONB NOT NULL DEFAULT '{}'",
				"created_by BIGINT NOT NULL REFERENCES users(id)",
				"ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ",
			},
		},
		{
			filename: "306_group_image_count_billing.sql",
			rawHash:  "c91d6d31937658e71c68fa3af6a26fc47380f61243a681ac275aeb4706b242ab",
			markers: []string{
				"ALTER TABLE groups",
				"ADD COLUMN IF NOT EXISTS image_count_billing BOOLEAN NOT NULL DEFAULT false",
				"COMMENT ON COLUMN groups.image_count_billing",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.filename, func(t *testing.T) {
			content, err := FS.ReadFile(tc.filename)
			require.NoError(t, err)
			require.Equal(t, tc.rawHash, fmt.Sprintf("%x", sha256.Sum256(content)))
			sql := strings.Join(strings.Fields(string(content)), " ")
			for _, marker := range tc.markers {
				require.Contains(t, sql, marker)
			}
		})
	}
}
