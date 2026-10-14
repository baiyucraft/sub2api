package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/upstreamcacheusage"
	"github.com/Wei-Shaw/sub2api/internal/upstreamcacheguard"
	coderws "github.com/coder/websocket"
)

type cacheGuardEvidenceTestStore struct {
	upstreamcacheguard.Store
	events []upstreamcacheguard.Evidence
}

func (s *cacheGuardEvidenceTestStore) Enqueue(_ context.Context, e upstreamcacheguard.Evidence) error {
	s.events = append(s.events, e)
	return nil
}

func cacheGuardTestRunner(t *testing.T) *cacheGuardEvidenceTestStore {
	t.Helper()
	store := &cacheGuardEvidenceTestStore{}
	runner := &UpstreamCacheGuardRunner{store: store, wake: make(chan struct{}, 1)}
	old := installedCacheGuardRunner.Swap(runner)
	t.Cleanup(func() { installedCacheGuardRunner.Store(old) })
	return store
}

func cacheGuardTestAdmittedContext() context.Context {
	group := int64(4)
	ctx := WithUpstreamCacheGuardScheduling(context.Background(), 8)
	ctx = WithUpstreamCacheGuardAttempt(ctx, 8, &group, "logical-session", true)
	attempt := ctx.Value(cacheUsageAttemptContextKey{}).(*cacheUsageAttempt)
	attempt.begun = true
	attempt.admission = &UpstreamCacheGuardAdmission{
		Request:   upstreamcacheguard.Request{ID: "request", GroupID: group, AccountID: 12, Protocol: "openai_ws", StartedAt: time.Now()},
		Admission: upstreamcacheguard.Admission{Sample: true, WarmAtStart: true},
	}
	return ctx
}

func TestCacheGuardLifecycleFreezesBeforeBillingAndCompletesOnce(t *testing.T) {
	store := cacheGuardTestRunner(t)
	ctx := cacheGuardTestAdmittedContext()
	raw := &UpstreamCacheUsageEvidence{Status: upstreamcacheusage.Known, Protocol: "openai", TotalInputTokens: 2000, CacheReadTokens: 0}
	result := &OpenAIForwardResult{Usage: OpenAIUsage{CacheUsageEvidence: raw}, OpenAIWSMode: true, UpstreamTerminalEvent: "response.completed"}
	FinishOpenAIUpstreamCacheGuardAttempt(ctx, result, nil)
	FinishOpenAIUpstreamCacheGuardAttempt(ctx, result, nil)
	if len(store.events) != 1 || !store.events[0].Success || store.events[0].CompletedAt.IsZero() {
		t.Fatalf("completion was lost or duplicated: %+v", store.events)
	}
	if store.events[0].Request.Protocol != "openai_ws" || store.events[0].RawUsage.Protocol != "openai" {
		t.Fatalf("raw family replaced the warm transport identity: %+v", store.events[0])
	}
	raw.CacheReadTokens = 1800
	if store.events[0].RawUsage.CacheReadTokens != 0 || raw.Success || !raw.CompletedAt.IsZero() {
		t.Fatalf("billing/later frames mutated frozen evidence: %+v", store.events[0])
	}
}

func TestCacheGuardLifecycleExcludesUnknownInvalidFailedAndIncomplete(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		status                   upstreamcacheusage.Status
		incomplete, disconnected bool
		terminal                 string
		err                      error
	}{
		{name: "missing", status: upstreamcacheusage.Absent},
		{name: "invalid", status: upstreamcacheusage.Invalid},
		{name: "upstream_error", status: upstreamcacheusage.Known, err: errors.New("stream failed")},
		{name: "incomplete", status: upstreamcacheusage.Known, incomplete: true},
		{name: "client_disconnected", status: upstreamcacheusage.Known, disconnected: true},
		{name: "failed_terminal", status: upstreamcacheusage.Known, terminal: "response.failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := cacheGuardTestRunner(t)
			ctx := cacheGuardTestAdmittedContext()
			raw := &UpstreamCacheUsageEvidence{Status: tc.status, Protocol: "openai", TotalInputTokens: 2000, CacheReadTokens: 1800, StreamIncomplete: tc.incomplete}
			terminal := tc.terminal
			if terminal == "" {
				terminal = "response.completed"
			}
			result := &OpenAIForwardResult{CacheUsageEvidence: raw, ClientDisconnect: tc.disconnected, OpenAIWSMode: true, UpstreamTerminalEvent: terminal}
			FinishOpenAIUpstreamCacheGuardAttempt(ctx, result, tc.err)
			if len(store.events) != 1 || store.events[0].Success || store.events[0].ValidSample() {
				t.Fatalf("invalid response counted as a successful sample: %+v", store.events)
			}
		})
	}
}

