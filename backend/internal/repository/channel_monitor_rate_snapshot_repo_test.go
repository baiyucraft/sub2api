package repository

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestChannelMonitorRepository_ListGroupRateSnapshots(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	from := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	until := from.Add(24 * time.Hour)
	observed := from.Add(-48 * time.Hour)
	effective := from.Add(-time.Hour)
	mock.ExpectQuery(regexp.QuoteMeta(listGroupRateSnapshotsSQL)).
		WithArgs(sqlmock.AnyArg(), from, until).
		WillReturnRows(sqlmock.NewRows([]string{
			"group_id", "rate_multiplier", "peak_rate_enabled", "peak_start",
			"peak_end", "peak_rate_multiplier", "timezone", "effective_at",
			"observed_since",
		}).AddRow(
			int64(7), 1.25, true, "10:00", "12:00", 1.5, "Asia/Shanghai",
			effective, observed,
		))

	repo := &channelMonitorRepository{db: db}
	seriesByGroup, err := repo.ListGroupRateSnapshots(context.Background(), []int64{7}, from, until)
	require.NoError(t, err)
	require.Equal(t, observed, seriesByGroup[7].ObservedSince)
	require.Len(t, seriesByGroup[7].Snapshots, 1)
	require.Equal(t, 1.25, seriesByGroup[7].Snapshots[0].RateMultiplier)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestChannelMonitorRepository_ListGroupRateSnapshots_EmptyIDs(t *testing.T) {
	repo := &channelMonitorRepository{}
	got, err := repo.ListGroupRateSnapshots(context.Background(), nil, time.Time{}, time.Now())
	require.NoError(t, err)
	require.Empty(t, got)
}
