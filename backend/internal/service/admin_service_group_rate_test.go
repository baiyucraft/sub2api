//go:build unit

package service

import (
	"context"
	"errors"
	"math"
	"net/http"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

// userGroupRateRepoStubForGroupRate implements UserGroupRateRepository for group rate tests.
type userGroupRateRepoStubForGroupRate struct {
	getByGroupIDData map[int64][]UserGroupRateEntry
	getByGroupIDErr  error

	deletedGroupIDs  []int64
	deleteByGroupErr error

	syncedGroupID int64
	syncedEntries []GroupRateMultiplierInput
	syncGroupErr  error

	syncedUserID   int64
	syncedRates    map[int64]*float64
	syncedPercents map[int64]*float64
	syncUserErr    error

	rpmSyncedGroupID int64
	rpmSyncedEntries []GroupRPMOverrideInput
	rpmSyncErr       error
}

func (s *userGroupRateRepoStubForGroupRate) GetByUserID(_ context.Context, _ int64) (map[int64]float64, error) {
	panic("unexpected GetByUserID call")
}

func (s *userGroupRateRepoStubForGroupRate) GetByUserAndGroup(_ context.Context, _, _ int64) (*float64, error) {
	panic("unexpected GetByUserAndGroup call")
}

func (s *userGroupRateRepoStubForGroupRate) GetRPMOverrideByUserAndGroup(_ context.Context, _, _ int64) (*int, error) {
	panic("unexpected GetRPMOverrideByUserAndGroup call")
}

func (s *userGroupRateRepoStubForGroupRate) GetByGroupID(_ context.Context, groupID int64) ([]UserGroupRateEntry, error) {
	if s.getByGroupIDErr != nil {
		return nil, s.getByGroupIDErr
	}
	return s.getByGroupIDData[groupID], nil
}

func (s *userGroupRateRepoStubForGroupRate) SyncUserGroupRates(_ context.Context, userID int64, rates map[int64]*float64) error {
	s.syncedUserID = userID
	s.syncedRates = rates
	return s.syncUserErr
}

func (s *userGroupRateRepoStubForGroupRate) SyncUserGroupRatePercents(_ context.Context, userID int64, percents map[int64]*float64) error {
	s.syncedUserID = userID
	s.syncedPercents = percents
	return s.syncUserErr
}

func (s *userGroupRateRepoStubForGroupRate) SyncGroupRateMultipliers(_ context.Context, groupID int64, entries []GroupRateMultiplierInput) error {
	s.syncedGroupID = groupID
	s.syncedEntries = entries
	return s.syncGroupErr
}

func (s *userGroupRateRepoStubForGroupRate) SyncGroupRPMOverrides(_ context.Context, groupID int64, entries []GroupRPMOverrideInput) error {
	s.rpmSyncedGroupID = groupID
	s.rpmSyncedEntries = entries
	return s.rpmSyncErr
}

func (s *userGroupRateRepoStubForGroupRate) ClearGroupRPMOverrides(_ context.Context, _ int64) error {
	panic("unexpected ClearGroupRPMOverrides call")
}

func (s *userGroupRateRepoStubForGroupRate) DeleteByGroupID(_ context.Context, groupID int64) error {
	s.deletedGroupIDs = append(s.deletedGroupIDs, groupID)
	return s.deleteByGroupErr
}

func (s *userGroupRateRepoStubForGroupRate) DeleteByUserID(_ context.Context, _ int64) error {
	panic("unexpected DeleteByUserID call")
}

func TestAdminService_GetGroupRateMultipliers(t *testing.T) {
	t.Run("returns entries for group", func(t *testing.T) {
		repo := &userGroupRateRepoStubForGroupRate{
			getByGroupIDData: map[int64][]UserGroupRateEntry{
				10: {
					{UserID: 1, UserName: "alice", UserEmail: "alice@test.com", RateMultiplier: ptrFloat(1.5)},
					{UserID: 2, UserName: "bob", UserEmail: "bob@test.com", RateMultiplier: ptrFloat(0.8)},
				},
			},
		}
		svc := &adminServiceImpl{userGroupRateRepo: repo}

		entries, err := svc.GetGroupRateMultipliers(context.Background(), 10)
		require.NoError(t, err)
		require.Len(t, entries, 2)
		require.Equal(t, int64(1), entries[0].UserID)
		require.Equal(t, "alice", entries[0].UserName)
		require.NotNil(t, entries[0].RateMultiplier)
		require.Equal(t, 1.5, *entries[0].RateMultiplier)
		require.Equal(t, int64(2), entries[1].UserID)
		require.NotNil(t, entries[1].RateMultiplier)
		require.Equal(t, 0.8, *entries[1].RateMultiplier)
	})

	t.Run("returns nil when repo is nil", func(t *testing.T) {
		svc := &adminServiceImpl{userGroupRateRepo: nil}

		entries, err := svc.GetGroupRateMultipliers(context.Background(), 10)
		require.NoError(t, err)
		require.Nil(t, entries)
	})

	t.Run("returns empty slice for group with no entries", func(t *testing.T) {
		repo := &userGroupRateRepoStubForGroupRate{
			getByGroupIDData: map[int64][]UserGroupRateEntry{},
		}
		svc := &adminServiceImpl{userGroupRateRepo: repo}

		entries, err := svc.GetGroupRateMultipliers(context.Background(), 99)
		require.NoError(t, err)
		require.Nil(t, entries)
	})

	t.Run("propagates repo error", func(t *testing.T) {
		repo := &userGroupRateRepoStubForGroupRate{
			getByGroupIDErr: errors.New("db error"),
		}
		svc := &adminServiceImpl{userGroupRateRepo: repo}

		_, err := svc.GetGroupRateMultipliers(context.Background(), 10)
		require.Error(t, err)
		require.Contains(t, err.Error(), "db error")
	})
}

func TestAdminService_ClearGroupRateMultipliers(t *testing.T) {
	t.Run("clears only the rate portion", func(t *testing.T) {
		repo := &userGroupRateRepoStubForGroupRate{}
		svc := &adminServiceImpl{userGroupRateRepo: repo}

		err := svc.ClearGroupRateMultipliers(context.Background(), 42)
		require.NoError(t, err)
		require.Equal(t, int64(42), repo.syncedGroupID)
		require.Empty(t, repo.syncedEntries)
		require.Empty(t, repo.deletedGroupIDs, "清空倍率不得删除同一行上的 RPM override")
	})

	t.Run("returns nil when repo is nil", func(t *testing.T) {
		svc := &adminServiceImpl{userGroupRateRepo: nil}

		err := svc.ClearGroupRateMultipliers(context.Background(), 42)
		require.NoError(t, err)
	})

	t.Run("propagates repo error", func(t *testing.T) {
		repo := &userGroupRateRepoStubForGroupRate{
			syncGroupErr: errors.New("sync failed"),
		}
		svc := &adminServiceImpl{userGroupRateRepo: repo}

		err := svc.ClearGroupRateMultipliers(context.Background(), 42)
		require.Error(t, err)
		require.Contains(t, err.Error(), "sync failed")
	})
}

func TestAdminService_BatchSetGroupRateMultipliers(t *testing.T) {
	t.Run("syncs entries to repo", func(t *testing.T) {
		repo := &userGroupRateRepoStubForGroupRate{}
		svc := &adminServiceImpl{userGroupRateRepo: repo}

		entries := []GroupRateMultiplierInput{
			{UserID: 1, RatePercent: ptrFloat(150)},
			{UserID: 2, RatePercent: ptrFloat(80)},
		}
		err := svc.BatchSetGroupRateMultipliers(context.Background(), 10, entries)
		require.NoError(t, err)
		require.Equal(t, int64(10), repo.syncedGroupID)
		require.Equal(t, entries, repo.syncedEntries)
	})

	t.Run("accepts explicit zero and invalidates group caches", func(t *testing.T) {
		repo := &userGroupRateRepoStubForGroupRate{}
		invalidator := &authCacheInvalidatorStub{}
		svc := &adminServiceImpl{userGroupRateRepo: repo, authCacheInvalidator: invalidator}

		err := svc.BatchSetGroupRateMultipliers(context.Background(), 10, []GroupRateMultiplierInput{
			{UserID: 1, RatePercent: ptrFloat(0)},
		})
		require.NoError(t, err)
		require.Equal(t, []GroupRateMultiplierInput{{UserID: 1, RatePercent: ptrFloat(0)}}, repo.syncedEntries)
		require.Equal(t, []int64{10}, invalidator.groupIDs)
	})

	t.Run("converts legacy effective multiplier to percent", func(t *testing.T) {
		repo := &userGroupRateRepoStubForGroupRate{}
		groupRepo := &groupRepoStubForAdmin{getByID: &Group{ID: 10, RateMultiplier: 0.8}}
		svc := &adminServiceImpl{userGroupRateRepo: repo, groupRepo: groupRepo}

		err := svc.BatchSetGroupRateMultipliers(context.Background(), 10, []GroupRateMultiplierInput{
			{UserID: 1, RateMultiplier: ptrFloat(0.4)},
		})
		require.NoError(t, err)
		require.Len(t, repo.syncedEntries, 1)
		require.Nil(t, repo.syncedEntries[0].RateMultiplier)
		require.NotNil(t, repo.syncedEntries[0].RatePercent)
		require.InDelta(t, 50, *repo.syncedEntries[0].RatePercent, 1e-12)
	})

	t.Run("rejects percent and multiplier together", func(t *testing.T) {
		repo := &userGroupRateRepoStubForGroupRate{}
		svc := &adminServiceImpl{userGroupRateRepo: repo}

		err := svc.BatchSetGroupRateMultipliers(context.Background(), 10, []GroupRateMultiplierInput{
			{UserID: 1, RatePercent: ptrFloat(50), RateMultiplier: ptrFloat(0.4)},
		})
		require.Error(t, err)
		require.Equal(t, http.StatusBadRequest, infraerrors.Code(err))
		require.Zero(t, repo.syncedGroupID)
	})

	t.Run("rejects duplicate users before persistence", func(t *testing.T) {
		repo := &userGroupRateRepoStubForGroupRate{}
		svc := &adminServiceImpl{userGroupRateRepo: repo}

		err := svc.BatchSetGroupRateMultipliers(context.Background(), 10, []GroupRateMultiplierInput{
			{UserID: 1, RatePercent: ptrFloat(50)},
			{UserID: 1, RatePercent: ptrFloat(80)},
		})
		require.Error(t, err)
		require.Equal(t, "DUPLICATE_USER_ID", infraerrors.Reason(err))
		require.Zero(t, repo.syncedGroupID)
	})

	t.Run("rejects non-finite and negative values", func(t *testing.T) {
		for _, rate := range []float64{-0.01, math.NaN(), math.Inf(1), math.Inf(-1)} {
			repo := &userGroupRateRepoStubForGroupRate{}
			svc := &adminServiceImpl{userGroupRateRepo: repo}
			err := svc.BatchSetGroupRateMultipliers(context.Background(), 10, []GroupRateMultiplierInput{{UserID: 1, RatePercent: &rate}})
			require.Error(t, err)
			require.Zero(t, repo.syncedGroupID)
		}
	})

	t.Run("returns nil when repo is nil", func(t *testing.T) {
		svc := &adminServiceImpl{userGroupRateRepo: nil}

		err := svc.BatchSetGroupRateMultipliers(context.Background(), 10, nil)
		require.NoError(t, err)
	})

	t.Run("propagates repo error", func(t *testing.T) {
		repo := &userGroupRateRepoStubForGroupRate{
			syncGroupErr: errors.New("sync failed"),
		}
		svc := &adminServiceImpl{userGroupRateRepo: repo}

		err := svc.BatchSetGroupRateMultipliers(context.Background(), 10, []GroupRateMultiplierInput{
			{UserID: 1, RatePercent: ptrFloat(100)},
		})
		require.Error(t, err)
		require.Contains(t, err.Error(), "sync failed")
	})
}

func TestAdminService_BatchSetGroupRPMOverrides(t *testing.T) {
	t.Run("syncs entries to repo", func(t *testing.T) {
		repo := &userGroupRateRepoStubForGroupRate{}
		svc := &adminServiceImpl{userGroupRateRepo: repo}
		override := 20
		entries := []GroupRPMOverrideInput{{UserID: 2, RPMOverride: &override}}

		err := svc.BatchSetGroupRPMOverrides(context.Background(), 10, entries)
		require.NoError(t, err)
		require.Equal(t, int64(10), repo.rpmSyncedGroupID)
		require.Equal(t, entries, repo.rpmSyncedEntries)
	})

	t.Run("rejects negative override as bad request", func(t *testing.T) {
		repo := &userGroupRateRepoStubForGroupRate{}
		svc := &adminServiceImpl{userGroupRateRepo: repo}
		negative := -1

		err := svc.BatchSetGroupRPMOverrides(context.Background(), 10, []GroupRPMOverrideInput{
			{UserID: 2, RPMOverride: &negative},
		})
		require.Error(t, err)
		require.Equal(t, http.StatusBadRequest, infraerrors.Code(err))
		require.Zero(t, repo.rpmSyncedGroupID)
	})
}
