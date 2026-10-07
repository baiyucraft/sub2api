//go:build unit

package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

// TestDailyActivityGiftCostFailureRollsBackAward verifies the monetary ledger
// cannot commit a credited gift when its cost entry failed.
func TestDailyActivityGiftCostFailureRollsBackAward(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	svc := NewDailyActivityService(db, nil)
	caches := &activityCostBalanceCache{}
	svc.SetBillingCache(caches)
	costInvalidations := 0
	svc.SetCostCacheInvalidators(func() { costInvalidations++ })
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT COALESCE\\(SUM\\(amount\\),0\\)").WillReturnRows(sqlmock.NewRows([]string{"amount"}).AddRow(20))
	mock.ExpectQuery("INSERT INTO activity_reward_records").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(81))
	mock.ExpectExec("UPDATE users SET balance").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT id,activity_type,amount,period_date::text,source,created_at").WillReturnRows(sqlmock.NewRows([]string{"id", "activity_type", "amount", "period_date", "source", "created_at"}).AddRow(81, "daily_gift", .25, "2026-10-07", "daily_recharge", time.Now()))
	costErr := errors.New("cost write unavailable")
	mock.ExpectQuery("INSERT INTO extra_cost_entries").WithArgs(int64(81), int64(7)).WillReturnError(costErr)
	mock.ExpectRollback()
	_, err = svc.OpenDailyGift(context.Background(), 7, "", time.Date(2026, 10, 7, 4, 0, 0, 0, time.UTC))
	require.ErrorIs(t, err, costErr)
	require.Zero(t, caches.calls)
	require.Zero(t, costInvalidations)
	require.NoError(t, mock.ExpectationsWereMet())
}

type activityCostBalanceCache struct{ calls int }

func (c *activityCostBalanceCache) InvalidateUserBalance(context.Context, int64) error {
	c.calls++
	return nil
}

func rewardCostRows(id int64, typ string, createdAt time.Time) *sqlmock.Rows {
	return sqlmock.NewRows([]string{"id", "activity_type", "amount", "period_date", "source", "created_at"}).AddRow(id, typ, 0, nil, typ, createdAt)
}

func expectCostCreditSync(mock sqlmock.Sqlmock) {
	mock.ExpectBegin()
	mock.ExpectQuery("(?s)SELECT COALESCE\\(SUM\\(FLOOR\\(daily.amount / \\$4\\)\\)").WillReturnRows(sqlmock.NewRows([]string{"credits"}).AddRow(0))
	mock.ExpectQuery("(?s)SUM\\(GREATEST\\(actual_cost, 0\\)\\)").WillReturnRows(sqlmock.NewRows([]string{"credits"}).AddRow(0))
	mock.ExpectQuery("SELECT aff_rebate_rate_percent IS NOT NULL").WillReturnRows(sqlmock.NewRows([]string{"exclusive"}).AddRow(true))
	mock.ExpectQuery("SELECT id FROM users WHERE id=\\$1 FOR UPDATE").WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(7))
	for _, typ := range []string{activityRechargeDraw, activitySpendDraw} {
		mock.ExpectQuery("SELECT COUNT\\(\\*\\), COALESCE\\(MAX\\(credit_index\\)\\+1,0\\)").WithArgs(int64(7), typ).WillReturnRows(sqlmock.NewRows([]string{"count", "next"}).AddRow(0, 0))
	}
	mock.ExpectCommit()
}

