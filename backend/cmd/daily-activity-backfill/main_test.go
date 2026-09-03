package main

import (
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestMissingCreditsNeverNegative(t *testing.T) {
	require.Equal(t, int64(3), missing(5, 2))
	require.Zero(t, missing(2, 5))
}

func TestCreditStateAnomaliesDetectsGaps(t *testing.T) {
	require.Zero(t, creditStateAnomalies(creditState{issued: 2, next: 2, min: nullableInt64Value(0)}))
	require.Equal(t, int64(1), creditStateAnomalies(creditState{issued: 2, next: 3, min: nullableInt64Value(0)}))
	require.Equal(t, int64(1), creditStateAnomalies(creditState{issued: 2, next: 2}))
}

func TestChecksumPlanChangesForGiftAndCreditState(t *testing.T) {
	base := plan{Mode: "dry-run", TargetDate: "2026-09-03", PlanSHA256: "ignored"}
	row := entitlementRow{userID: 7, recharge: creditState{issued: 1, next: 1, min: nullableInt64Value(0)}}
	first := checksumPlan(base, []entitlementRow{row}, nil)
	row.giftClaimed = true
	second := checksumPlan(base, []entitlementRow{row}, nil)
	require.NotEqual(t, first, second)
	row.giftClaimed = false
	row.recharge.min = nullableInt64Value(1)
	require.NotEqual(t, first, checksumPlan(base, []entitlementRow{row}, nil))
}

func TestSummarizeSeparatesTargetDayFromCumulativeEntitlement(t *testing.T) {
	target := time.Date(2026, time.September, 3, 0, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	cfg := activityConfig{DailyGiftThreshold: 10, RechargeDrawThreshold: 50, ConsumptionDrawThreshold: 50, InviteQualificationAmount: 10, InviteDrawRequiredCount: 5}
	rows := []entitlementRow{{
		userID:               7,
		targetRechargeAmount: 100,
		targetRechargeEarned: 2,
		rechargeEntitled:     4,
		recharge:             creditState{issued: 3, next: 3, min: nullableInt64Value(0)},
	}}
	result := summarize(target, target.Add(-24*time.Hour), target.Add(-time.Hour), "cfg", cfg, rows, nil)
	require.Equal(t, int64(2), result.RechargeEarnedDraws)
	require.Equal(t, int64(4), result.RechargeEntitledThroughTarget)
	require.Equal(t, int64(1), result.RechargeToGrant)
	require.Equal(t, int64(1), result.DailyGiftQualifiedUsers)
}

func nullableInt64Value(value int64) (result sql.NullInt64) {
	result.Int64 = value
	result.Valid = true
	return result
}
