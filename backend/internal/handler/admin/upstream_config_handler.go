package admin

import (
	"encoding/json"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/util/logredact"
	"github.com/gin-gonic/gin"
)

type UpstreamConfigHandler struct {
	service *service.UpstreamConfigService
}

func NewUpstreamConfigHandler(service *service.UpstreamConfigService) *UpstreamConfigHandler {
	return &UpstreamConfigHandler{service: service}
}

type upstreamConfigRequest struct {
	Name                              string         `json:"name"`
	Provider                          string         `json:"provider"`
	SiteURL                           string         `json:"site_url"`
	APIURL                            *string        `json:"api_url"`
	ClearAPIURL                       bool           `json:"clear_api_url"`
	AuthMode                          string         `json:"auth_mode"`
	Credentials                       map[string]any `json:"credentials"`
	Extra                             map[string]any `json:"extra"`
	SchedulerConcurrencyOverride      *int           `json:"scheduler_concurrency_override"`
	ClearSchedulerConcurrencyOverride bool           `json:"clear_scheduler_concurrency_override"`
	ProxyID                           *int64         `json:"proxy_id"`
	ClearProxy                        bool           `json:"clear_proxy"`
	RechargeRate                      *float64       `json:"recharge_rate"`
	BalanceToCNYRate                  *float64       `json:"balance_to_cny_rate"`
	ClearBalanceToCNYRate             bool           `json:"clear_balance_to_cny_rate"`
	SchedulingEnabled                 *bool          `json:"scheduling_enabled"`
	Status                            string         `json:"status"`
}

type upstreamSchedulingRequest struct {
	SchedulingEnabled *bool `json:"scheduling_enabled" binding:"required"`
}

type upstreamSettingsRequest struct {
	BalanceLowThresholdCNY  float64 `json:"balance_low_threshold_cny"`
	Sub2APINotInCNConfirmed bool    `json:"sub2api_not_in_cn_confirmed"`
	CostIncludedGroupIDs    []int64 `json:"cost_included_group_ids"`
}

type updateUpstreamKeyPlatformRequest struct {
	Platform             string    `json:"platform" binding:"required"`
	ExpectedUpdatedAt    time.Time `json:"expected_updated_at" binding:"required"`
	DisableBoundAccounts bool      `json:"disable_bound_accounts"`
}

type updateUpstreamKeyBaseURLRequest struct {
	BaseURL           *string   `json:"base_url"`
	ClearBaseURL      bool      `json:"clear_base_url"`
	ExpectedUpdatedAt time.Time `json:"expected_updated_at" binding:"required"`
}

type upstreamConfigDeleteRequest struct {
	DeleteSyncManagedAccounts bool `json:"delete_sync_managed_accounts"`
}

