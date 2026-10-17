//go:build upstreamtablepg

package repository

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"os"
	"strings"
	"testing"
	"time"
)

// Frozen SQL from commit 285f5d22, evaluated by the same row decoder as the
// optimized query. Never regenerate expected results from the optimized SQL.
const qualityStatsPreSinglePassSQL = `
		WITH successful AS MATERIALIZED (
			SELECT
				ul.%[1]s,
					ul.created_at,
					ul.duration_ms,
					ul.first_token_ms,
					ul.input_tokens,
					ul.cache_creation_tokens,
					ul.cache_read_tokens
			FROM usage_logs ul
			WHERE ul.%[1]s = ANY($1)
				AND ul.created_at >= $2
				AND ul.created_at < $4
				AND (ul.actual_cost > 0 OR (ul.actual_cost = 0 AND ul.total_cost > 0 AND ul.rate_multiplier = 0))
				AND ul.stream = TRUE
		), quality AS (
			SELECT
				%[1]s,
				COUNT(*) FILTER (WHERE created_at >= $3) AS realtime_count,
				COUNT(first_token_ms) FILTER (WHERE created_at >= $3) AS realtime_first_count,
					AVG(first_token_ms) FILTER (WHERE created_at >= $3) AS realtime_first_avg,
					AVG(duration_ms) FILTER (WHERE created_at >= $3) AS realtime_duration_avg,
					COALESCE(SUM(cache_read_tokens) FILTER (WHERE created_at >= $3), 0) AS realtime_cache_read_tokens,
					COALESCE(SUM(input_tokens + cache_creation_tokens + cache_read_tokens) FILTER (WHERE created_at >= $3), 0) AS realtime_cache_input_tokens,
					COUNT(*) AS recent_count,
					COUNT(first_token_ms) AS recent_first_count,
					AVG(first_token_ms) AS recent_first_avg,
					AVG(duration_ms) AS recent_duration_avg,
					COALESCE(SUM(cache_read_tokens), 0) AS recent_cache_read_tokens,
					COALESCE(SUM(input_tokens + cache_creation_tokens + cache_read_tokens), 0) AS recent_cache_input_tokens
			FROM successful
			WHERE duration_ms IS NOT NULL
			GROUP BY %[1]s
		), activity AS (
			SELECT
				%[1]s,
				COUNT(*) FILTER (WHERE created_at >= $3) AS successful_requests_1h,
				COUNT(*) AS successful_requests_24h,
				MAX(created_at) AS last_success_at
			FROM successful
			GROUP BY %[1]s
		), errors AS (
			SELECT
				oe.%[1]s,
				COUNT(DISTINCT COALESCE(NULLIF(oe.request_id, ''), oe.id::text)) FILTER (WHERE oe.created_at >= $3) AS failed_requests_1h,
				COUNT(DISTINCT COALESCE(NULLIF(oe.request_id, ''), oe.id::text)) AS failed_requests_24h,
				MAX(oe.created_at) AS last_error_at
			FROM ops_error_logs oe
			WHERE oe.%[1]s = ANY($1)
				AND oe.created_at >= $2
				AND oe.created_at < $4
				AND oe.stream = TRUE
				AND oe.is_count_tokens = FALSE
			GROUP BY oe.%[1]s
		)
		SELECT
			COALESCE(a.%[1]s, e.%[1]s),
			COALESCE(q.realtime_count, 0),
			COALESCE(q.realtime_first_count, 0),
				q.realtime_first_avg,
				q.realtime_duration_avg,
				COALESCE(q.realtime_cache_read_tokens, 0),
				COALESCE(q.realtime_cache_input_tokens, 0),
				COALESCE(q.recent_count, 0),
				COALESCE(q.recent_first_count, 0),
				q.recent_first_avg,
				q.recent_duration_avg,
				COALESCE(q.recent_cache_read_tokens, 0),
				COALESCE(q.recent_cache_input_tokens, 0),
			COALESCE(a.successful_requests_1h, 0),
			COALESCE(e.failed_requests_1h, 0),
			COALESCE(a.successful_requests_24h, 0),
			COALESCE(e.failed_requests_24h, 0),
			a.last_success_at,
			e.last_error_at
		FROM activity a
		FULL OUTER JOIN errors e ON e.%[1]s = a.%[1]s
		LEFT JOIN quality q ON q.%[1]s = COALESCE(a.%[1]s, e.%[1]s)
	`

type frozenQualitySQL struct {
	sqlExecutor
	scope string
}

type explainQualitySQL struct {
	sqlExecutor
	t    *testing.T
	name string
}

func (q explainQualitySQL) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	writeUpstreamTableExplain(q.t, ctx, q.sqlExecutor, q.name, query, args...)
	return q.sqlExecutor.QueryContext(ctx, query, args...)
}

