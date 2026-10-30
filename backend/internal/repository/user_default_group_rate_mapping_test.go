//go:build unit

package repository

import (
	"context"
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUserDefaultGroupRateRepositoryNullableAndOuterTransaction(t *testing.T) {
	repo, client := newUserEntRepo(t)
	ctx := context.Background()
	u := &service.User{Email: "default-rate-transaction@example.test", PasswordHash: "synthetic-hash", Status: service.StatusActive, Role: service.RoleUser, Balance: 12}
	require.NoError(t, repo.Create(ctx, u))
	require.Nil(t, u.DefaultGroupRatePercent)

	percent := 95.0
	tx, err := client.Tx(ctx)
	require.NoError(t, err)
	txCtx := dbent.NewTxContext(ctx, tx)
	u.DefaultGroupRatePercent = &percent
	require.NoError(t, repo.Update(txCtx, u, service.UserUpdateFields{DefaultGroupRatePercent: true}))
	pending, err := tx.Client().User.Get(txCtx, u.ID)
	require.NoError(t, err)
	require.NotNil(t, pending.DefaultGroupRatePercent)
	require.Equal(t, 95.0, *pending.DefaultGroupRatePercent)
	require.NoError(t, tx.Rollback())
	u, err = repo.GetByID(ctx, u.ID)
	require.NoError(t, err)
	require.Nil(t, u.DefaultGroupRatePercent, "outer rollback must also roll back the repository update")

	u.DefaultGroupRatePercent = &percent
	require.NoError(t, repo.Update(ctx, u, service.UserUpdateFields{DefaultGroupRatePercent: true}))
	u, err = repo.GetByID(ctx, u.ID)
	require.NoError(t, err)
	require.Equal(t, 95.0, *u.DefaultGroupRatePercent)
	stalePercent := 40.0
	u.DefaultGroupRatePercent = &stalePercent
	u.Username = "renamed"
	require.NoError(t, repo.Update(ctx, u, service.UserUpdateFields{Username: true}))
	u, err = repo.GetByID(ctx, u.ID)
	require.NoError(t, err)
	require.Equal(t, 95.0, *u.DefaultGroupRatePercent, "omitted default must not overwrite a concurrent value")
	require.Equal(t, 12.0, u.Balance)

	zero := 0.0
	u.DefaultGroupRatePercent = &zero
	require.NoError(t, repo.Update(ctx, u, service.UserUpdateFields{DefaultGroupRatePercent: true}))
	u, err = repo.GetByID(ctx, u.ID)
	require.NoError(t, err)
	require.NotNil(t, u.DefaultGroupRatePercent)
	require.Zero(t, *u.DefaultGroupRatePercent)
	u.DefaultGroupRatePercent = nil
	require.NoError(t, repo.Update(ctx, u, service.UserUpdateFields{DefaultGroupRatePercent: true}))
	u, err = repo.GetByID(ctx, u.ID)
	require.NoError(t, err)
	require.Nil(t, u.DefaultGroupRatePercent)
}

func TestUserDefaultGroupRateRepositoryCreateAndMapping(t *testing.T) {
	repo, _ := newUserEntRepo(t)
	percent := 95.0
	u := &service.User{Email: "default-rate-create@example.test", PasswordHash: "synthetic-hash", Status: service.StatusActive, Role: service.RoleUser, DefaultGroupRatePercent: &percent}
	require.NoError(t, repo.Create(context.Background(), u))
	require.NotNil(t, u.DefaultGroupRatePercent)
	require.Equal(t, percent, *u.DefaultGroupRatePercent)
	loaded, err := repo.GetByID(context.Background(), u.ID)
	require.NoError(t, err)
	require.Equal(t, percent, *loaded.DefaultGroupRatePercent)
}
