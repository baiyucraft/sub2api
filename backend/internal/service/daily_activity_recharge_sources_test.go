//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestDailyActivityRechargeSourcesMatchAffiliateQualification(t *testing.T) {
	svc := &DailyActivityService{}
	query := svc.activityRechargeSourcesSQL("$1", "$2", "$3")

	require.Contains(t, query, "po.order_type IN ('balance','subscription')")
	require.Contains(t, query, "po.status='COMPLETED'")
	require.Contains(t, query, "rc.type='balance'")
	require.Contains(t, query, "rc.value > 0")
	require.Contains(t, query, "po.recharge_code=rc.code")
	require.Contains(t, query, "activity_recharge_events")
	require.Contains(t, query, "source_type='admin_balance_add'")
	require.Contains(t, query, "po.paid_at < $3")
	require.Contains(t, query, "rc.used_at < $3")
	require.Contains(t, query, "are.occurred_at < $3")
	require.NotContains(t, query, "rc.type='admin_balance'")
}

func TestDailyActivityRechargeAmountUsesUnifiedSources(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	svc := NewDailyActivityService(db, nil)
	start := time.Date(2026, time.September, 3, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	mock.ExpectQuery(`(?s)SELECT COALESCE\(SUM\(amount\),0\).*order_type IN \('balance','subscription'\).*redeem_codes.*activity_recharge_events`).
		WithArgs(int64(7), start, end).
		WillReturnRows(sqlmock.NewRows([]string{"coalesce"}).AddRow(100.0))

	amount, err := svc.rechargeAmount(context.Background(), db, 7, start, end)
	require.NoError(t, err)
	require.Equal(t, 100.0, amount)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDailyActivityRecordAdminRechargeUsesAffiliateSwitch(t *testing.T) {
	tests := []struct {
		name    string
		enabled bool
		amount  float64
		wantSQL bool
	}{
		{name: "switch disabled", amount: 5},
		{name: "positive add accepted", enabled: true, amount: 5, wantSQL: true},
		{name: "non-positive ignored", enabled: true, amount: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer db.Close()

			svc := NewDailyActivityService(db, adminRechargeSettingService(tt.enabled))
			now := time.Date(2026, time.September, 3, 12, 0, 0, 0, time.UTC)
			if tt.wantSQL {
				mock.ExpectExec("INSERT INTO activity_recharge_events").
					WithArgs(int64(7), "source-key", tt.amount, now).
					WillReturnResult(sqlmock.NewResult(1, 1))
			}

			require.NoError(t, svc.RecordAdminRecharge(context.Background(), 7, tt.amount, "source-key", now))
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
