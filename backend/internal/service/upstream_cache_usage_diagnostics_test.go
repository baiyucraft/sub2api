package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/upstreamcacheusage"
	"github.com/Wei-Shaw/sub2api/internal/upstreamcacheguard"
	"github.com/stretchr/testify/require"
)

func TestCacheGuardTransportRetriesKeepOneAccountAttempt(t *testing.T) {
	store := cacheGuardTestRunner(t)
	ctx := cacheGuardTestAdmittedContext()
	key := int64(23)
	account := &Account{ID: 12, UpstreamKeyID: &key}
	holder := ctx.Value(cacheUsageAttemptContextKey{}).(*cacheUsageAttempt)
	holder.account = account
	holder.admission.Request.UpstreamKeyID = key
	markUpstreamCacheGuardSend(ctx, account)
	markUpstreamCacheGuardSend(ctx, account)
	markUpstreamCacheGuardSend(ctx, &Account{ID: 13, UpstreamKeyID: &key})
	usage := &UpstreamCacheUsageEvidence{Status: upstreamcacheusage.Known, Protocol: "openai", TotalInputTokens: 1000}
	FinishUpstreamCacheGuardAttempt(ctx, usage, true, time.Now())
	FinishUpstreamCacheGuardAttempt(ctx, usage, true, time.Now())
	markUpstreamCacheGuardSend(ctx, account)
	require.Len(t, store.events, 1)
	require.True(t, store.events[0].ValidSample())
	rows, _ := currentUpstreamCacheGuardRunner().diagnostics.snapshot(account.ID, key)
	require.Len(t, rows, 1)
	require.Equal(t, uint64(2), rows[0].Counts["transport_send_attempt"])
	require.Equal(t, uint64(1), rows[0].Counts["account_attempt_sent"])
	require.Equal(t, uint64(1), rows[0].Counts["completion_valid_candidate"])
	require.Equal(t, uint64(1), rows[0].Counts["delivery_acknowledged"])
}

func TestCacheGuardCompletionDiagnosticsDistinguishExclusions(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		mutate       func(*OpenAIForwardResult)
		cancel       bool
	}{
		{name: "unknown", reason: "usage_unknown", mutate: func(r *OpenAIForwardResult) { r.CacheUsageEvidence.Status = upstreamcacheusage.Absent }},
		{name: "invalid", reason: "usage_invalid", mutate: func(r *OpenAIForwardResult) { r.CacheUsageEvidence.Status = upstreamcacheusage.Invalid }},
		{name: "incomplete", reason: "stream_incomplete", mutate: func(r *OpenAIForwardResult) { r.CacheUsageEvidence.StreamIncomplete = true }},
		{name: "disconnected", reason: "client_disconnected", mutate: func(r *OpenAIForwardResult) { r.ClientDisconnect = true }},
		{name: "image", reason: "non_text_output", mutate: func(r *OpenAIForwardResult) { r.ImageCount = 1 }},
		{name: "canceled_after_terminal", reason: "canceled", cancel: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := cacheGuardTestRunner(t)
			ctx, cancel := context.WithCancel(cacheGuardTestAdmittedContext())
			defer cancel()
			result := &OpenAIForwardResult{OpenAIWSMode: true, UpstreamTerminalEvent: "response.completed", CacheUsageEvidence: &UpstreamCacheUsageEvidence{Status: upstreamcacheusage.Known, Protocol: "openai", TotalInputTokens: 1000, CacheReadTokens: 900}}
			if tc.mutate != nil {
				tc.mutate(result)
			}
			if tc.cancel {
				cancel()
			}
			FinishOpenAIUpstreamCacheGuardAttempt(ctx, result, nil)
			require.Len(t, store.events, 1)
			require.Equal(t, "completion_"+tc.reason, store.events[0].CompletionReason)
			require.False(t, store.events[0].ValidSample())
		})
	}
}

func TestCacheGuardQueueFullRecordsLossWithoutPhantomRetry(t *testing.T) {
	store := newRunnerTestStore()
	runner := testRunner(t, store)
	store.enqueueDown = true
	runner.queue = make(chan upstreamcacheguard.Evidence)
	ctx := cacheGuardTestAdmittedContext()
	holder := ctx.Value(cacheUsageAttemptContextKey{}).(*cacheUsageAttempt)
	holder.admission.Request.UpstreamKeyID = 3
	usage := &UpstreamCacheUsageEvidence{Status: upstreamcacheusage.Known, Protocol: "openai", TotalInputTokens: 1000}
	FinishUpstreamCacheGuardAttempt(ctx, usage, true, time.Now())
	require.True(t, runner.lost.Load())
	require.Zero(t, runner.retrying.Load())
	rows, _ := runner.diagnostics.snapshot(12, 3)
	require.Len(t, rows, 1)
	require.Equal(t, uint64(1), rows[0].Counts["enqueue_failed"])
	require.Equal(t, uint64(1), rows[0].Counts["completion_queue_full"])
	require.Zero(t, rows[0].Counts["delivery_acknowledged"])
}
