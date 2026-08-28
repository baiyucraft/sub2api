package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func dashboardWindow(window service.UpstreamDashboardWindow) (time.Duration, string) {
	switch window {
	case service.UpstreamDashboardWindow1h:
		return time.Hour, "1h"
	case service.UpstreamDashboardWindow7d:
		return 7 * 24 * time.Hour, "7d"
	case service.UpstreamDashboardWindow15d:
		return 15 * 24 * time.Hour, "15d"
	case service.UpstreamDashboardWindow30d:
		return 30 * 24 * time.Hour, "30d"
	default:
		return 24 * time.Hour, "24h"
	}
}

// dashboardProbeNullableSelect keeps left-joined probe fields scan-safe when a
// config has no observations in the requested window (or no confidence v2 row).
const dashboardProbeNullableSelect = "COALESCE(p.latest_state,''),COALESCE(p.latest_reason,''),p.latest_observed_at,p.avg_ttft,p.avg_duration,COALESCE(p.confidence_samples,0),COALESCE(p.confidence_status,'')"

func (r *upstreamConfigRepository) GetUpstreamDashboard(ctx context.Context, filter service.UpstreamDashboardFilter) (*service.UpstreamDashboardResponse, error) {
	now := filter.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	duration, normalizedWindow := dashboardWindow(filter.Window)
	start := now.Add(-duration)
	where := []string{"c.deleted_at IS NULL"}
	args := []any{start, now}
	if p := strings.TrimSpace(filter.Provider); p != "" {
		args = append(args, p)
		where = append(where, fmt.Sprintf("c.provider = $%d", len(args)))
	}
	if q := strings.TrimSpace(filter.Search); q != "" {
		args = append(args, "%"+q+"%")
		where = append(where, fmt.Sprintf("(c.name ILIKE $%d OR c.site_url ILIKE $%d)", len(args), len(args)))
	}
	if filter.ConfigID > 0 {
		args = append(args, filter.ConfigID)
		where = append(where, fmt.Sprintf("c.id = $%d", len(args)))
	}
	query := fmt.Sprintf(`
WITH usage AS (
  SELECT COALESCE(ul.upstream_config_id, a.upstream_config_id) config_id,
         COUNT(DISTINCT ul.request_id) requests,
         COALESCE(SUM(ul.actual_cost),0) revenue,
         CASE WHEN COUNT(*) FILTER (WHERE ul.upstream_cost_to_cny_rate IS NULL OR ul.upstream_cost_to_cny_rate <= 0) > 0 THEN NULL
              ELSE SUM(COALESCE(ul.account_stats_cost, ul.total_cost) * ul.upstream_cost_to_cny_rate) END upstream_cost,
         percentile_cont(0.5) WITHIN GROUP (ORDER BY NULLIF(ul.first_token_ms,0)) FILTER (WHERE ul.first_token_ms > 0) p50_ttft,
         percentile_cont(0.95) WITHIN GROUP (ORDER BY NULLIF(ul.first_token_ms,0)) FILTER (WHERE ul.first_token_ms > 0) p95_ttft,
         percentile_cont(0.5) WITHIN GROUP (ORDER BY NULLIF(ul.duration_ms,0)) FILTER (WHERE ul.duration_ms > 0) p50_latency,
         percentile_cont(0.95) WITHIN GROUP (ORDER BY NULLIF(ul.duration_ms,0)) FILTER (WHERE ul.duration_ms > 0) p95_latency
    FROM usage_logs ul LEFT JOIN accounts a ON a.id = ul.account_id
   WHERE ul.created_at >= $1 AND ul.created_at < $2
   GROUP BY COALESCE(ul.upstream_config_id, a.upstream_config_id)
), usage_requests AS (
  SELECT DISTINCT ul.request_id FROM usage_logs ul WHERE ul.created_at >= $1 AND ul.created_at < $2 AND ul.request_id IS NOT NULL
), error_rows AS (
	SELECT DISTINCT ON (COALESCE(NULLIF(e.request_id, ''), e.id::text)) e.*
    FROM ops_error_logs e
   WHERE e.created_at >= $1 AND e.created_at < $2
	 ORDER BY COALESCE(NULLIF(e.request_id, ''), e.id::text), e.created_at DESC, e.id DESC
), errors AS (
  SELECT COALESCE(e.account_id,0) account_id,
         COUNT(*) failed,
         COUNT(*) FILTER (WHERE COALESCE(e.upstream_status_code,e.status_code)=429) err429,
         COUNT(*) FILTER (WHERE COALESCE(e.upstream_status_code,e.status_code) >= 500) err5xx,
         COUNT(*) FILTER (WHERE e.error_type ILIKE '%%timeout%%' OR e.network_error_type ILIKE '%%timeout%%') timeouts,
         COUNT(*) FILTER (WHERE e.error_type ILIKE '%%auth%%' OR e.error_type ILIKE '%%config%%' OR COALESCE(e.upstream_status_code,e.status_code) IN (401,403)) auth_errors
    FROM error_rows e LEFT JOIN usage_requests ur ON ur.request_id = e.request_id
   WHERE ur.request_id IS NULL GROUP BY COALESCE(e.account_id,0)
), account_errors AS (
  SELECT a.upstream_config_id config_id, SUM(e.failed) failed, SUM(e.err429) err429, SUM(e.err5xx) err5xx,
         SUM(e.timeouts) timeouts, SUM(e.auth_errors) auth_errors
    FROM accounts a JOIN errors e ON e.account_id=a.id GROUP BY a.upstream_config_id
), account_counts AS (
  SELECT upstream_config_id config_id, COUNT(*) account_count,
         COUNT(*) FILTER (WHERE status='active' AND schedulable=true AND (temp_unschedulable_until IS NULL OR temp_unschedulable_until <= NOW())) schedulable_count,
         COUNT(*) FILTER (WHERE temp_unschedulable_until > NOW()) temp_unschedulable_count
    FROM accounts WHERE deleted_at IS NULL AND upstream_config_id IS NOT NULL GROUP BY upstream_config_id
), probes AS (
  SELECT upstream_config_id config_id, COUNT(*) samples,
         COUNT(*) FILTER (WHERE state IN ('healthy','operational','success')) healthy_samples,
         (array_agg(state ORDER BY observed_at DESC))[1] latest_state,
         (array_agg(reason ORDER BY observed_at DESC))[1] latest_reason,
         (array_agg(observed_at ORDER BY observed_at DESC))[1] latest_observed_at,
         AVG(NULLIF(ttft_ms,0)) FILTER (WHERE ttft_ms > 0) avg_ttft,
         AVG(NULLIF(duration_ms,0)) FILTER (WHERE duration_ms > 0) avg_duration,
         COUNT(*) FILTER (WHERE confidence_prompt_version='openai-juice-multiprobe-v2') confidence_samples,
         (array_agg(confidence_status ORDER BY observed_at DESC) FILTER (WHERE confidence_prompt_version='openai-juice-multiprobe-v2'))[1] confidence_status
    FROM upstream_health_observations WHERE observed_at >= $1 AND observed_at < $2 GROUP BY upstream_config_id
)
		SELECT c.id,c.name,c.provider,c.site_url,COALESCE(c.scheduling_enabled,true),c.status,
       COALESCE(u.requests,0),COALESCE(ae.failed,0),COALESCE(ae.err429,0),COALESCE(ae.err5xx,0),COALESCE(ae.timeouts,0),COALESCE(ae.auth_errors,0),
       u.revenue,u.upstream_cost,u.p50_ttft,u.p95_ttft,u.p50_latency,u.p95_latency,
       COALESCE(ac.account_count,0),COALESCE(ac.schedulable_count,0),COALESCE(ac.temp_unschedulable_count,0),
       COALESCE(p.samples,0),COALESCE(p.healthy_samples,0),%s
  FROM upstream_configs c
  LEFT JOIN usage u ON u.config_id=c.id LEFT JOIN account_errors ae ON ae.config_id=c.id LEFT JOIN account_counts ac ON ac.config_id=c.id LEFT JOIN probes p ON p.config_id=c.id
 WHERE %s ORDER BY c.name,c.id`, dashboardProbeNullableSelect, strings.Join(where, " AND "))

	rows, err := r.client.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := &service.UpstreamDashboardResponse{Window: normalizedWindow, StartAt: start, EndAt: now, Items: []service.UpstreamDashboardCard{}}
	for rows.Next() {
		var c service.UpstreamDashboardCard
		var enabled bool
		var configStatus string
		var revenue, cost sql.NullFloat64
		var p50ttft, p95ttft, p50lat, p95lat sql.NullFloat64
		var samples, healthy, confidence int64
		var latestAt sql.NullTime
		var avgTTFT, avgDuration sql.NullFloat64
		if err := rows.Scan(&c.ID, &c.Name, &c.Provider, &c.SiteURL, &enabled, &configStatus, &c.Requests, &c.FailedRequests, &c.Error429, &c.Error5xx, &c.Timeouts, &c.AuthConfigErrors, &revenue, &cost, &p50ttft, &p95ttft, &p50lat, &p95lat, &c.AccountCount, &c.SchedulableAccountCount, &c.TempUnschedulableCount, &samples, &healthy, &c.Probe.LatestState, &c.Probe.LatestReason, &latestAt, &avgTTFT, &avgDuration, &confidence, &c.Probe.ConfidenceStatus); err != nil {
			return nil, err
		}
		c.Enabled, c.ConfigStatus = enabled, configStatus
		c.CompletedRequests = c.Requests
		if total := c.Requests + c.FailedRequests; total > 0 {
			rate := float64(c.Requests) / float64(total)
			c.SuccessRate = &rate
		}
		c.Probe.Samples, c.Probe.HealthySamples, c.Probe.ConfidenceSamples = samples, healthy, confidence
		if latestAt.Valid {
			t := latestAt.Time
			c.Probe.LatestObservedAt = &t
		}
		if revenue.Valid {
			v := revenue.Float64
			c.Revenue = &v
		}
		if cost.Valid {
			v := cost.Float64
			c.UpstreamCost = &v
		}
		if c.Revenue != nil && c.UpstreamCost != nil {
			gp := *c.Revenue - *c.UpstreamCost
			c.EstimatedGrossProfit = &gp
			if *c.Revenue != 0 {
				r := gp / *c.Revenue
				c.EstimatedGrossProfitRate = &r
			}
		}
		assignPercentile := func(v sql.NullFloat64) *int64 {
			if !v.Valid {
				return nil
			}
			n := int64(v.Float64)
			return &n
		}
		c.P50TTFTMs, c.P95TTFTMs, c.P50LatencyMs, c.P95LatencyMs = assignPercentile(p50ttft), assignPercentile(p95ttft), assignPercentile(p50lat), assignPercentile(p95lat)
		if avgTTFT.Valid {
			v := avgTTFT.Float64
			c.Probe.AverageTTFTMs = &v
		}
		if avgDuration.Valid {
			v := avgDuration.Float64
			c.Probe.AverageDurationMs = &v
		}
		c.DataQuality = "sufficient"
		if c.Requests == 0 && c.Probe.Samples == 0 {
			c.DataQuality = "insufficient"
		}
		c.OverallStatus = "operational"
		if !enabled || strings.EqualFold(configStatus, "disabled") {
			c.OverallStatus = "disabled"
		} else if c.AccountCount > 0 && c.SchedulableAccountCount == 0 {
			c.OverallStatus = "critical"
		} else if (c.FailedRequests > 0 && (c.SuccessRate == nil || *c.SuccessRate < 0.95)) || c.Probe.LatestState == "degraded" || c.Probe.LatestState == "error" {
			c.OverallStatus = "degraded"
		} else if c.DataQuality == "insufficient" {
			c.OverallStatus = "data_insufficient"
		}
		result.Items = append(result.Items, c)
	}
	return result, rows.Err()
}

