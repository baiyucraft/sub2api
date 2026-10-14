package service

import (
	"context"
	"fmt"
)

// WithGatewaySchedulingProtectionMigration leaves an existing binding intact
// until local request preparation and all pre-send admission checks have passed.
func (s *GatewayService) WithGatewaySchedulingProtectionMigration(ctx context.Context, groupID *int64, sessionHash string, selection *AccountSelectionResult) context.Context {
	if selection == nil || !selection.IsSchedulingProtectionMigration() {
		return WithSchedulingProtectionBeforeSend(ctx, 0, nil)
	}
	account := selection.Account
	return WithSchedulingProtectionBeforeSend(ctx, account.ID, func(sendCtx context.Context) error {
		latest, vetoed, reason := s.GatewayProfitControlVetoLatest(sendCtx, account)
		if vetoed {
			return fmt.Errorf("scheduling protection profit admission rejected: %s", reason)
		}
		if latest == nil || accountUpstreamKeyID(latest) != accountUpstreamKeyID(account) {
			return fmt.Errorf("scheduling protection upstream key changed before send")
		}
		return s.BindStickySessionAfterProfitAdmission(sendCtx, groupID, sessionHash, account.ID)
	})
}

// The existing metadata preparation is provider-independent. Freeze the cohort
// only after the migration callback succeeds at the actual transport boundary.
func beginGatewaySchedulingProtectionSend(ctx context.Context, account *Account, model, protocol string) error {
	PrepareOpenAIUpstreamCacheGuardSend(ctx, account, model, protocol)
	if err := runSchedulingProtectionBeforeSend(ctx, account, model); err != nil {
		return err
	}
	return BeginUpstreamCacheGuardSend(ctx, account, model, protocol)
}
