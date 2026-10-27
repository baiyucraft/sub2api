package service

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

type openAIImageSelectionTraceKey struct{}

type openAIImageSelectionTrace struct {
	filtered        atomic.Bool
	mu              sync.Mutex
	ordinaryFailure *UpstreamFailoverError
}

// WithOpenAIImageSelectionTrace starts a request-scoped, concurrency-safe trace.
// Call once before selection so sticky, fresh and failover contexts share it.
func WithOpenAIImageSelectionTrace(ctx context.Context) context.Context {
	if trace, _ := ctx.Value(openAIImageSelectionTraceKey{}).(*openAIImageSelectionTrace); trace != nil {
		return ctx
	}
	return context.WithValue(ctx, openAIImageSelectionTraceKey{}, &openAIImageSelectionTrace{})
}

// ResetOpenAIImageSelectionTrace starts a new logical request on a reused context.
// WebSocket ingress calls this after a successful turn; failed attempts within
// the same turn must retain their shared selection evidence for failover.
func ResetOpenAIImageSelectionTrace(ctx context.Context) {
	if ctx == nil {
		return
	}
	if trace, _ := ctx.Value(openAIImageSelectionTraceKey{}).(*openAIImageSelectionTrace); trace != nil {
		trace.mu.Lock()
		defer trace.mu.Unlock()
		trace.ordinaryFailure = nil
		trace.filtered.Store(false)
	}
}

func isOpenAIImageCapabilityFailure(failure *UpstreamFailoverError) bool {
	return IsOpenAIImagePermissionFailover(failure)
}

// RecordOpenAIImageFailover preserves the latest ordinary failure so a later image-only
// candidate rejection cannot overwrite a network or unrelated upstream failure.
func RecordOpenAIImageFailover(ctx context.Context, failure *UpstreamFailoverError) {
	if failure == nil || isOpenAIImageCapabilityFailure(failure) || ctx == nil {
		return
	}
	if trace, _ := ctx.Value(openAIImageSelectionTraceKey{}).(*openAIImageSelectionTrace); trace != nil {
		trace.mu.Lock()
		defer trace.mu.Unlock()
		copy := *failure
		trace.ordinaryFailure = &copy
	}
}

func OpenAIImageFailoverForExhaustion(ctx context.Context, last *UpstreamFailoverError) *UpstreamFailoverError {
	if ctx != nil {
		if trace, _ := ctx.Value(openAIImageSelectionTraceKey{}).(*openAIImageSelectionTrace); trace != nil {
			trace.mu.Lock()
			defer trace.mu.Unlock()
			if trace.ordinaryFailure != nil {
				return trace.ordinaryFailure
			}
		}
	}
	return last
}

func OpenAIImageSelectionExhaustion(ctx context.Context, failure *UpstreamFailoverError) bool {
	selected := OpenAIImageFailoverForExhaustion(ctx, failure)
	if selected != nil {
		return isOpenAIImageCapabilityFailure(selected)
	}
	return OpenAIImageSelectionFiltered(ctx)
}

func OpenAIImageSelectionFiltered(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	trace, _ := ctx.Value(openAIImageSelectionTraceKey{}).(*openAIImageSelectionTrace)
	return trace != nil && trace.filtered.Load()
}

type openAIImageKnownOnlyKey struct{}

func openAIImageKnownOnly(ctx context.Context) bool {
	knownOnly, _ := ctx.Value(openAIImageKnownOnlyKey{}).(bool)
	return knownOnly
}

func openAIImageRequestNeedsPreference(ctx context.Context) bool {
	r, ok := OpenAIImageRequestFromContext(ctx)
	return ok && (r.RequiresCapability() || r.Passive || r.IsCodex)
}

func (s *OpenAIGatewayService) openAIImageCandidateFailure(ctx context.Context, account *Account, req OpenAIAccountScheduleRequest) (reason string) {
	model := req.RequestedModel
	if model == "" {
		if r, ok := OpenAIImageRequestFromContext(ctx); ok {
			model = r.Model
		}
	}
	defer func() {
		// The known-only preference pass may admit the same unknown account in
		// its fallback pass; it is not a definitive capability rejection.
		if reason == "" || reason == "image_permission_unknown_fallback" {
			return
		}
		trace, _ := ctx.Value(openAIImageSelectionTraceKey{}).(*openAIImageSelectionTrace)
		if trace == nil {
			return
		}
		// Legacy loops consult this helper before their ordinary gates. A
		// model-ineligible or inactive account is not evidence of image exhaustion.
		if _, excluded := req.ExcludedIDs[account.ID]; excluded {
			return
		}
		platform := req.Platform
		if platform == "" {
			platform = account.Platform
		}
		if openAICompatibleAccountEligibilityFailureReason(ctx, account, platform, model, req.RequireCompact, req.RequiredCapability) != "" {
			return
		}
		if req.RequirePrivacySet && !account.IsPrivacySet() {
			return
		}
		if openAICapacityTargetExcluded(ctx, account) || !s.isAccountSchedulableForRPM(ctx, account) {
			return
		}
		if s != nil {
			if s.isOpenAIAccountRequestRuntimeBlockedWithContext(ctx, account, model, req.RequireCompact) ||
				s.isOpenAIProxyStreamQuarantined(ctx, account) {
				return
			}
			// Trace collection must not invoke ApplyAccountSchedulingThreshold:
			// that ordinary admission helper also persists account cooldowns.
			if limits := s.rateLimitService; limits != nil && limits.settingService != nil && account.UpstreamConfigID == nil {
				now := time.Now().UTC()
				decision := EvaluateAccountSchedulingThreshold(account, limits.settingService.GetAccountSchedulingThresholds(ctx), now)
				if decision.ShouldPause && decision.Until != nil && decision.Until.After(now) {
					return
				}
			}
			if s.openAIGroupRequiresPrivacySet(ctx, req.GroupID) && !account.IsPrivacySet() {
				return
			}
			if !parentHealthyForShadow(account, s.parentAccountLookup(ctx)) {
				return
			}
			if req.GroupID != nil && s.needsUpstreamChannelRestrictionCheck(ctx, req.GroupID) &&
				s.isUpstreamModelRestrictedByChannel(ctx, *req.GroupID, account, model, req.RequireCompact) {
				return
			}
		}
		trace.filtered.Store(true)
	}()
	if account == nil || account.Platform != PlatformOpenAI {
		return ""
	}
	requires := s.AccountRequiresOpenAIImageCapability(ctx, req.GroupID, account)
	requiresResponses := s.AccountRequiresOpenAIResponsesForImage(ctx, req.GroupID, account)
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
