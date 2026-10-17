package repository

import (
	"context"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/stretchr/testify/require"
)

func TestUpstreamHealthHistoryOnlyMissingKeysReadLegacyExtra(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(map[bool]string{false: "all-persisted", true: "legacy-fallback"}[missing], func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			repo := &upstreamConfigRepository{client: dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))}
			columns := []string{"id", "upstream_config_id", "upstream_key_id", "account_id", "platform", "model", "protocol", "source", "state", "result", "reason", "http_status", "ttft_ms", "duration_ms", "input_tokens", "output_tokens", "output_tps", "confidence_score", "confidence_prompt_version", "requested_effort", "reasoning_tokens", "confidence_checks", "confidence_status", "confidence_evidence", "observed_at"}
			now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
			rows := sqlmock.NewRows(columns).AddRow(9, 11, 1, nil, "openai", "", "", "probe", "healthy", "success", "", nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, now)
			ids := []int64{1}
			args := "{1}"
			if missing {
				ids = []int64{1, 2}
				args = "{1,2}"
			}
			mock.ExpectQuery(`FROM unnest\(\$1::bigint\[\]\).*CROSS JOIN LATERAL.*ORDER BY o.observed_at DESC, o.id DESC LIMIT \$2`).WithArgs(args, 24).WillReturnRows(rows)
			if missing {
				mock.ExpectQuery(`SELECT .* FROM "upstream_keys" WHERE .*id.* IN`).WithArgs(int64(2)).WillReturnRows(sqlmock.NewRows([]string{"id", "extra"}).AddRow(2, `{"health_history":[{"observed_at":"2026-10-17T11:00:00Z","state":"degraded","source":"probe"}]}`))
			}
			got, err := repo.ListKeyHealthHistories(context.Background(), ids, 24)
			require.NoError(t, err)
			require.Len(t, got[1], 1)
			require.Equal(t, int64(9), got[1][0].ID)
			if missing {
				require.Len(t, got[2], 1)
			}
			require.NoError(t, mock.ExpectationsWereMet(), "complete persisted pages never read legacy JSON")
		})
	}
}
