package service

import (
	"context"
	"errors"
)

type openAIFixedRetryAccountKey struct{}

var ErrOpenAIFixedRetryUnavailable = errors.New("fixed OpenAI retry account is unavailable")

// WithOpenAIFixedRetryAccount constrains a single selection attempt, not a session.
func WithOpenAIFixedRetryAccount(ctx context.Context, accountID int64) context.Context {
	return context.WithValue(ctx, openAIFixedRetryAccountKey{}, accountID)
}

func openAIFixedRetryAccountID(ctx context.Context) int64 {
	if ctx == nil {
		return 0
	}
	id, _ := ctx.Value(openAIFixedRetryAccountKey{}).(int64)
	return id
}

func (s *OpenAIGatewayService) selectFixedOpenAIRetryAccount(ctx context.Context, req OpenAIAccountScheduleRequest, accountID int64) (*AccountSelectionResult, OpenAIAccountScheduleDecision, error) {
	decision := OpenAIAccountScheduleDecision{Layer: "same_account_retry"}
	if err := ctx.Err(); err != nil {
		return nil, decision, err
	}
	if s.checkChannelPricingRestriction(ctx, req.GroupID, req.RequestedModel) {
		return nil, decision, ErrOpenAIFixedRetryUnavailable
	}
	req.StickyAccountID = accountID
	req.PreserveStickyBinding = true
	req.DisableStickyEscape = true
	// The explicit account ID is authoritative; this value is never cached.
	if req.SessionHash == "" {
		req.SessionHash = "fixed-account-retry"
	}
	scheduler := &defaultOpenAIAccountScheduler{service: s, stats: newOpenAIAccountRuntimeStats()}
	selection, _, err := scheduler.selectBySessionHash(ctx, req)
	if err != nil {
		return nil, decision, errors.Join(ErrOpenAIFixedRetryUnavailable, err)
	}
	if selection == nil || selection.Account == nil || !selection.Acquired {
		if selection != nil && selection.ReleaseFunc != nil {
			selection.ReleaseFunc()
		}
		return nil, decision, ErrOpenAIFixedRetryUnavailable
	}
	decision.SelectedAccountID = selection.Account.ID
	decision.SelectedAccountType = selection.Account.Type
	return selection, decision, nil
}
