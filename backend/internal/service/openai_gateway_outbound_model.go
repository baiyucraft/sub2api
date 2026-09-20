package service

import (
	"strings"

	"github.com/tidwall/gjson"
)

func extractOpenAIOutboundModel(body []byte) string {
	return strings.TrimSpace(gjson.GetBytes(body, "model").String())
}

// openAIAccountOutboundModel mirrors Forward's account and compact mappings for
// plugin admission. requestedModel already includes any channel mapping.
func (s *OpenAIGatewayService) openAIAccountOutboundModel(account *Account, requestedModel string, requireCompact bool) string {
	model := strings.TrimSpace(requestedModel)
	if account == nil || model == "" {
		return model
	}
	if !account.IsOpenAI() {
		return canonicalOpenAIAccountSchedulingModel(account, model)
	}
	_, upstreamModel := resolveOpenAIForwardMappedModels(account, model, requireCompact)
	if requireCompact {
		if compactModel := strings.TrimSpace(s.resolveOpenAICompactFallbackModel(account, model)); compactModel != "" {
			upstreamModel = compactModel
		}
	}
	if upstreamModel = strings.TrimSpace(upstreamModel); upstreamModel != "" {
		return upstreamModel
	}
	return model
}