func (q frozenQualitySQL) QueryContext(ctx context.Context, _ string, args ...any) (*sql.Rows, error) {
	return q.sqlExecutor.QueryContext(ctx, fmt.Sprintf(qualityStatsPreSinglePassSQL, q.scope), args...)
}

func TestAccountQualitySinglePassPostgresMatchesFrozenSQL(t *testing.T) {
	dsn := os.Getenv("SUB2API_UPSTREAM_TABLE_TEST_DSN")
	if dsn == "" {
		t.Skip("SUB2API_UPSTREAM_TABLE_TEST_DSN is required")
	}
	require.Equal(t, "1", os.Getenv("SUB2API_UPSTREAM_TABLE_TEST_ISOLATED"), "disposable test database acknowledgement required")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	defer db.Close()
	var name string
	require.NoError(t, db.QueryRowContext(ctx, "SELECT current_database()").Scan(&name))
	require.Contains(t, strings.ToLower(name), "test", "refusing non-test database")
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer tx.Rollback()
	// Temporary tables shadow any real names and disappear with the transaction.
	_, err = tx.ExecContext(ctx, `CREATE TEMP TABLE usage_logs (
 account_id bigint, group_id bigint, created_at timestamptz,
 duration_ms int, first_token_ms int, input_tokens int, cache_creation_tokens int, cache_read_tokens int,
 actual_cost numeric, total_cost numeric, rate_multiplier numeric, stream boolean);
 CREATE TEMP TABLE ops_error_logs (id bigint,account_id bigint,group_id bigint,request_id text,created_at timestamptz,stream boolean,is_count_tokens boolean)`)
	require.NoError(t, err)
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	// Boundaries are exactly representable by PostgreSQL's microsecond clock.
	offsets := []time.Duration{-24*time.Hour - time.Microsecond, -24 * time.Hour, -time.Hour - time.Microsecond, -time.Hour, -time.Microsecond, 0, time.Microsecond}
	for i, offset := range offsets {
		for _, id := range []int64{1, 2, 3} {
			for variant := 0; variant < 5; variant++ {
				var duration, first any = int64(2000 + i*117), int64(200 + i*31)
				if variant == 1 {
					duration = nil
				}
				if variant == 2 {
					first = nil
				}
				cost, total, rate, stream := 0.01, 0.01, 1.0, true
				if variant == 3 {
					cost, rate = 0, 0
				}
				if variant == 4 {
					stream = false
				}
				_, err = tx.ExecContext(ctx, `INSERT INTO usage_logs VALUES($1,$2,$3,$4,$5,11,7,19,$6,$7,$8,$9)`, id, id%2+10, now.Add(offset), duration, first, cost, total, rate, stream)
				require.NoError(t, err)
			}
		}
	}
	// Entirely untimed account and error-only account exercise outer-join NULLs.
	_, err = tx.ExecContext(ctx, `INSERT INTO usage_logs VALUES(4,12,$1,NULL,999,1,2,3,0.01,0.01,1,true)`, now.Add(-time.Minute))
	require.NoError(t, err)
	for i, offset := range offsets {
		for _, id := range []int64{1, 2, 5} {
			for variant := 0; variant < 4; variant++ {
				requestID := fmt.Sprintf("req-%d-%d", id, i)
				if variant == 2 {
					requestID = ""
				}
				_, err = tx.ExecContext(ctx, `INSERT INTO ops_error_logs VALUES($1,$2,$3,$4,$5,true,$6)`, i*1000+int(id)*10+variant, id, id%2+10, requestID, now.Add(offset), variant == 3)
				require.NoError(t, err)
			}
		}
	}
	_, err = tx.ExecContext(ctx, "ANALYZE usage_logs; ANALYZE ops_error_logs")
	require.NoError(t, err)
	for _, scope := range []string{"account_id", "group_id"} {
		t.Run(scope, func(t *testing.T) {
			ids := []int64{1, 2, 3, 4, 5, 10, 11, 12, 99}
			optimized := newUsageLogRepositoryWithSQL(nil, explainQualitySQL{sqlExecutor: tx, t: t, name: "quality_" + scope + "_optimized"})
			frozen := newUsageLogRepositoryWithSQL(nil, frozenQualitySQL{sqlExecutor: tx, scope: scope})
			writeUpstreamTableExplain(t, ctx, tx, "quality_"+scope+"_baseline", fmt.Sprintf(qualityStatsPreSinglePassSQL, scope), pq.Array(ids), now.Add(-24*time.Hour), now.Add(-time.Hour), now)
			actual, err := optimized.getQualityStatsBatch(ctx, ids, now.Add(-24*time.Hour), now.Add(-time.Hour), now, scope)
			require.NoError(t, err)
			expected, err := frozen.getQualityStatsBatch(ctx, ids, now.Add(-24*time.Hour), now.Add(-time.Hour), now, scope)
			require.NoError(t, err)
			require.Equal(t, expected, actual, "samples, cache weighting, untimed activity, and deduplicated errors must all match")
		})
	}
}
