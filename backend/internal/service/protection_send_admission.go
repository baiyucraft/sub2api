package service

import (
	"context"
	"fmt"
	"sync"
)

type schedulingProtectionBeforeSendKey struct{}
type schedulingProtectionBeforeSend struct {
	once      sync.Once
	ctx       context.Context
	accountID int64
	commit    func(context.Context) error
	err       error
}

// WithSchedulingProtectionBeforeSend freezes one migration's final binding.
// Retries on the same account and WS turns share its once-only send boundary.
// Passing no callback removes an earlier attempt's callback from a reused ctx.
func WithSchedulingProtectionBeforeSend(ctx context.Context, accountID int64, commit func(context.Context) error) context.Context {
	if ctx == nil {
		return ctx
	}
	if accountID <= 0 || commit == nil {
		if ctx.Value(schedulingProtectionBeforeSendKey{}) == nil {
			return ctx
		}
		return context.WithValue(ctx, schedulingProtectionBeforeSendKey{}, (*schedulingProtectionBeforeSend)(nil))
	}
	return context.WithValue(ctx, schedulingProtectionBeforeSendKey{}, &schedulingProtectionBeforeSend{ctx: ctx, accountID: accountID, commit: commit})
}

func runSchedulingProtectionBeforeSend(ctx context.Context, account *Account, actualModels ...string) error {
	if ctx == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	admission, _ := ctx.Value(schedulingProtectionBeforeSendKey{}).(*schedulingProtectionBeforeSend)
	if admission != nil {
		if err := admission.ctx.Err(); err != nil {
			return err
		}
		if account == nil || account.ID != admission.accountID {
			return fmt.Errorf("scheduling protection send account changed")
		}
		admission.once.Do(func() {
			if err := admission.ctx.Err(); err != nil {
				admission.err = err
				return
			}
			admission.err = admission.commit(admission.ctx)
		})
		if admission.err != nil {
			return admission.err
		}
	}
	actualModel := ""
	if len(actualModels) > 0 {
		actualModel = actualModels[0]
	}
	if attempt, _ := ctx.Value(cacheUsageAttemptContextKey{}).(*cacheUsageAttempt); attempt != nil {
		attempt.mu.Lock()
		preparedAccount, model, protocol := attempt.account, attempt.model, attempt.protocol
		attempt.mu.Unlock()
		if actualModel == "" {
			actualModel = model
		}
		if preparedAccount != nil && model != "" {
			beginUpstreamCacheGuardSend(ctx, preparedAccount, model, protocol)
		}
	}
	if actualModel == "" {
		metadata, _ := ctx.Value(pluginRequestMetadataKey{}).(pluginRequestMetadata)
		actualModel = metadata.Model
	}
	if selection, _ := ctx.Value(protectionSelectionKey{}).(*protectionSelection); selection != nil && selection.trial != nil && account != nil {
		ticket := selection.trial
		if ticket.key.accountID == account.ID && ticket.key.upstreamKeyID == accountUpstreamKeyID(account) && actualModel != "" && ticket.key.model == normalizeOpenAIAccountModelTransientModel(actualModel) {
			ticket.sent.Store(true)
		}
	}
	return nil
}
