//go:build unit

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

type upstreamGateTestCache struct {
	SchedulerCache
	snapshot []*Account
	accounts map[int64]*Account
}

func (c *upstreamGateTestCache) GetSnapshot(context.Context, SchedulerBucket) ([]*Account, bool, error) {
	return c.snapshot, true, nil
}

func (c *upstreamGateTestCache) GetAccount(_ context.Context, accountID int64) (*Account, error) {
	return c.accounts[accountID], nil
}

type upstreamGateTestRepo struct {
	AccountRepository
	allowedIDs []int64
	err        error
	calls      [][]int64
}

func (r *upstreamGateTestRepo) ListUpstreamSchedulingEnabledAccountIDs(_ context.Context, accountIDs []int64) ([]int64, error) {
	r.calls = append(r.calls, append([]int64(nil), accountIDs...))
	if r.err != nil {
		return nil, r.err
	}
	return append([]int64(nil), r.allowedIDs...), nil
}

func activeSnapshotAccount(id int64, upstreamConfigID *int64) *Account {
	return &Account{
		ID:               id,
		Platform:         PlatformAnthropic,
		Type:             AccountTypeAPIKey,
		Status:           StatusActive,
		Schedulable:      true,
		Concurrency:      1,
		Priority:         1,
		UpstreamConfigID: upstreamConfigID,
	}
}

func TestGatewayListSchedulableAccountsFailsClosedOnStaleUpstreamSnapshot(t *testing.T) {
	upstreamID := int64(10)
	cache := &upstreamGateTestCache{snapshot: []*Account{
		activeSnapshotAccount(1, &upstreamID),
		activeSnapshotAccount(2, nil),
	}}
	repo := &upstreamGateTestRepo{}
	snapshot := NewSchedulerSnapshotService(cache, nil, repo, nil, nil)
	svc := &GatewayService{schedulerSnapshot: snapshot}

	accounts, _, err := svc.listSchedulableAccounts(context.Background(), nil, PlatformAnthropic, true)

	require.NoError(t, err)
	require.Len(t, accounts, 1)
	require.Equal(t, int64(2), accounts[0].ID)
	require.Equal(t, [][]int64{{1}}, repo.calls)
}

func TestGeminiListSchedulableAccountsFailsClosedWhenUpstreamGateQueryFails(t *testing.T) {
	upstreamID := int64(10)
	cache := &upstreamGateTestCache{snapshot: []*Account{activeSnapshotAccount(1, &upstreamID)}}
	repo := &upstreamGateTestRepo{err: errors.New("database unavailable")}
	snapshot := NewSchedulerSnapshotService(cache, nil, repo, nil, nil)
	svc := &GeminiMessagesCompatService{schedulerSnapshot: snapshot}

	accounts, err := svc.listSchedulableAccountsOnce(context.Background(), nil, PlatformGemini, true)

	require.ErrorContains(t, err, "verify upstream scheduling gate")
	require.Nil(t, accounts)
}

func TestGatewayAndGeminiStickyLookupRejectStaleDisabledUpstreamAccount(t *testing.T) {
	upstreamID := int64(10)
	account := activeSnapshotAccount(1, &upstreamID)
	cache := &upstreamGateTestCache{accounts: map[int64]*Account{1: account}}
	repo := &upstreamGateTestRepo{}
	snapshot := NewSchedulerSnapshotService(cache, nil, repo, nil, nil)

	gatewayAccount, gatewayErr := (&GatewayService{schedulerSnapshot: snapshot}).getSchedulableAccount(context.Background(), 1)
	geminiAccount, geminiErr := (&GeminiMessagesCompatService{schedulerSnapshot: snapshot}).getSchedulableAccount(context.Background(), 1)

	require.NoError(t, gatewayErr)
	require.Nil(t, gatewayAccount)
	require.NoError(t, geminiErr)
	require.Nil(t, geminiAccount)
	require.Equal(t, [][]int64{{1}, {1}}, repo.calls)
}

func TestSchedulerSnapshotDoesNotQueryUpstreamGateForOrdinaryAccounts(t *testing.T) {
	cache := &upstreamGateTestCache{snapshot: []*Account{activeSnapshotAccount(2, nil)}}
	snapshot := NewSchedulerSnapshotService(cache, nil, nil, nil, nil)

	accounts, _, err := snapshot.ListSchedulableAccounts(context.Background(), nil, PlatformAnthropic, true)

	require.NoError(t, err)
	require.Len(t, accounts, 1)
	require.Equal(t, int64(2), accounts[0].ID)
}

func TestGatewayNewSelectionResultReleasesAcquiredSlotWhenUpstreamGateCloses(t *testing.T) {
	upstreamID := int64(10)
	account := activeSnapshotAccount(1, &upstreamID)
	cache := &upstreamGateTestCache{accounts: map[int64]*Account{1: account}}
	repo := &upstreamGateTestRepo{}
	snapshot := NewSchedulerSnapshotService(cache, nil, repo, nil, nil)
	svc := &GatewayService{schedulerSnapshot: snapshot}
	releaseCalls := 0

	result, err := svc.newSelectionResult(context.Background(), account, true, func() {
		releaseCalls++
	}, nil)

	require.ErrorContains(t, err, "not found during hydration")
	require.Nil(t, result)
	require.Equal(t, 1, releaseCalls)
}

func TestGatewayNewSelectionResultDoesNotReleaseUnacquiredWaitPlan(t *testing.T) {
	upstreamID := int64(10)
	account := activeSnapshotAccount(1, &upstreamID)
	cache := &upstreamGateTestCache{accounts: map[int64]*Account{1: account}}
	repo := &upstreamGateTestRepo{}
	snapshot := NewSchedulerSnapshotService(cache, nil, repo, nil, nil)
	svc := &GatewayService{schedulerSnapshot: snapshot}
	releaseCalls := 0

	result, err := svc.newSelectionResult(context.Background(), account, false, func() {
		releaseCalls++
	}, &AccountWaitPlan{AccountID: account.ID})

	require.ErrorContains(t, err, "not found during hydration")
	require.Nil(t, result)
	require.Zero(t, releaseCalls)
}
