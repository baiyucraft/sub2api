package middleware

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

const deferAPIKeyGroupBillingContextKey = "defer_api_key_group_billing"

// DeferAPIKeyGroupBilling marks matching requests so authentication only
// establishes the API key identity. A post-auth extension may then replace the
// request-scoped group before FinalAPIKeyGroupBilling enforces admission.
func DeferAPIKeyGroupBilling(shouldDefer ...func(*gin.Context) bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		deferBilling := len(shouldDefer) == 0 || shouldDefer[0] == nil || shouldDefer[0](c)
		if deferBilling {
			c.Set(deferAPIKeyGroupBillingContextKey, true)
		}
		c.Next()
	}
}

func shouldDeferAPIKeyGroupBilling(c *gin.Context) bool {
	if c == nil {
		return false
	}
	value, ok := c.Get(deferAPIKeyGroupBillingContextKey)
	deferBilling, _ := value.(bool)
	return ok && deferBilling
}

// NeedsDeferredWebSocketGroupBilling postpones group-dependent admission until
// the first response.create frame reveals the connection group.
func NeedsDeferredWebSocketGroupBilling(c *gin.Context) bool {
	if !shouldDeferAPIKeyGroupBilling(c) || c.Request == nil || c.Request.URL == nil || c.Request.Method != http.MethodGet {
		return false
	}
	switch c.Request.URL.Path {
	case "/v1/responses", "/responses", "/backend-api/codex/responses":
		return true
	default:
		return false
	}
}

// FinalGroupBillingFailure can be rendered as an HTTP error or, after upgrade,
// as a WebSocket policy close without writing a second HTTP response.
type FinalGroupBillingFailure struct {
	Status  int
	Code    string
	Message string
	Quota   bool
}

func (f *FinalGroupBillingFailure) Error() string { return f.Message }

func skipAPIKeyBillingForRequest(c *gin.Context) bool {
	if c == nil || c.Request == nil || c.Request.URL == nil {
		return false
	}
	path := c.Request.URL.Path
	return path == "/v1/usage" || path == "/v1/sub2api/billing" || isAsyncImageTaskRead(c.Request.Method, path)
}

func setDeferredAuthenticatedAPIKeyContext(c *gin.Context, apiKey *service.APIKey, apiKeyService *service.APIKeyService, billingInfoRequest bool) {
	ctx := context.WithValue(c.Request.Context(), ctxkey.UserID, apiKey.User.ID)
	c.Request = c.Request.WithContext(ctx)
	c.Set(string(ContextKeyAPIKey), apiKey)
	attachManagedMonitorSwitchReporter(c, apiKey)
	c.Set(string(ContextKeyUser), AuthSubject{UserID: apiKey.User.ID, Concurrency: apiKey.User.Concurrency})
	c.Set(string(ContextKeyUserRole), apiKey.User.Role)
	setGroupContext(c, apiKey.Group)
	if !billingInfoRequest {
		_ = apiKeyService.TouchLastUsed(c.Request.Context(), apiKey.ID)
	}
}

// FinalAPIKeyGroupBilling enforces the same group, API key and billing checks
// as authentication, but against the final request-scoped group selected by
// post-auth extensions.
func FinalAPIKeyGroupBilling(subscriptionService *service.SubscriptionService, cfg *config.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		if NeedsDeferredWebSocketGroupBilling(c) {
			c.Next()
			return
		}
		failure := ValidateFinalAPIKeyGroupBilling(c, subscriptionService, cfg)
		if failure != nil {
			if failure.Quota {
				abortFinalAPIKeyQuotaError(c)
			} else {
				abortFinalGroupBilling(c, failure.Status, failure.Code, failure.Message)
			}
			return
		}
		c.Next()
	}
}

