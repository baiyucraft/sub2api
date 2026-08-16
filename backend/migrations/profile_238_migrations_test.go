package migrations

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestImageCostRoutingMigrationContract(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("237_image_cost_routing.sql"))
	require.NoError(t, err)
	sql := string(content)
	for _, fragment := range []string{
		"ADD COLUMN IF NOT EXISTS image_cost_routing_enabled",
		"image_cost_routing_enabled BOOLEAN NOT NULL DEFAULT FALSE",
		"image_cost_routing_mode VARCHAR(20) NOT NULL DEFAULT 'prefer_lowest'",
		"image_cost_tolerance_percent DECIMAL(5,2) NOT NULL DEFAULT 5",
		"image_cost_stale_after_seconds INTEGER NOT NULL DEFAULT 86400",
		"CREATE OR REPLACE FUNCTION enqueue_group_auth_cache_invalidation()",
		"OLD.image_cost_routing_enabled IS NOT DISTINCT FROM NEW.image_cost_routing_enabled",
		"OLD.image_cost_routing_mode IS NOT DISTINCT FROM NEW.image_cost_routing_mode",
		"OLD.image_cost_tolerance_percent IS NOT DISTINCT FROM NEW.image_cost_tolerance_percent",
		"OLD.image_cost_stale_after_seconds IS NOT DISTINCT FROM NEW.image_cost_stale_after_seconds",
		"OLD.video_model_prices IS NOT DISTINCT FROM NEW.video_model_prices",
		"OLD.web_search_price_per_call IS NOT DISTINCT FROM NEW.web_search_price_per_call",
		"OLD.audio_stt_price_per_hour IS NOT DISTINCT FROM NEW.audio_stt_price_per_hour",
	} {
		require.Contains(t, strings.ToUpper(sql), strings.ToUpper(fragment))
	}
}
