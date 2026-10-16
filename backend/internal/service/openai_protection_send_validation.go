package service

import "context"

// ValidateSchedulingProtectionSend checks a nominated migration immediately
// before its physical send, independently of the optional profit-control gate.
func (s *OpenAIGatewayService) ValidateSchedulingProtectionSend(ctx context.Context, selected *Account) error {
	if s == nil || selected == nil || s.accountRepo == nil {
		return schedulingProtectionScopeChangedError()
	}
	var groupID *int64
	if id, ok := ctx.Value(schedulingProtectionGroupKey{}).(int64); ok {
		groupID = &id
	}
	model := schedulingProtectionSendValidationModel(ctx)
	requireCompact := false
	if admission, _ := ctx.Value(openAISchedulingAdmissionContextKey{}).(*openAISchedulingAdmission); admission != nil {
		requireCompact = admission.requireCompact
	}
	latest := s.recheckSelectedOpenAIAccountFromDBBeforeProfit(withCacheGuardTentativeSelection(ctx), selected, groupID, selected.Platform, model, requireCompact, "")
	if latest == nil || latest.ID != selected.ID || latest.Platform != selected.Platform || accountUpstreamKeyID(latest) != accountUpstreamKeyID(selected) || derefGroupID(latest.UpstreamConfigID) != derefGroupID(selected.UpstreamConfigID) || latest.SchedulingConcurrencyTarget() != selected.SchedulingConcurrencyTarget() {
		return schedulingProtectionScopeChangedError()
	}
	if vetoed, _ := openAIProfitControlVetoReason(ctx, latest); vetoed {
		return schedulingProtectionScopeChangedError()
	}
	return nil
}

// Account model whitelists use the model chosen before account mapping. A
// transport's mapped name cannot replace it (for example public -> upstream).
func schedulingProtectionSendValidationModel(ctx context.Context) string {
	if model, ok := ResolvedUpstreamModelFromContext(ctx); ok {
		return model
	}
	if request, ok := ctx.Value(gatewayProtectionRecheckKey{}).(gatewayProtectionRecheck); ok && request.model != "" {
		return request.model
	}
	if admission, _ := ctx.Value(openAISchedulingAdmissionContextKey{}).(*openAISchedulingAdmission); admission != nil && admission.requestedModel != "" {
		return admission.requestedModel
	}
	metadata, _ := ctx.Value(pluginRequestMetadataKey{}).(pluginRequestMetadata)
	if metadata.Model != "" {
		return metadata.Model
	}
	if model, ok := RequestedPublicModelFromContext(ctx); ok {
		return model
	}
	if attempt, _ := ctx.Value(cacheUsageAttemptContextKey{}).(*cacheUsageAttempt); attempt != nil {
		attempt.mu.Lock()
		model := attempt.model
		attempt.mu.Unlock()
		if model != "" {
			return model
		}
	}
	model, _ := ctx.Value(schedulingProtectionSendModelKey{}).(string)
	return model
}
