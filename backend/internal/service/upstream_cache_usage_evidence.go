package service

import (
	"encoding/json"
	"github.com/Wei-Shaw/sub2api/internal/pkg/upstreamcacheusage"
	"github.com/tidwall/gjson"
)

// UpstreamCacheUsageEvidence is an immutable copy of raw upstream usage. It is
// independent from billable token buckets and contains no request content.
type UpstreamCacheUsageEvidence = upstreamcacheusage.Evidence

func parseOpenAICacheUsageEvidence(usage gjson.Result) *UpstreamCacheUsageEvidence {
	return upstreamcacheusage.ParseOpenAI(usage)
}

func parseGeminiCacheUsageEvidence(usage gjson.Result) *UpstreamCacheUsageEvidence {
	return upstreamcacheusage.ParseGemini(usage)
}

func parseAnthropicCacheUsageEvidenceResult(usage gjson.Result) *UpstreamCacheUsageEvidence {
	return upstreamcacheusage.ParseAnthropic(usage)
}

func parseAnthropicCacheUsageEvidence(usage map[string]any) *UpstreamCacheUsageEvidence {
	raw, err := json.Marshal(usage)
	if err != nil {
		return &UpstreamCacheUsageEvidence{Status: upstreamcacheusage.Invalid, Protocol: "anthropic"}
	}
	return parseAnthropicCacheUsageEvidenceResult(gjson.ParseBytes(raw))
}

func cacheGuardGeminiTerminalSucceeded(reason string) bool {
	return reason == "STOP" || reason == "MAX_TOKENS"
}

// Anthropic reports input/cache usage in message_start and output totals in
// message_delta. Output-only deltas do not invalidate the native input fact.
func observeAnthropicCacheUsageEvent(payload string, usage *ClaudeUsage) {
	if usage == nil {
		return
	}
	root := gjson.Parse(payload)
	switch root.Get("type").String() {
	case "message_start":
		usage.CacheUsageEvidence = parseAnthropicCacheUsageEvidenceResult(root.Get("message.usage"))
	case "message_delta":
		node := root.Get("usage")
		if node.Get("input_tokens").Exists() || node.Get("prompt_tokens").Exists() || node.Get("cache_read_input_tokens").Exists() {
			usage.CacheUsageEvidence = parseAnthropicCacheUsageEvidenceResult(node)
		}
	}
}
