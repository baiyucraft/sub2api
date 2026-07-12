package repository

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRateTrendBucket(t *testing.T) {
	at := time.Date(2026, 7, 12, 13, 47, 59, 0, time.UTC)
	require.Equal(t, time.Date(2026, 7, 12, 13, 0, 0, 0, time.UTC), rateTrendBucket(at, "24h"))
	require.Equal(t, time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC), rateTrendBucket(at, "7d"))
	require.Equal(t, time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC), rateTrendBucket(at, "30d"))
}
