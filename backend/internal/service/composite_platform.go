package service

import (
	"context"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
)

// WithResolvedTargetPlatform stores the concrete provider chosen for a request
// made through a composite group.
func WithResolvedTargetPlatform(ctx context.Context, platform string) context.Context {
	platform = strings.TrimSpace(platform)
	if ctx == nil || platform == "" {
		return ctx
	}
	return context.WithValue(ctx, ctxkey.ResolvedTargetPlatform, platform)
}

// ResolvedTargetPlatformFromContext returns the concrete provider chosen for
// the current request, if one was resolved.
func ResolvedTargetPlatformFromContext(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	platform, ok := ctx.Value(ctxkey.ResolvedTargetPlatform).(string)
	platform = strings.TrimSpace(platform)
	if !ok || platform == "" {
		return "", false
	}
	return platform, true
}

func WithCompositeRouteDecision(ctx context.Context, decision CompositeRouteDecision) context.Context {
	if ctx == nil || !decision.Matched {
		return ctx
	}
	ctx = WithResolvedTargetPlatform(ctx, decision.TargetPlatform)
	if model := strings.TrimSpace(decision.UpstreamModel); model != "" {
		ctx = context.WithValue(ctx, ctxkey.ResolvedUpstreamModel, model)
	}
	if model := strings.TrimSpace(decision.PublicModel); model != "" {
		ctx = context.WithValue(ctx, ctxkey.RequestedPublicModel, model)
	}
	if source := strings.TrimSpace(decision.Source); source != "" {
		ctx = context.WithValue(ctx, ctxkey.CompositeRouteSource, source)
	}
	return ctx
}

func ResolvedUpstreamModelFromContext(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	model, ok := ctx.Value(ctxkey.ResolvedUpstreamModel).(string)
	model = strings.TrimSpace(model)
	if !ok || model == "" {
		return "", false
	}
	return model, true
}

func RequestedPublicModelFromContext(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	model, ok := ctx.Value(ctxkey.RequestedPublicModel).(string)
	model = strings.TrimSpace(model)
	if !ok || model == "" {
		return "", false
	}
	return model, true
}

func WithRequestedPublicModel(ctx context.Context, model string) context.Context {
	model = strings.TrimSpace(model)
	if ctx == nil || model == "" {
		return ctx
	}
	return context.WithValue(ctx, ctxkey.RequestedPublicModel, model)
}

func CompositeRouteSourceFromContext(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	source, ok := ctx.Value(ctxkey.CompositeRouteSource).(string)
	source = strings.TrimSpace(source)
	if !ok || source == "" {
		return "", false
	}
	return source, true
}

// DetectModelPlatform maps common public model IDs to the concrete provider
// platform used by sub2api. It intentionally returns false for ambiguous model
// names so composite groups fail closed instead of guessing.
func DetectModelPlatform(model string) (string, bool) {
	platform, status := detectModelPlatformDetailed(model)
	return platform, status == UpstreamKeyModelRouteStatusAvailable
}

