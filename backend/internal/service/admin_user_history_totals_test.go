package service

import (
	"context"
	"errors"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/stretchr/testify/require"
)

func TestBalanceHistorySelectedTypeTotalsUseFullLedger(t *testing.T) {
	for _, tt := range []struct {
		kind     string
		value    float64
		quantity bool
	}{
		{RedeemTypeBalance, 600.5, false},
		{"admin_balance", -20.25, false},
		{RedeemTypeAffiliateBalance, 15.5, false},
		{RedeemTypeConcurrency, 8, true},
		{"admin_concurrency", -3, true},
	} {
		t.Run(tt.kind, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer db.Close()
			svc := &adminServiceImpl{entClient: dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))}
			if tt.kind == RedeemTypeAffiliateBalance {
				mock.ExpectQuery(`SELECT COALESCE\(SUM\(amount\),0\) FROM user_affiliate_ledger WHERE user_id=\$1 AND action='transfer'`).
					WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"sum"}).AddRow(tt.value))
			} else {
				mock.ExpectQuery(`SELECT COALESCE\(SUM\(value\),0\) FROM redeem_codes WHERE used_by=\$1 AND type=\$2`).
					WithArgs(int64(7), tt.kind).WillReturnRows(sqlmock.NewRows([]string{"sum"}).AddRow(tt.value))
			}
			amount, quantity, err := svc.filteredBalanceHistoryTotals(context.Background(), 7, tt.kind)
			require.NoError(t, err)
			if tt.quantity {
				require.Nil(t, amount)
				require.Equal(t, tt.value, *quantity)
			} else {
				require.Nil(t, quantity)
				require.Equal(t, tt.value, *amount)
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestBalanceHistoryFilteredAmountDoesNotChangeWithPage(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	svc := &adminServiceImpl{entClient: dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db))),
		redeemCodeRepo: activityHistoryRedeemStub{items: []RedeemCode{
			{ID: 1, Type: "admin_balance", Value: 100},
			{ID: 2, Type: "admin_balance", Value: -20},
			{ID: 3, Type: "admin_balance", Value: 50},
		}}}
	for pageNo := 1; pageNo <= 4; pageNo++ {
		mock.ExpectQuery(`(?s)SELECT COUNT\(\*\), COALESCE\(SUM\(amount\),0\).*activity_reward_records`).
			WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"count", "sum"}).AddRow(2, 1.25))
		mock.ExpectQuery(`SELECT COALESCE\(SUM\(value\),0\) FROM redeem_codes WHERE used_by=\$1 AND type=\$2`).
			WithArgs(int64(7), "admin_balance").WillReturnRows(sqlmock.NewRows([]string{"sum"}).AddRow(130))
		page, err := svc.userBalanceHistory(context.Background(), 7, pagination.PaginationParams{Page: pageNo, PageSize: 1}, "admin_balance")
		require.NoError(t, err)
		require.Equal(t, int64(3), page.Total)
		require.Equal(t, 130.0, *page.FilteredAmount)
		require.Equal(t, 100.0, page.TotalRecharged)
		require.Equal(t, 1.25, page.TotalRewarded)
		if pageNo == 4 {
			require.Empty(t, page.Items)
		} else {
			require.Len(t, page.Items, 1)
		}
	}
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestBalanceHistoryTotalsNoMixedUnitsAndQueryFailure(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	svc := &adminServiceImpl{entClient: dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))}
	for _, kind := range []string{"", "subscription", "unknown"} {
		amount, quantity, err := svc.filteredBalanceHistoryTotals(context.Background(), 7, kind)
		require.NoError(t, err)
		require.Nil(t, amount)
		require.Nil(t, quantity)
	}
	queryErr := errors.New("summary unavailable")
	mock.ExpectQuery(`SELECT COALESCE\(SUM\(value\),0\) FROM redeem_codes`).WithArgs(int64(7), RedeemTypeBalance).WillReturnError(queryErr)
	amount, quantity, err := svc.filteredBalanceHistoryTotals(context.Background(), 7, RedeemTypeBalance)
	require.ErrorIs(t, err, queryErr)
	require.Nil(t, amount)
	require.Nil(t, quantity)
	mock.ExpectQuery(`SELECT COALESCE\(SUM\(value\),0\) FROM redeem_codes`).WithArgs(int64(7), RedeemTypeBalance).
		WillReturnRows(sqlmock.NewRows([]string{"sum"}).AddRow(0))
	amount, _, err = svc.filteredBalanceHistoryTotals(context.Background(), 7, RedeemTypeBalance)
	require.NoError(t, err)
	require.NotNil(t, amount)
	require.Zero(t, *amount)
	require.NoError(t, mock.ExpectationsWereMet())
}