func (h *UpstreamConfigHandler) Dashboard(c *gin.Context) {
	window := service.UpstreamDashboardWindow(c.DefaultQuery("window", string(service.UpstreamDashboardWindow24h)))
	result, err := h.service.GetUpstreamDashboard(c.Request.Context(), service.UpstreamDashboardFilter{Window: window, Provider: c.Query("provider"), Status: c.Query("status"), Search: c.Query("search"), Now: time.Now().UTC()})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

func (h *UpstreamConfigHandler) DashboardDetail(c *gin.Context) {
	id, ok := parseUpstreamIDParam(c, "id")
	if !ok {
		return
	}
	result, err := h.service.GetUpstreamDashboardDetail(c.Request.Context(), id, service.UpstreamDashboardFilter{Window: service.UpstreamDashboardWindow(c.DefaultQuery("window", "24h")), Now: time.Now().UTC()})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

func (h *UpstreamConfigHandler) List(c *gin.Context) {
	page, pageSize := response.ParsePagination(c)
	threshold := h.upstreamBalanceThreshold(c)
	configs, total, err := h.service.List(c.Request.Context(), pagination.PaginationParams{
		Page:      page,
		PageSize:  pageSize,
		SortBy:    c.Query("sort_by"),
		SortOrder: c.Query("sort_order"),
	}, service.UpstreamConfigListFilter{
		Provider:             c.Query("provider"),
		Status:               c.Query("status"),
		Search:               c.Query("search"),
		BalanceSortAvailable: threshold > 0,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Paginated(c, sanitizeUpstreamConfigsWithThreshold(configs, threshold), total.Total, page, pageSize)
}

func (h *UpstreamConfigHandler) GetByID(c *gin.Context) {
	id, ok := parseUpstreamIDParam(c, "id")
	if !ok {
		return
	}
	config, err := h.service.GetByID(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, sanitizeUpstreamConfigWithThreshold(config, h.upstreamBalanceThreshold(c)))
}

func (h *UpstreamConfigHandler) Create(c *gin.Context) {
	var req upstreamConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	created, err := h.service.Create(c.Request.Context(), upstreamConfigFromRequest(req))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, sanitizeUpstreamConfig(created))
}

func (h *UpstreamConfigHandler) Update(c *gin.Context) {
	id, ok := parseUpstreamIDParam(c, "id")
	if !ok {
		return
	}
	var req upstreamConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	updated, err := h.service.Update(c.Request.Context(), id, upstreamConfigFromRequest(req))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, sanitizeUpstreamConfig(updated))
}

func (h *UpstreamConfigHandler) UpdateScheduling(c *gin.Context) {
	id, ok := parseUpstreamIDParam(c, "id")
	if !ok {
		return
	}
	var req upstreamSchedulingRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.SchedulingEnabled == nil {
		response.BadRequest(c, "Invalid request: scheduling_enabled is required")
		return
	}
	updated, err := h.service.SetSchedulingEnabled(c.Request.Context(), id, *req.SchedulingEnabled)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, sanitizeUpstreamConfig(updated))
}

func (h *UpstreamConfigHandler) Delete(c *gin.Context) {
	id, ok := parseUpstreamIDParam(c, "id")
	if !ok {
		return
	}
	request := upstreamConfigDeleteRequest{}
	if c.Request.Body != nil {
		var decoded *upstreamConfigDeleteRequest
		decoder := json.NewDecoder(c.Request.Body)
		if err := decoder.Decode(&decoded); err != nil {
			if err != io.EOF {
				response.BadRequest(c, "Invalid request: "+err.Error())
				return
			}
		} else if decoded == nil {
			response.BadRequest(c, "Invalid request: expected a JSON object")
			return
		} else {
			request = *decoded
		}
		if err := decoder.Decode(&struct{}{}); err != io.EOF {
			if err == nil {
				response.BadRequest(c, "Invalid request: only one JSON object is allowed")
			} else {
				response.BadRequest(c, "Invalid request: "+err.Error())
			}
			return
		}
	}
	result, err := h.service.DeleteWithOptions(c.Request.Context(), id, service.UpstreamConfigDeleteOptions{
		DeleteSyncManagedAccounts: request.DeleteSyncManagedAccounts,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{
		"message":               "upstream config deleted",
		"deleted_account_count": result.DeletedAccountCount,
		"deleted_key_count":     result.DeletedKeyCount,
	})
}

func (h *UpstreamConfigHandler) GetAuthSession(c *gin.Context) {
	id, ok := parseUpstreamIDParam(c, "id")
	if !ok {
		return
	}
	status, err := h.service.GetAuthSessionStatus(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, status)
}

func (h *UpstreamConfigHandler) ClearAuthSession(c *gin.Context) {
	id, ok := parseUpstreamIDParam(c, "id")
	if !ok {
		return
	}
	if err := h.service.ClearAuthSession(c.Request.Context(), id); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"ok": true})
}

func (h *UpstreamConfigHandler) ClearAuthSessionCooldown(c *gin.Context) {
	id, ok := parseUpstreamIDParam(c, "id")
	if !ok {
		return
	}
	if err := h.service.ClearAuthSessionCooldown(c.Request.Context(), id); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"ok": true})
}

