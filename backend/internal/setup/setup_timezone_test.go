package setup

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSetupDatabaseDSNIncludesApplicationTimezone(t *testing.T) {
	cfg := &SetupConfig{
		Timezone: "UTC",
		Database: DatabaseConfig{
			Host: "localhost", Port: 5432, User: "postgres", Password: "secret",
			DBName: "sub2api", SSLMode: "disable",
		},
	}
	dsn := setupDatabaseDSN(cfg)
	require.Contains(t, dsn, "TimeZone=UTC")
	require.NotContains(t, dsn, "TimeZone=Asia/Shanghai")
}

func TestSetupDatabaseDSNDefaultsTimezone(t *testing.T) {
	cfg := &SetupConfig{Database: DatabaseConfig{}}
	require.True(t, strings.HasSuffix(setupDatabaseDSN(cfg), "TimeZone=Asia/Shanghai"))
}
