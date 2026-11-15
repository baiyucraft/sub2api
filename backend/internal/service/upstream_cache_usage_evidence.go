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

func observeGeminiCacheUsageEvidence(raw []byte, previous *UpstreamCacheUsageEvidence) *UpstreamCacheUsageEvidence {
	usage := gjson.GetBytes(raw, "usageMetadata")
	if previous != nil && (!usage.Exists() || usage.IsObject() && !usage.Get("promptTokenCount").Exists() && !usage.Get("cachedContentTokenCount").Exists()) {
		return previous
	}
	return parseGeminiCacheUsageEvidence(usage)
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

func cacheGuardGeminiResponseFailed(raw []byte) bool {
	root := gjson.ParseBytes(raw)
	if root.Get("response").IsObject() {
		root = root.Get("response")
	}
	err := root.Get("error")
	return err.Exists() && err.Type != gjson.Null
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
		// Output-only deltas do not revise the input snapshot. An explicit
		// malformed usage or partial input revision must not inherit stale cache.
		if (node.Exists() && !node.IsObject()) || node.Get("input_tokens").Exists() || node.Get("prompt_tokens").Exists() || node.Get("cache_read_input_tokens").Exists() || node.Get("cache_creation_input_tokens").Exists() || node.Get("cached_tokens").Exists() || node.Get("prompt_tokens_details").Exists() || node.Get("prompt_cache_hit_tokens").Exists() {
			usage.CacheUsageEvidence = parseAnthropicCacheUsageEvidenceResult(node)
		}
	}
}
