package service

import (
	"context"
	"errors"

	dbent "github.com/Wei-Shaw/sub2api/ent"
)

// persistAdminUserGroupConfiguration keeps permissions, the inherited default,
// and explicit overrides in the same transaction. Notifications happen only
// after commit; the database outbox handles delivery to the other instances.
func (s *adminServiceImpl) persistAdminUserGroupConfiguration(ctx context.Context, user *User, fields UserUpdateFields, input *UpdateUserInput) (resultErr error) {
	rateChanged := input.DefaultGroupRatePercentSet || input.ResetGroupRatePercents || input.GroupRates != nil || input.GroupRatePercents != nil
	var percentWriter UserGroupRatePercentWriter
	if input.ResetGroupRatePercents || input.GroupRatePercents != nil {
		var ok bool
		percentWriter, ok = s.userGroupRateRepo.(UserGroupRatePercentWriter)
		if !ok {
			return errors.New("user group rate percent storage is unavailable")
		}
	}
	if input.GroupRates != nil && s.userGroupRateRepo == nil {
		return errors.New("user group rate storage is unavailable")
	}

	opCtx := ctx
	var tx *dbent.Tx
	if rateChanged && s.entClient != nil {
		var err error
		tx, err = s.entClient.Tx(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		opCtx = dbent.NewTxContext(ctx, tx)
	} else if input.DefaultGroupRatePercentSet || input.ResetGroupRatePercents {
		// The new compound operation may never fall back to partial saves.
		return errors.New("transactional user group configuration is unavailable")
	}
	if err := s.userRepo.Update(opCtx, user, fields); err != nil {
		return err
	}
	defer func() {
		// Legacy non-transactional test/extension repositories can have already
		// saved user fields on an override failure. Keep their old eviction rule.
		if tx == nil && rateChanged && resultErr != nil {
			InvalidateUserGroupRateCachesByUser(user.ID)
			if s.authCacheInvalidator != nil {
				s.authCacheInvalidator.InvalidateAuthCacheByUserID(ctx, user.ID)
			}
		}
	}()
	if input.ResetGroupRatePercents {
		if err := percentWriter.SyncUserGroupRatePercents(opCtx, user.ID, map[int64]*float64{}); err != nil {
			return err
		}
	}
	if input.GroupRates != nil {
		if err := s.userGroupRateRepo.SyncUserGroupRates(opCtx, user.ID, input.GroupRates); err != nil {
			return err
		}
	}
	if input.GroupRatePercents != nil && !(input.ResetGroupRatePercents && len(input.GroupRatePercents) == 0) {
		if err := percentWriter.SyncUserGroupRatePercents(opCtx, user.ID, input.GroupRatePercents); err != nil {
			return err
		}
	}
	if tx != nil {
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	if rateChanged {
		InvalidateUserGroupRateCachesByUser(user.ID)
		if s.authCacheInvalidator != nil {
			s.authCacheInvalidator.InvalidateAuthCacheByUserID(ctx, user.ID)
		}
	}
	return nil
}
