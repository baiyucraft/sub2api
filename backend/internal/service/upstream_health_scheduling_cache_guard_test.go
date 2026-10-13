package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGatewayCacheGuardMigrationRetainsSourceWhenDestinationsWait(t *testing.T) {
	p := cacheGuardPreference{Active: true, Migration: true, KeepAccountID: 1,
		Tiers: [][]int64{{2}, {3}}, ScopeIDs: map[int64]struct{}{1: {}, 2: {}, 3: {}}, CandidateIDs: []int64{1, 2, 3, 4}}
	base := map[int64]struct{}{99: {}}
	calls, releases := 0, 0
	result, err, handled := selectGatewayCacheGuardTiers(context.Background(), p, base, func(ctx context.Context, excluded map[int64]struct{}) (*AccountSelectionResult, error) {
		require.True(t, cacheGuardTentativeSelection(ctx))
		require.Contains(t, excluded, int64(99))
		require.Contains(t, excluded, int64(4), "ordinary candidates must not replace a warm source")
		id := []int64{2, 3, 1}[calls]
		calls++
		require.NotContains(t, excluded, id)
		for _, other := range []int64{1, 2, 3} {
			if other != id {
				require.Contains(t, excluded, other)
			}
		}
		return &AccountSelectionResult{Account: &Account{ID: id}, WaitPlan: &AccountWaitPlan{}, ReleaseFunc: func() { releases++ }}, nil
	})
	require.NoError(t, err)
	require.True(t, handled)
	require.Equal(t, int64(1), result.Account.ID)
	require.False(t, result.Acquired)
	require.NotNil(t, result.WaitPlan)
	require.Equal(t, 2, releases)
	require.Equal(t, map[int64]struct{}{99: {}}, base, "caller exclusions must not be mutated")
}

func TestGatewayCacheGuardMigrationRequiresCapacityAndFallsBackOnHardInvalidSource(t *testing.T) {
	p := cacheGuardPreference{Active: true, Migration: true, KeepAccountID: 1,
		Tiers: [][]int64{{2}, {3}}, CandidateIDs: []int64{1, 2, 3}}
	t.Run("second destination has capacity", func(t *testing.T) {
		calls := 0
		result, err, handled := selectGatewayCacheGuardTiers(context.Background(), p, nil, func(ctx context.Context, excluded map[int64]struct{}) (*AccountSelectionResult, error) {
			calls++
			id := int64(calls + 1)
			return &AccountSelectionResult{Account: &Account{ID: id}, Acquired: calls == 2, WaitPlan: &AccountWaitPlan{}}, nil
		})
		require.NoError(t, err)
		require.True(t, handled)
		require.Equal(t, int64(3), result.Account.ID)
		require.True(t, result.Acquired)
		require.Equal(t, 2, calls)
	})
	t.Run("hard invalid source restores original path", func(t *testing.T) {
		calls := 0
		result, err, handled := selectGatewayCacheGuardTiers(context.Background(), p, nil, func(ctx context.Context, excluded map[int64]struct{}) (*AccountSelectionResult, error) {
			calls++
			return nil, ErrNoAvailableAccounts
		})
		require.Nil(t, result)
		require.NoError(t, err)
		require.False(t, handled)
		require.Equal(t, 3, calls, "the warm source must be tried before original fallback")
	})
}

func TestGatewayCacheGuardKeepSourceExcludesOrdinaryAccounts(t *testing.T) {
	p := cacheGuardPreference{Active: true, KeepAccountID: 1, ScopeIDs: map[int64]struct{}{1: {}, 2: {}}, CandidateIDs: []int64{1, 2, 3}}
	result, err, handled := selectGatewayCacheGuardTiers(context.Background(), p, map[int64]struct{}{8: {}}, func(ctx context.Context, excluded map[int64]struct{}) (*AccountSelectionResult, error) {
		require.NotContains(t, excluded, int64(1))
		for _, id := range []int64{2, 3, 8} {
			require.Contains(t, excluded, id)
		}
		return &AccountSelectionResult{Account: &Account{ID: 1}, WaitPlan: &AccountWaitPlan{}}, nil
	})
	require.NoError(t, err)
	require.True(t, handled)
	require.Equal(t, int64(1), result.Account.ID)
	require.NotNil(t, result.WaitPlan)
	require.Equal(t, map[int64]struct{}{1: {}, 2: {}}, p.ScopeIDs)
}

func TestGatewayCacheGuardPreservesUnexpectedSelectionErrors(t *testing.T) {
	wantErr := errors.New("capacity service unavailable")
	p := cacheGuardPreference{Active: true, Tiers: [][]int64{{1}, {2}}, ScopeIDs: map[int64]struct{}{1: {}, 2: {}}}
	calls := 0
	result, err, handled := selectGatewayCacheGuardTiers(context.Background(), p, nil, func(ctx context.Context, excluded map[int64]struct{}) (*AccountSelectionResult, error) {
		calls++
		return nil, wantErr
	})
	require.Nil(t, result)
	require.ErrorIs(t, err, wantErr)
	require.True(t, handled)
	require.Equal(t, 1, calls)
}

func TestGatewayCacheGuardRejectsRefreshedCandidateOutsideMigrationTier(t *testing.T) {
	p := cacheGuardPreference{Active: true, Migration: true, KeepAccountID: 1, Tiers: [][]int64{{2}}, CandidateIDs: []int64{1, 2}}
	calls, releases := 0, 0
	result, err, handled := selectGatewayCacheGuardTiers(context.Background(), p, nil, func(ctx context.Context, excluded map[int64]struct{}) (*AccountSelectionResult, error) {
		calls++
		if calls == 1 {
			// A concurrent account snapshot refresh must not turn a newly legal
			// account with no cache evidence into an approved migration target.
			return &AccountSelectionResult{Account: &Account{ID: 3}, Acquired: true, ReleaseFunc: func() { releases++ }}, nil
		}
		return &AccountSelectionResult{Account: &Account{ID: 1}, WaitPlan: &AccountWaitPlan{}}, nil
	})
	require.NoError(t, err)
	require.True(t, handled)
	require.Equal(t, int64(1), result.Account.ID)
	require.Equal(t, 1, releases)
}
