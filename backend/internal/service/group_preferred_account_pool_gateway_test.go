//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGatewayLoadAwarenessPreferredPoolFallsBackToOrdinaryImmediateCapacity(t *testing.T) {
	ctx := context.Background()
	groupID := int64(7)
	repo := &mockAccountRepoForPlatform{
		accounts: []Account{
			{ID: 1, Platform: PlatformAnthropic, Priority: 1, Status: StatusActive, Schedulable: true, Concurrency: 1, RateMultiplier: testFloat64Ptr(0.01), AccountGroups: []AccountGroup{{GroupID: groupID}}},
			{ID: 2, Platform: PlatformAnthropic, Priority: 1, Status: StatusActive, Schedulable: true, Concurrency: 1, RateMultiplier: testFloat64Ptr(0.20), AccountGroups: []AccountGroup{{GroupID: groupID, SchedulerPreferred: true}}},
		},
		accountsByID: map[int64]*Account{},
	}
	for i := range repo.accounts {
		repo.accountsByID[repo.accounts[i].ID] = &repo.accounts[i]
	}
	groupRepo := &mockGroupRepoForGateway{groups: map[int64]*Group{
		groupID: {ID: groupID, Platform: PlatformAnthropic, Status: StatusActive, Hydrated: true},
	}}
	acquiredIDs := make([]int64, 0, 2)
	concurrencyCache := schedulerTestConcurrencyCache{
		acquiredIDs: &acquiredIDs,
		acquireResults: map[int64]bool{
			2: false,
			1: true,
		},
		loadMap: map[int64]*AccountLoadInfo{
			1: {AccountID: 1, LoadRate: 0},
			2: {AccountID: 2, LoadRate: 0},
		},
	}
	cfg := testConfig()
	cfg.Gateway.Scheduling.LoadBatchEnabled = true
	svc := &GatewayService{
		accountRepo:        repo,
		groupRepo:          groupRepo,
		cache:              &mockGatewayCacheForPlatform{},
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(concurrencyCache),
	}

	result, err := svc.SelectAccountWithLoadAwareness(ctx, &groupID, "", "claude-3-5-sonnet-20241022", nil, "", 0)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.Account)
	require.Equal(t, int64(1), result.Account.ID)
	require.Equal(t, []int64{2, 1}, acquiredIDs)
	if result.ReleaseFunc != nil {
		result.ReleaseFunc()
	}
}
