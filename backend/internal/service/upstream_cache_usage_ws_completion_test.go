package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/upstreamcacheusage"
	"github.com/stretchr/testify/require"
)

func TestCacheGuardWSDeliveredCompletionSurvivesLaterCancelWithoutSamplingImageOutput(t *testing.T) {
	for _, imageTokens := range []int{0, 12} {
		t.Run(map[bool]string{true: "text", false: "image"}[imageTokens == 0], func(t *testing.T) {
			store := cacheGuardTestRunner(t)
			ctx, cancel := context.WithCancel(cacheGuardTestAdmittedContext())
			defer cancel()
			result := &OpenAIForwardResult{
				OpenAIWSMode: true, UpstreamTerminalEvent: "response.completed",
				Usage: OpenAIUsage{ImageOutputTokens: imageTokens,
					CacheUsageEvidence: &UpstreamCacheUsageEvidence{Status: upstreamcacheusage.Known, Protocol: "openai", TotalInputTokens: 100, CacheReadTokens: 90}},
			}
			turns := newUpstreamCacheGuardWSTurns(ctx)
			turns.pending[1] = ctx
			turns.freezeCompletion(1, result)
			cancel()
			turns.finish(1, result, nil)
			turns.finishRemaining()
			require.Len(t, store.events, 1)
			require.Equal(t, imageTokens == 0, store.events[0].ValidSample())
		})
	}
}

func TestCacheGuardWSBufferedResponseRetainsExplicitIncompleteStatus(t *testing.T) {
	store := cacheGuardTestRunner(t)
	ctx, cancel := context.WithCancel(cacheGuardTestAdmittedContext())
	defer cancel()
	usage := OpenAIUsage{}
	parseOpenAIWSResponseUsageFromCompletedEvent([]byte(`{"type":"response.completed","response":{"status":"incomplete","usage":{"input_tokens":100,"output_tokens":7,"input_tokens_details":{"cached_tokens":80}}}}`), &usage)
	populateOpenAIUsageFromResponseJSON([]byte(`{"status":"incomplete","usage":{"input_tokens":100,"output_tokens":7,"input_tokens_details":{"cached_tokens":80}}}`), &usage)
	require.True(t, usage.CacheUsageEvidence.Known())
	require.True(t, usage.CacheUsageEvidence.StreamIncomplete)
	require.Equal(t, 100, usage.InputTokens)
	require.Equal(t, 7, usage.OutputTokens)
	result := &OpenAIForwardResult{Usage: usage, OpenAIWSMode: true, UpstreamTerminalEvent: "response.completed"}
	freezeOpenAIWSCacheGuardCompletion(ctx, result)
	cancel()
	FinishOpenAIUpstreamCacheGuardAttempt(ctx, result, nil)
	require.Len(t, store.events, 1)
	require.False(t, store.events[0].ValidSample())
}
