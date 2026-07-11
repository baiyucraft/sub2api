package repository

import (
	"context"
	"database/sql"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type UpstreamUsageTrendQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

const upstreamUsageTrendSQL = `
	WITH params AS (
		SELECT
			$1::timestamptz AS start_time,
			$2::timestamptz AS end_time,
			$3::interval AS bucket_interval,
			$4::text AS bucket_unit,
			$5::bigint AS upstream_config_id
	),
	buckets AS (
		SELECT generate_series(
			p.start_time,
			p.end_time - p.bucket_interval,
			p.bucket_interval
		) AS bucket_start
		FROM params p
	),
	attributed AS (
		SELECT
			date_trunc(p.bucket_unit, ul.created_at AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' AS bucket_start,
			(ul.upstream_config_id IS NULL AND a.upstream_config_id IS NOT NULL) AS legacy_attributed,
			COALESCE(ul.account_stats_cost, ul.total_cost) AS upstream_base_cost,
			COALESCE(ul.account_rate_multiplier, 1) AS account_rate_multiplier,
			ul.actual_cost AS billed_cost,
			CASE
				WHEN ul.upstream_cost_to_cny_rate > 0 THEN ul.upstream_cost_to_cny_rate
				ELSE NULL
			END AS cny_rate
		FROM usage_logs ul
		LEFT JOIN accounts a ON a.id = ul.account_id
		CROSS JOIN params p
		WHERE ul.created_at >= p.start_time
		  AND ul.created_at < p.end_time
		  AND COALESCE(ul.upstream_config_id, a.upstream_config_id) IS NOT NULL
		  AND (
			p.upstream_config_id IS NULL
			OR COALESCE(ul.upstream_config_id, a.upstream_config_id) = p.upstream_config_id
		  )
	),
	aggregated AS (
		SELECT
			bucket_start,
			COUNT(*) AS requests,
			COALESCE(SUM(CASE WHEN cny_rate IS NOT NULL THEN upstream_base_cost * cny_rate ELSE 0 END), 0) AS upstream_base_cost,
			COALESCE(SUM(CASE WHEN cny_rate IS NOT NULL THEN upstream_base_cost * account_rate_multiplier * cny_rate ELSE 0 END), 0) AS upstream_cost,
			COALESCE(SUM(CASE WHEN cny_rate IS NOT NULL THEN billed_cost * cny_rate ELSE 0 END), 0) AS billed_cost,
			COALESCE(SUM(CASE WHEN cny_rate IS NULL THEN upstream_base_cost * account_rate_multiplier ELSE 0 END), 0) AS unconverted_cost
		FROM attributed
		GROUP BY bucket_start
	),
	totals AS (
		SELECT COUNT(*) FILTER (WHERE legacy_attributed) AS legacy_attributed_requests
		FROM attributed
	)
	SELECT
		b.bucket_start,
		COALESCE(a.requests, 0) AS requests,
		COALESCE(a.upstream_base_cost, 0) AS upstream_base_cost,
		COALESCE(a.upstream_cost, 0) AS upstream_cost,
		COALESCE(a.billed_cost, 0) AS billed_cost,
		COALESCE(a.billed_cost, 0) - COALESCE(a.upstream_cost, 0) AS gross_profit,
		COALESCE(a.unconverted_cost, 0) AS unconverted_cost,
		COALESCE(t.legacy_attributed_requests, 0) AS legacy_attributed_requests
	FROM buckets b
	LEFT JOIN aggregated a ON a.bucket_start = b.bucket_start
	CROSS JOIN totals t
	ORDER BY b.bucket_start ASC
`

func QueryUpstreamUsageTrend(ctx context.Context, sqlq UpstreamUsageTrendQuerier, query usagestats.UpstreamUsageTrendQuery) (result *usagestats.UpstreamUsageTrend, err error) {
	spec, err := usagestats.ResolveUpstreamUsageTrendRange(query.Range, query.Now)
	if err != nil {
		return nil, err
	}

	interval := "1 day"
	if spec.BucketUnit == "hour" {
		interval = "1 hour"
	}
	rows, err := sqlq.QueryContext(
		ctx,
		upstreamUsageTrendSQL,
		spec.StartTime,
		spec.EndTime,
		interval,
		spec.BucketUnit,
		query.UpstreamConfigID,
	)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
			result = nil
		}
	}()

	result = &usagestats.UpstreamUsageTrend{
		Range:    spec.Range,
		Currency: usagestats.UpstreamUsageTrendCurrency,
		Points:   make([]usagestats.UpstreamUsageTrendPoint, 0, spec.PointCount),
	}
	for rows.Next() {
		var (
			bucket time.Time
			point  usagestats.UpstreamUsageTrendPoint
			legacy int64
		)
		if err = rows.Scan(
			&bucket,
			&point.Requests,
			&point.UpstreamBaseCost,
			&point.UpstreamCost,
			&point.BilledCost,
			&point.GrossProfit,
			&point.UnconvertedCost,
			&legacy,
		); err != nil {
			return nil, err
		}
		point.Bucket = bucket.UTC().Format(time.RFC3339)
		result.LegacyAttributedRequests = legacy
		result.Points = append(result.Points, point)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func (r *upstreamConfigRepository) GetUpstreamUsageTrend(ctx context.Context, configID int64, rangeName string, now time.Time) (*service.UpstreamUsageTrend, error) {
	var configFilter *int64
	if configID > 0 {
		configFilter = &configID
	}
	trend, err := QueryUpstreamUsageTrend(ctx, r.client, usagestats.UpstreamUsageTrendQuery{
		UpstreamConfigID: configFilter,
		Range:            rangeName,
		Now:              now,
	})
	if err != nil {
		return nil, err
	}

	points := make([]service.UpstreamUsageTrendPoint, 0, len(trend.Points))
	for _, point := range trend.Points {
		points = append(points, service.UpstreamUsageTrendPoint{
			Bucket:           point.Bucket,
			Requests:         point.Requests,
			UpstreamBaseCost: point.UpstreamBaseCost,
			UpstreamCost:     point.UpstreamCost,
			BilledCost:       point.BilledCost,
			GrossProfit:      point.GrossProfit,
			UnconvertedCost:  point.UnconvertedCost,
		})
	}
	return &service.UpstreamUsageTrend{
		Range:                    trend.Range,
		Currency:                 trend.Currency,
		LegacyAttributedRequests: trend.LegacyAttributedRequests,
		Points:                   points,
	}, nil
}
