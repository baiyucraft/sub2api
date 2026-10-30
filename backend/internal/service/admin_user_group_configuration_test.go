//go:build unit

package service

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

type adminGroupConfigurationUserRepo struct {
	*userRepoStub
	update func(context.Context, *User, UserUpdateFields) error
}

func (r *adminGroupConfigurationUserRepo) Update(ctx context.Context, user *User, fields UserUpdateFields) error {
	return r.update(ctx, user, fields)
}

type adminGroupConfigurationRateRepo struct {
	*userGroupRateRepoStubForGroupRate
	percent func(context.Context, int64, map[int64]*float64) error
	legacy  func(context.Context, int64, map[int64]*float64) error
}

func (r *adminGroupConfigurationRateRepo) SyncUserGroupRatePercents(ctx context.Context, userID int64, percents map[int64]*float64) error {
	return r.percent(ctx, userID, percents)
}

func (r *adminGroupConfigurationRateRepo) SyncUserGroupRates(ctx context.Context, userID int64, rates map[int64]*float64) error {
	return r.legacy(ctx, userID, rates)
}

// Deliberately expose only the legacy interface, even though the delegated
// repository can write percentages. The helper must reject this before Update.
type adminGroupConfigurationMissingPercentWriter struct{ UserGroupRateRepository }

func adminGroupConfigurationFloat64(value float64) *float64 { return &value }

func adminGroupConfigurationCacheProbe(t *testing.T) func() bool {
	t.Helper()
	resolver := newUserGroupRateResolver(nil, nil, time.Minute, nil, "test.admin_group_configuration")
	resolver.cache.Set("42:8", cachedUserGroupRate{percent: 83, hasOverride: true}, time.Minute)
	t.Cleanup(func() { userGroupRateResolverRegistry.Delete(resolver) })
	return func() bool {
		_, exists := resolver.cache.Get("42:8")
		return exists
	}
}

type adminGroupConfigurationInvalidator struct {
	*authCacheInvalidatorStub
	notify func(context.Context, int64)
}

func (r *adminGroupConfigurationInvalidator) InvalidateAuthCacheByUserID(ctx context.Context, userID int64) {
	r.notify(ctx, userID)
	r.authCacheInvalidatorStub.InvalidateAuthCacheByUserID(ctx, userID)
}

func newAdminGroupConfigurationSQLMock(t *testing.T) (*dbent.Client, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, mock.ExpectationsWereMet())
		_ = db.Close()
	})
	return dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db))), mock
}

func TestAdminUserGroupConfigurationDefaultPatchStates(t *testing.T) {
	for _, tt := range []struct {
		name string
		set  bool
		next *float64
	}{
		{name: "95 percent", set: true, next: adminGroupConfigurationFloat64(95)},
		{name: "explicit zero", set: true, next: adminGroupConfigurationFloat64(0)},
		{name: "explicit null", set: true},
		{name: "omitted", set: false},
		{name: "omitted flag ignores a value", set: false, next: adminGroupConfigurationFloat64(95)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client, mock := newAdminGroupConfigurationSQLMock(t)
			if tt.set {
				mock.ExpectBegin()
				mock.ExpectCommit()
			}
			original := adminGroupConfigurationFloat64(83)
			userRepo := &adminGroupConfigurationUserRepo{userRepoStub: &userRepoStub{user: &User{ID: 42, Email: "u@example.test", DefaultGroupRatePercent: original, AllowedGroups: []int64{7}, GroupRates: map[int64]float64{7: 0.2}, GroupRatePercents: map[int64]float64{7: 50}, RPMLimit: 88}}}
			updates := 0
			userRepo.update = func(ctx context.Context, user *User, fields UserUpdateFields) error {
				updates++
				require.Equal(t, tt.set, fields.DefaultGroupRatePercent)
				require.Equal(t, UserUpdateFields{DefaultGroupRatePercent: tt.set}, fields)
				require.Equal(t, []int64{7}, user.AllowedGroups)
				require.Equal(t, map[int64]float64{7: 0.2}, user.GroupRates)
				require.Equal(t, map[int64]float64{7: 50}, user.GroupRatePercents)
				require.Equal(t, 88, user.RPMLimit)
				if tt.set {
					require.NotNil(t, dbent.TxFromContext(ctx))
					require.Equal(t, tt.next, user.DefaultGroupRatePercent)
				} else {
					require.Nil(t, dbent.TxFromContext(ctx))
					require.Equal(t, original, user.DefaultGroupRatePercent)
				}
				return nil
			}
			invalidator := &adminGroupConfigurationInvalidator{authCacheInvalidatorStub: &authCacheInvalidatorStub{}, notify: func(ctx context.Context, id int64) {
				require.Nil(t, dbent.TxFromContext(ctx))
				require.Equal(t, int64(42), id)
				require.NoError(t, mock.ExpectationsWereMet(), "commit must precede notification")
			}}
			svc := &adminServiceImpl{userRepo: userRepo, entClient: client, authCacheInvalidator: invalidator}
			updated, err := svc.UpdateUser(context.Background(), 42, &UpdateUserInput{DefaultGroupRatePercentSet: tt.set, DefaultGroupRatePercent: tt.next})
			require.NoError(t, err)
			require.NotNil(t, updated)
			require.Equal(t, 1, updates)
			if tt.set {
				require.Equal(t, []int64{42}, invalidator.userIDs)
			} else {
				require.Empty(t, invalidator.userIDs)
			}
		})
	}
}

