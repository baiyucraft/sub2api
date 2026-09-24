package repository

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

type monitorCacheTarget struct {
	groupID  int64
	platform string
	model    string
}

// BatchPrimaryCacheRates reads the same successful-usage rollups and token
// columns as V2, without applying V2's user-facing platform allow-list.
func (r *channelMonitorRepository) BatchPrimaryCacheRates(
	ctx context.Context, monitors []*service.ChannelMonitor, rateRange string, now time.Time,
) (map[int64]float64, error) {
	return r.batchPrimaryCacheRates(ctx, monitors, rateRange, now, nil)
}

// BatchPrimaryCacheRatesForGroups applies V1 display aliases only when the
// source group is visible to the current viewer. A nil map means unrestricted
// access (used by internal callers); a non-nil map is an explicit user scope.
func (r *channelMonitorRepository) BatchPrimaryCacheRatesForGroups(
	ctx context.Context, monitors []*service.ChannelMonitor, rateRange string, now time.Time, allowedGroupIDs map[int64]struct{},
) (map[int64]float64, error) {
	return r.batchPrimaryCacheRates(ctx, monitors, rateRange, now, allowedGroupIDs)
}

func (r *channelMonitorRepository) batchPrimaryCacheRates(
	ctx context.Context, monitors []*service.ChannelMonitor, rateRange string, now time.Time, allowedGroupIDs map[int64]struct{},
) (map[int64]float64, error) {
	rates := make(map[int64]float64)
	window, bucket, err := monitorCacheWindow(rateRange)
	if err != nil {
		return nil, err
	}
	end := now.UTC().Truncate(bucket).Add(bucket)
	start := end.Add(-window)
	v2 := &channelMonitorV2Repository{db: r.db}
	cfg, err := v2.GetConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("load cache sample threshold: %w", err)
	}
	if cfg == nil || !cfg.Enabled {
		return rates, nil
	}
	byTarget := make(map[monitorCacheTarget][]int64)
	var groups []int64
	var platforms, models []string
	for _, monitor := range monitors {
		if monitor == nil || monitor.GroupID == nil || *monitor.GroupID <= 0 {
			continue
		}
		displayGroupID := *monitor.GroupID
		sourceGroupID := service.ChannelMonitorV1CacheRateSourceGroup(displayGroupID, cfg.V1CacheRateSourceGroups)
		if allowedGroupIDs != nil && sourceGroupID != displayGroupID {
			if _, visible := allowedGroupIDs[sourceGroupID]; !visible {
				sourceGroupID = displayGroupID
			}
		}
		target := monitorCacheTarget{sourceGroupID, strings.ToLower(strings.TrimSpace(monitor.Provider)), strings.TrimSpace(monitor.PrimaryModel)}
		if target.platform == "" || target.model == "" || target.model == "quota" {
			continue
		}
		if _, exists := byTarget[target]; !exists {
			groups = append(groups, target.groupID)
			platforms = append(platforms, target.platform)
			models = append(models, target.model)
		}
		byTarget[target] = append(byTarget[target], monitor.ID)
	}
	if len(byTarget) == 0 {
		return rates, nil
	}
	wm, err := v2.GetAggregationWatermark(ctx)
	if err != nil {
		return nil, fmt.Errorf("load cache aggregation coverage: %w", err)
	}
	if wm == nil || !wm.HasData || wm.LastSuccessfulAt.IsZero() || wm.UsageCoverageStart.IsZero() || wm.UsageCoverageStart.After(start) || !wm.DataThrough.After(start) {
		return rates, nil
	}
	// Do not present stale partial aggregates as a current-window percentage.
	if wm.DataThrough.Before(now.UTC().Add(-2 * time.Duration(cfg.RefreshIntervalSeconds) * time.Second)) {
		return rates, nil
	}

	rows, err := r.db.QueryContext(ctx, `
		SELECT m.group_id, m.platform, m.model,
		       SUM(m.success_requests), SUM(m.input_tokens),
		       SUM(m.cache_creation_tokens), SUM(m.cache_read_tokens)
		FROM channel_monitor_v2_metrics_rollup m
		JOIN unnest($4::bigint[], $5::text[], $6::text[]) AS target(group_id, platform, model)
		  ON target.group_id = m.group_id AND target.platform = m.platform AND target.model = m.model
		WHERE m.bucket_start >= $1 AND m.bucket_start < $2 AND m.bucket_seconds = $3
		GROUP BY m.group_id, m.platform, m.model`,
		start, end, int(bucket.Seconds()), pq.Array(groups), pq.Array(platforms), pq.Array(models))
	if err != nil {
		return nil, fmt.Errorf("load monitor cache rates: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var target monitorCacheTarget
		var samples, input, created, read int64
		if err := rows.Scan(&target.groupID, &target.platform, &target.model, &samples, &input, &created, &read); err != nil {
			return nil, err
		}
		if rate, ok := monitorCacheRate(samples, input, created, read, cfg.HealthThresholds.MinimumSample); ok {
			for _, id := range byTarget[target] {
				rates[id] = rate
			}
		}
	}
	return rates, rows.Err()
}

func monitorCacheWindow(rateRange string) (time.Duration, time.Duration, error) {
	switch rateRange {
	case service.MonitorRateRange24Hours:
		return 24 * time.Hour, time.Hour, nil
	case service.MonitorRateRange7Days:
		return 7 * 24 * time.Hour, 12 * time.Hour, nil
	case service.MonitorRateRange15Days:
		return 15 * 24 * time.Hour, 12 * time.Hour, nil
	case service.MonitorRateRange30Days:
		return 30 * 24 * time.Hour, 24 * time.Hour, nil
	default:
		return 0, 0, service.ErrChannelMonitorInvalidRateRange
	}
}

func monitorCacheRate(samples, input, created, read, minimumSample int64) (float64, bool) {
	denominator := input + created + read
	if samples < minimumSample || denominator <= 0 {
		return 0, false
	}
	return float64(read) / float64(denominator), true
}