func TestDailyActivityGiftCostCommitsAndInvalidatesCaches(t *testing.T) {
	for _, replay := range []bool{false, true} {
		t.Run(fmt.Sprint(replay), func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer db.Close()
			svc := NewDailyActivityService(db, nil)
			cache := &activityCostBalanceCache{}
			svc.SetBillingCache(cache)
			invalidations := 0
			svc.SetCostCacheInvalidators(func() { require.NoError(t, mock.ExpectationsWereMet()); invalidations++ }, nil)
			mock.ExpectBegin()
			mock.ExpectQuery("SELECT COALESCE\\(SUM\\(amount\\),0\\)").WillReturnRows(sqlmock.NewRows([]string{"amount"}).AddRow(20))
			if replay {
				mock.ExpectQuery("SELECT id,activity_type,amount,period_date::text,source,created_at").WillReturnRows(rewardCostRows(81, activityDailyGift, time.Date(2026, 10, 6, 16, 0, 0, 0, time.UTC)))
			} else {
				mock.ExpectQuery("SELECT id,activity_type,amount,period_date::text,source,created_at").WillReturnError(sql.ErrNoRows)
				mock.ExpectQuery("INSERT INTO activity_reward_records").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(81))
				mock.ExpectExec("UPDATE users SET balance").WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectQuery("SELECT id,activity_type,amount,period_date::text,source,created_at").WillReturnRows(rewardCostRows(81, activityDailyGift, time.Date(2026, 10, 6, 16, 0, 0, 0, time.UTC)))
			}
			mock.ExpectQuery(regexp.QuoteMeta(activityRewardCostSQL)).WithArgs(int64(81), int64(7)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(900))
			mock.ExpectCommit()
			reward, err := svc.OpenDailyGift(context.Background(), 7, "gift-key", time.Date(2026, 10, 7, 4, 0, 0, 0, time.UTC))
			require.NoError(t, err)
			require.Equal(t, int64(81), reward.ID)
			require.Equal(t, 1, invalidations)
			require.Equal(t, 1, cache.calls)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestDailyActivityDrawCostsShareTransactionForEveryType(t *testing.T) {
	for _, typ := range []string{activityRechargeDraw, activitySpendDraw, activityInviteDraw} {
		for _, failSecondCost := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/fail=%t", typ, failSecondCost), func(t *testing.T) {
				db, mock, err := sqlmock.New()
				require.NoError(t, err)
				defer db.Close()
				svc := NewDailyActivityService(db, nil)
				cache := &activityCostBalanceCache{}
				svc.SetBillingCache(cache)
				invalidations := 0
				svc.SetCostCacheInvalidators(func() { require.NoError(t, mock.ExpectationsWereMet()); invalidations++ })
				expectCostCreditSync(mock)
				mock.ExpectBegin()
				mock.ExpectQuery("SELECT id FROM activity_draw_credits").WithArgs(int64(7), typ, 2).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(101).AddRow(102))
				costErr := errors.New("second cost unavailable")
				for i := int64(0); i < 2; i++ {
					mock.ExpectQuery("INSERT INTO activity_reward_records").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(81 + i))
					mock.ExpectExec("UPDATE activity_draw_credits SET consumed_at").WithArgs(101 + i).WillReturnResult(sqlmock.NewResult(0, 1))
					mock.ExpectQuery("SELECT id,activity_type,amount,period_date::text,source,created_at").WithArgs(81 + i).WillReturnRows(rewardCostRows(81+i, typ, time.Now()))
					cost := mock.ExpectQuery(regexp.QuoteMeta(activityRewardCostSQL)).WithArgs(81+i, int64(7))
					if failSecondCost && i == 1 {
						cost.WillReturnError(costErr)
					} else {
						cost.WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(900 + i))
					}
				}
				if failSecondCost {
					mock.ExpectRollback()
				} else {
					mock.ExpectExec("UPDATE users SET balance").WithArgs(sqlmock.AnyArg(), int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
					mock.ExpectCommit()
				}
				rewards, err := svc.Draw(context.Background(), 7, typ, 2, "", time.Date(2026, 10, 7, 4, 0, 0, 0, time.UTC))
				if failSecondCost {
					require.ErrorIs(t, err, costErr)
					require.Zero(t, invalidations)
					require.Zero(t, cache.calls)
				} else {
					require.NoError(t, err)
					require.Len(t, rewards, 2)
					require.Equal(t, 1, invalidations)
					require.Equal(t, 1, cache.calls)
				}
				require.NoError(t, mock.ExpectationsWereMet())
			})
		}
	}
}

func TestActivityRewardCostRejectsConflictingOrUncreditedSource(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(activityRewardCostSQL)).WithArgs(int64(81), int64(7)).WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()
	tx, err := db.Begin()
	require.NoError(t, err)
	require.ErrorIs(t, recordActivityRewardCost(context.Background(), tx, 81, 7), ErrExtraCostIdempotencyConflict)
	require.NoError(t, tx.Rollback())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestActivityRewardCostUsesAuthoritativeNumericAndShanghaiOccurrenceDate(t *testing.T) {
	require.Contains(t, activityRewardCostSQL, "SELECT (r.created_at AT TIME ZONE 'Asia/Shanghai')::date, r.amount")
	require.Contains(t, activityRewardCostSQL, "r.status = 'credited'")
	require.Contains(t, activityRewardCostSQL, "NULL, r.created_at, NULL")
	require.NotContains(t, activityRewardCostSQL, "period_date")
}