func TestAdminUserGroupConfigurationRejectsInvalidDefaultBeforeRepository(t *testing.T) {
	for _, value := range []float64{-1, math.NaN(), math.Inf(1), math.Inf(-1)} {
		t.Run(fmtFloatForAdminGroupTest(value), func(t *testing.T) {
			client, _ := newAdminGroupConfigurationSQLMock(t)
			repo := &userRepoStub{getErr: errors.New("unexpected user lookup")}
			invalidator := &authCacheInvalidatorStub{}
			svc := &adminServiceImpl{userRepo: repo, entClient: client, authCacheInvalidator: invalidator}
			updated, err := svc.UpdateUser(context.Background(), 42, &UpdateUserInput{DefaultGroupRatePercentSet: true, DefaultGroupRatePercent: &value})
			require.Nil(t, updated)
			require.Equal(t, "INVALID_DEFAULT_GROUP_RATE_PERCENT", infraerrors.Reason(err))
			require.Empty(t, repo.updated)
			require.Empty(t, invalidator.userIDs)
		})
	}
}

func fmtFloatForAdminGroupTest(value float64) string {
	switch {
	case math.IsNaN(value):
		return "NaN"
	case math.IsInf(value, 1):
		return "positive infinity"
	case math.IsInf(value, -1):
		return "negative infinity"
	default:
		return "negative"
	}
}

