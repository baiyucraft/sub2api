package usagestats

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestResolveUpstreamUsageTrendRange(t *testing.T) {
	now := time.Date(2026, 7, 10, 14, 35, 22, 0, time.FixedZone("CST", 8*60*60))
	tests := []struct {
		name       string
		rangeName  string
		wantRange  string
		wantStart  time.Time
		wantEnd    time.Time
		wantUnit   string
		wantPoints int
	}{
		{
			name:       "default 24h",
			rangeName:  "",
			wantRange:  UpstreamUsageTrendRange24H,
			wantStart:  time.Date(2026, 7, 9, 7, 0, 0, 0, time.UTC),
			wantEnd:    time.Date(2026, 7, 10, 7, 0, 0, 0, time.UTC),
			wantUnit:   "hour",
			wantPoints: 24,
		},
		{
			name:       "seven UTC days",
			rangeName:  " 7D ",
			wantRange:  UpstreamUsageTrendRange7D,
			wantStart:  time.Date(2026, 7, 4, 0, 0, 0, 0, time.UTC),
			wantEnd:    time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC),
			wantUnit:   "day",
			wantPoints: 7,
		},
		{
			name:       "thirty UTC days",
			rangeName:  "30d",
			wantRange:  UpstreamUsageTrendRange30D,
			wantStart:  time.Date(2026, 6, 11, 0, 0, 0, 0, time.UTC),
			wantEnd:    time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC),
			wantUnit:   "day",
			wantPoints: 30,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec, err := ResolveUpstreamUsageTrendRange(tt.rangeName, now)
			require.NoError(t, err)
			require.Equal(t, tt.wantRange, spec.Range)
			require.Equal(t, tt.wantStart, spec.StartTime)
			require.Equal(t, tt.wantEnd, spec.EndTime)
			require.Equal(t, tt.wantUnit, spec.BucketUnit)
			require.Equal(t, tt.wantPoints, spec.PointCount)
		})
	}
}

func TestResolveUpstreamUsageTrendRangeRejectsUnknownRange(t *testing.T) {
	_, err := ResolveUpstreamUsageTrendRange("90d", time.Now())
	require.ErrorContains(t, err, "unsupported upstream usage trend range")
}
