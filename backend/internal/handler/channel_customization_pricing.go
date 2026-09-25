package handler

import (
	"context"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// HasCustomizationBillingPricing guards the public requested model before a
// mapped request reaches an upstream account.
func (h *Handlers) HasCustomizationBillingPricing(ctx context.Context, apiKey *service.APIKey, model string) bool {
	apiKey = service.ChannelCustomizationBillingAPIKey(ctx, apiKey)
	return h != nil && h.Gateway != nil && h.Gateway.gatewayService != nil &&
		h.Gateway.gatewayService.HasCustomizationBillingPricing(ctx, apiKey, model)
}

// CustomizationChannelTarget resolves B→C for structured rule-hit diagnostics.
func (h *Handlers) CustomizationChannelTarget(ctx context.Context, apiKey *service.APIKey, model string) string {
	if h == nil || h.Gateway == nil || h.Gateway.gatewayService == nil || apiKey == nil {
		return model
	}
	mapping, _ := h.Gateway.gatewayService.ResolveChannelMappingAndRestrict(ctx, apiKey.GroupID, model)
	if mapping.MappedModel == "" {
		return model
	}
	return mapping.MappedModel
}
