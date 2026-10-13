package service

import (
	"context"

	"github.com/gin-gonic/gin"
)

type cacheGuardInboundProtocolKey struct{}
type cacheGuardProtocolResolverKey struct{}
type cacheGuardProtocolResolver func(context.Context, *Account, string, string) string

func withCacheGuardInboundProtocol(ctx context.Context, c *gin.Context, body []byte) context.Context {
	protocol := APIProtocolResponses
	if c != nil && c.Request != nil && c.Request.URL != nil {
		if inbound := modelEndpointProtocol(c.Request.URL.Path); inbound != "" {
			protocol = inbound
		}
	}
	// The compatibility Chat Completions entrance also accepts a Responses
	// body. Match the effective inbound protocol used by its forwarder.
	if protocol == APIProtocolChatCompletions {
		view := openAIRequestPayloadView(body)
		if !view.Get("messages").Exists() && view.Get("input").Exists() {
			protocol = APIProtocolResponses
		}
	}
	return context.WithValue(ctx, cacheGuardInboundProtocolKey{}, protocol)
}

func cacheGuardInboundProtocol(ctx context.Context) string {
	if ctx != nil {
		if protocol, ok := ctx.Value(cacheGuardInboundProtocolKey{}).(string); ok && protocol != "" {
			return protocol
		}
	}
	return APIProtocolResponses
}

// The preference builder can read the actual account protocol without owning
// the OpenAI service or changing a sticky binding. Use the same resolver as
// forwarding, including model rules and its existing protocol catalog.
func (s *OpenAIGatewayService) withCacheGuardOpenAIProtocolResolver(ctx context.Context) context.Context {
	if ctx == nil || s == nil {
		return ctx
	}
	return context.WithValue(ctx, cacheGuardProtocolResolverKey{}, cacheGuardProtocolResolver(s.resolveUpstreamProtocolFor))
}

func cacheGuardAccountProtocol(ctx context.Context, account *Account, model, fallback string) string {
	// WebSocket observations have their own warm context even if this account
	// also supports native HTTP protocols. Do not consult HTTP routing here.
	if fallback == "openai_ws" || ctx == nil || account == nil {
		return fallback
	}
	resolve, ok := ctx.Value(cacheGuardProtocolResolverKey{}).(cacheGuardProtocolResolver)
	if !ok || resolve == nil {
		return fallback
	}
	switch resolve(ctx, account, cacheGuardInboundProtocol(ctx), model) {
	case APIProtocolAnthropic:
		return "anthropic"
	case APIProtocolChatCompletions, APIProtocolResponses:
		return "openai"
	default:
		return fallback
	}
}
