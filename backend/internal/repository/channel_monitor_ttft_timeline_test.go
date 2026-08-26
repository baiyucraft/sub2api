//go:build unit

package repository

import (
	"context"
	"regexp"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestListRecentHistoryForMonitorsScansTTFTInProjectedOrder(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	checkedAt := time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT monitor_id, status, latency_ms, ttft_ms, ping_latency_ms, checked_at")).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), 60).
		WillReturnRows(sqlmock.NewRows([]string{"monitor_id", "status", "latency_ms", "ttft_ms", "ping_latency_ms", "checked_at"}).
			AddRow(int64(7), "operational", int64(1200), int64(180), int64(20), checkedAt))

	r := &channelMonitorRepository{db: db}
	got, err := r.ListRecentHistoryForMonitors(context.Background(), []int64{7}, map[int64]string{7: "gpt"}, 60)
	require.NoError(t, err)
	require.Len(t, got[7], 1)
	require.NotNil(t, got[7][0].TTFTMs)
	require.Equal(t, 180, *got[7][0].TTFTMs)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestInt64PtrToIntValueNormalizesNonPositiveTTFT(t *testing.T) {
	zero := int64(0)
	negative := int64(-1)
	require.Nil(t, int64PtrToIntValue(&zero))
	require.Nil(t, int64PtrToIntValue(&negative))
	positive := int64(3)
	require.Equal(t, 3, *int64PtrToIntValue(&positive))
}
