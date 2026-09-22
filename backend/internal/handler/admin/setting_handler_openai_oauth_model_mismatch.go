package admin

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type openAIOAuthModelMismatchRulesResponse struct {
	Rules []service.OpenAIOAuthModelMismatchRule `json:"rules"`
}

func (h *SettingHandler) GetOpenAIOAuthModelMismatchRules(c *gin.Context) {
	rules, err := h.settingService.GetOpenAIOAuthModelMismatchRules(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, openAIOAuthModelMismatchRulesResponse{Rules: rules})
}

func (h *SettingHandler) UpdateOpenAIOAuthModelMismatchRules(c *gin.Context) {
	var req openAIOAuthModelMismatchRulesResponse
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "invalid model mismatch rules: "+err.Error())
		return
	}
	rules, err := h.settingService.SetOpenAIOAuthModelMismatchRules(c.Request.Context(), req.Rules)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, openAIOAuthModelMismatchRulesResponse{Rules: rules})
}
