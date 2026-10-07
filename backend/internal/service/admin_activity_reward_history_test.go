package service

import (
	"context"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/stretchr/testify/require"
)

func TestActivityRewardHistoryStableMixedPagination(t *testing.T) {
	at := time.Date(2026, 10, 7, 4, 0, 0, 0, time.UTC)
	items := []UserBalanceHistoryRecord{
		{ID: 1, RecordSource: "redeem_code", SourceID: 1, CreatedAt: at},
		{ID: -1, RecordSource: "affiliate_ledger", SourceID: 1, CreatedAt: at},
		{ID: 1, RecordSource: "activity_reward", SourceID: 1, CreatedAt: at},
		{ID: 2, RecordSource: "redeem_code", SourceID: 2, CreatedAt: at},
	}
	first := paginateUserBalanceHistory(items, pagination.PaginationParams{Page: 1, PageSize: 2})
	second := paginateUserBalanceHistory(items, pagination.PaginationParams{Page: 2, PageSize: 2})
	require.Equal(t, "activity_reward", first[0].RecordSource)
	require.Equal(t, "affiliate_ledger", first[1].RecordSource)
	require.Equal(t, int64(2), second[0].SourceID)
	require.Equal(t, int64(1), second[1].SourceID)
	require.Len(t, paginateUserBalanceHistory(items, pagination.PaginationParams{Page: 3, PageSize: 2}), 0)
}

type activityHistoryRedeemStub struct {
	RedeemCodeRepository
	items []RedeemCode
}

func (activityHistoryRedeemStub) SumPositiveBalanceByUser(context.Context, int64) (float64, error) {
	return 100, nil
}

func (s activityHistoryRedeemStub) ListByUserPaginated(_ context.Context, _ int64, params pagination.PaginationParams, _ string) ([]RedeemCode, *pagination.PaginationResult, error) {
	start := min(params.Offset(), len(s.items))
	end := min(start+params.Limit(), len(s.items))
	return s.items[start:end], &pagination.PaginationResult{Total: int64(len(s.items))}, nil
}

func TestActivityRewardHistoryFilterKeepsLifetimeTotalsAndZeroRewards(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	svc := &adminServiceImpl{entClient: client, redeemCodeRepo: activityHistoryRedeemStub{}}
	at := time.Date(2026, 10, 7, 4, 0, 0, 0, time.UTC)
	mock.ExpectQuery(`(?s)SELECT COUNT\(\*\), COALESCE\(SUM\(amount\), ?0\).*activity_reward_records.*status ?= ?'credited'`).
		WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"count", "sum"}).AddRow(3, 1.25))
	mock.ExpectQuery(`(?s)SELECT id, ?activity_type, ?amount.*activity_reward_records.*ORDER BY created_at DESC, id DESC`).
		WithArgs(int64(7), 2, 0).WillReturnRows(sqlmock.NewRows([]string{"id", "activity_type", "amount", "period_date", "source", "created_at"}).
		AddRow(3, "daily_gift", 0, "2026-10-07", "daily_recharge", at).
		AddRow(2, "spend_draw", .75, nil, "spend_draw", at.Add(-time.Second)))
	page, err := svc.GetUserBalanceHistory(context.Background(), 7, 1, 2, "activity_reward")
	require.NoError(t, err)
	require.Equal(t, int64(3), page.Total)
	require.Equal(t, 100.0, page.TotalRecharged)
	require.Equal(t, 1.25, page.TotalRewarded)
	require.Len(t, page.Items, 2)
	require.Zero(t, page.Items[0].Value)
	require.Equal(t, "activity_reward", page.Items[0].Type)
	require.Equal(t, "daily_gift", page.Items[0].ActivityType)
	require.Empty(t, page.Items[0].Code)
	require.Equal(t, "2026-10-07", *page.Items[0].PeriodDate)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestActivityRewardHistoryLegacyProjectionKeepsRechargeFields(t *testing.T) {
	at := time.Now()
	code := RedeemCode{ID: -9, Type: RedeemTypeAffiliateBalance, Code: "AFF-9", Value: 1.5, UsedAt: &at, Notes: "transfer"}
	item := userBalanceHistoryFromRedeem(code)
	require.Equal(t, "affiliate_ledger", item.RecordSource)
	require.Equal(t, int64(9), item.SourceID)
	require.Equal(t, int64(-9), item.ID)
	require.Equal(t, code.Code, item.Code)
	require.Equal(t, code.Notes, item.Notes)
	require.Equal(t, at, *item.UsedAt)
	require.Empty(t, item.ActivityType)
}

