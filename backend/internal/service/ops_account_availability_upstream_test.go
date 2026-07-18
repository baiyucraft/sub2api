//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type opsUpstreamGateAccountRepo struct {
	AccountRepository
	accounts []Account
}

func (r *opsUpstreamGateAccountRepo) ListOpsAccountsForStats(context.Context, string, *int64) ([]Account, error) {
	return append([]Account(nil), r.accounts...), nil
}

func TestGetAccountAvailabilityStatsIncludesUpstreamSchedulingGate(t *testing.T) {
	group := &Group{ID: 7, Name: "g", Platform: PlatformAnthropic}
	enabled := true
	disabled := false
	repo := &opsUpstreamGateAccountRepo{accounts: []Account{
		{ID: 1, Platform: PlatformAnthropic, Status: StatusActive, Schedulable: true, Groups: []*Group{group}},
		{ID: 2, Platform: PlatformAnthropic, Status: StatusActive, Schedulable: true, UpstreamSchedulingEnabled: &enabled, Groups: []*Group{group}},
		{ID: 3, Platform: PlatformAnthropic, Status: StatusActive, Schedulable: true, UpstreamSchedulingEnabled: &disabled, Groups: []*Group{group}},
	}}
	svc := &OpsService{accountRepo: repo}

	_, groups, accounts, _, err := svc.GetAccountAvailabilityStats(context.Background(), PlatformAnthropic, nil)

	require.NoError(t, err)
	require.Equal(t, int64(3), groups[group.ID].TotalAccounts)
	require.Equal(t, int64(2), groups[group.ID].AvailableCount)
	require.True(t, accounts[1].IsAvailable)
	require.True(t, accounts[2].IsAvailable)
	require.False(t, accounts[3].IsAvailable)
}