func (r *upstreamConfigRepository) GetUpstreamDashboardDetail(ctx context.Context, id int64, filter service.UpstreamDashboardFilter) (*service.UpstreamDashboardDetail, error) {
	filter.ConfigID = id
	result, err := r.GetUpstreamDashboard(ctx, filter)
	if err != nil {
		return nil, err
	}
	if len(result.Items) == 0 {
		return nil, service.ErrUpstreamConfigNotFound
	}
	detail := &service.UpstreamDashboardDetail{UpstreamDashboardCard: result.Items[0]}

	// Keep drill-down queries bounded and independent from the list aggregate.
	trendQuery := `SELECT date_trunc('hour', ul.created_at), COUNT(DISTINCT ul.request_id),
        0::bigint, COALESCE(SUM(ul.actual_cost),0),
        COALESCE(SUM(COALESCE(ul.account_stats_cost, ul.total_cost) * ul.upstream_cost_to_cny_rate),0)
      FROM usage_logs ul
      WHERE ul.upstream_config_id=$1 AND ul.created_at >= $2 AND ul.created_at < $3
      GROUP BY 1 ORDER BY 1`
	trendRows, err := r.client.QueryContext(ctx, trendQuery, id, result.StartAt, result.EndAt)
	if err != nil {
		return nil, err
	}
	defer trendRows.Close()
	for trendRows.Next() {
		var point service.UpstreamDashboardTrendPoint
		if err := trendRows.Scan(&point.Bucket, &point.Requests, &point.Errors, &point.Revenue, &point.UpstreamCost); err != nil {
			return nil, err
		}
		detail.Trend = append(detail.Trend, point)
	}
	if err := trendRows.Err(); err != nil {
		return nil, err
	}

	modelRows, err := r.client.QueryContext(ctx, `SELECT COALESCE(NULLIF(TRIM(requested_model),''), NULLIF(TRIM(model),''), '-') model, COUNT(DISTINCT request_id)
      FROM usage_logs WHERE upstream_config_id=$1 AND created_at >= $2 AND created_at < $3
      GROUP BY 1 ORDER BY 2 DESC LIMIT 20`, id, result.StartAt, result.EndAt)
	if err != nil {
		return nil, err
	}
	defer modelRows.Close()
	for modelRows.Next() {
		var stat service.UpstreamDashboardModelStat
		if err := modelRows.Scan(&stat.Model, &stat.Requests); err != nil {
			return nil, err
		}
		detail.Traffic.Models = append(detail.Traffic.Models, stat)
	}
	if err := modelRows.Err(); err != nil {
		return nil, err
	}

	errorRows, err := r.client.QueryContext(ctx, `SELECT created_at, COALESCE(NULLIF(TRIM(model),''),'-'),
      CASE WHEN COALESCE(upstream_status_code,status_code) = 429 THEN 'rate_limit'
           WHEN COALESCE(upstream_status_code,status_code) >= 500 THEN 'server'
           WHEN error_type ILIKE '%timeout%' OR network_error_type ILIKE '%timeout%' THEN 'timeout'
           WHEN error_type ILIKE '%auth%' OR error_type ILIKE '%config%' OR COALESCE(upstream_status_code,status_code) IN (401,403) THEN 'auth_config'
           ELSE 'other' END,
      COALESCE(upstream_status_code,status_code,0)
      FROM ops_error_logs WHERE account_id IN (SELECT id FROM accounts WHERE upstream_config_id=$1)
        AND created_at >= $2 AND created_at < $3
      ORDER BY created_at DESC, id DESC LIMIT 20`, id, result.StartAt, result.EndAt)
	if err != nil {
		return nil, err
	}
	defer errorRows.Close()
	for errorRows.Next() {
		var e service.UpstreamDashboardError
		var at time.Time
		if err := errorRows.Scan(&at, &e.Model, &e.Category, &e.StatusCode); err != nil {
			return nil, err
		}
		e.OccurredAt = at.UTC().Format(time.RFC3339)
		detail.Errors = append(detail.Errors, e)
	}
	if err := errorRows.Err(); err != nil {
		return nil, err
	}
	if detail.Revenue == nil || detail.UpstreamCost == nil {
		detail.ProfitUnavailable = true
		detail.ProfitReason = "cost_or_currency_data_missing"
	}
	return detail, nil
}
