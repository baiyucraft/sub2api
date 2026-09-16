package service

import "context"

type sessionSwitchStickyScopeContextKey struct{}
type sessionSwitchStickyOperationContextKey struct{}

// WithSessionSwitchStickyScope isolates sticky-session cache entries for a
// speed-first API key. Cache-first keys keep the upstream cache namespace.
func WithSessionSwitchStickyScope(ctx context.Context, apiKey *APIKey) context.Context {
	if ctx == nil || apiKey == nil || apiKey.ID <= 0 {
		return ctx
	}
	mode, err := NormalizeAPIKeySchedulingMode(apiKey.SchedulingMode)
	if err != nil || mode != APIKeySchedulingModeSpeedFirst {
		return ctx
	}
	return context.WithValue(ctx, sessionSwitchStickyScopeContextKey{}, apiKey.ID)
}

// SessionSwitchStickyScopeAPIKeyID returns the API key namespace attached by
// authentication or by the session-switch runtime. Zero means upstream sticky
// cache behavior must remain unchanged.
func SessionSwitchStickyScopeAPIKeyID(ctx context.Context) int64 {
	if ctx == nil {
		return 0
	}
	apiKeyID, _ := ctx.Value(sessionSwitchStickyScopeContextKey{}).(int64)
	if apiKeyID <= 0 {
		return 0
	}
	return apiKeyID
}

// WithSessionSwitchStickyOperation marks a cache call as an actual sticky
// binding operation. Other GatewayCache users reuse the same primitive methods
// for Responses/WS state and must never inherit the API-key namespace.
func WithSessionSwitchStickyOperation(ctx context.Context) context.Context {
	if ctx == nil {
		return ctx
	}
	return context.WithValue(ctx, sessionSwitchStickyOperationContextKey{}, true)
}

func SessionSwitchStickyNamespaceAPIKeyID(ctx context.Context) int64 {
	if ctx == nil {
		return 0
	}
	stickyOperation, _ := ctx.Value(sessionSwitchStickyOperationContextKey{}).(bool)
	if !stickyOperation {
		return 0
	}
	return SessionSwitchStickyScopeAPIKeyID(ctx)
}
