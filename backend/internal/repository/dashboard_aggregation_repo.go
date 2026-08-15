package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

type dashboardAggregationRepository struct {
	sql   sqlExecutor
	clock func() time.Time
}

const usageLogsCleanupBatchSize = 10000
const usageBillingDedupCleanupBatchSize = 10000
const userUsageBackfillErrorLimit = 500

const (
	userUsageBackfillAvailable   = "available"
	userUsageBackfillBuilding    = "building"
	userUsageBackfillPartial     = "partial"
	userUsageBackfillUnavailable = "unavailable"
)

type userUsageBackfillState struct {
	status        string
	coverageStart sql.NullTime
	coverageEnd   sql.NullTime
	targetEnd     sql.NullTime
}

// NewDashboardAggregationRepository 创建仪表盘预聚合仓储。
func NewDashboardAggregationRepository(sqlDB *sql.DB) service.DashboardAggregationRepository {
	if sqlDB == nil {
		return nil
	}
	if !isPostgresDriver(sqlDB) {
		log.Printf("[DashboardAggregation] 检测到非 PostgreSQL 驱动，已自动禁用预聚合")
		return nil
	}
	return newDashboardAggregationRepositoryWithSQL(sqlDB)
}

func newDashboardAggregationRepositoryWithSQL(sqlq sqlExecutor) *dashboardAggregationRepository {
	return &dashboardAggregationRepository{sql: sqlq, clock: time.Now}
}

func (r *dashboardAggregationRepository) now() time.Time {
	if r.clock != nil {
		return r.clock()
	}
	return time.Now()
}

func isPostgresDriver(db *sql.DB) bool {
	if db == nil {
		return false
	}
	_, ok := db.Driver().(*pq.Driver)
	return ok
}

func (r *dashboardAggregationRepository) AggregateRange(ctx context.Context, start, end time.Time) error {
	if r == nil || r.sql == nil {
		return nil
	}
	loc := timezone.Location()
	startLocal := start.In(loc)
	endLocal := end.In(loc)
	if !endLocal.After(startLocal) {
		return nil
	}

	hourStart := startLocal.Truncate(time.Hour)
	hourEnd := endLocal.Truncate(time.Hour)
	if endLocal.After(hourEnd) {
		hourEnd = hourEnd.Add(time.Hour)
	}
	dayStart := truncateToDay(startLocal)
	dayEnd := truncateToDay(endLocal)
	if endLocal.After(dayEnd) {
		dayEnd = dayEnd.AddDate(0, 0, 1)
	}

	if db, ok := r.sql.(*sql.DB); ok {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		txRepo := newDashboardAggregationRepositoryWithSQL(tx)
		if err := txRepo.aggregateRangeInTx(ctx, hourStart, hourEnd, dayStart, dayEnd); err != nil {
			_ = tx.Rollback()
			return err
		}
		return tx.Commit()
	}
	return r.aggregateRangeInTx(ctx, hourStart, hourEnd, dayStart, dayEnd)
}

func (r *dashboardAggregationRepository) aggregateRangeInTx(ctx context.Context, hourStart, hourEnd, dayStart, dayEnd time.Time) error {
	// 以桶边界聚合，允许覆盖 end 所在桶的剩余区间。
	if err := r.insertHourlyActiveUsers(ctx, hourStart, hourEnd); err != nil {
		return err
	}
	if err := r.insertDailyActiveUsers(ctx, hourStart, hourEnd); err != nil {
		return err
	}
	if err := r.upsertHourlyAggregates(ctx, hourStart, hourEnd); err != nil {
		return err
	}
	if err := r.upsertDailyAggregates(ctx, dayStart, dayEnd); err != nil {
		return err
	}
	return nil
}

// AggregateUserUsageRange updates the durable per-user usage ledger for the
// near-real-time range only. Historical dashboard backfills deliberately do
// not call this method: raw logs at the retention boundary may be partial and
// must never overwrite an already-complete permanent daily row.
func (r *dashboardAggregationRepository) AggregateUserUsageRange(ctx context.Context, start, end time.Time) error {
	if r == nil || r.sql == nil {
		return nil
	}

	if db, ok := r.sql.(*sql.DB); ok {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		txRepo := newDashboardAggregationRepositoryWithSQL(tx)
		if err := txRepo.aggregateAvailableUserUsageRangeInTx(ctx, start, end); err != nil {
			_ = tx.Rollback()
			return err
		}
		return tx.Commit()
	}
	return r.aggregateAvailableUserUsageRangeInTx(ctx, start, end)
}

func (r *dashboardAggregationRepository) aggregateAvailableUserUsageRangeInTx(
	ctx context.Context,
	requestedStart, end time.Time,
) error {
	state, err := r.getUserUsageBackfillStateForUpdate(ctx)
	if err != nil {
		return err
	}
	if state.status != userUsageBackfillAvailable || !state.coverageEnd.Valid {
		return errors.New("user usage backfill is not available")
	}

	coverageEnd := state.coverageEnd.Time.UTC()
	effectiveStart := coverageEnd
	end = end.UTC()
	if !end.After(effectiveStart) {
		return nil
	}

	hourlySourceStart := requestedStart.UTC()
	if !end.After(hourlySourceStart) {
		hourlySourceStart = end.Add(-time.Hour)
	}
	hourStart, hourEnd, _, _ := userUsageBucketRange(hourlySourceStart, end)
	return r.aggregateUserUsageRangeInTx(
		ctx,
		hourStart,
		hourEnd,
		effectiveStart,
		end,
	)
}

func (r *dashboardAggregationRepository) aggregateUserUsageRangeInTx(
	ctx context.Context,
	hourStart, hourEnd, coverageStart, coverageEnd time.Time,
) error {
	if err := r.upsertUserHourlyAggregates(ctx, hourStart, hourEnd); err != nil {
		return err
	}
	if err := r.syncUserDailyAggregatesFromCoverage(ctx, coverageStart, coverageEnd); err != nil {
		return err
	}
	return r.advanceAvailableUserUsageCoverage(ctx, coverageStart, coverageEnd)
}