func (h *UpstreamConfigHandler) ForceAuthSessionReauth(c *gin.Context) {
	id, ok := parseUpstreamIDParam(c, "id")
	if !ok {
		return
	}
	if err := h.service.ForceAuthSessionReauth(c.Request.Context(), id); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"ok": true})
}

func (h *UpstreamConfigHandler) Test(c *gin.Context) {
	id, ok := parseUpstreamIDParam(c, "id")
	if !ok {
		return
	}
	if err := h.service.Test(c.Request.Context(), id); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"ok": true})
}

func (h *UpstreamConfigHandler) SyncKeys(c *gin.Context) {
	id, ok := parseUpstreamIDParam(c, "id")
	if !ok {
		return
	}
	keys, result, err := h.service.SyncKeys(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{
		"run_id":                result.RunID,
		"keys":                  sanitizeUpstreamKeys(keys),
		"key_count":             result.KeyCount,
		"updated_account_count": result.UpdatedAccountCount,
		"result":                sanitizeUpstreamSyncResult(result),
	})
}

func (h *UpstreamConfigHandler) SyncAllKeys(c *gin.Context) {
	runID, results, err := h.service.SyncActiveUpstreamConfigsManual(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"run_id": runID, "results": sanitizeUpstreamSyncResults(results)})
}

func (h *UpstreamConfigHandler) GetSettings(c *gin.Context) {
	settings, err := h.service.GetUpstreamSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, settings)
}

func (h *UpstreamConfigHandler) UpdateSettings(c *gin.Context) {
	var req upstreamSettingsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	settings := service.UpstreamSettings{
		BalanceLowThresholdCNY:  req.BalanceLowThresholdCNY,
		Sub2APINotInCNConfirmed: req.Sub2APINotInCNConfirmed,
		CostIncludedGroupIDs:    req.CostIncludedGroupIDs,
	}
	if err := h.service.UpdateUpstreamSettings(c.Request.Context(), settings); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, settings)
}

func (h *UpstreamConfigHandler) ListSyncRuns(c *gin.Context) {
	page, pageSize := response.ParsePagination(c)
	items, total, err := h.service.ListSyncRuns(c.Request.Context(), pageSize, (page-1)*pageSize)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Paginated(c, items, total, page, pageSize)
}

func (h *UpstreamConfigHandler) GetSyncRun(c *gin.Context) {
	id, ok := parseUpstreamIDParam(c, "runID")
	if !ok {
		return
	}
	item, err := h.service.GetSyncRun(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, item)
}

func (h *UpstreamConfigHandler) ListEvents(c *gin.Context) {
	page, pageSize := response.ParsePagination(c)
	configID, _ := strconv.ParseInt(c.Query("config_id"), 10, 64)
	items, total, err := h.service.ListEvents(c.Request.Context(), configID, pageSize, (page-1)*pageSize)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Paginated(c, items, total, page, pageSize)
}

func (h *UpstreamConfigHandler) ListIncidents(c *gin.Context) {
	page, pageSize := response.ParsePagination(c)
	configID, _ := strconv.ParseInt(c.Query("config_id"), 10, 64)
	items, total, err := h.service.ListIncidents(c.Request.Context(), configID, c.Query("status"), pageSize, (page-1)*pageSize)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Paginated(c, items, total, page, pageSize)
}

func (h *UpstreamConfigHandler) ListBalanceHistory(c *gin.Context) {
	id, ok := parseUpstreamIDParam(c, "id")
	if !ok {
		return
	}
	page, pageSize := response.ParsePagination(c)
	items, total, err := h.service.ListBalanceHistory(c.Request.Context(), id, pageSize, (page-1)*pageSize)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Paginated(c, items, total, page, pageSize)
}

