package service

import (
	"context"

	"github.com/gin-gonic/gin"
)

type cacheGuardLogicalSessionKey struct{}
type cacheGuardLogicalSessionHeaderKey struct{}
type cacheGuardWSConnectionKey struct{}

// Connection metadata survives an ineligible first frame, but never grants
// sampling eligibility by itself. Each response.create supplies its own marker.
type cacheGuardWSConnectionSeed struct {
	apiKeyID      int64
	groupID       int64
	sessionHash   string
	headerSession string
	firstSession  string
}

func WithUpstreamCacheGuardWSConnectionSeed(ctx context.Context, apiKeyID int64, groupID *int64, sessionHash string, c *gin.Context) context.Context {
	if ctx == nil || apiKeyID <= 0 || groupID == nil {
		return ctx
	}
	seed := cacheGuardWSConnectionSeed{apiKeyID: apiKeyID, groupID: *groupID, sessionHash: sessionHash}
	if header := ExtractClientSessionID(c); header != "" {
		seed.headerSession = "explicit:" + header
	}
	// The first physical turn can be a replacement bridge for an already
	// identified turn. Preserve that current-turn identity across failover only.
	if identity, ok := ctx.Value(cacheGuardSchedulingKey{}).(cacheGuardSchedulingIdentity); ok && identity.APIKeyID == apiKeyID && identity.RequestID != "" {
		seed.firstSession, _ = ctx.Value(cacheGuardLogicalSessionKey{}).(string)
	}
	return context.WithValue(ctx, cacheGuardWSConnectionKey{}, seed)
}

// The routing hash may include the entire prompt or model. Cache protection
// needs an independently stable conversation identity across rounds/models.
// Missing markers stay neutral instead of inventing distinct trial sessions.
func UpstreamCacheGuardLogicalSession(c *gin.Context, body []byte) string {
	if session := ExtractClientSessionID(c); session != "" {
		return "explicit:" + session
	}
	view := openAIRequestPayloadView(body)
	if session := sanitizeSessionID(view.Get("prompt_cache_key").String()); session != "" {
		return "explicit:" + session
	}
	if metadata := ParseMetadataUserID(view.Get("metadata.user_id").String()); metadata != nil {
		if session := sanitizeSessionID(metadata.SessionID); session != "" {
			return "explicit:" + session
		}
	}
	return ""
}

func WithUpstreamCacheGuardLogicalSession(ctx context.Context, apiKeyID int64, session string) context.Context {
	if ctx == nil || session == "" {
		return ctx
	}
	ctx = WithUpstreamCacheGuardScheduling(ctx, apiKeyID)
	return context.WithValue(ctx, cacheGuardLogicalSessionKey{}, session)
}

func WithUpstreamCacheGuardRequestSession(ctx context.Context, apiKeyID int64, c *gin.Context, body []byte) context.Context {
	if ctx == nil {
		return nil
	}
	ctx = WithUpstreamCacheGuardScheduling(ctx, apiKeyID)
	ctx = WithUpstreamCacheGuardLogicalSession(ctx, apiKeyID, UpstreamCacheGuardLogicalSession(c, body))
	ctx = context.WithValue(ctx, cacheGuardLogicalSessionHeaderKey{}, ExtractClientSessionID(c) != "")
	return withCacheGuardInboundProtocol(ctx, c, body)
}