func TestActivityRewardHistoryAllSourcesAreMergedBeforePage(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	at := time.Date(2026, 10, 7, 4, 0, 0, 0, time.UTC)
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	svc := &adminServiceImpl{entClient: client, redeemCodeRepo: activityHistoryRedeemStub{items: []RedeemCode{
		{ID: 2, Type: RedeemTypeBalance, Value: 60, UsedAt: &at},
		{ID: 1, Type: RedeemTypeBalance, Value: 40, UsedAt: &at},
	}}}
	mock.ExpectQuery(`(?s)SELECT COUNT\(\*\), COALESCE\(SUM\(amount\),0\).*activity_reward_records`).
		WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"count", "sum"}).AddRow(2, .75))
	mock.ExpectQuery(`(?s)FROM user_affiliate_ledger.*action = 'transfer'.*ORDER BY created_at DESC, id DESC`).
		WithArgs(int64(7), 0, 1000).WillReturnRows(sqlmock.NewRows([]string{"id", "amount", "created_at"}).AddRow(2, 5, at).AddRow(1, 3, at))
	mock.ExpectQuery(`(?s)SELECT COUNT\(\*\).*FROM user_affiliate_ledger`).
		WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
	mock.ExpectQuery(`(?s)FROM activity_reward_records.*status='credited'.*ORDER BY created_at DESC, id DESC`).
		WithArgs(int64(7), 4, 0).WillReturnRows(sqlmock.NewRows([]string{"id", "activity_type", "amount", "period_date", "source", "created_at"}).
		AddRow(2, "spend_draw", .75, nil, "spend_draw", at).AddRow(1, "daily_gift", 0, "2026-10-07", "daily_recharge", at))
	page, err := svc.GetUserBalanceHistory(context.Background(), 7, 2, 2, "")
	require.NoError(t, err)
	require.Equal(t, int64(6), page.Total)
	require.Equal(t, 100.0, page.TotalRecharged)
	require.Equal(t, .75, page.TotalRewarded)
	require.Len(t, page.Items, 2)
	require.Equal(t, "affiliate_ledger", page.Items[0].RecordSource)
	require.Equal(t, int64(2), page.Items[0].SourceID)
	require.Equal(t, int64(1), page.Items[1].SourceID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestActivityRewardHistoryLegacyFilterStillReturnsRewardTotal(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	svc := &adminServiceImpl{entClient: dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db))),
		redeemCodeRepo: activityHistoryRedeemStub{items: []RedeemCode{{ID: 1, Type: RedeemTypeBalance, Value: 100}}}}
	mock.ExpectQuery(`(?s)SELECT COUNT\(\*\), COALESCE\(SUM\(amount\),0\).*status='credited'`).
		WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"count", "sum"}).AddRow(7, 3.5))
	page, err := svc.GetUserBalanceHistory(context.Background(), 7, 1, 1, "balance")
	require.NoError(t, err)
	require.Equal(t, int64(1), page.Total)
	require.Equal(t, 3.5, page.TotalRewarded)
	require.Equal(t, 100.0, page.TotalRecharged)
	require.Equal(t, "redeem_code", page.Items[0].RecordSource)
	require.NoError(t, mock.ExpectationsWereMet())
}