func (r *dashboardAggregationRepository) RecomputeRange(ctx context.Context, start, end time.Time) error {
	if r == nil || r.sql == nil {
		return nil
	}
	loc := timezone.Location()
	startLocal := start.In(loc)
	endLocal := end.In(loc)
	if !endLocal.After(startLocal) {
		return nil
	}

	hourStart := startLocal.Truncate(time.Hour)
	hourEnd := endLocal.Truncate(time.Hour)
	if endLocal.After(hourEnd) {
		hourEnd = hourEnd.Add(time.Hour)
	}

	dayStart := truncateToDay(startLocal)
	dayEnd := truncateToDay(endLocal)
	if endLocal.After(dayEnd) {
		dayEnd = dayEnd.AddDate(0, 0, 1)
	}

	// 尽量使用事务保证范围内的一致性（允许在非 *sql.DB 的情况下退化为非事务执行）。
	if db, ok := r.sql.(*sql.DB); ok {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if err := lockGroupUsageRollupState(ctx, tx); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := invalidateGroupUsageRollupsAt(ctx, tx, start); err != nil {
			_ = tx.Rollback()
			return err
		}
		txRepo := newDashboardAggregationRepositoryWithSQL(tx)
		if err := txRepo.recomputeRangeInTx(ctx, hourStart, hourEnd, dayStart, dayEnd); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := txRepo.syncGroupUsageRollupsInTx(ctx, service.GroupUsageTodayStart(r.now())); err != nil {
			_ = tx.Rollback()
			return err
		}
		return tx.Commit()
	}
	return r.recomputeRangeInTx(ctx, hourStart, hourEnd, dayStart, dayEnd)
}

func (r *dashboardAggregationRepository) recomputeRangeInTx(ctx context.Context, hourStart, hourEnd, dayStart, dayEnd time.Time) error {
	// 先清空范围内桶，再重建（避免仅增量插入导致活跃用户等指标无法回退）。
	if _, err := r.sql.ExecContext(ctx, "DELETE FROM usage_dashboard_hourly WHERE bucket_start >= $1 AND bucket_start < $2", hourStart, hourEnd); err != nil {
		return err
	}
	if _, err := r.sql.ExecContext(ctx, "DELETE FROM usage_dashboard_hourly_users WHERE bucket_start >= $1 AND bucket_start < $2", hourStart, hourEnd); err != nil {
		return err
	}
	if _, err := r.sql.ExecContext(ctx, "DELETE FROM usage_dashboard_daily WHERE bucket_date >= $1::date AND bucket_date < $2::date", dayStart, dayEnd); err != nil {
		return err
	}
	if _, err := r.sql.ExecContext(ctx, "DELETE FROM usage_dashboard_daily_users WHERE bucket_date >= $1::date AND bucket_date < $2::date", dayStart, dayEnd); err != nil {
		return err
	}
	if err := r.insertHourlyActiveUsers(ctx, hourStart, hourEnd); err != nil {
		return err
	}
	if err := r.insertDailyActiveUsers(ctx, hourStart, hourEnd); err != nil {
		return err
	}
	if err := r.upsertHourlyAggregates(ctx, hourStart, hourEnd); err != nil {
		return err
	}
	if err := r.upsertDailyAggregates(ctx, dayStart, dayEnd); err != nil {
		return err
	}
	return nil
}

func (r *dashboardAggregationRepository) GetAggregationWatermark(ctx context.Context) (time.Time, error) {
	var ts time.Time
	query := "SELECT last_aggregated_at FROM usage_dashboard_aggregation_watermark WHERE id = 1"
	if err := scanSingleRow(ctx, r.sql, query, nil, &ts); err != nil {
		if err == sql.ErrNoRows {
			return time.Unix(0, 0).UTC(), nil
		}
		return time.Time{}, err
	}
	return ts.UTC(), nil
}

func (r *dashboardAggregationRepository) UpdateAggregationWatermark(ctx context.Context, aggregatedAt time.Time) error {
	query := `
		INSERT INTO usage_dashboard_aggregation_watermark (id, last_aggregated_at, updated_at)
		VALUES (1, $1, NOW())
		ON CONFLICT (id)
		DO UPDATE SET last_aggregated_at = EXCLUDED.last_aggregated_at, updated_at = EXCLUDED.updated_at
	`
	_, err := r.sql.ExecContext(ctx, query, aggregatedAt.UTC())
	return err
}

func (r *dashboardAggregationRepository) CleanupAggregates(ctx context.Context, hourlyCutoff, dailyCutoff time.Time) error {
	hourlyCutoffUTC := hourlyCutoff.UTC()
	dailyCutoffUTC := dailyCutoff.UTC()
	if _, err := r.sql.ExecContext(ctx, "DELETE FROM usage_dashboard_hourly WHERE bucket_start < $1", hourlyCutoffUTC); err != nil {
		return err
	}
	if _, err := r.sql.ExecContext(ctx, "DELETE FROM usage_dashboard_hourly_users WHERE bucket_start < $1", hourlyCutoffUTC); err != nil {
		return err
	}
	if _, err := r.sql.ExecContext(ctx, "DELETE FROM usage_dashboard_user_hourly WHERE bucket_start < $1", hourlyCutoffUTC); err != nil {
		return err
	}
	if _, err := r.sql.ExecContext(ctx, "DELETE FROM usage_dashboard_daily WHERE bucket_date < $1::date", dailyCutoffUTC); err != nil {
		return err
	}
	if _, err := r.sql.ExecContext(ctx, "DELETE FROM usage_dashboard_daily_users WHERE bucket_date < $1::date", dailyCutoffUTC); err != nil {
		return err
	}
	return nil
}

func (r *dashboardAggregationRepository) CleanupUsageLogs(ctx context.Context, cutoff time.Time) error {
	isPartitioned, err := r.isUsageLogsPartitioned(ctx)
	if err != nil {
		return err
	}
	if isPartitioned {
		if err := r.cleanupPartitionedUsageLogs(ctx, cutoff); err != nil {
			return err
		}
	} else if err := r.cleanupUsageLogsBatches(ctx, cutoff); err != nil {
		return err
	}
	return r.SyncGroupUsageRollups(ctx, service.GroupUsageTodayStart(r.now()))
}

