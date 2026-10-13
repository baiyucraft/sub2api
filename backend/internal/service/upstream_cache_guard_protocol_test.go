package service

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestCacheGuardRequestCapturesEffectiveInboundProtocol(t *testing.T) {
	for _, tc := range []struct {
		name, path, body, want string
	}{
		{"responses", "/v1/responses", `{}`, APIProtocolResponses},
		{"chat", "/v1/chat/completions", `{"messages":[]}`, APIProtocolChatCompletions},
		{"messages", "/v1/messages", `{}`, APIProtocolAnthropic},
		{"prefixed_messages", "/compat/v1/messages", `{}`, APIProtocolAnthropic},
		{"responses_shaped_chat", "/v1/chat/completions", `{"input":[]}`, APIProtocolResponses},
		{"missing_request", "", `{}`, APIProtocolResponses},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			if tc.path != "" {
				c.Request = httptest.NewRequest("POST", tc.path, nil)
			}
			ctx := WithUpstreamCacheGuardRequestSession(context.Background(), 7, c, []byte(tc.body))
			if got := cacheGuardInboundProtocol(ctx); got != tc.want {
				t.Fatalf("inbound protocol = %q, want %q", got, tc.want)
			}
		})
	}
	ctx := WithUpstreamCacheGuardRequestSession(context.Background(), 7, nil, nil)
	if got := cacheGuardInboundProtocol(ctx); got != APIProtocolResponses {
		t.Fatalf("nil request protocol = %q", got)
	}
}

func TestCacheGuardAccountProtocolUsesNativeAnthropicAndModelRules(t *testing.T) {
	s := &OpenAIGatewayService{}
	ctx := s.withCacheGuardOpenAIProtocolResolver(context.Background())
	native := &Account{Platform: PlatformKimi, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_protocol": APIProtocolAnthropic}}
	if got := cacheGuardAccountProtocol(ctx, native, "kimi-test", "openai"); got != "anthropic" {
		t.Fatalf("native Anthropic warm context = %q", got)
	}
	adaptive := &Account{
		Platform: PlatformCommandCode,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_protocol": APIProtocolAdaptive,
			"protocol_rules": []any{
				map[string]any{"pattern": "claude-*", "protocol": APIProtocolAnthropic},
				map[string]any{"pattern": "gpt-*", "protocol": APIProtocolResponses},
			},
		},
	}
	for _, tc := range []struct{ model, want string }{
		{"claude-sonnet-4-6", "anthropic"},
		{"gpt-5.5", "openai"},
		{"other-model", "openai"},
	} {
		if got := cacheGuardAccountProtocol(ctx, adaptive, tc.model, "openai"); got != tc.want {
			t.Fatalf("adaptive %s warm context = %q, want %q", tc.model, got, tc.want)
		}
	}
}

func TestCacheGuardAccountProtocolPreservesInboundModelAndWSContext(t *testing.T) {
	account := &Account{ID: 9}
	ctx := context.WithValue(context.Background(), cacheGuardInboundProtocolKey{}, APIProtocolAnthropic)
	calls := 0
	ctx = context.WithValue(ctx, cacheGuardProtocolResolverKey{}, cacheGuardProtocolResolver(func(gotCtx context.Context, gotAccount *Account, inbound, model string) string {
		calls++
		if gotCtx != ctx || gotAccount != account || inbound != APIProtocolAnthropic || model != "canonical-model" {
			t.Fatalf("resolver lost request context, source account, inbound or canonical model")
		}
		return APIProtocolAnthropic
	}))
	if got := cacheGuardAccountProtocol(ctx, account, "canonical-model", "openai"); got != "anthropic" || calls != 1 {
		t.Fatalf("resolved context = %q, calls = %d", got, calls)
	}
	if got := cacheGuardAccountProtocol(ctx, account, "canonical-model", "openai_ws"); got != "openai_ws" || calls != 1 {
		t.Fatalf("WS context = %q, calls = %d", got, calls)
	}
	if got := cacheGuardAccountProtocol(context.Background(), account, "canonical-model", "anthropic"); got != "anthropic" {
		t.Fatalf("context without OpenAI resolver changed gateway protocol to %q", got)
	}
}

func TestCacheGuardAdaptiveModelProtocolFollowsRequestEntrance(t *testing.T) {
	s := &OpenAIGatewayService{}
	account := &Account{
		Platform: PlatformCommandCode,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_protocol": APIProtocolAdaptive,
			"protocol_rules": []any{map[string]any{
				"pattern": "shared-*", "protocols": []any{APIProtocolResponses, APIProtocolAnthropic},
			}},
		},
	}
	for _, tc := range []struct{ path, want string }{
		{"/v1/messages", "anthropic"},
		{"/v1/responses", "openai"},
		{"/v1/chat/completions", "openai"},
	} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("POST", tc.path, nil)
		ctx := WithUpstreamCacheGuardRequestSession(context.Background(), 7, c, []byte(`{"messages":[]}`))
		ctx = s.withCacheGuardOpenAIProtocolResolver(ctx)
		if got := cacheGuardAccountProtocol(ctx, account, "shared-model", "openai"); got != tc.want {
			t.Fatalf("adaptive model through %s warm context = %q, want %q", tc.path, got, tc.want)
		}
	}
}
