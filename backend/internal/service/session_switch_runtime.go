package service

import (
	"context"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"go.uber.org/zap"
)

// SessionSwitchRuntime is the gateway-facing contract for session-local
// account cooldowns. Implementations must fail open when settings or Redis are
// unavailable so this optional preference never blocks ordinary routing.
type SessionSwitchRuntime interface {
	SessionSwitchExcludedAccountIDs(ctx context.Context, apiKey *APIKey, groupID *int64, sessionHash, routeModel string) map[int64]struct{}
	RecordSessionSwitchFailure(ctx context.Context, apiKey *APIKey, groupID *int64, sessionHash, routeModel string, accountID int64, statusCode int) bool
	ClearSessionSwitchFailures(ctx context.Context, apiKey *APIKey, groupID *int64, sessionHash, routeModel string, accountID int64)
}

type sessionSwitchRuntime struct {
	cache               GatewayCache
	settingService      *SettingService
	deleteStickySession func(context.Context, int64, string) error
}

func (s *GatewayService) sessionSwitchRuntime() sessionSwitchRuntime {
	if s == nil {
		return sessionSwitchRuntime{}
	}
	return sessionSwitchRuntime{cache: s.cache, settingService: s.settingService}
}

func (s *OpenAIGatewayService) sessionSwitchRuntime() sessionSwitchRuntime {
	if s == nil {
		return sessionSwitchRuntime{}
	}
	return sessionSwitchRuntime{
		cache:          s.cache,
		settingService: s.settingService,
		deleteStickySession: func(ctx context.Context, groupID int64, sessionHash string) error {
			return s.deleteStickySessionAccountID(ctx, &groupID, sessionHash)
		},
	}
}

func (s *GatewayService) SessionSwitchExcludedAccountIDs(ctx context.Context, apiKey *APIKey, groupID *int64, sessionHash, routeModel string) map[int64]struct{} {
	return s.sessionSwitchRuntime().excludedAccountIDs(ctx, apiKey, groupID, sessionHash, routeModel)
}

func (s *OpenAIGatewayService) SessionSwitchExcludedAccountIDs(ctx context.Context, apiKey *APIKey, groupID *int64, sessionHash, routeModel string) map[int64]struct{} {
	return s.sessionSwitchRuntime().excludedAccountIDs(ctx, apiKey, groupID, sessionHash, routeModel)
}

func (s *GatewayService) RecordSessionSwitchFailure(ctx context.Context, apiKey *APIKey, groupID *int64, sessionHash, routeModel string, accountID int64, statusCode int) bool {
	return s.sessionSwitchRuntime().recordFailure(ctx, apiKey, groupID, sessionHash, routeModel, accountID, statusCode)
}

func (s *OpenAIGatewayService) RecordSessionSwitchFailure(ctx context.Context, apiKey *APIKey, groupID *int64, sessionHash, routeModel string, accountID int64, statusCode int) bool {
	return s.sessionSwitchRuntime().recordFailure(ctx, apiKey, groupID, sessionHash, routeModel, accountID, statusCode)
}

func (s *GatewayService) ClearSessionSwitchFailures(ctx context.Context, apiKey *APIKey, groupID *int64, sessionHash, routeModel string, accountID int64) {
	s.sessionSwitchRuntime().clearFailures(ctx, apiKey, groupID, sessionHash, routeModel, accountID)
}

func (s *OpenAIGatewayService) ClearSessionSwitchFailures(ctx context.Context, apiKey *APIKey, groupID *int64, sessionHash, routeModel string, accountID int64) {
	s.sessionSwitchRuntime().clearFailures(ctx, apiKey, groupID, sessionHash, routeModel, accountID)
}

func (r sessionSwitchRuntime) scope(apiKey *APIKey, groupID *int64, sessionHash, routeModel string) (SessionSwitchScope, SessionSwitchStateStore, bool) {
	if apiKey == nil || groupID == nil || *groupID <= 0 || apiKey.ID <= 0 {
		return SessionSwitchScope{}, nil, false
	}
	mode, err := NormalizeAPIKeySchedulingMode(apiKey.SchedulingMode)
	if err != nil || mode != APIKeySchedulingModeSpeedFirst {
		return SessionSwitchScope{}, nil, false
	}
	sessionHash = strings.TrimSpace(sessionHash)
	routeModel = strings.TrimSpace(routeModel)
	if sessionHash == "" || routeModel == "" {
		return SessionSwitchScope{}, nil, false
	}
	store, ok := r.cache.(SessionSwitchStateStore)
	if !ok || store == nil {
		return SessionSwitchScope{}, nil, false
	}
	return SessionSwitchScope{
		APIKeyID: apiKey.ID, GroupID: *groupID, SessionHash: sessionHash, RouteModel: routeModel,
	}, store, true
}

func (r sessionSwitchRuntime) settings(ctx context.Context) (SessionSwitchSettings, bool) {
	if r.settingService == nil {
		return SessionSwitchSettings{}, false
	}
	settings, ok := r.settingService.GetSessionSwitchSettingsSnapshot(ctx)
	if !ok {
		logger.FromContext(ctx).Warn("gateway.session_switch_settings_unavailable")
		return SessionSwitchSettings{}, false
	}
	return settings, true
}

