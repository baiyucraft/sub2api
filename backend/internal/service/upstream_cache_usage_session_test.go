package service

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestCacheGuardReliableSessionDoesNotInventConversationFromPrompt(t *testing.T) {
	for _, tc := range []struct{ name, header, value, body, want string }{
		{name: "header", header: "session_id", value: "stable-conversation", body: `{"model":"gpt-test"}`, want: "explicit:stable-conversation"},
		{name: "claude", header: "X-Claude-Code-Session-Id", value: "claude-conversation", body: `{"model":"claude-test"}`, want: "explicit:claude-conversation"},
		{name: "prompt_cache_key", body: `{"model":"gpt-test","prompt_cache_key":"cache-conversation"}`, want: "explicit:cache-conversation"},
		{name: "ws_envelope", body: `{"type":"response.create","response":{"model":"gpt-test","prompt_cache_key":"cache-conversation"}}`, want: "explicit:cache-conversation"},
		{name: "metadata", body: `{"metadata":{"user_id":"user_a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2_account__session_22345678-1234-1234-1234-123456789abc"}}`, want: "explicit:22345678-1234-1234-1234-123456789abc"},
		{name: "unrelated_user_id", body: `{"metadata":{"user_id":"same-user-all-conversations"}}`},
		{name: "content_fallback", body: `{"model":"gpt-test","instructions":"prefix","input":[{"role":"user","content":"identical prompt"}]}`},
		{name: "gemini_workspace", body: `{"contents":[{"parts":[{"text":".gemini/tmp/abcd1234/chats"}]}]}`},
		{name: "cached_content_handle", body: `{"cachedContent":"cachedContents/constant"}`},
		{name: "invalid_header", header: "session_id", value: "bad\tvalue", body: `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
			if tc.header != "" {
				c.Request.Header.Set(tc.header, tc.value)
			}
			if got := UpstreamCacheGuardLogicalSession(c, []byte(tc.body)); got != tc.want {
				t.Fatalf("logical session = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCacheGuardHolderRejectsMissingSchedulingIdentity(t *testing.T) {
	group := int64(4)
	ctx := WithUpstreamCacheGuardAttempt(context.Background(), 8, &group, "openai-pool-retry-one-off", true)
	if ctx.Value(cacheUsageAttemptContextKey{}) != nil {
		t.Fatal("one-off routing key fabricated a reliable conversation")
	}
	ctx = WithUpstreamCacheGuardLogicalSession(context.Background(), 8, "explicit:session")
	ctx = WithUpstreamCacheGuardAttempt(ctx, 8, &group, "route-digest", true)
	if ctx.Value(cacheUsageAttemptContextKey{}) == nil || ctx.Value(cacheGuardLogicalSessionKey{}) != "explicit:session" {
		t.Fatal("reliable session metadata did not reach holder")
	}
	ctx = WithUpstreamCacheGuardAttempt(context.Background(), 8, &group, "route-digest", false)
	if ctx.Value(cacheUsageAttemptContextKey{}) != nil {
		t.Fatal("nonstreaming request got an admission holder")
	}
}

func TestCacheGuardWSTurnSessionMarkerPrecedence(t *testing.T) {
	group := int64(4)
	for _, header := range []bool{false, true} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("GET", "/v1/responses", nil)
		if header {
			c.Request.Header.Set("session_id", "handshake")
		}
		seed := WithUpstreamCacheGuardRequestSession(context.Background(), 8, c, []byte(`{"prompt_cache_key":"first-conversation"}`))
		seed = WithUpstreamCacheGuardAttempt(seed, 8, &group, "routing-digest", true)
		second := WithUpstreamCacheGuardTurn(seed, []byte(`{"type":"response.create","prompt_cache_key":"next-conversation","model":"gpt-other"}`), false)
		want := "explicit:next-conversation"
		if header {
			want = "explicit:handshake"
		}
		if got := second.Value(cacheGuardLogicalSessionKey{}); got != want {
			t.Fatalf("WS logical conversation = %v, want %s", got, want)
		}
	}
}
