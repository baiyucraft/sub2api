//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// 这些用例锁死"每个入口只声明自己真正要改的列"：
// 任何退回整行回写的改动都会让并发写入被陈旧快照覆盖，并在这里变红。

func TestUpdateProfile_OnlyDeclaresRequestedColumns(t *testing.T) {
	username := "renamed"
	tests := []struct {
		name string
		req  UpdateProfileRequest
		want UserUpdateFields
	}{
		{
			name: "username only",
			req:  UpdateProfileRequest{Username: &username},
			want: UserUpdateFields{Username: true},
		},
		{
			name: "notify settings only",
			req:  UpdateProfileRequest{BalanceNotifyEnabled: boolPtr(true)},
			want: UserUpdateFields{BalanceNotifySettings: true},
		},
		{
			name: "username and notify threshold",
			req:  UpdateProfileRequest{Username: &username, BalanceNotifyThreshold: float64Ptr(1.5)},
			want: UserUpdateFields{Username: true, BalanceNotifySettings: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &mockUserRepo{getByIDUser: &User{ID: 7, Balance: 0.30, Status: StatusActive}}
			svc := NewUserService(repo, nil, nil, nil)

			_, err := svc.UpdateProfile(context.Background(), 7, tt.req)
			require.NoError(t, err)
			require.Equal(t, []UserUpdateFields{tt.want}, repo.updateFields)
		})
	}
}

// 只改头像时用户行没有任何列要写，不应产生一次整行更新。
func TestUpdateProfile_AvatarOnlySkipsUserRowWrite(t *testing.T) {
	repo := &mockUserRepo{getByIDUser: &User{ID: 7, Balance: 0.30}}
	svc := NewUserService(repo, nil, nil, nil)

	avatar := "https://cdn.example.com/a.png"
	_, err := svc.UpdateProfile(context.Background(), 7, UpdateProfileRequest{AvatarURL: &avatar})
	require.NoError(t, err)
	require.Len(t, repo.upsertAvatarArgs, 1, "avatar must still be stored")
	require.Equal(t, []UserUpdateFields{{}}, repo.updateFields, "no user column should be declared")
}

func TestUpdateProfile_BalanceNotificationChangesInvalidateAuthCache(t *testing.T) {
	email := "registered@example.com"
	tests := []struct {
		name string
		req  UpdateProfileRequest
	}{
		{name: "toggle", req: UpdateProfileRequest{BalanceNotifyEnabled: boolPtr(false)}},
		{name: "threshold", req: UpdateProfileRequest{BalanceNotifyThreshold: float64Ptr(8.5)}},
		{name: "registration email", req: UpdateProfileRequest{Email: &email}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &mockUserRepo{getByIDUser: &User{ID: 7, Email: email, BalanceNotifyEnabled: true}}
			invalidator := &mockAuthCacheInvalidator{}
			svc := NewUserService(repo, nil, invalidator, nil)

			_, err := svc.UpdateProfile(context.Background(), 7, tt.req)
			require.NoError(t, err)
			require.Equal(t, []int64{7}, invalidator.invalidatedUserIDs)
		})
	}
}

func TestBalanceNotifyExtraEmailChangesInvalidateAuthCache(t *testing.T) {
	t.Run("add", func(t *testing.T) {
		repo := &mockUserRepo{getByIDUser: &User{ID: 7}}
		invalidator := &mockAuthCacheInvalidator{}
		svc := NewUserService(repo, nil, invalidator, nil)

		require.NoError(t, svc.addOrVerifyNotifyEmail(context.Background(), 7, "extra@example.com"))
		require.Equal(t, []int64{7}, invalidator.invalidatedUserIDs)
		require.Equal(t, []UserUpdateFields{{BalanceNotifyExtraEmails: true}}, repo.updateFields)
	})

	t.Run("remove", func(t *testing.T) {
		repo := &mockUserRepo{getByIDUser: &User{ID: 7, BalanceNotifyExtraEmails: []NotifyEmailEntry{{Email: "extra@example.com", Verified: true}}}}
		invalidator := &mockAuthCacheInvalidator{}
		svc := NewUserService(repo, nil, invalidator, nil)

		require.NoError(t, svc.RemoveNotifyEmail(context.Background(), 7, "extra@example.com"))
		require.Equal(t, []int64{7}, invalidator.invalidatedUserIDs)
	})

	t.Run("toggle", func(t *testing.T) {
		repo := &mockUserRepo{getByIDUser: &User{ID: 7, BalanceNotifyExtraEmails: []NotifyEmailEntry{{Email: "extra@example.com", Verified: true}}}}
		invalidator := &mockAuthCacheInvalidator{}
		svc := NewUserService(repo, nil, invalidator, nil)

		require.NoError(t, svc.ToggleNotifyEmail(context.Background(), 7, "extra@example.com", true))
		require.Equal(t, []int64{7}, invalidator.invalidatedUserIDs)
	})
}

func TestChangePassword_OnlyDeclaresPasswordHash(t *testing.T) {
	user := &User{ID: 7, Balance: 0.30}
	require.NoError(t, user.SetPassword("old-password"))
	repo := &mockUserRepo{getByIDUser: user}
	svc := NewUserService(repo, nil, nil, nil)

	err := svc.ChangePassword(context.Background(), 7, ChangePasswordRequest{
		CurrentPassword: "old-password",
		NewPassword:     "new-password",
	})
	require.NoError(t, err)
	require.Equal(t, []UserUpdateFields{{PasswordHash: true}}, repo.updateFields)
}

func TestUpdateStatus_OnlyDeclaresStatus(t *testing.T) {
	repo := &mockUserRepo{getByIDUser: &User{ID: 7, Balance: 0.30, Status: StatusActive}}
	svc := NewUserService(repo, nil, nil, nil)

	require.NoError(t, svc.UpdateStatus(context.Background(), 7, StatusDisabled))
	require.Equal(t, []UserUpdateFields{{Status: true}}, repo.updateFields)
}