func (r sessionSwitchRuntime) excludedAccountIDs(ctx context.Context, apiKey *APIKey, groupID *int64, sessionHash, routeModel string) map[int64]struct{} {
	scope, store, ok := r.scope(apiKey, groupID, sessionHash, routeModel)
	if !ok {
		return nil
	}
	if _, ok := r.settings(ctx); !ok {
		return nil
	}
	accountIDs, err := store.ListSessionSwitchCooldownAccountIDs(ctx, scope)
	if err != nil {
		logger.FromContext(ctx).Warn("gateway.session_switch_cooldown_read_failed",
			zap.Int64("api_key_id", scope.APIKeyID), zap.Int64("group_id", scope.GroupID),
			zap.String("model", scope.RouteModel), zap.Error(err),
		)
		return nil
	}
	if len(accountIDs) == 0 {
		return nil
	}
	excluded := make(map[int64]struct{}, len(accountIDs))
	for _, accountID := range accountIDs {
		if accountID > 0 {
			excluded[accountID] = struct{}{}
		}
	}
	return excluded
}

func (r sessionSwitchRuntime) recordFailure(ctx context.Context, apiKey *APIKey, groupID *int64, sessionHash, routeModel string, accountID int64, statusCode int) bool {
	ctx = WithSessionSwitchStickyScope(ctx, apiKey)
	scope, store, ok := r.scope(apiKey, groupID, sessionHash, routeModel)
	if !ok || accountID <= 0 {
		return false
	}
	settings, ok := r.settings(ctx)
	if !ok || !sessionSwitchStatusCodeEnabled(settings.StatusCodes, statusCode) {
		return false
	}
	state, err := store.RecordSessionSwitchFailure(ctx, SessionSwitchAccountScope{
		SessionSwitchScope: scope,
		AccountID:          accountID,
	}, time.Duration(settings.WindowSeconds)*time.Second, settings.FailureThreshold, time.Duration(settings.CooldownSeconds)*time.Second)
	if err != nil {
		logger.FromContext(ctx).Warn("gateway.session_switch_failure_record_failed",
			zap.Int64("api_key_id", scope.APIKeyID), zap.Int64("group_id", scope.GroupID),
			zap.String("model", scope.RouteModel), zap.Int64("account_id", accountID),
			zap.Int("upstream_status", statusCode), zap.Error(err),
		)
		return false
	}
	logger.FromContext(ctx).Info("gateway.session_switch_failure_recorded",
		zap.Int64("api_key_id", scope.APIKeyID), zap.Int64("group_id", scope.GroupID),
		zap.String("model", scope.RouteModel), zap.Int64("account_id", accountID),
		zap.Int("upstream_status", statusCode), zap.Int64("failure_count", state.FailureCount),
		zap.Bool("tripped", state.Tripped),
	)
	if !state.Tripped {
		return false
	}
	deleteStickySession := r.deleteStickySession
	if deleteStickySession == nil && r.cache != nil {
		deleteStickySession = func(ctx context.Context, groupID int64, sessionHash string) error {
			return r.cache.DeleteSessionAccountID(WithSessionSwitchStickyOperation(ctx), groupID, sessionHash)
		}
	}
	if deleteStickySession != nil {
		if err := deleteStickySession(ctx, scope.GroupID, scope.SessionHash); err != nil {
			logger.FromContext(ctx).Warn("gateway.session_switch_sticky_delete_failed",
				zap.Int64("api_key_id", scope.APIKeyID), zap.Int64("group_id", scope.GroupID),
				zap.String("model", scope.RouteModel), zap.Int64("account_id", accountID), zap.Error(err),
			)
		}
	}
	logger.FromContext(ctx).Warn("gateway.session_switch_account_cooled_down",
		zap.Int64("api_key_id", scope.APIKeyID), zap.Int64("group_id", scope.GroupID),
		zap.String("model", scope.RouteModel), zap.Int64("account_id", accountID),
		zap.Int("upstream_status", statusCode), zap.Time("cooldown_until", state.CooldownUntil),
		zap.String("reason", "session_failure_threshold_reached"),
	)
	return true
}

func (r sessionSwitchRuntime) clearFailures(ctx context.Context, apiKey *APIKey, groupID *int64, sessionHash, routeModel string, accountID int64) {
	scope, store, ok := r.scope(apiKey, groupID, sessionHash, routeModel)
	if !ok || accountID <= 0 {
		return
	}
	if err := store.ClearSessionSwitchFailures(ctx, SessionSwitchAccountScope{
		SessionSwitchScope: scope,
		AccountID:          accountID,
	}); err != nil {
		logger.FromContext(ctx).Warn("gateway.session_switch_failure_clear_failed",
			zap.Int64("api_key_id", scope.APIKeyID), zap.Int64("group_id", scope.GroupID),
			zap.String("model", scope.RouteModel), zap.Int64("account_id", accountID), zap.Error(err),
		)
	}
}

func sessionSwitchStatusCodeEnabled(statusCodes []int, statusCode int) bool {
	for _, configured := range statusCodes {
		if configured == statusCode {
			return true
		}
	}
	return false
}

var _ SessionSwitchRuntime = (*GatewayService)(nil)
var _ SessionSwitchRuntime = (*OpenAIGatewayService)(nil)
