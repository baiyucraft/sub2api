package migrations

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOptionalImageToolsMigrationPreservesAuthInvalidationContract(t *testing.T) {
	content, err := os.ReadFile("303_group_strip_optional_image_tools.sql")
	require.NoError(t, err)
	sql := string(content)
	require.Contains(t, sql, "strip_optional_image_tools BOOLEAN NOT NULL DEFAULT FALSE")
	require.Contains(t, sql, "OLD.strip_optional_image_tools IS NOT DISTINCT FROM NEW.strip_optional_image_tools")

	// Replacing the trigger function must retain every existing comparison.
	previous, err := os.ReadFile("237_image_cost_routing.sql")
	require.NoError(t, err)
	for _, line := range strings.Split(string(previous), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "AND OLD.") {
			require.Contains(t, sql, line)
		}
	}
}
