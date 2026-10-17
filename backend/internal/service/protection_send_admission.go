package service

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
)

type schedulingProtectionBeforeSendKey struct{}
type schedulingProtectionSendModelKey struct{}
type schedulingProtectionBeforeSend struct {
	once      sync.Once
	ctx       context.Context
	accountID int64
	commit    func(context.Context) error
	err       error
	committed atomic.Bool
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
	if admission != nil && (account == nil || account.ID != admission.accountID) {
		return fmt.Errorf("scheduling protection send account changed")
	}
	if !schedulingProtectionSendSelected(ctx, account) {
		if admission != nil && !admission.committed.Load() {
			return schedulingProtectionScopeChangedError()
		}
		return nil
	}
	// Freeze synchronous cache admission before committing a migration binding.
	// A budget rejection leaves the source sticky binding untouched.
	if attempt, _ := ctx.Value(cacheUsageAttemptContextKey{}).(*cacheUsageAttempt); attempt != nil {
		attempt.mu.Lock()
		preparedAccount, model, protocol := attempt.account, attempt.model, attempt.protocol
		attempt.mu.Unlock()
		if preparedAccount != nil && model != "" {
			if err := beginUpstreamCacheGuardSend(ctx, preparedAccount, model, protocol); err != nil {
				// A later WS turn is already pinned to its established physical
				// connection. Fail open there without inventing another migration.
				if admission == nil || !admission.committed.Load() {
					return err
				}
				attempt.mu.Lock()
				attempt.beginError = nil
				attempt.mu.Unlock()
			}
		}
	}
	if admission != nil {
		if err := admission.ctx.Err(); err != nil {
			return err
		}
		admission.once.Do(func() {
			if err := admission.ctx.Err(); err != nil {
				admission.err = err
				return
			}
			if !schedulingProtectionSendSelected(ctx, account) {
				admission.err = schedulingProtectionScopeChangedError()
				return
			}
			commitCtx := ctx
			if len(actualModels) > 0 && actualModels[0] != "" {
				commitCtx = context.WithValue(ctx, schedulingProtectionSendModelKey{}, actualModels[0])
			}
			admission.err = admission.commit(commitCtx)
			admission.committed.Store(admission.err == nil)
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
		model := attempt.model
		attempt.mu.Unlock()
		if actualModel == "" {
			actualModel = model
		}
	}
	if actualModel == "" {
		metadata, _ := ctx.Value(pluginRequestMetadataKey{}).(pluginRequestMetadata)
		actualModel = metadata.Model
	}
	if selection, _ := ctx.Value(protectionSelectionKey{}).(*protectionSelection); selection != nil && selection.trial != nil && account != nil {
		ticket := selection.trial
		if actualModel == "" {
			actualModel = selection.allocationModel
		}
		if ticket.key.accountID != account.ID || ticket.key.upstreamKeyID != accountUpstreamKeyID(account) || actualModel == "" || ticket.key.model != normalizeOpenAIAccountModelTransientModel(actualModel) || !ticket.validate() {
			return schedulingProtectionScopeChangedError()
		}
	}
	return nil
}

func schedulingProtectionScopeChangedError() *UpstreamFailoverError {
	// Scope changes happen before any send. Reuse request-local admission
	// reselection so this cannot consume the upstream-failure budget or health.
	return &UpstreamFailoverError{
		PluginAdmissionRejected: true,
		StatusCode:              http.StatusServiceUnavailable,
		Scope:                   GatewayFailureScopeRequest,
		Reason:                  "scheduling_protection_scope_changed",
		ResponseBody:            []byte(`{"error":{"type":"api_error","message":"Scheduling protection settings changed; retry account selection"}}`),
	}
}
