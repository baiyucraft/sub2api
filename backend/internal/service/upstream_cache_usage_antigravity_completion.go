package service

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

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
	if ctx != nil && ctx.Err() != nil {
		ok = false
	}
	if s.terminalSucceeded && ok {
		s.terminalDelivered = true
	}
	freezeUpstreamCacheGuardCompletion(ctx, s.evidence, s.terminalSucceeded && s.terminalDelivered, ok)
}

func (s *antigravityCacheCompletion) finish(result *antigravityStreamResult, err error) {
	if result != nil {
		// A read cancellation after the successful terminal reached the client
		// cannot rewrite that completed outcome. Keep disconnect separately for
		// the existing billing and response consumers.
		result.succeeded = s.terminalSucceeded && s.terminalDelivered &&
			(err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded))
	}
	if result != nil && result.usage != nil {
		result.usage.CacheUsageEvidence = cacheUsageWithStreamCompletion(result.usage.CacheUsageEvidence,
			s.terminalSucceeded && s.terminalDelivered && err == nil && !result.clientDisconnect)
	}
}

// Preserve the existing response while retaining its physical write outcome.
func antigravityWriteJSONBytes(c *gin.Context, body []byte) bool {
	deliverable := c.Request.Context().Err() == nil
	c.Header("Content-Type", "application/json")
	c.Status(http.StatusOK)
	n, err := c.Writer.Write(body)
	return deliverable && err == nil && n == len(body)
}

func antigravityAnthropicResponseFailed(payload []byte) bool {
	root := gjson.ParseBytes(payload)
	return !root.IsObject() || root.Get("type").String() == "error" || root.Get("error").Exists()
}
