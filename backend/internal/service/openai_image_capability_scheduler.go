package service

import "context"

type openAIImageKnownOnlyKey struct{}

func openAIImageKnownOnly(ctx context.Context) bool {
	knownOnly, _ := ctx.Value(openAIImageKnownOnlyKey{}).(bool)
	return knownOnly
}

func openAIImageRequestNeedsPreference(ctx context.Context) bool {
	r, ok := OpenAIImageRequestFromContext(ctx)
	return ok && (r.RequiresCapability() || r.Passive || r.IsCodex)
}

func (s *OpenAIGatewayService) openAIImageCandidateFailure(ctx context.Context, account *Account, req OpenAIAccountScheduleRequest) string {
	if account == nil || account.Platform != PlatformOpenAI {
		return ""
	}
	requires := s.AccountRequiresOpenAIImageCapability(ctx, req.GroupID, account)
	requiresResponses := s.AccountRequiresOpenAIResponsesForImage(ctx, req.GroupID, account)
	model := req.RequestedModel
	if model == "" {
		if r, ok := OpenAIImageRequestFromContext(ctx); ok {
			model = r.Model
		}
	}
	if model != "" && isOpenAIImageGenerationModel(canonicalOpenAIAccountSchedulingModel(account, model)) {
		requires = true
		requiresResponses = req.RequiredImageCapability == ""
	}
	if !requires && req.RequiredImageCapability == "" {
		return ""
	}
	if requiresResponses && !account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityResponses) {
		return "image_responses_unsupported"
	}
	if account.isRateLimitActiveForKey(openAIImageGenerationRateLimitKey) {
		return "image_capability_cooldown"
	}
	rank := OpenAIImagePermissionRank(account, openAIImageStaleAfter(ctx))
	if rank == OpenAIImagePermissionDenied {
		return "image_permission_denied"
	}
	if rank != OpenAIImagePermissionAllowed && openAIImageKnownOnly(ctx) {
		return "image_permission_unknown_fallback"
	}
	return ""
}
