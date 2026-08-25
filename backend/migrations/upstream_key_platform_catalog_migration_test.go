package migrations

import (
	"strings"
	"testing"
)

func TestUpstreamKeyPlatformCatalogMigrationIncludesRegisteredPlatforms(t *testing.T) {
	sql, err := FS.ReadFile("251_upstream_key_platform_catalog.sql")
	if err != nil {
		t.Fatal(err)
	}
	normalized := strings.ToLower(string(sql))
	for _, platform := range []string{"antigravity", "kimi", "zhipu", "deepseek"} {
		if !strings.Contains(normalized, "'"+platform+"'") {
			t.Fatalf("migration does not include %s", platform)
		}
	}
}