func TestAdminUserGroupConfigurationCompoundTransaction(t *testing.T) {
	cacheContainsUser := adminGroupConfigurationCacheProbe(t)
	client, mock := newAdminGroupConfigurationSQLMock(t)
	mock.ExpectBegin()
	mock.ExpectCommit()
	var operations []string
	var sharedTx *dbent.Tx
	percent := adminGroupConfigurationFloat64(90)
	userRepo := &adminGroupConfigurationUserRepo{userRepoStub: &userRepoStub{user: &User{ID: 42, Email: "u@example.test", AllowedGroups: []int64{7}}}}
	userRepo.update = func(ctx context.Context, user *User, fields UserUpdateFields) error {
		sharedTx = dbent.TxFromContext(ctx)
		require.NotNil(t, sharedTx)
		require.True(t, fields.DefaultGroupRatePercent)
		require.True(t, fields.AllowedGroups)
		require.Equal(t, adminGroupConfigurationFloat64(95), user.DefaultGroupRatePercent)
		require.Equal(t, []int64{7, 8}, user.AllowedGroups)
		operations = append(operations, "user")
		return nil
	}
	rateRepo := &adminGroupConfigurationRateRepo{userGroupRateRepoStubForGroupRate: &userGroupRateRepoStubForGroupRate{}}
	rateRepo.percent = func(ctx context.Context, id int64, patch map[int64]*float64) error {
		require.Same(t, sharedTx, dbent.TxFromContext(ctx))
		require.Equal(t, int64(42), id)
		if len(operations) == 1 {
			require.NotNil(t, patch)
			require.Empty(t, patch)
			operations = append(operations, "reset")
		} else {
			require.Equal(t, map[int64]*float64{8: percent, 9: nil}, patch)
			operations = append(operations, "patch")
		}
		return nil
	}
	invalidator := &adminGroupConfigurationInvalidator{authCacheInvalidatorStub: &authCacheInvalidatorStub{}, notify: func(ctx context.Context, id int64) {
		require.Nil(t, dbent.TxFromContext(ctx))
		require.NoError(t, mock.ExpectationsWereMet())
		operations = append(operations, "notify")
	}}
	svc := &adminServiceImpl{userRepo: userRepo, userGroupRateRepo: rateRepo, entClient: client, authCacheInvalidator: invalidator}
	allowedGroups := []int64{7, 8}
	updated, err := svc.UpdateUser(context.Background(), 42, &UpdateUserInput{AllowedGroups: &allowedGroups, DefaultGroupRatePercentSet: true, DefaultGroupRatePercent: adminGroupConfigurationFloat64(95), ResetGroupRatePercents: true, GroupRatePercents: map[int64]*float64{8: percent, 9: nil}})
	require.NoError(t, err)
	require.NotNil(t, updated)
	require.Equal(t, []int64{7, 8}, updated.AllowedGroups)
	require.Equal(t, adminGroupConfigurationFloat64(95), updated.DefaultGroupRatePercent)
	require.Equal(t, []string{"user", "reset", "patch", "notify"}, operations)
	require.Empty(t, rateRepo.syncedRates, "legacy rates must remain omitted")
	require.False(t, cacheContainsUser(), "the committed default must evict every user/group resolver entry")
}

func TestAdminUserGroupConfigurationFailuresRollbackWithoutNotification(t *testing.T) {
	for _, stage := range []string{"begin", "user", "reset", "patch", "commit"} {
		t.Run(stage, func(t *testing.T) {
			cacheContainsUser := adminGroupConfigurationCacheProbe(t)
			client, mock := newAdminGroupConfigurationSQLMock(t)
			failure := errors.New(stage + " failed")
			if stage == "begin" {
				mock.ExpectBegin().WillReturnError(failure)
			} else {
				mock.ExpectBegin()
				if stage == "commit" {
					mock.ExpectCommit().WillReturnError(failure)
				} else {
					mock.ExpectRollback()
				}
			}
			var operations []string
			var sharedTx *dbent.Tx
			rollbackAttempted := false
			userRepo := &adminGroupConfigurationUserRepo{userRepoStub: &userRepoStub{user: &User{ID: 42, Email: "u@example.test"}}}
			userRepo.update = func(ctx context.Context, _ *User, _ UserUpdateFields) error {
				operations = append(operations, "user")
				sharedTx = dbent.TxFromContext(ctx)
				require.NotNil(t, sharedTx)
				sharedTx.OnRollback(func(next dbent.Rollbacker) dbent.Rollbacker {
					return dbent.RollbackFunc(func(ctx context.Context, tx *dbent.Tx) error {
						rollbackAttempted = true
						return next.Rollback(ctx, tx)
					})
				})
				if stage == "user" {
					return failure
				}
				return nil
			}
			rateRepo := &adminGroupConfigurationRateRepo{userGroupRateRepoStubForGroupRate: &userGroupRateRepoStubForGroupRate{}}
			rateRepo.percent = func(ctx context.Context, _ int64, patch map[int64]*float64) error {
				require.Same(t, sharedTx, dbent.TxFromContext(ctx))
				operation := "patch"
				if len(patch) == 0 {
					operation = "reset"
				}
				operations = append(operations, operation)
				if stage == operation {
					return failure
				}
				return nil
			}
			invalidator := &authCacheInvalidatorStub{}
			svc := &adminServiceImpl{userRepo: userRepo, userGroupRateRepo: rateRepo, entClient: client, authCacheInvalidator: invalidator}
			updated, err := svc.UpdateUser(context.Background(), 42, &UpdateUserInput{DefaultGroupRatePercentSet: true, DefaultGroupRatePercent: adminGroupConfigurationFloat64(95), ResetGroupRatePercents: true, GroupRatePercents: map[int64]*float64{8: adminGroupConfigurationFloat64(90)}})
			require.Nil(t, updated)
			require.ErrorIs(t, err, failure)
			require.Empty(t, invalidator.userIDs)
			require.True(t, cacheContainsUser(), "failed transactions must not publish a rate-cache eviction")
			require.Equal(t, stage != "begin", rollbackAttempted, "deferred rollback must also be attempted on commit errors")
			if stage == "begin" {
				require.Empty(t, operations)
			}
			if stage == "user" {
				require.Equal(t, []string{"user"}, operations)
			}
			if stage == "reset" {
				require.Equal(t, []string{"user", "reset"}, operations)
			}
		})
	}
}

