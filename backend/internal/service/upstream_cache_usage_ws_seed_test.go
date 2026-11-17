package service

import (
	"context"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/upstreamcacheusage"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestCacheGuardWSConnectionRecoversSamplingAfterIneligibleFirstTurn(t *testing.T) {
	for _, first := range []string{
		`{"type":"response.create","model":"gpt-test"}`,
		`{"type":"response.create","model":"image-test","prompt_cache_key":"image-conversation"}`,
		`{"type":"response.create","response":{"model":"gpt-test","prompt_cache_key":"image-conversation","tool_choice":{"type":"image_generation"}}}`,
	} {
		t.Run(first, func(t *testing.T) {
			store := newRunnerTestStore()
			runner := testRunner(t, store)
			runner.secret = []byte("ws-turn-test-only")
			require.NoError(t, runner.engine.Refresh(context.Background()))
			group, key, configID := int64(1), int64(3), int64(4)
			account := &Account{ID: 2, Platform: PlatformOpenAI, UpstreamConfigID: &configID, UpstreamKeyID: &key}
			seed := WithUpstreamCacheGuardWSConnectionSeed(context.Background(), 8, &group, "routing-only-digest", nil)
			group = 99 // callers cannot change the connection's authenticated group.
			require.Nil(t, seed.Value(cacheUsageAttemptContextKey{}))
			require.Nil(t, seed.Value(cacheGuardSchedulingKey{}))
			turns := newUpstreamCacheGuardWSTurns(seed)
			writes := 0
			conn := &upstreamCacheGuardWSFrameConn{turns: turns, account: account, inner: &cacheGuardFakeFrameConn{write: func() {
				writes++
				holder, _ := turns.pending[writes].Value(cacheUsageAttemptContextKey{}).(*cacheUsageAttempt)
				if writes == 1 {
					require.Nil(t, holder, "ineligible first frame must stay outside cache sampling")
				} else {
					require.NotNil(t, holder)
					require.True(t, holder.begun, "cache admission must precede the actual frame write")
					require.NotNil(t, holder.admission)
					require.Equal(t, int64(1), *holder.groupID)
				}
			}}}
			require.NoError(t, conn.WriteFrame(seed, coderws.MessageText, []byte(first)))
			turns.finish(1, nil, nil)
			body := []byte(`{"type":"response.create","model":"gpt-test","prompt_cache_key":"text-conversation"}`)
			require.NoError(t, conn.WriteFrame(seed, coderws.MessageText, body))
			second := turns.pending[2]
			retry, err := turns.beginAdmitted(2, account, body, "gpt-test", "openai_ws")
			require.NoError(t, err)
			require.Same(t, second, retry, "same-turn retry must retain its holder and admission")
			require.Equal(t, "explicit:text-conversation", second.Value(cacheGuardLogicalSessionKey{}))
			result := &OpenAIForwardResult{OpenAIWSMode: true, UpstreamTerminalEvent: "response.completed", CacheUsageEvidence: &UpstreamCacheUsageEvidence{
				Status: upstreamcacheusage.Known, Protocol: "openai", TotalInputTokens: 1000, CacheReadTokens: 900,
			}}
			turns.finish(2, result, nil)
			turns.finish(2, result, nil)
			store.mu.Lock()
			defer store.mu.Unlock()
			require.Len(t, store.events, 1, "only the later eligible turn records one completion")
			for _, event := range store.events {
				identity := second.Value(cacheGuardSchedulingKey{}).(cacheGuardSchedulingIdentity)
				require.Equal(t, identity.RequestID, event.Request.ID)
				require.Equal(t, "openai_ws", event.Request.Protocol)
				require.True(t, event.Success)
				require.Equal(t, int64(900), event.RawUsage.CacheReadTokens)
			}
		})
	}
}

