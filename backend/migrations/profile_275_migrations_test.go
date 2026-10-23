package migrations

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProfile275OfficialMigrationRawBytesAndScope(t *testing.T) {
	content, err := FS.ReadFile("302_enable_gemini_image_generation_groups.sql")
	require.NoError(t, err)
	require.Equal(t, "f49b458cfbd52bd17f5bb02e30e3813c8e192e4ae38bce6358713ef65f3874b5", fmt.Sprintf("%x", sha256.Sum256(content)))
	_, err = FS.ReadFile("246_enable_gemini_image_generation_groups.sql")
	require.ErrorIs(t, err, fs.ErrNotExist)
	for _, filename := range []string{"246_upstream_auth_sessions.sql", "301_upstream_cache_guard_admissions.sql"} {
		_, err = FS.ReadFile(filename)
		require.NoError(t, err, filename)
	}
	var statements []string
	for _, line := range strings.Split(string(content), "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "--") {
			statements = append(statements, line)
		}
	}
	require.Equal(t, "UPDATE groups SET allow_image_generation = true WHERE platform IN ('gemini', 'antigravity') AND allow_image_generation = false;", strings.Join(strings.Fields(strings.Join(statements, " ")), " "))
}
