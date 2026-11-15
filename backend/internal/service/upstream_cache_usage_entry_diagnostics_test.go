package service

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestCacheGuardExcludedEntryDiagnosticsDoNotAdmitOrInventSessions(t *testing.T) {
	for _, tc := range []struct {
		name, event, path, body, session string
		stream                           bool
	}{
		{name: "native_non_streaming", event: "skipped_non_streaming", path: "/v1beta/models/gemini:generateContent", body: `{}`, session: "conversation"},
		{name: "native_missing_session", event: "skipped_missing_reliable_session", path: "/v1beta/models/gemini:streamGenerateContent", body: `{}`, stream: true},
		{name: "cache_handle_is_not_session", event: "skipped_missing_reliable_session", path: "/v1beta/models/gemini:streamGenerateContent", body: `{"cachedContent":"cachedContents/example"}`, stream: true},
		{name: "explicit_body_session", event: "eligible_text_request", path: "/v1/responses", body: `{"prompt_cache_key":"conversation"}`, stream: true},
		{name: "explicit_header_session", event: "eligible_text_request", path: "/v1/messages", body: `{}`, session: "conversation", stream: true},
		{name: "unsupported_media", event: "skipped_unsupported_request", path: "/v1/images/generations", body: `{}`, session: "conversation", stream: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := cacheGuardTestRunner(t)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", tc.path, nil)
			c.Request.Header.Set("X-Claude-Code-Session-Id", tc.session)
			groupID, keyID := int64(7), int64(23)
			account := &Account{ID: 12, UpstreamKeyID: &keyID, Platform: PlatformGemini}
			ctx := WithUpstreamCacheGuardSamplingDiagnostics(context.Background(), &groupID, c, []byte(tc.body), tc.stream)
			// Diagnostics alone must not install scheduling, a logical session or
			// an admission, even when the request is otherwise eligible.
			require.Nil(t, ctx.Value(cacheGuardSchedulingKey{}))
			require.Nil(t, ctx.Value(cacheGuardLogicalSessionKey{}))
			require.Nil(t, ctx.Value(cacheUsageAttemptContextKey{}))
			require.NoError(t, BeginUpstreamCacheGuardSend(ctx, account, "actual-model", "gemini"))
			require.NoError(t, BeginUpstreamCacheGuardSend(ctx, account, "actual-model", "gemini"))
			rows, _ := currentUpstreamCacheGuardRunner().diagnostics.snapshot(account.ID, keyID)
			require.Len(t, rows, 1)
			require.Equal(t, map[string]uint64{tc.event: 1}, rows[0].Counts)
			require.Empty(t, store.events)
		})
	}
}

func TestCacheGuardEntryDiagnosticsFreezeActualSendAndResetForWSTurn(t *testing.T) {
	cacheGuardTestRunner(t)
	groupID, keyID := int64(7), int64(23)
	account := &Account{ID: 12, UpstreamKeyID: &keyID, Platform: PlatformOpenAI}
	ctx := WithUpstreamCacheGuardSamplingDiagnostics(context.Background(), &groupID, nil, []byte(`{"prompt_cache_key":"conversation"}`), true)
	ctx = context.WithValue(ctx, cacheGuardLogicalSessionKey{}, "explicit:conversation")
	turns := newUpstreamCacheGuardWSTurns(ctx)
	first := turns.turnContext(1, []byte(`{"type":"response.create"}`))
	PrepareOpenAIUpstreamCacheGuardSend(first, account, "actual-model", "openai_ws")
	rows, _ := currentUpstreamCacheGuardRunner().diagnostics.snapshot(account.ID, keyID)
	// Merely preparing a request does not claim a physical send.
	require.Empty(t, rows)
	markUpstreamCacheGuardSend(first, account)
	markUpstreamCacheGuardSend(first, account)
	second := turns.turnContext(2, []byte(`{"type":"response.create"}`))
	PrepareOpenAIUpstreamCacheGuardSend(second, account, "actual-model", "openai_ws")
	markUpstreamCacheGuardSend(second, account)
	rows, _ = currentUpstreamCacheGuardRunner().diagnostics.snapshot(account.ID, keyID)
	require.Len(t, rows, 1)
	require.Equal(t, uint64(2), rows[0].Counts["eligible_text_request"])
}
