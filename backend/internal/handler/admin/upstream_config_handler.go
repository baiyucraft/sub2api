package admin

import (
	"strconv"
	"strings"

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
	Name                  string         `json:"name"`
	Provider              string         `json:"provider"`
	BaseURL               string         `json:"base_url"`
	AuthMode              string         `json:"auth_mode"`
	Credentials           map[string]any `json:"credentials"`
	Extra                 map[string]any `json:"extra"`
	ProxyID               *int64         `json:"proxy_id"`
	ClearProxy            bool           `json:"clear_proxy"`
	RechargeRate          *float64       `json:"recharge_rate"`
	BalanceToCNYRate      *float64       `json:"balance_to_cny_rate"`
	ClearBalanceToCNYRate bool           `json:"clear_balance_to_cny_rate"`
	Status                string         `json:"status"`
}

type upstreamSettingsRequest struct {
	BalanceLowThresholdCNY float64 `json:"balance_low_threshold_cny"`
}

type upstreamKeyRequest struct {
	Name              string         `json:"name"`
	Key               string         `json:"key"`
	RemoteKeyID       *int64         `json:"remote_key_id"`
	UpstreamGroupID   *int64         `json:"upstream_group_id"`
	UpstreamGroupName string         `json:"upstream_group_name"`
	Platform          string         `json:"platform"`
	RateMultiplier    *float64       `json:"rate_multiplier"`
	Status            string         `json:"status"`
	Extra             map[string]any `json:"extra"`
}

func (h *UpstreamConfigHandler) List(c *gin.Context) {
	page, pageSize := response.ParsePagination(c)
	configs, total, err := h.service.List(c.Request.Context(), pagination.PaginationParams{
		Page:     page,
		PageSize: pageSize,
	}, c.Query("provider"), c.Query("status"), c.Query("search"))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Paginated(c, sanitizeUpstreamConfigs(configs), total.Total, page, pageSize)
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
	response.Success(c, sanitizeUpstreamConfig(config))
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

func (h *UpstreamConfigHandler) Delete(c *gin.Context) {
	id, ok := parseUpstreamIDParam(c, "id")
	if !ok {
		return
	}
	if err := h.service.Delete(c.Request.Context(), id); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"message": "upstream config deleted"})
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
	settings := service.UpstreamSettings{BalanceLowThresholdCNY: req.BalanceLowThresholdCNY}
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
	trend, err := h.service.GetUsageTrend(c.Request.Context(), configID, c.Query("range"))
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

func (h *UpstreamConfigHandler) CreateKey(c *gin.Context) {
	id, ok := parseUpstreamIDParam(c, "id")
	if !ok {
		return
	}
	var req upstreamKeyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	key, err := h.service.CreateKey(c.Request.Context(), id, upstreamKeyFromRequest(req))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, sanitizeUpstreamKey(key))
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
		Name:        req.Name,
		Provider:    req.Provider,
		BaseURL:     req.BaseURL,
		AuthMode:    req.AuthMode,
		Credentials: req.Credentials,
		Extra:       req.Extra,
		ProxyID:     req.ProxyID,
		ClearProxy:  req.ClearProxy,
		RechargeRate: func() float64 {
			if req.RechargeRate != nil {
				return *req.RechargeRate
			}
			return 0
		}(),
		BalanceToCNYRate:      req.BalanceToCNYRate,
		ClearBalanceToCNYRate: req.ClearBalanceToCNYRate,
		Status:                req.Status,
	}
}

func upstreamKeyFromRequest(req upstreamKeyRequest) *service.UpstreamKey {
	return &service.UpstreamKey{
		Name:              req.Name,
		Key:               req.Key,
		RemoteKeyID:       req.RemoteKeyID,
		UpstreamGroupID:   req.UpstreamGroupID,
		UpstreamGroupName: req.UpstreamGroupName,
		Platform:          req.Platform,
		RateMultiplier:    req.RateMultiplier,
		Status:            req.Status,
		Extra:             req.Extra,
	}
}

func sanitizeUpstreamConfigs(configs []service.UpstreamConfig) []gin.H {
	out := make([]gin.H, 0, len(configs))
	for i := range configs {
		out = append(out, sanitizeUpstreamConfig(&configs[i]))
	}
	return out
}

func sanitizeUpstreamConfig(config *service.UpstreamConfig) gin.H {
	if config == nil {
		return nil
	}
	return gin.H{
		"id":                  config.ID,
		"name":                config.Name,
		"provider":            config.Provider,
		"base_url":            config.BaseURL,
		"auth_mode":           config.AuthMode,
		"credentials_status":  upstreamCredentialsStatus(config.Credentials),
		"extra":               redactedUpstreamExtra(config.Extra),
		"proxy_id":            config.ProxyID,
		"recharge_rate":       config.RechargeRate,
		"balance_to_cny_rate": config.BalanceToCNYRate,
		"status":              config.Status,
		"last_error":          redactedUpstreamLastError(config.LastError),
		"last_checked_at":     config.LastCheckedAt,
		"last_success_at":     config.LastSuccessAt,
		"created_at":          config.CreatedAt,
		"updated_at":          config.UpdatedAt,
		"keys":                sanitizeUpstreamKeyPtrs(config.Keys),
	}
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
	return gin.H{
		"id":                        key.ID,
		"upstream_config_id":        key.UpstreamConfigID,
		"name":                      key.Name,
		"key_status":                gin.H{"has_key": strings.TrimSpace(key.Key) != "", "suffix": keySuffix(key.Key)},
		"remote_key_id":             key.RemoteKeyID,
		"upstream_group_id":         key.UpstreamGroupID,
		"upstream_group_name":       key.UpstreamGroupName,
		"platform":                  key.Platform,
		"rate_multiplier":           key.RateMultiplier,
		"effective_cost_multiplier": key.EffectiveCostMultiplier,
		"status":                    key.Status,
		"last_seen_at":              key.LastSeenAt,
		"extra":                     redactedUpstreamExtra(key.Extra),
		"created_at":                key.CreatedAt,
		"updated_at":                key.UpdatedAt,
	}
}

func redactedUpstreamExtra(extra map[string]any) map[string]any {
	return logredact.RedactMap(extra, "api_key", "jwt", "token", "key", "secret", "authorization", "bearer", "cookie", "session")
}

func upstreamCredentialsStatus(credentials map[string]any) gin.H {
	return gin.H{
		"has_login_email":           strings.TrimSpace(stringFromAny(credentials[service.AccountCredentialSub2APILoginEmail])) != "",
		"has_login_password":        strings.TrimSpace(stringFromAny(credentials[service.AccountCredentialSub2APILoginPassword])) != "",
		"has_access_token":          strings.TrimSpace(stringFromAny(credentials[service.AccountCredentialSub2APIAccessToken])) != "",
		"has_refresh_token":         strings.TrimSpace(stringFromAny(credentials[service.AccountCredentialSub2APIRefreshToken])) != "",
		"has_newapi_login_username": strings.TrimSpace(stringFromAny(credentials[service.AccountCredentialNewAPILoginUsername])) != "",
		"has_newapi_login_password": strings.TrimSpace(stringFromAny(credentials[service.AccountCredentialNewAPILoginPassword])) != "",
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
