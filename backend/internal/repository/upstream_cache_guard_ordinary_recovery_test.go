package repository

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/upstreamcacheguard"
	"github.com/stretchr/testify/require"
)

func TestCacheGuardRecoveryFactsSingleReadSnapshot(t *testing.T) {
	at := time.Date(2026, 10, 26, 2, 0, 0, 0, time.UTC)
	for _, scenario := range []struct {
		name                              string
		gap, empty, malformed, queryError bool
	}{
		{name: "processed history with delayed account completion", gap: true},
		{name: "processed history without gap"},
		{name: "empty history retains gap", gap: true, empty: true},
		{name: "empty healthy history", empty: true},
		{name: "malformed evidence", malformed: true},
		{name: "database error", queryError: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer db.Close()
			repo := NewUpstreamCacheGuardRepository(db).(*upstreamCacheGuardRepository)
			mock.ExpectBegin()
			mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WithArgs(int64(2)).WillReturnResult(sqlmock.NewResult(0, 1))
			query := mock.ExpectQuery(`(?s)WITH history AS .*processed=TRUE.*LIMIT \$4.*health AS .*account_id=\$1 AND upstream_key_id=\$2 AND processed=FALSE AND completed_at<\$5.*LEFT JOIN history`).WithArgs(int64(2), int64(5), at.Add(-time.Hour), upstreamcacheguard.MaxFacts+1, at.Add(-5*time.Second))
			if scenario.queryError {
				query.WillReturnError(errors.New("synthetic read failure"))
			} else {
				rows := sqlmock.NewRows([]string{"evidence_json", "pending_gap"})
				switch {
				case scenario.empty:
					rows.AddRow(nil, scenario.gap)
				case scenario.malformed:
					rows.AddRow([]byte("{"), false)
				default:
					fact := upstreamcacheguard.Evidence{ID: "processed", Request: upstreamcacheguard.Request{AccountID: 2, UpstreamKeyID: 5}, CompletedAt: at}
					raw, marshalErr := json.Marshal(fact)
					require.NoError(t, marshalErr)
					rows.AddRow(raw, scenario.gap)
				}
				query.WillReturnRows(rows)
			}
			failed := scenario.queryError || scenario.malformed
			if failed {
				mock.ExpectRollback()
			} else {
				mock.ExpectCommit()
			}
			err = repo.TransactAccount(context.Background(), 9, 2, 5, func(tx upstreamcacheguard.Tx) error {
				view, readErr := tx.(upstreamcacheguard.RecoveryFactsTx).AccountRecoveryFacts(2, 5, at.Add(-time.Hour), at)
				if readErr != nil {
					return readErr
				}
				require.Equal(t, scenario.gap, view.HasPendingGap)
				if scenario.empty {
					require.Empty(t, view.Facts)
				} else {
					require.Len(t, view.Facts, 1)
					require.Equal(t, "processed", view.Facts[0].ID)
				}
				return nil
			})
			require.Equal(t, failed, err != nil)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestCacheGuardRecoveryFactsRejectsCrossIdentity(t *testing.T) {
	tx := &upstreamCacheGuardTx{accountID: 2, keyID: 5}
	for _, identity := range [][2]int64{{3, 5}, {2, 6}} {
		_, err := tx.AccountRecoveryFacts(identity[0], identity[1], time.Now(), time.Now())
		require.ErrorContains(t, err, "identity changed")
	}
}