func detectModelPlatformDetailed(model string) (string, string) {
	normalized := strings.ToLower(strings.TrimSpace(model))
	if normalized == "" {
		return "", UpstreamKeyModelRouteStatusUnknown
	}

	hadModelsPrefix := strings.HasPrefix(normalized, "models/")
	normalized = strings.TrimPrefix(normalized, "models/")
	if slash := strings.IndexByte(normalized, '/'); slash > 0 {
		provider := strings.TrimSpace(normalized[:slash])
		rest := strings.TrimSpace(normalized[slash+1:])
		switch provider {
		case "anthropic", "claude":
			return PlatformAnthropic, UpstreamKeyModelRouteStatusAvailable
		case "openai", "chatgpt":
			return PlatformOpenAI, UpstreamKeyModelRouteStatusAvailable
		case "google", "google-ai-studio", "gemini":
			return PlatformGemini, UpstreamKeyModelRouteStatusAvailable
		case "antigravity":
			return PlatformAntigravity, UpstreamKeyModelRouteStatusAvailable
		case "xai", "x-ai", "grok":
			return PlatformGrok, UpstreamKeyModelRouteStatusAvailable
		case "kimi", "moonshot":
			return PlatformKimi, UpstreamKeyModelRouteStatusAvailable
		case "zhipu", "glm", "bigmodel":
			return PlatformZhipu, UpstreamKeyModelRouteStatusAvailable
		case "deepseek":
			return PlatformDeepseek, UpstreamKeyModelRouteStatusAvailable
		case "minimax":
			return PlatformMiniMax, UpstreamKeyModelRouteStatusAvailable
		}
		if rest != "" {
			normalized = strings.TrimPrefix(rest, "models/")
		}
	}
	// Explicit model families carry stronger provider intent than a mirrored
	// catalog entry. Antigravity exposes some Claude IDs internally, but a
	// bare claude-* model remains an Anthropic request unless qualified by an
	// antigravity/ provider prefix above. Likewise, the Gemini API's models/
	// namespace is an explicit Gemini qualification.
	if strings.HasPrefix(normalized, "claude-") || strings.HasPrefix(normalized, "anthropic.claude-") {
		return PlatformAnthropic, UpstreamKeyModelRouteStatusAvailable
	}
	if hadModelsPrefix && (strings.HasPrefix(normalized, "gemini-") || strings.HasPrefix(normalized, "learnlm-")) {
		return PlatformGemini, UpstreamKeyModelRouteStatusAvailable
	}
	if platform, matched, ambiguous := DetectRegisteredModelPlatform(normalized); ambiguous {
		// Claude IDs are unambiguous in the public API even though Antigravity
		// mirrors some Claude catalog entries internally. Preserve the explicit
		// Claude family prefix instead of turning normal Anthropic models into an
		// ambiguous route. The Gemini API's `models/` namespace is likewise an
		// explicit provider-qualified form; bare overlapping Gemini IDs remain
		// ambiguous and require provider metadata or manual routing.
		return "", UpstreamKeyModelRouteStatusAmbiguous
	} else if matched && IsConcreteRequestPlatform(platform) {
		return platform, UpstreamKeyModelRouteStatusAvailable
	}

	switch {
	case strings.HasPrefix(normalized, "anthropic.claude-"),
		strings.HasPrefix(normalized, "claude-"):
		return PlatformAnthropic, UpstreamKeyModelRouteStatusAvailable
	case strings.HasPrefix(normalized, "gpt-"),
		strings.HasPrefix(normalized, "chatgpt-"),
		strings.HasPrefix(normalized, "codex-"),
		strings.HasPrefix(normalized, "text-embedding-"),
		strings.HasPrefix(normalized, "text-moderation-"),
		strings.HasPrefix(normalized, "omni-moderation-"),
		strings.HasPrefix(normalized, "dall-e-"),
		strings.HasPrefix(normalized, "gpt-image-"),
		strings.HasPrefix(normalized, "tts-"),
		strings.HasPrefix(normalized, "whisper-"),
		hasOpenAISeriesPrefix(normalized):
		return PlatformOpenAI, UpstreamKeyModelRouteStatusAvailable
	case strings.HasPrefix(normalized, "gemini-"),
		strings.HasPrefix(normalized, "learnlm-"):
		return PlatformGemini, UpstreamKeyModelRouteStatusAvailable
	case normalized == "grok" || strings.HasPrefix(normalized, "grok-"):
		return PlatformGrok, UpstreamKeyModelRouteStatusAvailable
	case normalized == "k3",
		normalized == "k3-256k",
		strings.HasPrefix(normalized, "kimi-"),
		strings.HasPrefix(normalized, "moonshot-"):
		return PlatformKimi, UpstreamKeyModelRouteStatusAvailable
	case strings.HasPrefix(normalized, "glm-"):
		return PlatformZhipu, UpstreamKeyModelRouteStatusAvailable
	case strings.HasPrefix(normalized, "deepseek-"):
		return PlatformDeepseek, UpstreamKeyModelRouteStatusAvailable
	case strings.HasPrefix(normalized, "minimax-"),
		strings.HasPrefix(normalized, "abab5"),
		strings.HasPrefix(normalized, "abab6"),
		strings.HasPrefix(normalized, "abab7"):
		return PlatformMiniMax, UpstreamKeyModelRouteStatusAvailable
	default:
		return "", UpstreamKeyModelRouteStatusUnknown
	}
}

func hasOpenAISeriesPrefix(model string) bool {
	for _, prefix := range []string{"o1", "o2", "o3", "o4", "o5"} {
		if model == prefix || strings.HasPrefix(model, prefix+"-") {
			return true
		}
	}
	return false
}

func (s *GatewayService) resolveCompositeRouteDecision(ctx context.Context, group *Group, requestedModel, endpoint string) (CompositeRouteDecision, bool, error) {
	if group == nil || group.Platform != PlatformComposite {
		return CompositeRouteDecision{}, false, nil
	}
	if platform, ok := ResolvedTargetPlatformFromContext(ctx); ok {
		upstreamModel := requestedModel
		if resolvedModel, modelOK := ResolvedUpstreamModelFromContext(ctx); modelOK {
			upstreamModel = resolvedModel
		}
		source := CompositeRouteSourceDetector
		if resolvedSource, sourceOK := CompositeRouteSourceFromContext(ctx); sourceOK {
			source = resolvedSource
		}
		return CompositeRouteDecision{
			Matched:        true,
			Source:         source,
			GroupID:        group.ID,
			PublicModel:    requestedModel,
			TargetPlatform: platform,
			UpstreamModel:  upstreamModel,
			Endpoint:       normalizeCompositeRouteEndpoint(endpoint),
		}, true, nil
	}
	decision, err := s.compositeResolver.Resolve(ctx, group.ID, requestedModel, endpoint)
	if err != nil {
		return decision, false, err
	}
	return decision, decision.Matched, nil
}

func isConcreteRequestPlatform(platform string) bool {
	return IsConcreteRequestPlatform(platform)
}
