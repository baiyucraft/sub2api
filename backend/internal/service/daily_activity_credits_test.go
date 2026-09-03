//go:build unit

package service

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestDailyActivityCumulativeRechargeCreditsGroupsByShanghaiDay(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	svc := NewDailyActivityService(db, nil)
	start := time.Date(2026, time.September, 2, 16, 0, 0, 0, time.UTC)
	end := time.Date(2026, time.September, 4, 16, 0, 0, 0, time.UTC)
	query := regexp.QuoteMeta("AT TIME ZONE 'Asia/Shanghai'")
	mock.ExpectQuery("(?s)SELECT COALESCE\\(SUM\\(FLOOR\\(daily.amount / \\$4\\)\\).*"+query).
		WithArgs(int64(7), start, end, 50.0).
		WillReturnRows(sqlmock.NewRows([]string{"credits"}).AddRow(int64(2)))

	credits, err := svc.cumulativeRechargeDrawCredits(context.Background(), db, 7, start, end, 50)
	require.NoError(t, err)
	require.Equal(t, int64(2), credits)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDailyActivityCumulativeSpendCreditsUsesNonNegativeCostAndShanghaiDays(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	svc := NewDailyActivityService(db, nil)
	start := time.Date(2026, time.September, 2, 16, 0, 0, 0, time.UTC)
	end := time.Date(2026, time.September, 3, 16, 0, 0, 0, time.UTC)
	mock.ExpectQuery("(?s)SUM\\(GREATEST\\(actual_cost, 0\\)\\).*AT TIME ZONE 'Asia/Shanghai'").
		WithArgs(int64(7), start, end, 50.0).
		WillReturnRows(sqlmock.NewRows([]string{"credits"}).AddRow(int64(1)))

	credits, err := svc.cumulativeSpendDrawCredits(context.Background(), db, 7, start, end, 50)
	require.NoError(t, err)
	require.Equal(t, int64(1), credits)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDailyActivityCumulativeCreditsDoNotPoolAmountsAcrossDays(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	svc := NewDailyActivityService(db, nil)
	start := time.Date(2026, time.September, 2, 16, 0, 0, 0, time.UTC)
	end := time.Date(2026, time.September, 4, 16, 0, 0, 0, time.UTC)
	mock.ExpectQuery("(?s)SELECT COALESCE\\(SUM\\(FLOOR\\(daily.amount / \\$4\\)\\)").
		WithArgs(int64(7), start, end, 50.0).
		WillReturnRows(sqlmock.NewRows([]string{"credits"}).AddRow(int64(0)))

	credits, err := svc.cumulativeRechargeDrawCredits(context.Background(), db, 7, start, end, 50)
	require.NoError(t, err)
	require.Equal(t, int64(0), credits)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDailyActivitySyncCreditsUsesLifetimeIssuedCountAndStableIndexes(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	now := time.Date(2026, time.September, 4, 4, 0, 0, 0, time.UTC)
	startedAt := time.Date(2026, time.September, 3, 16, 0, 0, 0, time.UTC)
	end := time.Date(2026, time.September, 4, 16, 0, 0, 0, time.UTC)
	svc := NewDailyActivityService(db, nil)

	mock.ExpectBegin()
	mock.ExpectQuery("(?s)SELECT COALESCE\\(SUM\\(FLOOR\\(daily.amount / \\$4\\)\\)").
		WithArgs(int64(7), startedAt, end, DefaultDailyActivityConfig().RechargeDrawThreshold).
		WillReturnRows(sqlmock.NewRows([]string{"credits"}).AddRow(int64(2)))
	mock.ExpectQuery("(?s)SUM\\(GREATEST\\(actual_cost, 0\\)\\).*AT TIME ZONE 'Asia/Shanghai'").
		WithArgs(int64(7), startedAt, end, DefaultDailyActivityConfig().ConsumptionDrawThreshold).
		WillReturnRows(sqlmock.NewRows([]string{"credits"}).AddRow(int64(1)))
	mock.ExpectQuery("SELECT FLOOR\\(COUNT\\(\\*\\)/\\$2\\) FROM activity_invitation_milestones").
		WithArgs(int64(7), DefaultDailyActivityConfig().InviteDrawRequiredCount).
		WillReturnRows(sqlmock.NewRows([]string{"credits"}).AddRow(int64(1)))
	mock.ExpectQuery("SELECT id FROM users WHERE id=\\$1 FOR UPDATE").
		WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(7)))

	mock.ExpectQuery("SELECT COUNT\\(\\*\\), COALESCE\\(MAX\\(credit_index\\)\\+1,0\\) FROM activity_draw_credits").
		WithArgs(int64(7), activityRechargeDraw).
		WillReturnRows(sqlmock.NewRows([]string{"count", "next"}).AddRow(int64(1), int64(3)))
	mock.ExpectExec("INSERT INTO activity_draw_credits").
		WithArgs(int64(7), activityRechargeDraw, int64(3)).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery("SELECT COUNT\\(\\*\\), COALESCE\\(MAX\\(credit_index\\)\\+1,0\\) FROM activity_draw_credits").
		WithArgs(int64(7), activitySpendDraw).
		WillReturnRows(sqlmock.NewRows([]string{"count", "next"}).AddRow(int64(1), int64(1)))
	mock.ExpectQuery("SELECT COUNT\\(\\*\\), COALESCE\\(MAX\\(credit_index\\)\\+1,0\\) FROM activity_draw_credits").
		WithArgs(int64(7), activityInviteDraw).
		WillReturnRows(sqlmock.NewRows([]string{"count", "next"}).AddRow(int64(0), int64(0)))
	mock.ExpectExec("INSERT INTO activity_draw_credits").
		WithArgs(int64(7), activityInviteDraw, int64(0)).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	require.NoError(t, svc.SyncCredits(context.Background(), 7, now))
	require.NoError(t, mock.ExpectationsWereMet())
}
