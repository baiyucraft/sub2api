package handler

import (
	"context"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

type openAIFixedAccountRetry struct {
	account *service.Account
	failure *service.UpstreamFailoverError
}

type openAIUpstreamAttemptBudget map[int64]struct{}

func (b openAIUpstreamAttemptBudget) allows(accountID int64, maxSwitches int) bool {
	_, retried := b[accountID]
	return retried || len(b) < maxSwitches+1
}

func (b openAIUpstreamAttemptBudget) recordFailure(accountID int64, failure *service.UpstreamFailoverError) {
	if failure != nil && !failure.PluginAdmissionRejected {
		b[accountID] = struct{}{}
	}
}

func (r *openAIFixedAccountRetry) context(ctx context.Context) context.Context {
	if r == nil || r.account == nil {
		return ctx
	}
	return service.WithOpenAIFixedRetryAccount(ctx, r.account.ID)
}

// A failed fixed retry may switch only through the existing upstream budget.
func (h *OpenAIGatewayHandler) abandonFixedAccountRetry(c *gin.Context, retry *openAIFixedAccountRetry, excluded map[int64]struct{}, switches *int, maxSwitches int, oauth429State *service.OpenAIOAuth429FailoverState, log *zap.Logger) bool {
	if retry == nil || retry.account == nil || retry.failure == nil || retry.failure.PluginAdmissionRejected || failoverClientGone(c) {
		return false
	}
	if *switches >= maxSwitches || !allowUpstream429CapacitySwitch(c, h.capacityFailoverProvider, h.cfg, retry.account, retry.failure) {
		return false
	}
	excluded[retry.account.ID] = struct{}{}
	*switches++
	h.gatewayService.RecordOpenAIAccountSwitch()
	service.ReportMonitorSwitchCount(c.Request.Context(), *switches)
	log.Warn("openai.fixed_account_retry_unavailable",
		zap.Int64("account_id", retry.account.ID),
		zap.Int("switch_count", *switches),
		zap.Int("max_switches", maxSwitches),
	)
	return !h.gatewayService.ShouldStopOpenAIOAuth429Failover(retry.account, retry.failure.StatusCode, *switches, oauth429State)
}
