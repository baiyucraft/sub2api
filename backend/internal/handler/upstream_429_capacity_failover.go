package handler

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

const (
	upstream429CapacityStateKey = "upstream_429_capacity_failover_state"
	defaultCapacityRetryAfter   = 5
)

type upstream429CapacityState struct {
	switchCount           int
	shortestRetryAfter    int
	lastAccountID         int64
	lastPlatform          string
	sameAccountRetryCount int
	excludedAccountIDs    map[int64]struct{}
	semanticOutputStarted bool
}

type upstream429CapacityDecision struct {
	statusCode int
	retryAfter int
	state      *upstream429CapacityState
}

func gatewayCapacitySettingsSnapshot(
	ctx context.Context,
	provider service.GatewayCapacityFailoverConfigProvider,
	cfg *config.Config,
) service.GatewayCapacityFailoverSettings {
	if provider != nil {
		return provider.GatewayCapacityFailoverSettingsSnapshot(ctx)
	}
	return service.DefaultGatewayCapacityFailoverSettings(cfg)
}

func getUpstream429CapacityState(c *gin.Context) *upstream429CapacityState {
	if c == nil {
		return nil
	}
	if value, ok := c.Get(upstream429CapacityStateKey); ok {
		if state, ok := value.(*upstream429CapacityState); ok && state != nil {
			return state
		}
	}
	state := &upstream429CapacityState{}
	c.Set(upstream429CapacityStateKey, state)
	return state
}

func bindUpstreamFailoverAccount(c *gin.Context, account *service.Account, failoverErr *service.UpstreamFailoverError) {
	if failoverErr == nil || account == nil {
		return
	}
	failoverErr.BindOriginAccount(account)
	if !failoverErr.IsUpstreamBoundRateLimit() {
		return
	}
	state := getUpstream429CapacityState(c)
	if state == nil {
		return
	}
	if state.lastAccountID != account.ID {
		state.sameAccountRetryCount = 0
	}
	state.lastAccountID = account.ID
	state.lastPlatform = account.Platform
	if retryAfter, ok := parseCapacityRetryAfter(failoverErr.ResponseHeaders); ok &&
		(state.shortestRetryAfter == 0 || retryAfter < state.shortestRetryAfter) {
		state.shortestRetryAfter = retryAfter
	}
}

func noteUpstream429SameAccountRetry(
	c *gin.Context,
	account *service.Account,
	failoverErr *service.UpstreamFailoverError,
	retryCount int,
) {
	bindUpstreamFailoverAccount(c, account, failoverErr)
	if failoverErr == nil || !failoverErr.IsUpstreamBoundRateLimit() || retryCount <= 0 {
		return
	}
	state := getUpstream429CapacityState(c)
	if state != nil && retryCount > state.sameAccountRetryCount {
		state.sameAccountRetryCount = retryCount
	}
}

// allowUpstream429CapacitySwitch applies the shared capacity-failover switch
// budget only when an upstream-bound account is about to be excluded because
// its same-account 429 retries were exhausted. Disabled settings preserve the
// legacy upstream failover behavior.
func allowUpstream429CapacitySwitch(
	c *gin.Context,
	provider service.GatewayCapacityFailoverConfigProvider,
	cfg *config.Config,
	account *service.Account,
	failoverErr *service.UpstreamFailoverError,
) bool {
	bindUpstreamFailoverAccount(c, account, failoverErr)
	if failoverErr == nil || !failoverErr.IsUpstreamBoundRateLimit() {
		return true
	}
	settings := gatewayCapacitySettingsSnapshot(requestContext(c), provider, cfg)
	if !settings.Enabled {
		return true
	}
	state := getUpstream429CapacityState(c)
	if state == nil {
		return true
	}
	if settings.MaxSwitches > 0 && state.switchCount >= settings.MaxSwitches {
		return false
	}
	state.switchCount++
	if state.excludedAccountIDs == nil {
		state.excludedAccountIDs = make(map[int64]struct{})
	}
	state.excludedAccountIDs[account.ID] = struct{}{}
	retryAfter := defaultCapacityRetryAfter
	if state.shortestRetryAfter > 0 {
		retryAfter = state.shortestRetryAfter
	}
	logger.FromContext(requestContext(c)).Warn("gateway.upstream_429_capacity_failover",
		zap.String("capacity_source", "upstream_429"),
		zap.Bool("capacity_failover_enabled", settings.Enabled),
		zap.Int64("account_id", state.lastAccountID),
		zap.String("platform", state.lastPlatform),
		zap.Int("same_account_retry_count", state.sameAccountRetryCount),
		zap.Int("capacity_switch_count", state.switchCount),
		zap.Int("capacity_max_switches", settings.MaxSwitches),
		zap.Int("excluded_account_count", len(state.excludedAccountIDs)),
		zap.Int("retry_after", retryAfter),
		zap.Bool("exhausted", false),
	)
	return true
}