func TestAdminUserGroupConfigurationResetWithEmptyPatchRunsOnce(t *testing.T) {
	client, mock := newAdminGroupConfigurationSQLMock(t)
	mock.ExpectBegin()
	mock.ExpectCommit()
	userRepo := &adminGroupConfigurationUserRepo{userRepoStub: &userRepoStub{user: &User{ID: 42, Email: "u@example.test"}}, update: func(ctx context.Context, _ *User, _ UserUpdateFields) error {
		require.NotNil(t, dbent.TxFromContext(ctx))
		return nil
	}}
	resetCalls := 0
	rateRepo := &adminGroupConfigurationRateRepo{userGroupRateRepoStubForGroupRate: &userGroupRateRepoStubForGroupRate{}, percent: func(ctx context.Context, id int64, patch map[int64]*float64) error {
		require.NotNil(t, dbent.TxFromContext(ctx))
		require.Equal(t, int64(42), id)
		require.NotNil(t, patch)
		require.Empty(t, patch)
		resetCalls++
		return nil
	}}
	invalidator := &authCacheInvalidatorStub{}
	svc := &adminServiceImpl{userRepo: userRepo, userGroupRateRepo: rateRepo, entClient: client, authCacheInvalidator: invalidator}
	_, err := svc.UpdateUser(context.Background(), 42, &UpdateUserInput{ResetGroupRatePercents: true, GroupRatePercents: map[int64]*float64{}})
	require.NoError(t, err)
	require.Equal(t, 1, resetCalls)
	require.Equal(t, []int64{42}, invalidator.userIDs)
}

func TestAdminUserGroupConfigurationNewOperationsRequireTransactionAndWriter(t *testing.T) {
	for _, tt := range []struct {
		name          string
		input         UpdateUserInput
		missingWriter bool
	}{
		{name: "default without ent", input: UpdateUserInput{DefaultGroupRatePercentSet: true, DefaultGroupRatePercent: adminGroupConfigurationFloat64(95)}},
		{name: "reset without ent", input: UpdateUserInput{ResetGroupRatePercents: true}},
		{name: "reset without writer", input: UpdateUserInput{ResetGroupRatePercents: true}, missingWriter: true},
		{name: "patch without writer", input: UpdateUserInput{GroupRatePercents: map[int64]*float64{8: adminGroupConfigurationFloat64(90)}}, missingWriter: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			updates := 0
			userRepo := &adminGroupConfigurationUserRepo{userRepoStub: &userRepoStub{user: &User{ID: 42, Email: "u@example.test"}}, update: func(context.Context, *User, UserUpdateFields) error { updates++; return nil }}
			rateRepo := &userGroupRateRepoStubForGroupRate{}
			invalidator := &authCacheInvalidatorStub{}
			svc := &adminServiceImpl{userRepo: userRepo, userGroupRateRepo: rateRepo, authCacheInvalidator: invalidator}
			if tt.missingWriter {
				svc.entClient, _ = newAdminGroupConfigurationSQLMock(t)
				svc.userGroupRateRepo = &adminGroupConfigurationMissingPercentWriter{UserGroupRateRepository: rateRepo}
			}
			updated, err := svc.UpdateUser(context.Background(), 42, &tt.input)
			require.Error(t, err)
			require.Nil(t, updated)
			require.Zero(t, updates)
			require.Zero(t, rateRepo.syncedUserID)
			require.Empty(t, invalidator.userIDs)
		})
	}
}
