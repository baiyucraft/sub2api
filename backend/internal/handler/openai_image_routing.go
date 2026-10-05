package handler

import (
	"context"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func (h *OpenAIGatewayHandler) openAIImageRoutingContext(ctx context.Context, c *gin.Context, group *service.Group, model string, body []byte) context.Context {
	isCodex := openai.IsCodexOfficialClientByHeaders(c.GetHeader("User-Agent"), c.GetHeader("originator")) || (h.cfg != nil && h.cfg.Gateway.ForceCodexCLI)
	r := service.DescribeOpenAIImageRequest("/v1/responses", model, body, isCodex)
	switch strings.ToLower(strings.TrimSpace(c.GetHeader("X-OpenAI-Internal-Codex-Responses-Lite"))) {
	case "true":
		r.Lite = true
	}
	if service.IsOpenAIResponsesCompactPath(c) {
		r.Endpoint = "/v1/responses/compact"
	}
	ctx = service.WithOpenAIImageRequestDescriptor(ctx, r)
	ctx = service.WithOpenAIImageRequestGroup(ctx, group)
	if group != nil {
		ctx = service.WithOpenAIImagePermissionStaleAfter(ctx, group.ImageCostStaleAfterSeconds)
	}
	return ctx
}

func openAIImageCapabilityRequested(ctx context.Context) bool {
	r, ok := service.OpenAIImageRequestFromContext(ctx)
	return ok && r.RequiresCapability()
}

func (h *OpenAIGatewayHandler) handleOpenAIImageSelectionExhausted(c *gin.Context, ctx context.Context, failure *service.UpstreamFailoverError, streamStarted bool) bool {
	if !openAIImageCapabilityRequested(ctx) && !service.IsOpenAIImagePermissionFailover(failure) {
		return false
	}
	h.handleStreamingAwareErrorWithCode(c, http.StatusServiceUnavailable, "server_error", "image_generation_unavailable", "No image-capable upstream account is available", streamStarted, false)
	return true
}