func (h *UpstreamConfigHandler) UsageTrend(c *gin.Context) {
	configID, _ := strconv.ParseInt(c.Query("config_id"), 10, 64)
	var groupIDs []int64
	if raw, provided := c.GetQuery("group_ids"); provided {
		if strings.TrimSpace(raw) != "" {
			for _, part := range strings.Split(raw, ",") {
				id, parseErr := strconv.ParseInt(strings.TrimSpace(part), 10, 64)
				if parseErr != nil || id <= 0 {
					response.BadRequest(c, "group_ids must contain positive integers")
					return
				}
				groupIDs = append(groupIDs, id)
			}
		}
	}
	var trend *service.UpstreamUsageTrend
	var err error
	if _, provided := c.GetQuery("group_ids"); provided {
		trend, err = h.service.GetUsageTrend(c.Request.Context(), configID, c.Query("range"), groupIDs)
	} else {
		trend, err = h.service.GetUsageTrend(c.Request.Context(), configID, c.Query("range"))
	}
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, trend)
}

func (h *UpstreamConfigHandler) ListKeys(c *gin.Context) {
	id, ok := parseUpstreamIDParam(c, "id")
	if !ok {
		return
	}
	keys, err := h.service.ListKeys(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, sanitizeUpstreamKeys(keys))
}

func (h *UpstreamConfigHandler) KeyRateTrend(c *gin.Context) {
	configID, ok := parseUpstreamIDParam(c, "id")
	if !ok {
		return
	}
	keyID, ok := parseUpstreamIDParam(c, "keyID")
	if !ok {
		return
	}
	trend, err := h.service.GetKeyRateTrend(c.Request.Context(), configID, keyID, c.Query("range"))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, trend)
}

func (h *UpstreamConfigHandler) KeyRateTrendKeys(c *gin.Context) {
	configID, ok := parseUpstreamIDParam(c, "id")
	if !ok {
		return
	}
	items, err := h.service.ListKeyRateTrendKeys(c.Request.Context(), configID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, items)
}

func (h *UpstreamConfigHandler) DeleteKey(c *gin.Context) {
	id, ok := parseUpstreamIDParam(c, "keyID")
	if !ok {
		return
	}
	if err := h.service.DeleteKey(c.Request.Context(), id); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"message": "upstream key deleted"})
}

func (h *UpstreamConfigHandler) UpdateKeyPlatform(c *gin.Context) {
	configID, ok := parseUpstreamIDParam(c, "id")
	if !ok {
		return
	}
	keyID, ok := parseUpstreamIDParam(c, "keyID")
	if !ok {
		return
	}
	var req updateUpstreamKeyPlatformRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	key, err := h.service.UpdateKeyPlatform(c.Request.Context(), configID, keyID, service.UpdateUpstreamKeyPlatformRequest{
		Platform: req.Platform, ExpectedUpdatedAt: req.ExpectedUpdatedAt, DisableBoundAccounts: req.DisableBoundAccounts,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, sanitizeUpstreamKey(key))
}

func (h *UpstreamConfigHandler) UpdateKeyBaseURL(c *gin.Context) {
	configID, ok := parseUpstreamIDParam(c, "id")
	if !ok {
		return
	}
	keyID, ok := parseUpstreamIDParam(c, "keyID")
	if !ok {
		return
	}
	var req updateUpstreamKeyBaseURLRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	key, err := h.service.UpdateKeyBaseURL(c.Request.Context(), configID, keyID, service.UpdateUpstreamKeyBaseURLRequest{BaseURL: req.BaseURL, ClearBaseURL: req.ClearBaseURL, ExpectedUpdatedAt: req.ExpectedUpdatedAt})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, sanitizeUpstreamKey(key))
}

func sanitizeUpstreamSyncResults(results []service.UpstreamConfigSyncResult) []gin.H {
	out := make([]gin.H, 0, len(results))
	for i := range results {
		out = append(out, sanitizeUpstreamSyncResult(results[i]))
	}
	return out
}

