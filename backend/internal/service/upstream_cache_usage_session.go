package service

import (
	"context"

	"github.com/gin-gonic/gin"
)

type cacheGuardLogicalSessionKey struct{}
type cacheGuardLogicalSessionHeaderKey struct{}

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
	ctx = WithUpstreamCacheGuardLogicalSession(ctx, apiKeyID, UpstreamCacheGuardLogicalSession(c, body))
	ctx = context.WithValue(ctx, cacheGuardLogicalSessionHeaderKey{}, ExtractClientSessionID(c) != "")
	return withCacheGuardInboundProtocol(ctx, c, body)
}