// handleGatewayFailoverError keeps the generic gateway loop unchanged while
// applying the independent upstream-429 capacity switch budget only when the
// existing failover state actually advances to another account.
func (h *GatewayHandler) handleGatewayFailoverError(
	c *gin.Context,
	state *FailoverState,
	account *service.Account,
	failoverErr *service.UpstreamFailoverError,
) FailoverAction {
	bindUpstreamFailoverAccount(c, account, failoverErr)
	if state == nil || account == nil {
		return FailoverExhausted
	}
	beforeSwitches := state.SwitchCount
	beforeSameAccountRetries := state.SameAccountRetryCount[account.ID]
	action := state.HandleFailoverError(
		requestContext(c),
		h.gatewayService,
		account.ID,
		account.Platform,
		account.GetPoolModeRetryCount(),
		failoverErr,
	)
	if retryCount := state.SameAccountRetryCount[account.ID]; retryCount > beforeSameAccountRetries {
		noteUpstream429SameAccountRetry(c, account, failoverErr, retryCount)
	}
	if action == FailoverContinue && state.SwitchCount > beforeSwitches &&
		!allowUpstream429CapacitySwitch(c, h.settingService, h.cfg, account, failoverErr) {
		return FailoverExhausted
	}
	return action
}

func upstream429CapacityExhaustion(
	c *gin.Context,
	provider service.GatewayCapacityFailoverConfigProvider,
	cfg *config.Config,
	failoverErr *service.UpstreamFailoverError,
) (upstream429CapacityDecision, bool) {
	if failoverErr == nil || !failoverErr.IsUpstreamBoundRateLimit() {
		return upstream429CapacityDecision{}, false
	}
	settings := gatewayCapacitySettingsSnapshot(requestContext(c), provider, cfg)
	if !settings.Enabled {
		return upstream429CapacityDecision{}, false
	}
	state := getUpstream429CapacityState(c)
	retryAfter := defaultCapacityRetryAfter
	if state != nil && state.shortestRetryAfter > 0 {
		retryAfter = state.shortestRetryAfter
	} else if parsed, ok := parseCapacityRetryAfter(failoverErr.ResponseHeaders); ok {
		retryAfter = parsed
	}
	if c != nil {
		c.Header("Retry-After", strconv.Itoa(retryAfter))
		markOpsRoutingCapacityLimited(c)
		if state != nil {
			state.semanticOutputStarted = c.Writer != nil && c.Writer.Written() && !gatewayStreamHasOnlyHeartbeats(c)
		}
	}
	upstreamMsg := service.ExtractUpstreamErrorMessage(failoverErr.ResponseBody)
	service.SetOpsUpstreamError(c, failoverErr.StatusCode, upstreamMsg, "")
	decision := upstream429CapacityDecision{
		statusCode: settings.ExhaustedStatusCode,
		retryAfter: retryAfter,
		state:      state,
	}
	fields := []zap.Field{
		zap.String("capacity_source", "upstream_429"),
		zap.Bool("capacity_failover_enabled", settings.Enabled),
		zap.Int("capacity_max_switches", settings.MaxSwitches),
		zap.Int("client_status", decision.statusCode),
		zap.Int("retry_after", retryAfter),
		zap.Bool("exhausted", true),
		zap.Int64("account_id", failoverErr.OriginAccountID),
		zap.String("platform", failoverErr.OriginPlatform),
	}
	if state != nil {
		fields = append(fields,
			zap.Int("same_account_retry_count", state.sameAccountRetryCount),
			zap.Int("capacity_switch_count", state.switchCount),
			zap.Int("excluded_account_count", len(state.excludedAccountIDs)),
			zap.Bool("semantic_output_started", state.semanticOutputStarted),
		)
	}
	logger.FromContext(requestContext(c)).Warn("gateway.upstream_429_capacity_exhausted", fields...)
	return decision, true
}

func parseCapacityRetryAfter(headers http.Header) (int, bool) {
	if headers == nil {
		return 0, false
	}
	value := strings.TrimSpace(headers.Get("Retry-After"))
	if value == "" || len(value) > 128 || strings.ContainsAny(value, "\r\n") || !isSafeRetryAfter(value) {
		return 0, false
	}
	if seconds, err := strconv.ParseUint(value, 10, 32); err == nil {
		if seconds == 0 {
			return 1, true
		}
		return int(seconds), true
	}
	retryAt, err := http.ParseTime(value)
	if err != nil {
		return 0, false
	}
	remaining := time.Until(retryAt)
	if remaining <= 0 {
		return 1, true
	}
	seconds := int((remaining + time.Second - 1) / time.Second)
	return seconds, true
}

func requestContext(c *gin.Context) context.Context {
	if c != nil && c.Request != nil {
		return c.Request.Context()
	}
	return context.Background()
}
