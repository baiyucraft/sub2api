package admin

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type upstreamProbeModelsRequest struct {
	Models map[string]string `json:"models"`
}

func (h *UpstreamConfigHandler) ListUpstreamHealthHistories(ctx context.Context, keyIDs []int64, limit int) (map[int64][]service.UpstreamHealthObservation, error) {
	return h.service.ListUpstreamHealthHistories(ctx, keyIDs, limit)
}

func (h *UpstreamConfigHandler) upstreamHealthAdminResponse(ctx context.Context, item service.UpstreamHealthSnapshot, limit int) (gin.H, error) {
	histories, err := h.service.ListUpstreamHealthHistories(ctx, []int64{item.KeyID}, limit)
	if err != nil {
		return nil, err
	}
	return gin.H{
		"key_id": item.KeyID, "status": item.Status, "observation_enabled": item.ObservationEnabled,
		"reason": item.Reason, "last_probe_at": item.LastProbeAt, "last_probe_status": item.LastProbeStatus,
		"last_evidence_at": item.LastEvidenceAt, "last_traffic_status": item.LastTrafficStatus,
		"consecutive_failures": item.ConsecutiveFails, "recovery_samples": item.RecoverySamples,
		"recovery_samples_required": item.RecoverySamplesRequired, "updated_at": item.UpdatedAt,
		"history": histories[item.KeyID],
	}, nil
}

func (h *AccountHandler) ListUpstreamManagement(c *gin.Context) {
	q := c.Request.URL.Query()
	q.Set("scope", string(service.AccountListScopeUpstream))
	c.Request.URL.RawQuery = q.Encode()
	h.List(c)
}

func (h *UpstreamConfigHandler) GetUpstreamProbeModels(c *gin.Context) {
	models, err := h.service.GetProbeModels(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"models": gin.H{"openai": models.OpenAI, "anthropic": models.Anthropic, "gemini": models.Gemini}})
}

func (h *UpstreamConfigHandler) PutUpstreamProbeModels(c *gin.Context) {
	var req upstreamProbeModelsRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.Models == nil {
		response.BadRequest(c, "models is required")
		return
	}
	models := service.UpstreamProbeModels{
		OpenAI: req.Models["openai"], Anthropic: req.Models["anthropic"], Gemini: req.Models["gemini"],
	}
	if err := h.service.SetProbeModels(c.Request.Context(), models); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"models": gin.H{"openai": models.OpenAI, "anthropic": models.Anthropic, "gemini": models.Gemini}})
}

func parseUpstreamManagementKeyID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.Error(c, http.StatusBadRequest, "invalid upstream key id")
		return 0, false
	}
	return id, true
}

func (h *UpstreamConfigHandler) SetKeyObservationAdmin(c *gin.Context) {
	id, ok := parseUpstreamManagementKeyID(c)
	if !ok {
		return
	}
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Enabled == nil {
		response.BadRequest(c, "enabled is required")
		return
	}
	item, err := h.service.SetKeyObservation(c.Request.Context(), id, *req.Enabled)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	payload, err := h.upstreamHealthAdminResponse(c.Request.Context(), item, service.UpstreamHealthListHistoryLimit)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, payload)
}

func (h *UpstreamConfigHandler) ProbeKeyAdmin(c *gin.Context) {
	id, ok := parseUpstreamManagementKeyID(c)
	if !ok {
		return
	}
	item, err := h.service.ProbeKey(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	payload, err := h.upstreamHealthAdminResponse(c.Request.Context(), item, service.UpstreamHealthListHistoryLimit)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, payload)
}

func (h *UpstreamConfigHandler) ListKeyEventsAdmin(c *gin.Context) {
	id, ok := parseUpstreamManagementKeyID(c)
	if !ok {
		return
	}
	key, err := h.service.GetKeyByID(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if key == nil {
		response.ErrorFrom(c, service.ErrUpstreamKeyNotFound)
		return
	}
	limit := 50
	offset := 0
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		if parsed, parseErr := strconv.Atoi(raw); parseErr == nil {
			limit = parsed
		}
	}
	if raw := strings.TrimSpace(c.Query("offset")); raw != "" {
		if parsed, parseErr := strconv.Atoi(raw); parseErr == nil {
			offset = parsed
		}
	}
	items, total, err := h.service.ListKeyEvents(c.Request.Context(), key.UpstreamConfigID, id, limit, offset)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	histories, err := h.service.ListUpstreamHealthHistories(c.Request.Context(), []int64{id}, service.UpstreamHealthHistoryLimit)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"items": items, "total": total, "limit": limit, "offset": offset, "health_history": histories[id]})
}