func sanitizeUpstreamSyncResult(result service.UpstreamConfigSyncResult) gin.H {
	return gin.H{
		"run_id": result.RunID, "config_id": result.ConfigID, "name": result.Name,
		"provider": result.Provider, "success": result.Success, "status": result.Status,
		"stage": result.Stage, "error_code": result.ErrorCode, "retryable": result.Retryable,
		"key_count": result.KeyCount, "fallback_key_count": result.FallbackKeyCount,
		"unresolved_key_count": result.UnresolvedKeyCount, "updated_account_count": result.UpdatedAccountCount,
		"archived_account_count": result.ArchivedAccountCount, "restored_account_count": result.RestoredAccountCount,
		"model_sync_attempted": result.ModelSyncAttemptedCount, "model_sync_succeeded": result.ModelSyncSucceededCount,
		"model_sync_failed": result.ModelSyncFailedCount, "model_sync_skipped": result.ModelSyncSkippedCount,
		"warnings": result.Warnings, "duration_ms": result.DurationMS,
		"error": logredact.RedactText(result.Error, "password", "api_key", "jwt", "authorization", "refresh_token", "access_token", "cookie", "session"),
	}
}

func parseUpstreamIDParam(c *gin.Context, name string) (int64, bool) {
	id, err := strconv.ParseInt(c.Param(name), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid ID")
		return 0, false
	}
	return id, true
}

func upstreamConfigFromRequest(req upstreamConfigRequest) *service.UpstreamConfig {
	return &service.UpstreamConfig{
		Name:                              req.Name,
		Provider:                          req.Provider,
		SiteURL:                           req.SiteURL,
		APIURL:                            req.APIURL,
		ClearAPIURL:                       req.ClearAPIURL,
		AuthMode:                          req.AuthMode,
		Credentials:                       req.Credentials,
		Extra:                             req.Extra,
		SchedulerConcurrencyOverride:      req.SchedulerConcurrencyOverride,
		ClearSchedulerConcurrencyOverride: req.ClearSchedulerConcurrencyOverride,
		ProxyID:                           req.ProxyID,
		ClearProxy:                        req.ClearProxy,
		RechargeRate: func() float64 {
			if req.RechargeRate != nil {
				return *req.RechargeRate
			}
			return 0
		}(),
		BalanceToCNYRate:      req.BalanceToCNYRate,
		ClearBalanceToCNYRate: req.ClearBalanceToCNYRate,
		SchedulingEnabled:     req.SchedulingEnabled,
		Status:                req.Status,
	}
}

func sanitizeUpstreamConfigs(configs []service.UpstreamConfig) []gin.H {
	return sanitizeUpstreamConfigsWithThreshold(configs, 0)
}

func sanitizeUpstreamConfigsWithThreshold(configs []service.UpstreamConfig, threshold float64) []gin.H {
	out := make([]gin.H, 0, len(configs))
	for i := range configs {
		out = append(out, sanitizeUpstreamConfigWithThreshold(&configs[i], threshold))
	}
	return out
}

func sanitizeUpstreamConfig(config *service.UpstreamConfig) gin.H {
	return sanitizeUpstreamConfigWithThreshold(config, 0)
}

func sanitizeUpstreamConfigWithThreshold(config *service.UpstreamConfig, threshold float64) gin.H {
	if config == nil {
		return nil
	}
	if threshold > 0 {
		if balance, ok := finiteExtraFloat(config.Extra, "balance_cny"); ok {
			config.BalanceCNY = &balance
			config.BalanceThresholdCNY = &threshold
			config.BalanceAvailable = true
			if observedAt, ok := upstreamBalanceObservedAt(config.Extra); ok && time.Since(observedAt) > 24*time.Hour {
				config.BalanceAvailable = false
				config.BalanceUnavailableReason = "stale_snapshot"
			}
			config.BalanceLow = config.BalanceAvailable && balance < threshold
		} else {
			config.BalanceAvailable = false
			config.BalanceUnavailableReason = "no_snapshot"
		}
	} else {
		config.BalanceAvailable = false
		config.BalanceLow = false
		config.BalanceUnavailableReason = "threshold_not_configured"
	}
	resolvedConcurrency := service.ResolveUpstreamSchedulerConcurrency(config.Extra)
	return gin.H{
		"id":                    config.ID,
		"name":                  config.Name,
		"provider":              config.Provider,
		"site_url":              config.SiteURL,
		"api_url":               config.APIURL,
		"auth_mode":             config.AuthMode,
		"credentials_status":    upstreamCredentialsStatus(config.Credentials),
		"auth_session":          config.AuthSession,
		"extra":                 redactedUpstreamExtra(config.Extra),
		"scheduler_concurrency": resolvedConcurrency,
		"proxy_id":              config.ProxyID,
		"recharge_rate":         config.RechargeRate,
		"balance_to_cny_rate":   config.BalanceToCNYRate,
		"scheduling_enabled": func() bool {
			return config.SchedulingEnabled == nil || *config.SchedulingEnabled
		}(),
		"status":                     config.Status,
		"last_error":                 redactedUpstreamLastError(config.LastError),
		"last_checked_at":            config.LastCheckedAt,
		"last_success_at":            config.LastSuccessAt,
		"created_at":                 config.CreatedAt,
		"updated_at":                 config.UpdatedAt,
		"balance_cny":                config.BalanceCNY,
		"balance_available":          config.BalanceAvailable,
		"balance_low":                config.BalanceLow,
		"balance_threshold_cny":      config.BalanceThresholdCNY,
		"balance_unavailable_reason": config.BalanceUnavailableReason,
		"keys":                       sanitizeUpstreamKeyPtrs(config.Keys),
	}
}

