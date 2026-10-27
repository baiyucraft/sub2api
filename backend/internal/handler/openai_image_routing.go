package handler

import (
	"context"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

func (h *OpenAIGatewayHandler) openAIImageRoutingContext(ctx context.Context, c *gin.Context, group *service.Group, model string, body []byte) context.Context {
	isCodex := openai.IsCodexOfficialClientByHeaders(c.GetHeader("User-Agent"), c.GetHeader("originator")) || (h.cfg != nil && h.cfg.Gateway.ForceCodexCLI)
	r := service.DescribeOpenAIImageRequest("/v1/responses", model, body, isCodex)
	_, r, _, _ = service.ApplyOpenAIImageRequestGroupPolicy(body, r, group)
	switch strings.ToLower(strings.TrimSpace(c.GetHeader("X-OpenAI-Internal-Codex-Responses-Lite"))) {
	case "true":
		r.Lite = true
	}
	if service.IsOpenAIResponsesCompactPath(c) {
		r.Endpoint = "/v1/responses/compact"
	}
	ctx = service.WithOpenAIImageSelectionTrace(ctx)
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
	logOpenAIImageFailurePolicy(ctx, failure)
	if !service.OpenAIImageSelectionExhaustion(ctx, failure) {
		return false
	}
	h.handleStreamingAwareErrorWithCode(c, http.StatusServiceUnavailable, "server_error", "image_generation_unavailable", "No image-capable upstream account is available", streamStarted, false)
	return true
}

func logOpenAIImageFailurePolicy(ctx context.Context, failure *service.UpstreamFailoverError) {
	failure = service.OpenAIImageFailoverForExhaustion(ctx, failure)
	category := "selection"
	status := 0
	if failure != nil {
		category = "upstream"
		status = failure.StatusCode
		if service.IsOpenAIImagePermissionFailover(failure) {
			category = "image_capability"
		}
	} else if service.OpenAIImageSelectionExhaustion(ctx, nil) {
		category = "image_capability"
	}
	r, _ := service.OpenAIImageRequestFromContext(ctx)
	logger.FromContext(ctx).Info("openai.image_failure_policy",
		zap.String("failure_category", category), zap.Int("upstream_status", status),
		zap.Bool("required", r.RequiresCapability()), zap.Bool("history", r.History),
		zap.Bool("unknown_history", r.UnknownHistory),
		zap.Bool("image_filtered", service.OpenAIImageSelectionFiltered(ctx)))
}

// Classification metadata is deliberately limited to booleans and policy labels.
func logOpenAIIngressImagePolicy(log *zap.Logger, r service.OpenAIImageRequest, group *service.Group, changed bool) {
	reason := "disabled"
	if !service.GroupAllowsImageGeneration(group) {
		reason = "group_denied"
	} else if group != nil && group.StripOptionalImageTools {
		reason = "group_opt_in"
	}
	log.Info("openai.image_request_policy", zap.String("source", r.Source), zap.Bool("required", r.RequiresCapability()), zap.Bool("native", r.Native), zap.Bool("passive", r.Passive), zap.Bool("history", r.History), zap.Bool("unknown_history", r.UnknownHistory), zap.Bool("lite", r.Lite), zap.Bool("stripped", changed), zap.String("strip_reason", reason))
}