// ValidateFinalAPIKeyGroupBilling performs final-group admission without
// writing a response, so the first WebSocket turn can reuse the HTTP checks.
func ValidateFinalAPIKeyGroupBilling(c *gin.Context, subscriptionService *service.SubscriptionService, cfg *config.Config) *FinalGroupBillingFailure {
	if cfg == nil || cfg.RunMode == config.RunModeSimple || skipAPIKeyBillingForRequest(c) {
		return nil
	}
	apiKey, ok := GetAPIKeyFromContext(c)
	if !ok || apiKey == nil || apiKey.User == nil {
		return &FinalGroupBillingFailure{http.StatusUnauthorized, "INVALID_API_KEY", "Invalid API key", false}
	}
	if code, message, available := validateAPIKeyGroupAvailable(apiKey); !available {
		service.MarkOpsClientBusinessLimited(c, service.OpsClientBusinessLimitedReasonAPIKeyGroupUnavailable)
		if code == "GROUP_DELETED" {
			MarkIngressRejected(c, IngressRejectGroupDeleted)
		} else {
			MarkIngressRejected(c, IngressRejectGroupDisabled)
		}
		return &FinalGroupBillingFailure{http.StatusForbidden, code, message, false}
	}
	if !validateAPIKeyGroupAllowed(apiKey) {
		service.MarkOpsClientBusinessLimited(c, service.OpsClientBusinessLimitedReasonAPIKeyGroupUnavailable)
		MarkIngressRejected(c, IngressRejectGroupNotAllowed)
		return &FinalGroupBillingFailure{http.StatusForbidden, "GROUP_NOT_ALLOWED", "API Key 所属专属分组不再允许当前用户使用", false}
	}

	switch apiKey.Status {
	case service.StatusAPIKeyQuotaExhausted:
		return &FinalGroupBillingFailure{http.StatusTooManyRequests, "API_KEY_QUOTA_EXHAUSTED", "API key 额度已用完", true}
	case service.StatusAPIKeyExpired:
		return &FinalGroupBillingFailure{http.StatusForbidden, "API_KEY_EXPIRED", "API key 已过期", false}
	}
	if apiKey.IsExpired() {
		return &FinalGroupBillingFailure{http.StatusForbidden, "API_KEY_EXPIRED", "API key 已过期", false}
	}
	if apiKey.IsQuotaExhausted() {
		return &FinalGroupBillingFailure{http.StatusTooManyRequests, "API_KEY_QUOTA_EXHAUSTED", "API key 额度已用完", true}
	}

	if apiKey.Group != nil && apiKey.Group.IsSubscriptionType() && subscriptionService != nil {
		subscription, ok := GetSubscriptionFromContext(c)
		if !ok || subscription == nil || subscription.UserID != apiKey.User.ID || subscription.GroupID != apiKey.Group.ID {
			var err error
			subscription, err = subscriptionService.GetActiveSubscription(c.Request.Context(), apiKey.User.ID, apiKey.Group.ID)
			if err != nil {
				return &FinalGroupBillingFailure{http.StatusForbidden, "SUBSCRIPTION_NOT_FOUND", "No active subscription found for this group", false}
			}
		}
		needsMaintenance, validateErr := subscriptionService.ValidateAndCheckLimits(subscription, apiKey.Group)
		if needsMaintenance {
			refreshed, maintenanceErr := subscriptionService.EnsureWindowMaintenance(c.Request.Context(), subscription)
			if maintenanceErr != nil {
				return &FinalGroupBillingFailure{http.StatusInternalServerError, "SUBSCRIPTION_MAINTENANCE_FAILED", "Failed to maintain subscription usage windows", false}
			}
			subscription = refreshed
			_, validateErr = subscriptionService.ValidateAndCheckLimits(subscription, apiKey.Group)
		}
		if validateErr != nil {
			status, code := http.StatusForbidden, "SUBSCRIPTION_INVALID"
			if errors.Is(validateErr, service.ErrDailyLimitExceeded) || errors.Is(validateErr, service.ErrWeeklyLimitExceeded) || errors.Is(validateErr, service.ErrMonthlyLimitExceeded) {
				status, code = http.StatusTooManyRequests, "USAGE_LIMIT_EXCEEDED"
			}
			return &FinalGroupBillingFailure{status, code, validateErr.Error(), false}
		}
		c.Set(string(ContextKeySubscription), subscription)
	} else {
		c.Set(string(ContextKeySubscription), nil)
		if apiKeyBalanceBelowAuthThreshold(apiKey.User.Balance, cfg) {
			return &FinalGroupBillingFailure{http.StatusForbidden, "INSUFFICIENT_BALANCE", "Insufficient account balance", false}
		}
	}
	return nil
}

func abortFinalAPIKeyQuotaError(c *gin.Context) {
	const message = "API key 额度已用完"
	if c != nil && c.Request != nil && c.Request.URL != nil {
		path := c.Request.URL.Path
		if strings.HasPrefix(path, "/v1beta") || strings.HasPrefix(path, "/antigravity/v1beta") {
			abortWithGoogleError(c, http.StatusTooManyRequests, message)
			return
		}
	}
	abortWithAPIKeyQuotaError(c)
}

func abortFinalGroupBilling(c *gin.Context, status int, code, message string) {
	AbortWithRequestError(c, status, code, message)
}

// AbortWithRequestError preserves the native Google error envelope on Gemini
// endpoints while keeping the existing gateway error shape elsewhere.
func AbortWithRequestError(c *gin.Context, status int, code, message string) {
	if c != nil && c.Request != nil && c.Request.URL != nil {
		path := c.Request.URL.Path
		if strings.HasPrefix(path, "/v1beta") || strings.HasPrefix(path, "/antigravity/v1beta") {
			abortWithGoogleError(c, status, message)
			return
		}
	}
	AbortWithError(c, status, code, message)
}
