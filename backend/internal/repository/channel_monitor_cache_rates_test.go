package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestMonitorCacheRateMatchesV2AndRequiresSamples(t *testing.T) {
	acc := newMetricAccumulator()
	acc.success, acc.input, acc.cacheCreation, acc.cacheRead = 50, 60, 20, 20
	v2 := acc.metric(1, false)
	rate, ok := monitorCacheRate(acc.success, acc.input, acc.cacheCreation, acc.cacheRead, 50)
	require.True(t, ok)
	require.InDelta(t, v2.CacheRate, rate, 0.0001)
	_, ok = monitorCacheRate(49, 60, 20, 20, 50)
	require.False(t, ok)
	_, ok = monitorCacheRate(50, 0, 0, 0, 50)
	require.False(t, ok)
	zero, ok := monitorCacheRate(50, 100, 0, 0, 50)
	require.True(t, ok)
	require.Zero(t, zero)
}

func TestMonitorCacheWindowUsesExistingV2RollupTiers(t *testing.T) {
	for _, tc := range []struct {
		rangeValue string
		window     time.Duration
		bucket     time.Duration
	}{
		{service.MonitorRateRange24Hours, 24 * time.Hour, time.Hour},
		{service.MonitorRateRange7Days, 7 * 24 * time.Hour, 12 * time.Hour},
		{service.MonitorRateRange15Days, 15 * 24 * time.Hour, 12 * time.Hour},
		{service.MonitorRateRange30Days, 30 * 24 * time.Hour, 24 * time.Hour},
	} {
		window, bucket, err := monitorCacheWindow(tc.rangeValue)
		require.NoError(t, err)
		require.Equal(t, tc.window, window)
		require.Equal(t, tc.bucket, bucket)
	}
}

func TestBatchPrimaryCacheRatesFiltersByVisibleGroupPlatformAndModel(t *testing.T) {
	for _, tc := range []struct {
		name        string
		minimum     int64
		samples     int64
		coverageAge time.Duration
		stale       bool
		disabled    bool
		wantRate    bool
		wantQuery   bool
	}{
		{name: "enough samples", minimum: 50, samples: 50, coverageAge: 24 * time.Hour, wantRate: true, wantQuery: true},
		{name: "config threshold changes", minimum: 51, samples: 50, coverageAge: 24 * time.Hour, wantQuery: true},
		{name: "history not backfilled", minimum: 50, samples: 50, coverageAge: time.Hour},
		{name: "stale aggregation", minimum: 50, samples: 50, coverageAge: 24 * time.Hour, stale: true},
		{name: "aggregation disabled", minimum: 50, samples: 50, disabled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer db.Close()
			now := time.Date(2026, 9, 20, 12, 30, 0, 0, time.UTC)
			mock.ExpectQuery("FROM channel_monitor_v2_config").WillReturnRows(
				sqlmock.NewRows([]string{"version", "enabled", "refresh_interval_seconds", "platforms", "group_ids", "ignored_error_categories", "health_thresholds", "updated_at", "updated_by"}).
					AddRow(1, !tc.disabled, 60, `[]`, `{}`, `{}`, `{"minimum_sample":`+int64String(tc.minimum)+`}`, now, nil),
			)
			start := now.Truncate(time.Hour).Add(time.Hour).Add(-24 * time.Hour)
			if !tc.disabled {
				through := now
				if tc.stale {
					through = now.Add(-3 * time.Minute)
				}
				mock.ExpectQuery("FROM channel_monitor_v2_watermarks").WillReturnRows(
					sqlmock.NewRows([]string{"usage_coverage_start", "error_coverage_start", "data_through", "last_successful_at", "backfill_cursor"}).
						AddRow(start.Add(24*time.Hour-tc.coverageAge), start, through, now, start),
				)
			}
			if tc.wantQuery {
				mock.ExpectQuery("FROM channel_monitor_v2_metrics_rollup").
					WithArgs(start, now.Truncate(time.Hour).Add(time.Hour), 3600, sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
					WillReturnRows(sqlmock.NewRows([]string{"group_id", "platform", "model", "success_requests", "input_tokens", "cache_creation_tokens", "cache_read_tokens"}).
						AddRow(int64(7), "openai", "gpt", tc.samples, 60, 20, 20).
						AddRow(int64(8), "openai", "gpt", 100, 0, 0, 100))
			}
			group, otherGroup := int64(7), int64(8)
			monitors := []*service.ChannelMonitor{
				{ID: 1, GroupID: &group, Provider: "openai", PrimaryModel: "gpt"},
				{ID: 2, GroupID: &group, Provider: "openai", PrimaryModel: "other-model"},
				{ID: 3, GroupID: &otherGroup, Provider: "openai", PrimaryModel: "gpt"},
				{ID: 4, Provider: "openai", PrimaryModel: "gpt"},
			}
			repo := &channelMonitorRepository{db: db}
			rates, err := repo.BatchPrimaryCacheRates(context.Background(), monitors, service.MonitorRateRange24Hours, now)
			require.NoError(t, err)
			if tc.wantRate {
				require.InDelta(t, 0.2, rates[1], 0.0001)
				require.Equal(t, 1.0, rates[3])
			} else {
				require.NotContains(t, rates, int64(1))
			}
			require.NotContains(t, rates, int64(2))
			require.NotContains(t, rates, int64(4))
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func int64String(n int64) string {
	return fmt.Sprint(n)
}