func upstreamBalanceObservedAt(extra map[string]any) (time.Time, bool) {
	if extra == nil {
		return time.Time{}, false
	}
	if snapshot, ok := extra["upstream_provider_snapshot"].(map[string]any); ok {
		if value, ok := snapshot["synced_at"].(string); ok {
			at, err := time.Parse(time.RFC3339, strings.TrimSpace(value))
			if err == nil {
				return at, true
			}
		}
	}
	if value, ok := extra["sub2api_balance_synced_at"].(string); ok {
		at, err := time.Parse(time.RFC3339, strings.TrimSpace(value))
		if err == nil {
			return at, true
		}
	}
	return time.Time{}, false
}

func (h *UpstreamConfigHandler) upstreamBalanceThreshold(c *gin.Context) float64 {
	settings, err := h.service.GetUpstreamSettings(c.Request.Context())
	if err != nil || settings == nil || settings.BalanceLowThresholdCNY <= 0 {
		return 0
	}
	return settings.BalanceLowThresholdCNY
}

func finiteExtraFloat(extra map[string]any, key string) (float64, bool) {
	value, ok := extra[key]
	if !ok {
		return 0, false
	}
	var parsed float64
	switch typed := value.(type) {
	case float64:
		parsed = typed
	case json.Number:
		var err error
		parsed, err = typed.Float64()
		if err != nil {
			return 0, false
		}
	case string:
		var err error
		parsed, err = strconv.ParseFloat(strings.TrimSpace(typed), 64)
		if err != nil {
			return 0, false
		}
	default:
		return 0, false
	}
	if math.IsNaN(parsed) || math.IsInf(parsed, 0) {
		return 0, false
	}
	return parsed, true
}

func redactedUpstreamLastError(value *string) *string {
	if value == nil {
		return nil
	}
	redacted := logredact.RedactText(*value, "password", "api_key", "jwt", "authorization", "refresh_token", "access_token", "cookie", "session")
	return &redacted
}

func sanitizeUpstreamKeyPtrs(keys []*service.UpstreamKey) []gin.H {
	out := make([]gin.H, 0, len(keys))
	for _, key := range keys {
		out = append(out, sanitizeUpstreamKey(key))
	}
	return out
}

func sanitizeUpstreamKeys(keys []service.UpstreamKey) []gin.H {
	out := make([]gin.H, 0, len(keys))
	for i := range keys {
		out = append(out, sanitizeUpstreamKey(&keys[i]))
	}
	return out
}

