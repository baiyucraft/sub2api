package service

import (
	"context"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	"github.com/tidwall/gjson"
)

// The converter may synthesize a message_stop at EOF. Cache observations must
// instead retain the actual Gemini terminal and original input/cache presence.
type antigravityCacheCompletion struct {
	evidence          *UpstreamCacheUsageEvidence
	terminalSucceeded bool
	terminalDelivered bool
}

func (s *antigravityCacheCompletion) observeLine(line string) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "data:") {
		return
	}
	payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
	if payload == "" || payload == "[DONE]" {
		return
	}
	s.evidence = antigravity.ObserveGeminiCacheUsage([]byte(payload), s.evidence)
	root := gjson.Parse(payload)
	if root.Get("response").IsObject() {
		root = root.Get("response")
	}
	if reason := root.Get("candidates.0.finishReason").String(); reason != "" {
		s.terminalSucceeded = cacheGuardGeminiTerminalSucceeded(reason)
	}
	if cacheGuardGeminiResponseFailed([]byte(payload)) {
		s.terminalSucceeded = false
	}
}

func (s *antigravityCacheCompletion) delivered(ctx context.Context, ok bool) {
	if s.terminalSucceeded && ok {
		s.terminalDelivered = true
	}
	freezeUpstreamCacheGuardCompletion(ctx, s.evidence, s.terminalSucceeded && s.terminalDelivered, ok)
}

func (s *antigravityCacheCompletion) finish(result *antigravityStreamResult, err error) {
	if result != nil && result.usage != nil {
		result.usage.CacheUsageEvidence = cacheUsageWithStreamCompletion(result.usage.CacheUsageEvidence,
			s.terminalSucceeded && s.terminalDelivered && err == nil && !result.clientDisconnect)
	}
}
