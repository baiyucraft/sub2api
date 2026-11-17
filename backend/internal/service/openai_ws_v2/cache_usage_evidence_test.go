package openai_ws_v2

import (
	"context"
	"github.com/Wei-Shaw/sub2api/internal/pkg/upstreamcacheusage"
	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestCacheGuardWSParserKeepsTerminalPresencePerTurn(t *testing.T) {
	state := &relayState{}
	parseUsageAndAccumulate(state, []byte(`{"type":"response.in_progress","response":{"usage":{"input_tokens":2000,"input_tokens_details":{"cached_tokens":1800}}}}`), "response.in_progress", nil)
	parseUsageAndAccumulate(state, []byte(`{"type":"response.completed","response":{"usage":{"input_tokens":2000,"output_tokens":10}}}`), "response.completed", nil)
	first := finalizeRelayTurnUsage(state)
	if first.CacheUsageEvidence == nil || first.CacheUsageEvidence.Status != upstreamcacheusage.Absent {
		t.Fatalf("terminal inherited cache: %+v", first.CacheUsageEvidence)
	}
	parseUsageAndAccumulate(state, []byte(`{"type":"response.completed","response":{"usage":{"input_tokens":2000,"output_tokens":10,"input_tokens_details":{"cached_tokens":0}}}}`), "response.completed", nil)
	second := finalizeRelayTurnUsage(state)
	if second.CacheUsageEvidence == nil || second.CacheUsageEvidence.Status != upstreamcacheusage.Known || second.CacheUsageEvidence.CacheReadTokens != 0 {
		t.Fatalf("second turn zero missing: %+v", second.CacheUsageEvidence)
	}
	if first.CacheUsageEvidence.Status != upstreamcacheusage.Absent {
		t.Fatalf("next turn changed prior evidence")
	}
}

func TestCacheGuardWSRelayPreservesExplicitIncompleteStatusWithoutChangingBilling(t *testing.T) {
	for _, eventType := range []string{"response.completed", "response.done"} {
		for _, status := range []string{"", "completed", "incomplete", "canceled", "failed"} {
			t.Run(eventType+"/"+status, func(t *testing.T) {
				statusField := ""
				if status != "" {
					statusField = `"status":"` + status + `",`
				}
				payload := []byte(`{"type":"` + eventType + `","response":{"id":"resp_cache_status",` + statusField + `"usage":{"input_tokens":100,"output_tokens":7,"input_tokens_details":{"cached_tokens":80}}}}`)
				var turns []RelayTurnResult
				_, relayExit := RunEntry(EntryInput{
					Ctx: context.Background(), ClientConn: newPassthroughTestFrameConn(nil, false),
					UpstreamConn:       newPassthroughTestFrameConn([]passthroughTestFrame{{msgType: coderws.MessageText, payload: payload}}, true),
					FirstClientMessage: []byte(`{"type":"response.create","model":"gpt-4o","input":[]}`),
					Options:            RelayOptions{OnTurnComplete: func(turn RelayTurnResult) { turns = append(turns, turn) }},
				})
				require.Nil(t, relayExit)
				require.Len(t, turns, 1)
				require.Equal(t, status != "" && status != "completed", turns[0].UpstreamIncomplete)
				usage := turns[0].Usage
				require.Equal(t, 100, usage.InputTokens)
				require.Equal(t, 7, usage.OutputTokens)
				require.Equal(t, 80, usage.CacheReadInputTokens)
				require.NotNil(t, usage.CacheUsageEvidence)
				require.True(t, usage.CacheUsageEvidence.Known())
				require.Equal(t, status != "" && status != "completed", usage.CacheUsageEvidence.StreamIncomplete)
			})
		}
	}
}