func sanitizeUpstreamKey(key *service.UpstreamKey) gin.H {
	if key == nil {
		return nil
	}
	out := gin.H{
		"id":                        key.ID,
		"upstream_config_id":        key.UpstreamConfigID,
		"name":                      key.Name,
		"key_status":                gin.H{"has_key": strings.TrimSpace(key.Key) != "", "suffix": keySuffix(key.Key)},
		"remote_key_id":             key.RemoteKeyID,
		"upstream_group_id":         key.UpstreamGroupID,
		"upstream_group_name":       key.UpstreamGroupName,
		"base_url":                  key.BaseURL,
		"description":               key.Description,
		"platform":                  key.Platform,
		"platform_source":           key.PlatformSource,
		"detected_platform":         key.DetectedPlatform,
		"platform_detection_status": key.PlatformDetectionStatus,
		"platform_detected_at":      key.PlatformDetectedAt,
		"bound_account_count":       key.BoundAccountCount,
		"rate_multiplier":           key.RateMultiplier,
		"status":                    key.Status,
		"last_seen_at":              key.LastSeenAt,
		"missing_count":             key.MissingCount,
		"missing_since":             key.MissingSince,
		"extra":                     redactedUpstreamExtra(key.Extra),
		"created_at":                key.CreatedAt,
		"updated_at":                key.UpdatedAt,
	}
	if key.ImagePricing != nil {
		out["image_pricing"] = sanitizeUpstreamKeyImagePricing(key.ImagePricing)
	}
	return out
}

func sanitizeUpstreamKeyImagePricing(pricing *service.UpstreamKeyImagePricing) gin.H {
	if pricing == nil {
		return nil
	}
	return gin.H{
		"supported":     pricing.Supported,
		"status":        pricing.Status,
		"stale":         pricing.Stale,
		"currency":      pricing.Currency,
		"final_cost_1k": pricing.FinalCost1K,
		"final_cost_2k": pricing.FinalCost2K,
		"final_cost_4k": pricing.FinalCost4K,
		"observed_at":   pricing.ObservedAt,
	}
}

func redactedUpstreamExtra(extra map[string]any) map[string]any {
	if extra == nil {
		return nil
	}
	filtered := make(map[string]any, len(extra))
	for key, value := range extra {
		if key == service.Sub2APIImagePricingSnapshotExtraKey || key == service.LCodexImageCapabilitySnapshotExtraKey {
			continue
		}
		filtered[key] = value
	}
	return logredact.RedactMap(filtered, "api_key", "jwt", "token", "key", "secret", "authorization", "bearer", "cookie", "session")
}

func upstreamCredentialsStatus(credentials map[string]any) gin.H {
	return gin.H{
		"has_login_email":             strings.TrimSpace(stringFromAny(credentials[service.AccountCredentialSub2APILoginEmail])) != "",
		"has_login_password":          strings.TrimSpace(stringFromAny(credentials[service.AccountCredentialSub2APILoginPassword])) != "",
		"has_access_token":            strings.TrimSpace(stringFromAny(credentials[service.AccountCredentialSub2APIAccessToken])) != "",
		"has_refresh_token":           strings.TrimSpace(stringFromAny(credentials[service.AccountCredentialSub2APIRefreshToken])) != "",
		"has_newapi_login_username":   strings.TrimSpace(stringFromAny(credentials[service.AccountCredentialNewAPILoginUsername])) != "",
		"has_newapi_login_password":   strings.TrimSpace(stringFromAny(credentials[service.AccountCredentialNewAPILoginPassword])) != "",
		"has_newapi_cookie":           strings.TrimSpace(stringFromAny(credentials[service.AccountCredentialNewAPICookie])) != "",
		"has_newapi_access_token":     strings.TrimSpace(stringFromAny(credentials[service.AccountCredentialNewAPIAccessToken])) != "",
		"has_newapi_user_id":          strings.TrimSpace(stringFromAny(credentials[service.AccountCredentialNewAPIUserID])) != "",
		"has_lcodex_login_identifier": strings.TrimSpace(stringFromAny(credentials[service.AccountCredentialLCodexLoginIdentifier])) != "",
		"has_lcodex_login_password":   strings.TrimSpace(stringFromAny(credentials[service.AccountCredentialLCodexLoginPassword])) != "",
	}
}

func stringFromAny(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func keySuffix(key string) string {
	key = strings.TrimSpace(key)
	if len(key) <= 6 {
		return "***"
	}
	return key[len(key)-6:]
}
