package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type upstreamHealthTrendServiceRepo struct {
	UpstreamConfigRepository
	keyID     int64
	rangeName string
	now       time.Time
	trend     *UpstreamHealthTrend
}

func (r *upstreamHealthTrendServiceRepo) GetUpstreamHealthTrend(_ context.Context, keyID int64, rangeName string, now time.Time) (*UpstreamHealthTrend, error) {
	r.keyID, r.rangeName, r.now = keyID, rangeName, now
	return r.trend, nil
}

func TestNormalizeUpstreamHealthTrendRange(t *testing.T) {
	tests := []struct {
		input  string
		name   string
		window time.Duration
		bucket time.Duration
	}{
		{"", "6h", 6 * time.Hour, 5 * time.Minute},
		{" 24H ", "24h", 24 * time.Hour, 15 * time.Minute},
		{"7d", "7d", 7 * 24 * time.Hour, 2 * time.Hour},
		{"30d", "30d", 30 * 24 * time.Hour, 12 * time.Hour},
	}
	for _, tt := range tests {
		name, window, bucket, err := NormalizeUpstreamHealthTrendRange(tt.input)
		require.NoError(t, err)
		require.Equal(t, tt.name, name)
		require.Equal(t, tt.window, window)
		require.Equal(t, tt.bucket, bucket)
	}
	_, _, _, err := NormalizeUpstreamHealthTrendRange("1h")
	require.Error(t, err)
}

func TestGetUpstreamHealthTrendValidatesAndDelegates(t *testing.T) {
	want := &UpstreamHealthTrend{KeyID: 42, Range: "24h"}
	repo := &upstreamHealthTrendServiceRepo{trend: want}
	svc := NewUpstreamConfigService(repo, nil, nil)

	got, err := svc.GetUpstreamHealthTrend(context.Background(), 42, "24H")
	require.NoError(t, err)
	require.Same(t, want, got)
	require.Equal(t, int64(42), repo.keyID)
	require.Equal(t, "24h", repo.rangeName)
	require.False(t, repo.now.IsZero())
	require.Equal(t, time.UTC, repo.now.Location())

	_, err = svc.GetUpstreamHealthTrend(context.Background(), 0, "6h")
	require.Error(t, err)
	_, err = svc.GetUpstreamHealthTrend(context.Background(), 42, "90d")
	require.Error(t, err)
}
