package openai_ws_v2

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/upstreamcacheusage"
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