func TestActivityRewardCostsBackfillReportsInsertedCount(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT backfill_activity_reward_costs()`)).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(3))
	count, err := BackfillActivityRewardCosts(context.Background(), db)
	require.NoError(t, err)
	require.Equal(t, int64(3), count)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDailyActivityGiftMissingBalanceRecipientRollsBack(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	svc := NewDailyActivityService(db, nil)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT COALESCE\\(SUM\\(amount\\),0\\)").WillReturnRows(sqlmock.NewRows([]string{"amount"}).AddRow(20))
	mock.ExpectQuery("INSERT INTO activity_reward_records").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(81))
	mock.ExpectExec("UPDATE users SET balance").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()
	_, err = svc.OpenDailyGift(context.Background(), 7, "", time.Now())
	require.ErrorIs(t, err, ErrUserNotFound)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDailyActivityDrawMissingBalanceRecipientRollsBack(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	svc := NewDailyActivityService(db, nil)
	cache := &activityCostBalanceCache{}
	svc.SetBillingCache(cache)
	invalidations := 0
	svc.SetCostCacheInvalidators(func() { invalidations++ })
	expectCostCreditSync(mock)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id FROM activity_draw_credits").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(101))
	mock.ExpectQuery("INSERT INTO activity_reward_records").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(81))
	mock.ExpectExec("UPDATE activity_draw_credits SET consumed_at").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT id,activity_type,amount,period_date::text,source,created_at").WillReturnRows(rewardCostRows(81, activityRechargeDraw, time.Now()))
	mock.ExpectQuery(regexp.QuoteMeta(activityRewardCostSQL)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(900))
	mock.ExpectExec("UPDATE users SET balance").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()
	_, err = svc.Draw(context.Background(), 7, activityRechargeDraw, 1, "", time.Now())
	require.ErrorIs(t, err, ErrUserNotFound)
	require.Zero(t, cache.calls)
	require.Zero(t, invalidations)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDailyActivityDrawReplayValidatesCostsWithoutConsumingAgain(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	svc := NewDailyActivityService(db, nil)
	expectCostCreditSync(mock)
	mock.ExpectBegin()
	mock.ExpectQuery("INSERT INTO activity_draw_requests").WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("SELECT id,count FROM activity_draw_requests").WillReturnRows(sqlmock.NewRows([]string{"id", "count"}).AddRow(53, 2))
	mock.ExpectQuery("SELECT id,activity_type,amount,period_date::text,source,created_at FROM activity_reward_records").
		WithArgs(int64(7), "53").
		WillReturnRows(rewardCostRows(81, activityRechargeDraw, time.Now()).AddRow(82, activityRechargeDraw, 0, nil, activityRechargeDraw, time.Now()))
	for _, rewardID := range []int64{81, 82} {
		mock.ExpectQuery(regexp.QuoteMeta(activityRewardCostSQL)).WithArgs(rewardID, int64(7)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(900 + rewardID))
	}
	mock.ExpectCommit()
	rewards, err := svc.Draw(context.Background(), 7, activityRechargeDraw, 2, "replay", time.Now())
	require.NoError(t, err)
	require.Len(t, rewards, 2)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDailyActivityGiftCommitFailureDoesNotInvalidateCaches(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	svc := NewDailyActivityService(db, nil)
	cache := &activityCostBalanceCache{}
	svc.SetBillingCache(cache)
	invalidations := 0
	svc.SetCostCacheInvalidators(func() { invalidations++ })
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT COALESCE\\(SUM\\(amount\\),0\\)").WillReturnRows(sqlmock.NewRows([]string{"amount"}).AddRow(20))
	mock.ExpectQuery("INSERT INTO activity_reward_records").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(81))
	mock.ExpectExec("UPDATE users SET balance").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT id,activity_type,amount,period_date::text,source,created_at").WillReturnRows(rewardCostRows(81, activityDailyGift, time.Now()))
	mock.ExpectQuery(regexp.QuoteMeta(activityRewardCostSQL)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(900))
	commitErr := errors.New("commit unavailable")
	mock.ExpectCommit().WillReturnError(commitErr)
	_, err = svc.OpenDailyGift(context.Background(), 7, "", time.Now())
	require.ErrorIs(t, err, commitErr)
	require.Zero(t, cache.calls)
	require.Zero(t, invalidations)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDailyActivityDrawCreditCursorFailureRollsBack(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	svc := NewDailyActivityService(db, nil)
	expectCostCreditSync(mock)
	mock.ExpectBegin()
	cursorErr := errors.New("credit cursor unavailable")
	mock.ExpectQuery("SELECT id FROM activity_draw_credits").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(101).AddRow(102).RowError(1, cursorErr))
	mock.ExpectRollback()
	_, err = svc.Draw(context.Background(), 7, activityRechargeDraw, 2, "", time.Now())
	require.ErrorIs(t, err, cursorErr)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDailyActivityGiftConcurrentIdempotencyRaceReplaysWinner(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	svc := NewDailyActivityService(db, nil)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT COALESCE\\(SUM\\(amount\\),0\\)").WillReturnRows(sqlmock.NewRows([]string{"amount"}).AddRow(20))
	mock.ExpectQuery("SELECT id,activity_type,amount,period_date::text,source,created_at").WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("INSERT INTO activity_reward_records").WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("SELECT id,activity_type,amount,period_date::text,source,created_at").WillReturnRows(rewardCostRows(81, activityDailyGift, time.Now()))
	mock.ExpectQuery(regexp.QuoteMeta(activityRewardCostSQL)).WithArgs(int64(81), int64(7)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(900))
	mock.ExpectCommit()
	reward, err := svc.OpenDailyGift(context.Background(), 7, "race-winner", time.Now())
	require.NoError(t, err)
	require.Equal(t, int64(81), reward.ID)
	require.NoError(t, mock.ExpectationsWereMet())
}
