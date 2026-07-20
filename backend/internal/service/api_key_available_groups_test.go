package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type availableGroupsUserRepoStub struct {
	UserRepository
	user *User
}

func (s *availableGroupsUserRepoStub) GetByID(context.Context, int64) (*User, error) {
	return s.user, nil
}

type availableGroupsGroupRepoStub struct {
	GroupRepository
	groups []Group
}

func (s *availableGroupsGroupRepoStub) ListActive(context.Context) ([]Group, error) {
	return s.groups, nil
}

type availableGroupsSubscriptionRepoStub struct {
	UserSubscriptionRepository
	subscriptions []UserSubscription
}

func (s *availableGroupsSubscriptionRepoStub) ListActiveByUserID(context.Context, int64) ([]UserSubscription, error) {
	return s.subscriptions, nil
}

func TestAPIKeyServiceGetAvailableGroups_UsesPublicExclusiveAndActiveSubscriptionRules(t *testing.T) {
	userRepo := &availableGroupsUserRepoStub{user: &User{ID: 42, AllowedGroups: []int64{2}}}
	groupRepo := &availableGroupsGroupRepoStub{groups: []Group{
		{ID: 1, SubscriptionType: SubscriptionTypeStandard},
		{ID: 2, SubscriptionType: SubscriptionTypeStandard, IsExclusive: true},
		{ID: 3, SubscriptionType: SubscriptionTypeStandard, IsExclusive: true},
		{ID: 4, SubscriptionType: SubscriptionTypeSubscription},
		{ID: 5, SubscriptionType: SubscriptionTypeSubscription},
	}}
	subRepo := &availableGroupsSubscriptionRepoStub{subscriptions: []UserSubscription{{GroupID: 4}}}
	svc := NewAPIKeyService(nil, userRepo, groupRepo, subRepo, nil, nil, nil)

	groups, err := svc.GetAvailableGroups(context.Background(), 42)
	require.NoError(t, err)
	require.Equal(t, []int64{1, 2, 4}, groupIDs(groups))
}

func groupIDs(groups []Group) []int64 {
	ids := make([]int64, 0, len(groups))
	for i := range groups {
		ids = append(ids, groups[i].ID)
	}
	return ids
}