func TestCacheGuardWSConnectionRechecksEveryTurnMarkerAndSelection(t *testing.T) {
	group := int64(1)
	seed := WithUpstreamCacheGuardRequestSession(context.Background(), 8, nil, []byte(`{"prompt_cache_key":"first-conversation"}`))
	seed = WithUpstreamCacheGuardWSConnectionSeed(seed, 8, &group, "routing-digest", nil)
	selection := &protectionSelection{trial: &openAITTFTTrial{}, newAllocation: true, selectionPath: "new_allocation"}
	seed = context.WithValue(seed, protectionSelectionKey{}, selection)
	turns := newUpstreamCacheGuardWSTurns(seed)
	first := turns.turnContext(1, []byte(`{"type":"response.create","model":"gpt-test","prompt_cache_key":"first-conversation"}`))
	missing := turns.turnContext(2, []byte(`{"type":"response.create","model":"gpt-test"}`))
	image := turns.turnContext(3, []byte(`{"type":"response.create","response":{"model":"image-test","prompt_cache_key":"next-conversation"}}`))
	later := turns.turnContext(4, []byte(`{"type":"response.create","response":{"model":"gpt-test","prompt_cache_key":"next-conversation"}}`))
	for _, excluded := range []context.Context{missing, image} {
		holder, _ := excluded.Value(cacheUsageAttemptContextKey{}).(*cacheUsageAttempt)
		require.Nil(t, holder)
	}
	firstHolder, _ := first.Value(cacheUsageAttemptContextKey{}).(*cacheUsageAttempt)
	laterHolder, _ := later.Value(cacheUsageAttemptContextKey{}).(*cacheUsageAttempt)
	require.NotNil(t, firstHolder)
	require.NotNil(t, laterHolder)
	require.NotSame(t, firstHolder, laterHolder)
	require.Equal(t, "", missing.Value(cacheGuardLogicalSessionKey{}), "later frames cannot borrow the first body's marker")
	require.Equal(t, "explicit:next-conversation", later.Value(cacheGuardLogicalSessionKey{}))
	require.NotEqual(t, first.Value(cacheGuardSchedulingKey{}).(cacheGuardSchedulingIdentity).RequestID, later.Value(cacheGuardSchedulingKey{}).(cacheGuardSchedulingIdentity).RequestID)
	for _, next := range []context.Context{missing, image, later} {
		selected := next.Value(protectionSelectionKey{}).(*protectionSelection)
		require.Nil(t, selected.trial)
		require.False(t, selected.newAllocation)
		require.Equal(t, "session_binding", selected.selectionPath)
	}
	require.NotNil(t, selection.trial)
	require.True(t, selection.newAllocation, "deriving later turns must not mutate the first selection")
}

func TestCacheGuardWSConnectionHandshakeSessionSurvivesIneligibleFirstTurn(t *testing.T) {
	group := int64(1)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/v1/responses", nil)
	c.Request.Header.Set("session_id", "handshake-session")
	seed := WithUpstreamCacheGuardWSConnectionSeed(context.Background(), 8, &group, "routing-digest", c)
	c.Request.Header.Set("session_id", "mutated-after-handshake")
	turns := newUpstreamCacheGuardWSTurns(seed)
	image := turns.turnContext(1, []byte(`{"type":"response.create","model":"image-test"}`))
	holder, _ := image.Value(cacheUsageAttemptContextKey{}).(*cacheUsageAttempt)
	require.Nil(t, holder)
	for i, body := range []string{
		`{"type":"response.create","model":"gpt-test"}`,
		`{"type":"response.create","model":"gpt-test","prompt_cache_key":"weaker-body-marker"}`,
	} {
		turn := turns.turnContext(i+2, []byte(body))
		holder, _ := turn.Value(cacheUsageAttemptContextKey{}).(*cacheUsageAttempt)
		require.NotNil(t, holder)
		require.Equal(t, "explicit:handshake-session", turn.Value(cacheGuardLogicalSessionKey{}))
		require.Equal(t, true, turn.Value(cacheGuardLogicalSessionHeaderKey{}))
	}
}

func TestCacheGuardWSConnectionReplacementRetainsOnlyCurrentTurnSession(t *testing.T) {
	group := int64(1)
	seed := WithUpstreamCacheGuardWSConnectionSeed(context.Background(), 8, &group, "routing-digest", nil)
	failed := newUpstreamCacheGuardWSTurns(seed).turnContext(2, []byte(`{"type":"response.create","model":"gpt-test","prompt_cache_key":"current-turn"}`))
	err := newOpenAIWSCurrentTurnFailoverError(fmt.Errorf("capacity"), nil, failed)
	restored := ContextWithOpenAIWSCurrentTurnRetryIdentity(context.Background(), err, 8)
	restored = WithUpstreamCacheGuardWSConnectionSeed(restored, 8, &group, "replacement-route", nil)
	turns := newUpstreamCacheGuardWSTurns(restored)
	// A failover payload may omit its session marker. This is still the same
	// logical turn, even though it is the replacement bridge's first frame.
	replacement := turns.turnContext(1, []byte(`{"type":"response.create","model":"gpt-test"}`))
	holder, _ := replacement.Value(cacheUsageAttemptContextKey{}).(*cacheUsageAttempt)
	require.NotNil(t, holder)
	require.Equal(t, "explicit:current-turn", replacement.Value(cacheGuardLogicalSessionKey{}))
	require.Equal(t, failed.Value(cacheGuardSchedulingKey{}), replacement.Value(cacheGuardSchedulingKey{}))
	require.NotSame(t, failed.Value(cacheUsageAttemptContextKey{}), holder)
	later := turns.turnContext(2, []byte(`{"type":"response.create","model":"gpt-test"}`))
	require.Equal(t, "", later.Value(cacheGuardLogicalSessionKey{}))
	holder, _ = later.Value(cacheUsageAttemptContextKey{}).(*cacheUsageAttempt)
	require.Nil(t, holder, "current-turn failover metadata cannot authorize future turns")
}
