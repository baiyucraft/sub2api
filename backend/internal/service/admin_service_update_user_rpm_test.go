//go:build unit

package service

import (
	"context"
	"errors"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

// rpmUserRepoStub 复用 admin_service_update_balance_test.go 的基础 stub 结构，
// 只在 Update 时把入参克隆一份，便于断言修改后的 RPMLimit。
type rpmUserRepoStub struct {
	*userRepoStub
	lastUpdated *User
}

func (s *rpmUserRepoStub) Update(_ context.Context, user *User, _ UserUpdateFields) error {
	if user == nil {
		return nil
	}
	clone := *user
	s.lastUpdated = &clone
	if s.userRepoStub != nil {
		s.userRepoStub.user = &clone
	}
	return nil
}

func TestAdminService_UpdateUser_InvalidatesAuthCacheOnRPMLimitChange(t *testing.T) {
	base := &userRepoStub{user: &User{ID: 42, Email: "u@example.com", RPMLimit: 10}}
	repo := &rpmUserRepoStub{userRepoStub: base}
	invalidator := &authCacheInvalidatorStub{}
	svc := &adminServiceImpl{
		userRepo:             repo,
		redeemCodeRepo:       &redeemRepoStub{},
		authCacheInvalidator: invalidator,
	}

	newRPM := 60
	updated, err := svc.UpdateUser(context.Background(), 42, &UpdateUserInput{
		RPMLimit: &newRPM,
	})
	require.NoError(t, err)
	require.NotNil(t, updated)
	require.Equal(t, 60, updated.RPMLimit)
	require.Equal(t, []int64{42}, invalidator.userIDs, "仅修改 RPMLimit 也应失效 API Key 认证缓存")
}

func TestAdminService_UpdateUser_NoInvalidateWhenRPMLimitUnchanged(t *testing.T) {
	base := &userRepoStub{user: &User{ID: 42, Email: "u@example.com", RPMLimit: 10, Username: "old"}}
	repo := &rpmUserRepoStub{userRepoStub: base}
	invalidator := &authCacheInvalidatorStub{}
	svc := &adminServiceImpl{
		userRepo:             repo,
		redeemCodeRepo:       &redeemRepoStub{},
		authCacheInvalidator: invalidator,
	}

	newName := "new"
	sameRPM := 10
	_, err := svc.UpdateUser(context.Background(), 42, &UpdateUserInput{
		Username: &newName,
		RPMLimit: &sameRPM,
	})
	require.NoError(t, err)
	require.Empty(t, invalidator.userIDs, "只改 username 不应触发认证缓存失效")
}

func TestAdminService_UpdateUser_InvalidatesAuthCacheOnRegistrationEmailChange(t *testing.T) {
	base := &userRepoStub{user: &User{ID: 42, Email: "old@example.com"}}
	repo := &rpmUserRepoStub{userRepoStub: base}
	invalidator := &authCacheInvalidatorStub{}
	svc := &adminServiceImpl{
		userRepo:             repo,
		redeemCodeRepo:       &redeemRepoStub{},
		authCacheInvalidator: invalidator,
	}

	updated, err := svc.UpdateUser(context.Background(), 42, &UpdateUserInput{Email: "new@example.com"})
	require.NoError(t, err)
	require.Equal(t, "new@example.com", updated.Email)
	require.Equal(t, []int64{42}, invalidator.userIDs, "注册邮箱变更后应立即失效 API Key 认证快照")
}

func TestAdminService_UpdateUser_AllowsExplicitZeroGroupRateAndInvalidatesCaches(t *testing.T) {
	base := &userRepoStub{user: &User{ID: 42, Email: "u@example.com"}}
	repo := &rpmUserRepoStub{userRepoStub: base}
	rateRepo := &userGroupRateRepoStubForGroupRate{}
	invalidator := &authCacheInvalidatorStub{}
	svc := &adminServiceImpl{
		userRepo:             repo,
		redeemCodeRepo:       &redeemRepoStub{},
		userGroupRateRepo:    rateRepo,
		authCacheInvalidator: invalidator,
	}

	rate := 0.0
	updated, err := svc.UpdateUser(context.Background(), 42, &UpdateUserInput{
		GroupRates: map[int64]*float64{7: &rate},
	})
	require.NoError(t, err)
	require.NotNil(t, updated)
	require.Equal(t, int64(42), rateRepo.syncedUserID)
	require.Contains(t, rateRepo.syncedRates, int64(7))
	require.NotNil(t, rateRepo.syncedRates[7])
	require.Equal(t, 0.0, *rateRepo.syncedRates[7])
	require.Equal(t, []int64{42}, invalidator.userIDs, "显式 0 倍率变更后应立即失效 API Key 认证快照")
}

func TestAdminService_UpdateUser_WritesGroupRatePercentsAndInvalidatesCaches(t *testing.T) {
	base := &userRepoStub{user: &User{ID: 42, Email: "u@example.com"}}
	repo := &rpmUserRepoStub{userRepoStub: base}
	rateRepo := &userGroupRateRepoStubForGroupRate{}
	invalidator := &authCacheInvalidatorStub{}
	svc := &adminServiceImpl{
		userRepo:             repo,
		redeemCodeRepo:       &redeemRepoStub{},
		userGroupRateRepo:    rateRepo,
		authCacheInvalidator: invalidator,
	}

	percent := 50.0
	updated, err := svc.UpdateUser(context.Background(), 42, &UpdateUserInput{
		GroupRatePercents: map[int64]*float64{7: &percent},
	})
	require.NoError(t, err)
	require.NotNil(t, updated)
	require.Equal(t, int64(42), rateRepo.syncedUserID)
	require.Contains(t, rateRepo.syncedPercents, int64(7))
	require.NotNil(t, rateRepo.syncedPercents[7])
	require.Equal(t, 50.0, *rateRepo.syncedPercents[7])
	require.Equal(t, []int64{42}, invalidator.userIDs)
}

func TestAdminService_UpdateUser_ReturnsGroupRatePercentPersistenceError(t *testing.T) {
	base := &userRepoStub{user: &User{ID: 42, Email: "u@example.com"}}
	repo := &rpmUserRepoStub{userRepoStub: base}
	rateRepo := &userGroupRateRepoStubForGroupRate{syncUserErr: errors.New("rate persistence failed")}
	invalidator := &authCacheInvalidatorStub{}
	svc := &adminServiceImpl{
		userRepo:             repo,
		redeemCodeRepo:       &redeemRepoStub{},
		userGroupRateRepo:    rateRepo,
		authCacheInvalidator: invalidator,
	}

	percent := 50.0
	updated, err := svc.UpdateUser(context.Background(), 42, &UpdateUserInput{
		GroupRatePercents: map[int64]*float64{7: &percent},
	})
	require.Nil(t, updated)
	require.EqualError(t, err, "rate persistence failed")
	require.Equal(t, []int64{42}, invalidator.userIDs, "用户基础字段可能已落库，失败路径也必须驱逐认证快照")
}

func TestAdminService_UpdateUser_RejectsLegacyAndPercentRatesTogether(t *testing.T) {
	base := &userRepoStub{user: &User{ID: 42, Email: "u@example.com"}}
	repo := &rpmUserRepoStub{userRepoStub: base}
	svc := &adminServiceImpl{userRepo: repo, redeemCodeRepo: &redeemRepoStub{}}
	rate := 0.4
	percent := 50.0

	_, err := svc.UpdateUser(context.Background(), 42, &UpdateUserInput{
		GroupRates:        map[int64]*float64{7: &rate},
		GroupRatePercents: map[int64]*float64{7: &percent},
	})
	require.Error(t, err)
	require.Equal(t, "AMBIGUOUS_USER_GROUP_RATE", infraerrors.Reason(err))
	require.Nil(t, repo.lastUpdated)
}
