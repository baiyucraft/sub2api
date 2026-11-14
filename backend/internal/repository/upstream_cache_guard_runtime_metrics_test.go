package repository

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/upstreamcacheguard"
	"github.com/stretchr/testify/require"
)

func TestCacheGuardActiveRuntimeReadMetrics(t *testing.T) {
	for _, failure := range []string{"", "query", "read"} {
		t.Run(failure, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer db.Close()
			counts := map[string]int64{}
			timings := map[string]time.Duration{}
			ctx := upstreamcacheguard.WithStageCounter(context.Background(), func(name string, n int64) { counts[name] += n })
			ctx = upstreamcacheguard.WithStageObserver(ctx, func(name string, elapsed time.Duration) { timings[name] += elapsed })
			query := mock.ExpectQuery(regexp.QuoteMeta(upstreamCacheGuardActiveRuntimeRowsSQL))
			payload := []byte(`{"private_session":"must_not_be_a_metric"}`)
			if failure == "query" {
				query.WillReturnError(errors.New("query failed"))
			} else {
				rows := sqlmock.NewRows([]string{"group_id", "state_kind", "state_key", "version", "state_json", "updated_at"}).
					AddRow(7, "session", "private-session-key", 2, payload, time.Now())
				if failure == "read" {
					rows.AddRow(7, "session", "other-private-key", "invalid-version", payload, time.Now())
				}
				query.WillReturnRows(rows).RowsWillBeClosed()
			}
			r := &upstreamCacheGuardRepository{db: db}
			rows, err := r.ActiveRuntimeRows(ctx, []upstreamcacheguard.SessionIdentity{{GroupID: 7, Key: "private-session-key", Version: 1}}, time.Now().Add(-30*time.Minute))
			if failure == "" {
				require.NoError(t, err)
				require.Len(t, rows, 1)
			} else {
				require.Error(t, err)
			}
			require.EqualValues(t, 1, counts["active_runtime_resident_sessions"])
			if failure == "query" {
				require.Zero(t, counts["active_runtime_returned_rows"])
				require.Zero(t, counts["active_runtime_payload_bytes"])
				require.NotContains(t, timings, "active_runtime_read")
			} else {
				require.EqualValues(t, 1, counts["active_runtime_returned_rows"])
				require.EqualValues(t, len(payload), counts["active_runtime_payload_bytes"])
				require.Contains(t, timings, "active_runtime_read")
			}
			require.Len(t, counts, 3, "diagnostics contain fixed numeric counters, not row identities")
			require.Contains(t, timings, "active_runtime_query")
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