func (r *dashboardAggregationRepository) cleanupUsageLogsBatches(ctx context.Context, cutoff time.Time) error {
	db, transactional := r.sql.(*sql.DB)
	for {
		var affected int64
		var err error
		if transactional {
			affected, err = cleanupUsageLogsBatchWithCoverageAndRollupInvalidation(ctx, db, cutoff)
		} else {
			affected, err = r.cleanupUsageLogsBatch(ctx, cutoff)
		}
		if err != nil {
			return err
		}
		if affected < usageLogsCleanupBatchSize {
			return nil
		}
	}
}

func (r *dashboardAggregationRepository) cleanupPartitionedUsageLogs(ctx context.Context, cutoff time.Time) error {
	if _, ok := r.sql.(*sql.DB); ok {
		return r.dropUsageLogsPartitions(ctx, cutoff)
	}
	if err := r.requireUserUsageCoverageForCleanup(ctx, cutoff); err != nil {
		return err
	}
	return r.dropUsageLogsPartitions(ctx, cutoff)
}

func cleanupUsageLogsBatchWithCoverageAndRollupInvalidation(ctx context.Context, db *sql.DB, cutoff time.Time) (int64, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	rollback := func(err error) (int64, error) {
		_ = tx.Rollback()
		return 0, err
	}
	txRepo := newDashboardAggregationRepositoryWithSQL(tx)
	if err := txRepo.requireUserUsageCoverageForCleanup(ctx, cutoff); err != nil {
		return rollback(err)
	}
	if err := lockGroupUsageRollupState(ctx, tx); err != nil {
		return rollback(err)
	}
	rows, err := tx.QueryContext(ctx, `
		WITH victims AS (
			SELECT ctid
			FROM usage_logs
			WHERE created_at < $1
			ORDER BY created_at ASC, id ASC
			LIMIT $2
		)
		DELETE FROM usage_logs
		WHERE ctid IN (SELECT ctid FROM victims)
		RETURNING created_at
	`, cutoff.UTC(), usageLogsCleanupBatchSize)
	if err != nil {
		return rollback(err)
	}
	var affected int64
	var earliestDeletedAt time.Time
	for rows.Next() {
		var deletedAt time.Time
		if err := rows.Scan(&deletedAt); err != nil {
			_ = rows.Close()
			return rollback(err)
		}
		affected++
		if earliestDeletedAt.IsZero() || deletedAt.Before(earliestDeletedAt) {
			earliestDeletedAt = deletedAt
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return rollback(err)
	}
	if err := rows.Close(); err != nil {
		return rollback(err)
	}
	if affected > 0 {
		if err := invalidateGroupUsageRollupsAt(ctx, tx, earliestDeletedAt); err != nil {
			return rollback(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return affected, nil
}

func (r *dashboardAggregationRepository) cleanupUsageLogsBatch(ctx context.Context, cutoff time.Time) (int64, error) {
	if db, ok := r.sql.(*sql.DB); ok {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return 0, err
		}
		txRepo := newDashboardAggregationRepositoryWithSQL(tx)
		affected, err := txRepo.cleanupUsageLogsBatchInTx(ctx, cutoff)
		if err != nil {
			_ = tx.Rollback()
			return 0, err
		}
		if err := tx.Commit(); err != nil {
			return 0, err
		}
		return affected, nil
	}
	return r.cleanupUsageLogsBatchInTx(ctx, cutoff)
}

func (r *dashboardAggregationRepository) cleanupUsageLogsBatchInTx(ctx context.Context, cutoff time.Time) (int64, error) {
	if err := r.requireUserUsageCoverageForCleanup(ctx, cutoff); err != nil {
		return 0, err
	}
	res, err := r.sql.ExecContext(ctx, `
		WITH victims AS (
			SELECT ctid
			FROM usage_logs
			WHERE created_at < $1
			LIMIT $2
		)
		DELETE FROM usage_logs
		WHERE ctid IN (SELECT ctid FROM victims)
	`, cutoff.UTC(), usageLogsCleanupBatchSize)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (r *dashboardAggregationRepository) CleanupUsageBillingDedup(ctx context.Context, cutoff time.Time) error {
	for {
		res, err := r.sql.ExecContext(ctx, `
			WITH victims AS (
				SELECT ctid, request_id, api_key_id, request_fingerprint, created_at
				FROM usage_billing_dedup
				WHERE created_at < $1
				LIMIT $2
			), archived AS (
				INSERT INTO usage_billing_dedup_archive (request_id, api_key_id, request_fingerprint, created_at)
				SELECT request_id, api_key_id, request_fingerprint, created_at
				FROM victims
				ON CONFLICT (request_id, api_key_id) DO NOTHING
			)
			DELETE FROM usage_billing_dedup
			WHERE ctid IN (SELECT ctid FROM victims)
		`, cutoff.UTC(), usageBillingDedupCleanupBatchSize)
		if err != nil {
			return err
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if affected < usageBillingDedupCleanupBatchSize {
			return nil
		}
	}
}

func (r *dashboardAggregationRepository) EnsureUsageLogsPartitions(ctx context.Context, now time.Time) error {
	isPartitioned, err := r.isUsageLogsPartitioned(ctx)
	if err != nil || !isPartitioned {
		return err
	}
	monthStart := truncateToMonthUTC(now)
	prevMonth := monthStart.AddDate(0, -1, 0)
	nextMonth := monthStart.AddDate(0, 1, 0)

	for _, m := range []time.Time{prevMonth, monthStart, nextMonth} {
		if err := r.createUsageLogsPartition(ctx, m); err != nil {
			return err
		}
	}
	return nil
}

func (r *dashboardAggregationRepository) insertHourlyActiveUsers(ctx context.Context, start, end time.Time) error {
	tzName := timezone.Name()
	query := `
		INSERT INTO usage_dashboard_hourly_users (bucket_start, user_id)
		SELECT DISTINCT
			date_trunc('hour', created_at AT TIME ZONE $3) AT TIME ZONE $3 AS bucket_start,
			user_id
		FROM usage_logs
		WHERE created_at >= $1 AND created_at < $2
		ON CONFLICT DO NOTHING
	`
	_, err := r.sql.ExecContext(ctx, query, start, end, tzName)
	return err
}

func (r *dashboardAggregationRepository) insertDailyActiveUsers(ctx context.Context, start, end time.Time) error {
	tzName := timezone.Name()
	query := `
		INSERT INTO usage_dashboard_daily_users (bucket_date, user_id)
		SELECT DISTINCT
			(bucket_start AT TIME ZONE $3)::date AS bucket_date,
			user_id
		FROM usage_dashboard_hourly_users
		WHERE bucket_start >= $1 AND bucket_start < $2
		ON CONFLICT DO NOTHING
	`
	_, err := r.sql.ExecContext(ctx, query, start, end, tzName)
	return err
}

func (r *dashboardAggregationRepository) upsertHourlyAggregates(ctx context.Context, start, end time.Time) error {
	tzName := timezone.Name()
	query := `
		WITH hourly AS (
			SELECT
				date_trunc('hour', created_at AT TIME ZONE $3) AT TIME ZONE $3 AS bucket_start,
				COUNT(*) AS total_requests,
				COALESCE(SUM(input_tokens), 0) AS input_tokens,
				COALESCE(SUM(output_tokens), 0) AS output_tokens,
				COALESCE(SUM(cache_creation_tokens), 0) AS cache_creation_tokens,
				COALESCE(SUM(cache_read_tokens), 0) AS cache_read_tokens,
				COALESCE(SUM(total_cost), 0) AS total_cost,
				COALESCE(SUM(actual_cost), 0) AS actual_cost,
				COALESCE(SUM(COALESCE(account_stats_cost, total_cost) * COALESCE(account_rate_multiplier, 1)), 0) AS account_cost,
				COALESCE(SUM(COALESCE(duration_ms, 0)), 0) AS total_duration_ms
			FROM usage_logs
			WHERE created_at >= $1 AND created_at < $2
			GROUP BY 1
		),
		user_counts AS (
			SELECT bucket_start, COUNT(*) AS active_users
			FROM usage_dashboard_hourly_users
			WHERE bucket_start >= $1 AND bucket_start < $2
			GROUP BY bucket_start
		)
		INSERT INTO usage_dashboard_hourly (
			bucket_start,
			total_requests,
			input_tokens,
			output_tokens,
			cache_creation_tokens,
			cache_read_tokens,
			total_cost,
			actual_cost,
			account_cost,
			total_duration_ms,
			active_users,
			computed_at
		)
		SELECT
			hourly.bucket_start,
			hourly.total_requests,
			hourly.input_tokens,
			hourly.output_tokens,
			hourly.cache_creation_tokens,
			hourly.cache_read_tokens,
			hourly.total_cost,
			hourly.actual_cost,
			hourly.account_cost,
			hourly.total_duration_ms,
			COALESCE(user_counts.active_users, 0) AS active_users,
			NOW()
		FROM hourly
		LEFT JOIN user_counts ON user_counts.bucket_start = hourly.bucket_start
		ON CONFLICT (bucket_start)
		DO UPDATE SET
			total_requests = EXCLUDED.total_requests,
			input_tokens = EXCLUDED.input_tokens,
			output_tokens = EXCLUDED.output_tokens,
			cache_creation_tokens = EXCLUDED.cache_creation_tokens,
			cache_read_tokens = EXCLUDED.cache_read_tokens,
			total_cost = EXCLUDED.total_cost,
			actual_cost = EXCLUDED.actual_cost,
			account_cost = EXCLUDED.account_cost,
			total_duration_ms = EXCLUDED.total_duration_ms,
			active_users = EXCLUDED.active_users,
			computed_at = EXCLUDED.computed_at
	`
	_, err := r.sql.ExecContext(ctx, query, start, end, tzName)
	return err
}

func (r *dashboardAggregationRepository) upsertDailyAggregates(ctx context.Context, start, end time.Time) error {
	tzName := timezone.Name()
	query := `
		WITH daily AS (
			SELECT
				(bucket_start AT TIME ZONE $5)::date AS bucket_date,
				COALESCE(SUM(total_requests), 0) AS total_requests,
				COALESCE(SUM(input_tokens), 0) AS input_tokens,
				COALESCE(SUM(output_tokens), 0) AS output_tokens,
				COALESCE(SUM(cache_creation_tokens), 0) AS cache_creation_tokens,
				COALESCE(SUM(cache_read_tokens), 0) AS cache_read_tokens,
				COALESCE(SUM(total_cost), 0) AS total_cost,
				COALESCE(SUM(actual_cost), 0) AS actual_cost,
				COALESCE(SUM(account_cost), 0) AS account_cost,
				COALESCE(SUM(total_duration_ms), 0) AS total_duration_ms
			FROM usage_dashboard_hourly
			WHERE bucket_start >= $1 AND bucket_start < $2
			GROUP BY (bucket_start AT TIME ZONE $5)::date
		),
		user_counts AS (
			SELECT bucket_date, COUNT(*) AS active_users
			FROM usage_dashboard_daily_users
			WHERE bucket_date >= $3::date AND bucket_date < $4::date
			GROUP BY bucket_date
		)
		INSERT INTO usage_dashboard_daily (
			bucket_date,
			total_requests,
			input_tokens,
			output_tokens,
			cache_creation_tokens,
			cache_read_tokens,
			total_cost,
			actual_cost,
			account_cost,
			total_duration_ms,
			active_users,
			computed_at
		)
		SELECT
			daily.bucket_date,
			daily.total_requests,
			daily.input_tokens,
			daily.output_tokens,
			daily.cache_creation_tokens,
			daily.cache_read_tokens,
			daily.total_cost,
			daily.actual_cost,
			daily.account_cost,
			daily.total_duration_ms,
			COALESCE(user_counts.active_users, 0) AS active_users,
			NOW()
		FROM daily
		LEFT JOIN user_counts ON user_counts.bucket_date = daily.bucket_date
		ON CONFLICT (bucket_date)
		DO UPDATE SET
			total_requests = EXCLUDED.total_requests,
			input_tokens = EXCLUDED.input_tokens,
			output_tokens = EXCLUDED.output_tokens,
			cache_creation_tokens = EXCLUDED.cache_creation_tokens,
			cache_read_tokens = EXCLUDED.cache_read_tokens,
			total_cost = EXCLUDED.total_cost,
			actual_cost = EXCLUDED.actual_cost,
			account_cost = EXCLUDED.account_cost,
			total_duration_ms = EXCLUDED.total_duration_ms,
			active_users = EXCLUDED.active_users,
			computed_at = EXCLUDED.computed_at
	`
	_, err := r.sql.ExecContext(ctx, query, start, end, start, end, tzName)
	return err
}

func (r *dashboardAggregationRepository) upsertUserHourlyAggregates(ctx context.Context, start, end time.Time) error {
	query := `
		WITH hourly AS (
			SELECT
				date_trunc('hour', created_at AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' AS bucket_start,
				user_id,
				COALESCE(SUM(input_tokens), 0) AS input_tokens,
				COALESCE(SUM(output_tokens), 0) AS output_tokens,
				COALESCE(SUM(cache_creation_tokens), 0) AS cache_creation_tokens,
				COALESCE(SUM(cache_read_tokens), 0) AS cache_read_tokens,
				COALESCE(SUM(actual_cost), 0) AS user_spend,
				COALESCE(SUM(COALESCE(account_stats_cost, total_cost) * COALESCE(account_rate_multiplier, 1)), 0) AS account_cost
			FROM usage_logs
			WHERE created_at >= $1 AND created_at < $2
			GROUP BY 1, user_id
		)
		INSERT INTO usage_dashboard_user_hourly (
			bucket_start,
			user_id,
			input_tokens,
			output_tokens,
			cache_creation_tokens,
			cache_read_tokens,
			user_spend,
			account_cost,
			computed_at
		)
		SELECT
			bucket_start,
			user_id,
			input_tokens,
			output_tokens,
			cache_creation_tokens,
			cache_read_tokens,
			user_spend,
			account_cost,
			NOW()
		FROM hourly
		ON CONFLICT (bucket_start, user_id)
		DO UPDATE SET
			input_tokens = EXCLUDED.input_tokens,
			output_tokens = EXCLUDED.output_tokens,
			cache_creation_tokens = EXCLUDED.cache_creation_tokens,
			cache_read_tokens = EXCLUDED.cache_read_tokens,
			user_spend = EXCLUDED.user_spend,
			account_cost = EXCLUDED.account_cost,
			computed_at = EXCLUDED.computed_at
	`
	_, err := r.sql.ExecContext(ctx, query, start, end)
	return err
}

func (r *dashboardAggregationRepository) upsertUserDailyAggregates(ctx context.Context, start, end time.Time) error {
	tzName := timezone.Name()
	query := `
		WITH daily AS (
			SELECT
				(created_at AT TIME ZONE $3)::date AS bucket_date,
				user_id,
				COALESCE(SUM(input_tokens), 0) AS input_tokens,
				COALESCE(SUM(output_tokens), 0) AS output_tokens,
				COALESCE(SUM(cache_creation_tokens), 0) AS cache_creation_tokens,
				COALESCE(SUM(cache_read_tokens), 0) AS cache_read_tokens,
				COALESCE(SUM(actual_cost), 0) AS user_spend,
				COALESCE(SUM(COALESCE(account_stats_cost, total_cost) * COALESCE(account_rate_multiplier, 1)), 0) AS account_cost
			FROM usage_logs
			WHERE created_at >= $1 AND created_at < $2
			GROUP BY 1, user_id
		)
		INSERT INTO usage_dashboard_user_daily (
			bucket_date,
			user_id,
			input_tokens,
			output_tokens,
			cache_creation_tokens,
			cache_read_tokens,
			user_spend,
			account_cost,
			computed_at
		)
		SELECT
			bucket_date,
			user_id,
			input_tokens,
			output_tokens,
			cache_creation_tokens,
			cache_read_tokens,
			user_spend,
			account_cost,
			NOW()
		FROM daily
		ON CONFLICT (bucket_date, user_id)
		DO UPDATE SET
			input_tokens = EXCLUDED.input_tokens,
			output_tokens = EXCLUDED.output_tokens,
			cache_creation_tokens = EXCLUDED.cache_creation_tokens,
			cache_read_tokens = EXCLUDED.cache_read_tokens,
			user_spend = EXCLUDED.user_spend,
			account_cost = EXCLUDED.account_cost,
			computed_at = EXCLUDED.computed_at
	`
	_, err := r.sql.ExecContext(ctx, query, start, end, tzName)
	return err
}

func (r *dashboardAggregationRepository) addUserDailyAggregates(ctx context.Context, start, end time.Time) error {
	if !end.After(start) {
		return nil
	}
	tzName := timezone.Name()
	query := `
		WITH daily AS (
			SELECT
				(created_at AT TIME ZONE $3)::date AS bucket_date,
				user_id,
				COALESCE(SUM(input_tokens), 0) AS input_tokens,
				COALESCE(SUM(output_tokens), 0) AS output_tokens,
				COALESCE(SUM(cache_creation_tokens), 0) AS cache_creation_tokens,
				COALESCE(SUM(cache_read_tokens), 0) AS cache_read_tokens,
				COALESCE(SUM(actual_cost), 0) AS user_spend,
				COALESCE(SUM(COALESCE(account_stats_cost, total_cost) * COALESCE(account_rate_multiplier, 1)), 0) AS account_cost
			FROM usage_logs
			WHERE created_at >= $1 AND created_at < $2
			GROUP BY 1, user_id
		)
		INSERT INTO usage_dashboard_user_daily (
			bucket_date,
			user_id,
			input_tokens,
			output_tokens,
			cache_creation_tokens,
			cache_read_tokens,
			user_spend,
			account_cost,
			computed_at
		)
		SELECT
			bucket_date,
			user_id,
			input_tokens,
			output_tokens,
			cache_creation_tokens,
			cache_read_tokens,
			user_spend,
			account_cost,
			NOW()
		FROM daily
		ON CONFLICT (bucket_date, user_id)
		DO UPDATE SET
			input_tokens = usage_dashboard_user_daily.input_tokens + EXCLUDED.input_tokens,
			output_tokens = usage_dashboard_user_daily.output_tokens + EXCLUDED.output_tokens,
			cache_creation_tokens = usage_dashboard_user_daily.cache_creation_tokens + EXCLUDED.cache_creation_tokens,
			cache_read_tokens = usage_dashboard_user_daily.cache_read_tokens + EXCLUDED.cache_read_tokens,
			user_spend = usage_dashboard_user_daily.user_spend + EXCLUDED.user_spend,
			account_cost = usage_dashboard_user_daily.account_cost + EXCLUDED.account_cost,
			computed_at = EXCLUDED.computed_at
	`
	_, err := r.sql.ExecContext(ctx, query, start.UTC(), end.UTC(), tzName)
	return err
}

// syncUserDailyAggregatesFromCoverage advances an exact, non-overlapping
// coverage interval. The already-materialized prefix of the first configured
// day is preserved by adding only the uncovered suffix. Every later day starts
// at its natural boundary and can therefore be safely replaced from raw logs.
func (r *dashboardAggregationRepository) syncUserDailyAggregatesFromCoverage(
	ctx context.Context,
	coverageStart, coverageEnd time.Time,
) error {
	if !coverageEnd.After(coverageStart) {
		return nil
	}

	loc := timezone.Location()
	startLocal := coverageStart.In(loc)
	startDay := truncateToDay(startLocal)
	cursor := coverageStart.UTC()
	if !coverageStart.Equal(startDay) {
		partialEnd := nextConfiguredDayBoundary(coverageStart)
		if partialEnd.After(coverageEnd) {
			partialEnd = coverageEnd
		}
		if err := r.addUserDailyAggregates(ctx, cursor, partialEnd); err != nil {
			return err
		}
		cursor = partialEnd.UTC()
	}
	if coverageEnd.After(cursor) {
		return r.upsertUserDailyAggregates(ctx, cursor, coverageEnd.UTC())
	}
	return nil
}

// EnsureUserUsageBackfill materializes every currently retained usage log into
// the permanent per-user daily aggregates. Progress advances only in the same
// transaction as each one-day chunk, so a retry can safely resume at
// coverage_end without treating an incomplete chunk as covered.
func (r *dashboardAggregationRepository) EnsureUserUsageBackfill(ctx context.Context) (retErr error) {
	if r == nil || r.sql == nil {
		return nil
	}

	state, err := r.getUserUsageBackfillState(ctx)
	if err != nil {
		return err
	}
	if state.status == userUsageBackfillAvailable {
		now := time.Now().UTC()
		return r.AggregateUserUsageRange(ctx, now, now)
	}

	var earliest sql.NullTime
	var databaseNow time.Time
	if err := scanSingleRow(ctx, r.sql, `
		SELECT MIN(created_at), statement_timestamp()
		FROM usage_logs
	`, nil, &earliest, &databaseNow); err != nil {
		return err
	}
	databaseNow = databaseNow.UTC()

	start := databaseNow
	targetEnd := databaseNow
	if state.coverageStart.Valid {
		start = state.coverageStart.Time.UTC()
	} else if earliest.Valid {
		start = earliest.Time.UTC()
	}
	if state.targetEnd.Valid {
		targetEnd = state.targetEnd.Time.UTC()
	}
	cursor := start
	if state.coverageEnd.Valid && state.coverageEnd.Time.After(cursor) {
		cursor = state.coverageEnd.Time.UTC()
	}

	if err := r.beginUserUsageBackfill(ctx, start, targetEnd, earliest.Valid); err != nil {
		return err
	}
	defer func() {
		if retErr == nil {
			return
		}
		markCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		_ = r.markUserUsageBackfillPartial(markCtx, retErr)
	}()

	if !earliest.Valid && !state.coverageStart.Valid {
		return r.finalizeUserUsageBackfill(ctx)
	}

	for cursor.Before(targetEnd) {
		windowEnd := nextConfiguredDayBoundary(cursor)
		if windowEnd.After(targetEnd) {
			windowEnd = targetEnd
		}
		if err := r.backfillUserUsageChunk(ctx, cursor, windowEnd); err != nil {
			return err
		}
		cursor = windowEnd
	}

	return r.finalizeUserUsageBackfill(ctx)
}

func (r *dashboardAggregationRepository) getUserUsageBackfillState(ctx context.Context) (userUsageBackfillState, error) {
	return r.queryUserUsageBackfillState(ctx, false)
}

func (r *dashboardAggregationRepository) getUserUsageBackfillStateForUpdate(ctx context.Context) (userUsageBackfillState, error) {
	return r.queryUserUsageBackfillState(ctx, true)
}

func (r *dashboardAggregationRepository) queryUserUsageBackfillState(ctx context.Context, forUpdate bool) (userUsageBackfillState, error) {
	var state userUsageBackfillState
	query := `
		SELECT status, coverage_start, coverage_end, target_end
		FROM usage_dashboard_user_backfill_state
		WHERE id = 1
	`
	if forUpdate {
		query += " FOR UPDATE"
	}
	err := scanSingleRow(ctx, r.sql, query, nil, &state.status, &state.coverageStart, &state.coverageEnd, &state.targetEnd)
	if err == sql.ErrNoRows {
		return userUsageBackfillState{status: userUsageBackfillUnavailable}, nil
	}
	return state, err
}

func (r *dashboardAggregationRepository) beginUserUsageBackfill(ctx context.Context, start, targetEnd time.Time, hasLogs bool) error {
	var earliestDate any
	if hasLogs {
		earliestDate = configuredDate(start)
	}
	_, err := r.sql.ExecContext(ctx, `
		INSERT INTO usage_dashboard_user_backfill_state (
			id,
			earliest_covered_date,
			last_completed_date,
			status,
			coverage_start,
			coverage_end,
			target_end,
			attempt_count,
			last_error,
			updated_at,
			completed_at
		)
		VALUES (1, $1::date, NULL, $4, $2, $2, $3, 1, NULL, NOW(), NULL)
		ON CONFLICT (id)
		DO UPDATE SET
			earliest_covered_date = COALESCE(
				usage_dashboard_user_backfill_state.earliest_covered_date,
				EXCLUDED.earliest_covered_date
			),
			status = EXCLUDED.status,
			coverage_start = COALESCE(usage_dashboard_user_backfill_state.coverage_start, EXCLUDED.coverage_start),
			coverage_end = COALESCE(usage_dashboard_user_backfill_state.coverage_end, EXCLUDED.coverage_end),
			target_end = COALESCE(usage_dashboard_user_backfill_state.target_end, EXCLUDED.target_end),
			attempt_count = usage_dashboard_user_backfill_state.attempt_count + 1,
			last_error = NULL,
			updated_at = NOW(),
			completed_at = NULL
	`, earliestDate, start.UTC(), targetEnd.UTC(), userUsageBackfillBuilding)
	return err
}

func (r *dashboardAggregationRepository) backfillUserUsageChunk(ctx context.Context, start, end time.Time) error {
	if db, ok := r.sql.(*sql.DB); ok {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		txRepo := newDashboardAggregationRepositoryWithSQL(tx)
		if err := txRepo.backfillUserUsageChunkInTx(ctx, start, end); err != nil {
			_ = tx.Rollback()
			return err
		}
		return tx.Commit()
	}
	return r.backfillUserUsageChunkInTx(ctx, start, end)
}

func (r *dashboardAggregationRepository) backfillUserUsageChunkInTx(
	ctx context.Context,
	start, end time.Time,
) error {
	if err := r.upsertUserDailyAggregates(ctx, start.UTC(), end.UTC()); err != nil {
		return err
	}
	return r.advanceBuildingUserUsageCoverage(ctx, end)
}

func (r *dashboardAggregationRepository) advanceBuildingUserUsageCoverage(ctx context.Context, coverageEnd time.Time) error {
	res, err := r.sql.ExecContext(ctx, `
		UPDATE usage_dashboard_user_backfill_state
		SET coverage_end = GREATEST(COALESCE(coverage_end, $1), $1),
			last_completed_date = $2::date,
			updated_at = NOW()
		WHERE id = 1 AND status = $3
	`, coverageEnd.UTC(), configuredDate(coverageEnd.Add(-time.Nanosecond)), userUsageBackfillBuilding)
	if err != nil {
		return err
	}
	return requireExactlyOneAffected(res, "advance building user usage coverage")
}

func (r *dashboardAggregationRepository) advanceAvailableUserUsageCoverage(ctx context.Context, start, end time.Time) error {
	res, err := r.sql.ExecContext(ctx, `
		UPDATE usage_dashboard_user_backfill_state
		SET coverage_start = LEAST(COALESCE(coverage_start, $1), $1),
			coverage_end = GREATEST(COALESCE(coverage_end, $2), $2),
			earliest_covered_date = LEAST(COALESCE(earliest_covered_date, $3::date), $3::date),
			last_completed_date = GREATEST(COALESCE(last_completed_date, $4::date), $4::date),
			updated_at = NOW()
		WHERE id = 1
		  AND status = $5
		  AND coverage_start IS NOT NULL
		  AND coverage_end IS NOT NULL
		  AND $1 <= coverage_end
		  AND $2 >= coverage_start
	`, start.UTC(), end.UTC(), configuredDate(start), configuredDate(end.Add(-time.Nanosecond)), userUsageBackfillAvailable)
	if err != nil {
		return err
	}
	return requireExactlyOneAffected(res, "advance available user usage coverage")
}

func (r *dashboardAggregationRepository) finalizeUserUsageBackfill(ctx context.Context) error {
	if db, ok := r.sql.(*sql.DB); ok {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		txRepo := newDashboardAggregationRepositoryWithSQL(tx)
		if err := txRepo.finalizeUserUsageBackfillInTx(ctx); err != nil {
			_ = tx.Rollback()
			return err
		}
		return tx.Commit()
	}
	return r.finalizeUserUsageBackfillInTx(ctx)
}

func (r *dashboardAggregationRepository) finalizeUserUsageBackfillInTx(ctx context.Context) error {
	state, err := r.getUserUsageBackfillStateForUpdate(ctx)
	if err != nil {
		return err
	}
	if state.status != userUsageBackfillBuilding || !state.coverageEnd.Valid {
		return errors.New("user usage backfill cannot be finalized")
	}

	var databaseNow time.Time
	if err := scanSingleRow(ctx, r.sql, "SELECT statement_timestamp()", nil, &databaseNow); err != nil {
		return err
	}
	databaseNow = databaseNow.UTC()
	coverageEnd := state.coverageEnd.Time.UTC()
	if databaseNow.After(coverageEnd) {
		hourlyStart := databaseNow.Add(-time.Hour)
		if coverageEnd.After(hourlyStart) {
			hourlyStart = coverageEnd
		}
		hourStart, hourEnd, _, _ := userUsageBucketRange(hourlyStart, databaseNow)
		if err := r.upsertUserHourlyAggregates(ctx, hourStart, hourEnd); err != nil {
			return err
		}
		if err := r.syncUserDailyAggregatesFromCoverage(ctx, coverageEnd, databaseNow); err != nil {
			return err
		}
	}

	res, err := r.sql.ExecContext(ctx, `
		UPDATE usage_dashboard_user_backfill_state
		SET status = $1,
			coverage_end = GREATEST(COALESCE(coverage_end, $2), $2),
			last_completed_date = CASE
				WHEN coverage_start IS NULL OR coverage_start = $2 THEN last_completed_date
				ELSE $3::date
			END,
			target_end = NULL,
			last_error = NULL,
			updated_at = NOW(),
			completed_at = NOW()
		WHERE id = 1 AND status = $4
	`, userUsageBackfillAvailable, databaseNow, configuredDate(databaseNow.Add(-time.Nanosecond)), userUsageBackfillBuilding)
	if err != nil {
		return err
	}
	return requireExactlyOneAffected(res, "finalize user usage backfill")
}

func (r *dashboardAggregationRepository) markUserUsageBackfillPartial(ctx context.Context, cause error) error {
	message := cause.Error()
	if len(message) > userUsageBackfillErrorLimit {
		message = message[:userUsageBackfillErrorLimit]
	}
	_, err := r.sql.ExecContext(ctx, `
		UPDATE usage_dashboard_user_backfill_state
		SET status = $1,
			last_error = $2,
			updated_at = NOW()
		WHERE id = 1 AND status = $3
	`, userUsageBackfillPartial, message, userUsageBackfillBuilding)
	return err
}

func (r *dashboardAggregationRepository) requireUserUsageCoverageForCleanup(ctx context.Context, cutoff time.Time) error {
	covered, err := r.userUsageAggregatesCoverCleanupForUpdate(ctx, cutoff)
	if err != nil {
		return err
	}
	if !covered {
		return errors.New("user usage aggregates do not cover usage_logs cleanup range")
	}
	return nil
}

func (r *dashboardAggregationRepository) userUsageAggregatesCoverCleanupForUpdate(ctx context.Context, cutoff time.Time) (bool, error) {
	state, err := r.getUserUsageBackfillStateForUpdate(ctx)
	if err != nil {
		return false, err
	}
	if state.status != userUsageBackfillAvailable || !state.coverageStart.Valid || !state.coverageEnd.Valid {
		return false, nil
	}

	var earliest, latest sql.NullTime
	if err := scanSingleRow(ctx, r.sql, `
		SELECT MIN(created_at), MAX(created_at)
		FROM usage_logs
		WHERE created_at < $1
	`, []any{cutoff.UTC()}, &earliest, &latest); err != nil {
		return false, err
	}
	if !earliest.Valid {
		return true, nil
	}
	return !earliest.Time.Before(state.coverageStart.Time) && latest.Time.Before(state.coverageEnd.Time), nil
}

func userUsageBucketRange(start, end time.Time) (hourStart, hourEnd, dayStart, dayEnd time.Time) {
	loc := timezone.Location()
	startLocal := start.In(loc)
	endLocal := end.In(loc)
	hourStart = start.UTC().Truncate(time.Hour)
	hourEnd = end.UTC().Truncate(time.Hour)
	if end.UTC().After(hourEnd) {
		hourEnd = hourEnd.Add(time.Hour)
	}
	dayStart = truncateToDay(startLocal)
	dayEnd = truncateToDay(endLocal)
	if endLocal.After(dayEnd) {
		dayEnd = dayEnd.AddDate(0, 0, 1)
	}
	return hourStart, hourEnd, dayStart, dayEnd
}

func nextConfiguredDayBoundary(t time.Time) time.Time {
	return truncateToDay(t.In(timezone.Location())).AddDate(0, 0, 1)
}

func configuredDate(t time.Time) string {
	return t.In(timezone.Location()).Format("2006-01-02")
}

func requireExactlyOneAffected(result sql.Result, action string) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("%s affected %d rows", action, affected)
	}
	return nil
}

func (r *dashboardAggregationRepository) isUsageLogsPartitioned(ctx context.Context) (bool, error) {
	query := `
		SELECT EXISTS(
			SELECT 1
			FROM pg_partitioned_table pt
			JOIN pg_class c ON c.oid = pt.partrelid
			WHERE c.relname = 'usage_logs'
		)
	`
	var partitioned bool
	if err := scanSingleRow(ctx, r.sql, query, nil, &partitioned); err != nil {
		return false, err
	}
	return partitioned, nil
}

func (r *dashboardAggregationRepository) dropUsageLogsPartitions(ctx context.Context, cutoff time.Time) error {
	rows, err := r.sql.QueryContext(ctx, `
		SELECT c.relname
		FROM pg_inherits
		JOIN pg_class c ON c.oid = pg_inherits.inhrelid
		JOIN pg_class p ON p.oid = pg_inherits.inhparent
		WHERE p.relname = 'usage_logs'
	`)
	if err != nil {
		return err
	}
	cutoffMonth := truncateToMonthUTC(cutoff)
	type usageLogsPartition struct {
		name  string
		month time.Time
	}
	partitions := make([]usageLogsPartition, 0)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			_ = rows.Close()
			return err
		}
		if !strings.HasPrefix(name, "usage_logs_") {
			continue
		}
		suffix := strings.TrimPrefix(name, "usage_logs_")
		month, err := time.Parse("200601", suffix)
		if err != nil {
			continue
		}
		month = month.UTC()
		if month.Before(cutoffMonth) {
			partitions = append(partitions, usageLogsPartition{name: name, month: month})
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}

	sort.Slice(partitions, func(i, j int) bool {
		return partitions[i].month.Before(partitions[j].month)
	})
	if db, ok := r.sql.(*sql.DB); ok {
		for _, partition := range partitions {
			if err := dropUsageLogsPartitionWithRollupInvalidation(ctx, db, partition.name, partition.month, cutoff); err != nil {
				return err
			}
		}
		return nil
	}
	for _, partition := range partitions {
		if _, err := r.sql.ExecContext(ctx, fmt.Sprintf("DROP TABLE IF EXISTS %s", pq.QuoteIdentifier(partition.name))); err != nil {
			return err
		}
	}
	return nil
}

func dropUsageLogsPartitionWithRollupInvalidation(ctx context.Context, db *sql.DB, name string, monthStart, cutoff time.Time) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	rollback := func(err error) error {
		_ = tx.Rollback()
		return err
	}

	txRepo := newDashboardAggregationRepositoryWithSQL(tx)
	if err := txRepo.requireUserUsageCoverageForCleanup(ctx, cutoff); err != nil {
		return rollback(err)
	}
	if err := lockGroupUsageRollupState(ctx, tx); err != nil {
		return rollback(err)
	}
	if err := invalidateGroupUsageRollupsAt(ctx, tx, monthStart); err != nil {
		return rollback(err)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("DROP TABLE IF EXISTS %s", pq.QuoteIdentifier(name))); err != nil {
		return rollback(err)
	}
	return tx.Commit()
}

func (r *dashboardAggregationRepository) createUsageLogsPartition(ctx context.Context, month time.Time) error {
	monthStart := truncateToMonthUTC(month)
	nextMonth := monthStart.AddDate(0, 1, 0)
	name := fmt.Sprintf("usage_logs_%s", monthStart.Format("200601"))
	query := fmt.Sprintf(
		"CREATE TABLE IF NOT EXISTS %s PARTITION OF usage_logs FOR VALUES FROM (%s) TO (%s)",
		pq.QuoteIdentifier(name),
		pq.QuoteLiteral(monthStart.Format("2006-01-02")),
		pq.QuoteLiteral(nextMonth.Format("2006-01-02")),
	)
	_, err := r.sql.ExecContext(ctx, query)
	return err
}

func truncateToDay(t time.Time) time.Time {
	return timezone.StartOfDay(t)
}

func truncateToMonthUTC(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}
