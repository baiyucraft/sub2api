package admin

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type updateOpenAITTFTGuardSettingsRequest struct {
	Enabled                *bool `json:"enabled" binding:"required"`
	DegradationTTFTSeconds *int  `json:"degradation_ttft_seconds" binding:"required"`
	MinSamples             *int  `json:"min_samples" binding:"required"`
}

func (h *SettingHandler) GetOpenAITTFTGuardSettings(c *gin.Context) {
	settings, err := h.settingService.GetOpenAITTFTGuardSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, settings)
}

func (h *SettingHandler) UpdateOpenAITTFTGuardSettings(c *gin.Context) {
	var req updateOpenAITTFTGuardSettingsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	settings := &service.OpenAITTFTGuardSettings{
		Enabled:                *req.Enabled,
		DegradationTTFTSeconds: *req.DegradationTTFTSeconds,
		MinSamples:             *req.MinSamples,
	}
	if err := h.settingService.SetOpenAITTFTGuardSettings(c.Request.Context(), settings); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, settings)
}
