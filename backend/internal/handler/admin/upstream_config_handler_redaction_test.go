package admin

import (
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestKeySuffixNeverReturnsShortSecret(t *testing.T) {
	require.Equal(t, "***", keySuffix("abc123"))
	require.Equal(t, "456789", keySuffix("sk-123456789"))
}

func TestSanitizeUpstreamKeyIncludesMissingState(t *testing.T) {
	missingSince := time.Date(2026, 7, 12, 10, 30, 0, 0, time.UTC)
	rate := 0.42
	sourceRate := 0.84
	got := sanitizeUpstreamKey(&service.UpstreamKey{
		ID:                   42,
		RateMultiplier:       &rate,
		SourceRateMultiplier: &sourceRate,
		Status:               service.UpstreamKeyStatusStale,
		MissingCount:         3,
		MissingSince:         &missingSince,
	})

	require.Equal(t, &rate, got["rate_multiplier"])
	require.NotContains(t, got, "effective_cost_multiplier")
	require.NotContains(t, got, "raw_rate_multiplier")
	require.NotContains(t, got, "rate_multiplier_source")
	require.Equal(t, service.UpstreamKeyStatusStale, got["status"])
	require.Equal(t, 3, got["missing_count"])
	require.Equal(t, &missingSince, got["missing_since"])
}

func TestSanitizeUpstreamKeyHidesImagePricingSnapshot(t *testing.T) {
	price := 0.12
	got := sanitizeUpstreamKey(&service.UpstreamKey{
		Extra: map[string]any{
			service.Sub2APIImagePricingSnapshotExtraKey: map[string]any{"image_price_1k": price},
			"safe_note": "visible",
		},
		ImagePricing: &service.UpstreamKeyImagePricing{
			Supported: true, Status: service.UpstreamKeyImagePricingStatusAvailable, Currency: "USD",
			FinalCost1K: &price,
		},
	})
	require.NotContains(t, got["extra"], service.Sub2APIImagePricingSnapshotExtraKey)
	pricing, ok := got["image_pricing"].(gin.H)
	require.True(t, ok)
	require.Equal(t, service.UpstreamKeyImagePricingStatusAvailable, pricing["status"])
}

func TestSanitizeUpstreamKeyHidesLCodexImageCapabilitySnapshot(t *testing.T) {
	got := sanitizeUpstreamKey(&service.UpstreamKey{
		Extra: map[string]any{
			service.LCodexImageCapabilitySnapshotExtraKey: map[string]any{"allow_image_generation": true},
			"safe_note": "visible",
		},
		ImagePricing: &service.UpstreamKeyImagePricing{Supported: true, Status: service.UpstreamKeyImagePricingStatusPartial, Currency: "USD"},
	})
	require.NotContains(t, got["extra"], service.LCodexImageCapabilitySnapshotExtraKey)
	require.Equal(t, "visible", got["extra"].(map[string]any)["safe_note"])
	pricing := got["image_pricing"].(gin.H)
	require.Equal(t, service.UpstreamKeyImagePricingStatusPartial, pricing["status"])
	require.Nil(t, pricing["final_cost_1k"])
}

func TestUpstreamCredentialsStatusLCodexDoesNotExposeSecrets(t *testing.T) {
	got := upstreamCredentialsStatus(map[string]any{
		service.AccountCredentialLCodexLoginIdentifier: "user@example.com",
		service.AccountCredentialLCodexLoginPassword:   "secret-password",
	})
	require.Equal(t, true, got["has_lcodex_login_identifier"])
	require.Equal(t, true, got["has_lcodex_login_password"])
	require.NotContains(t, got, service.AccountCredentialLCodexLoginIdentifier)
	require.NotContains(t, got, service.AccountCredentialLCodexLoginPassword)
}
