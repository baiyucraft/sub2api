package repository

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRunMigrationsRequiresConfig(t *testing.T) {
	require.EqualError(t, RunMigrations(nil), "config is required")
}
