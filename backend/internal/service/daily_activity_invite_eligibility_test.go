package service

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

const inviterExclusiveRebateFilter = `.*LEFT JOIN user_affiliates inviter_aff ON inviter_aff.user_id = ua.inviter_id.*aff_rebate_rate_percent IS NULL`

func TestSyncInvitationMilestonesFiltersExclusiveRebateOnInviter(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	mock.ExpectExec(`(?s)`+regexp.QuoteMeta("INSERT INTO activity_invitation_milestones")+inviterExclusiveRebateFilter).
		WithArgs(int64(7), DefaultDailyActivityConfig().InviteQualificationAmount, unixEpochUTC()).
		WillReturnResult(sqlmock.NewResult(1, 1))

	svc := NewDailyActivityService(db, nil)
	require.NoError(t, svc.SyncInvitationMilestones(context.Background(), 7))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSyncInvitationMilestoneForInviteeFiltersExclusiveRebateOnInviter(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	mock.ExpectExec(`(?s)`+regexp.QuoteMeta("INSERT INTO activity_invitation_milestones")+inviterExclusiveRebateFilter).
		WithArgs(int64(11), int64(23), DefaultDailyActivityConfig().InviteQualificationAmount, unixEpochUTC()).
		WillReturnResult(sqlmock.NewResult(1, 1))

	svc := NewDailyActivityService(db, nil)
	require.NoError(t, svc.SyncInvitationMilestoneForInvitee(context.Background(), 11, 23))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestInvitationMilestoneFilterUsesInviteeForRechargeAndInviterForExclusion(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	query := `(?s)` + regexp.QuoteMeta("INSERT INTO activity_invitation_milestones") +
		`.*FROM user_affiliates ua.*LEFT JOIN user_affiliates inviter_aff ON inviter_aff\.user_id = ua\.inviter_id.*WHERE ua\.user_id=\$1.*inviter_aff\.aff_rebate_rate_percent IS NULL`
	mock.ExpectExec(query).
		WithArgs(int64(11), int64(23), DefaultDailyActivityConfig().InviteQualificationAmount, unixEpochUTC()).
		WillReturnResult(sqlmock.NewResult(1, 1))

	svc := NewDailyActivityService(db, nil)
	require.NoError(t, svc.SyncInvitationMilestoneForInvitee(context.Background(), 11, 23))
	require.NoError(t, mock.ExpectationsWereMet())
}

func unixEpochUTC() time.Time {
	return time.Unix(0, 0).UTC()
}