func TestCacheGuardWSTurnIdentityAndRetryScope(t *testing.T) {
	group := int64(4)
	seed := WithUpstreamCacheGuardScheduling(context.Background(), 8)
	seed = WithUpstreamCacheGuardAttempt(seed, 8, &group, "stable-session", true)
	group = 99 // callers cannot mutate the metadata frozen by admission.
	turns := newUpstreamCacheGuardWSTurns(seed)
	body := []byte(`{"type":"response.create","model":"gpt-test","input":"text"}`)
	first := turns.begin(1, nil, body, "gpt-test", "openai_ws")
	retry := turns.begin(1, nil, body, "gpt-test", "openai_ws")
	second := turns.begin(2, nil, body, "gpt-test", "openai_ws")
	if first != retry {
		t.Fatal("same-turn retry got a second holder")
	}
	firstID := first.Value(cacheGuardSchedulingKey{}).(cacheGuardSchedulingIdentity).RequestID
	secondID := second.Value(cacheGuardSchedulingKey{}).(cacheGuardSchedulingIdentity).RequestID
	if firstID != seed.Value(cacheGuardSchedulingKey{}).(cacheGuardSchedulingIdentity).RequestID || firstID == secondID {
		t.Fatal("each turn must retain scheduling ID only on its own request")
	}
	firstHolder := first.Value(cacheUsageAttemptContextKey{}).(*cacheUsageAttempt)
	secondHolder := second.Value(cacheUsageAttemptContextKey{}).(*cacheUsageAttempt)
	if firstHolder == secondHolder || *secondHolder.groupID != 4 || firstHolder.sessionHash != secondHolder.sessionHash {
		t.Fatal("WS turns mixed physical admission with logical session")
	}
	turns.finish(1, nil, errors.New("failed"))
	if !firstHolder.finished || secondHolder.finished {
		t.Fatal("first completion touched another turn")
	}
	turns.finishRemaining()
	if !secondHolder.finished || len(turns.pending) != 0 {
		t.Fatal("unfinished sent turns were not closed as failures")
	}
}

type cacheGuardFakeFrameConn struct{ write func() }

func (c *cacheGuardFakeFrameConn) ReadFrame(context.Context) (coderws.MessageType, []byte, error) {
	return coderws.MessageText, nil, nil
}
func (c *cacheGuardFakeFrameConn) WriteFrame(context.Context, coderws.MessageType, []byte) error {
	if c.write != nil {
		c.write()
	}
	return nil
}
func (c *cacheGuardFakeFrameConn) Close() error { return nil }

func TestCacheGuardWSBeginRunsBeforePhysicalFrameWrite(t *testing.T) {
	group := int64(4)
	ctx := WithUpstreamCacheGuardAttempt(WithUpstreamCacheGuardScheduling(context.Background(), 8), 8, &group, "session", true)
	turns := newUpstreamCacheGuardWSTurns(ctx)
	writes := 0
	conn := &upstreamCacheGuardWSFrameConn{turns: turns, account: &Account{ID: 12, Platform: PlatformOpenAI}, inner: &cacheGuardFakeFrameConn{write: func() {
		writes++
		if len(turns.pending) != writes {
			t.Fatal("physical send preceded turn admission")
		}
		holder := turns.pending[writes].Value(cacheUsageAttemptContextKey{}).(*cacheUsageAttempt)
		if !holder.begun {
			t.Fatal("physical send preceded Begin")
		}
	}}}
	for i := 0; i < 2; i++ {
		if err := conn.WriteFrame(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-test"}`)); err != nil {
			t.Fatal(err)
		}
	}
	if writes != 2 {
		t.Fatal("test did not send both independent rounds")
	}
}

func TestCacheGuardEligibilityExcludesNonTextAndKeepsTextWithSearchTool(t *testing.T) {
	for _, tc := range []struct {
		path, body string
		eligible   bool
	}{
		{"/v1/responses", `{"model":"gpt-test","tools":[{"type":"web_search"}]}`, true},
		{"/v1/responses/compact", `{"model":"gpt-test"}`, false},
		{"/v1/images/generations", `{"model":"image-test"}`, false},
		{"/v1/audio/transcriptions", `{"model":"audio-test"}`, false},
		{"/v1/messages/count_tokens", `{"model":"claude-test"}`, false},
		{"/v1/web_search", `{"model":"gpt-test"}`, false},
		{"/v1/responses", `{"model":"gpt-test","tools":[{"type":"image_generation"}]}`, false},
	} {
		if got := UpstreamCacheGuardTextRequest(tc.path, []byte(tc.body)); got != tc.eligible {
			t.Fatalf("eligibility %s %s = %v", tc.path, tc.body, got)
		}
	}
}
