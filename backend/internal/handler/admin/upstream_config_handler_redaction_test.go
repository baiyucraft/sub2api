package admin

import (
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestKeySuffixNeverReturnsShortSecret(t *testing.T) {
	require.Equal(t, "***", keySuffix("abc123"))
	require.Equal(t, "456789", keySuffix("sk-123456789"))
}

func TestSanitizeUpstreamKeyIncludesMissingState(t *testing.T) {
	missingSince := time.Date(2026, 7, 12, 10, 30, 0, 0, time.UTC)
	got := sanitizeUpstreamKey(&service.UpstreamKey{
		ID:           42,
		Status:       service.UpstreamKeyStatusStale,
		MissingCount: 3,
		MissingSince: &missingSince,
	})

	require.Equal(t, service.UpstreamKeyStatusStale, got["status"])
	require.Equal(t, 3, got["missing_count"])
	require.Equal(t, &missingSince, got["missing_since"])
}
