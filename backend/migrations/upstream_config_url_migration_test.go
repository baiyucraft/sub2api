package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigration177ReplacesBaseURLWithSiteAndAPIURLs(t *testing.T) {
	content, err := FS.ReadFile("177_upstream_config_site_and_api_urls.sql")
	require.NoError(t, err)

	sql := string(content)
	normalized := strings.ToLower(sql)
	require.Contains(t, normalized, "rename column base_url to site_url")
	require.Contains(t, normalized, "add column api_url varchar(512) null")
	require.NotContains(t, normalized, "add column site_url")
	require.NotContains(t, normalized, "update upstream_configs")
	require.NotContains(t, normalized, "drop column base_url")
}
