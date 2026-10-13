package service

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/upstreamcacheusage"
	"github.com/gin-gonic/gin"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCacheGuardOpenAIRealSSEParserTerminalAuthority(t *testing.T) {
	s := &OpenAIGatewayService{}
	for _, terminal := range []string{
		`{"type":"response.completed","response":{"usage":{"input_tokens":2000,"output_tokens":10}}}`,
		`{"type":"response.completed","response":{}}`,
	} {
		u := &OpenAIUsage{}
		s.parseSSEUsageBytesWithType([]byte(`{"type":"response.in_progress","response":{"usage":{"input_tokens":2000,"input_tokens_details":{"cached_tokens":1800}}}}`), "response.in_progress", u)
		previous := u.CacheUsageEvidence
		if previous == nil || !previous.Known() {
			t.Fatalf("progress evidence missing: %+v", u)
		}
		s.parseSSEUsageBytesWithType([]byte(terminal), "response.completed", u)
		if u.CacheUsageEvidence == nil || u.CacheUsageEvidence.Status != upstreamcacheusage.Absent || u.CacheUsageEvidence.Known() {
			t.Fatalf("terminal inherited cache: %+v", u.CacheUsageEvidence)
		}
		if previous.CacheReadTokens != 1800 {
			t.Fatalf("prior immutable evidence changed")
		}
	}
}

func TestCacheGuardGeminiRealStreamRequiresUpstreamFinish(t *testing.T) {
	for _, completed := range []bool{false, true} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("POST", "/v1beta/models/gemini-test:streamGenerateContent", nil)
		finish := ""
		if completed {
			finish = `,"finishReason":"STOP"`
		}
		body := `data: {"candidates":[{"content":{"parts":[{"text":"reply"}]}` + finish + `}],"usageMetadata":{"promptTokenCount":2000,"cachedContentTokenCount":1600}}` + "\n\n"
		resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
		service := &GeminiMessagesCompatService{}
		result, err := service.handleNativeStreamingResponse(c, resp, time.Now(), false, nil, "test-request")
		if err != nil || result == nil || result.usage == nil || result.usage.CacheUsageEvidence == nil {
			t.Fatalf("stream failed: result=%+v error=%v", result, err)
		}
		if result.usage.CacheUsageEvidence.StreamIncomplete == completed {
			t.Fatalf("EOF was confused with upstream terminal: %+v", result.usage.CacheUsageEvidence)
		}
		if result.usage.InputTokens != 400 || result.usage.CacheReadInputTokens != 1600 {
			t.Fatalf("terminal tracking changed billable buckets: %+v", result.usage)
		}
	}
}

func TestCacheGuardAnthropicPassthroughCapturesBeforeTransforms(t *testing.T) {
	u := &ClaudeUsage{}
	parseSSEUsagePassthrough(`{"type":"message_start","message":{"usage":{"input_tokens":200,"cache_read_input_tokens":1600,"cache_creation_input_tokens":200}}}`, u)
	parseSSEUsagePassthrough(`{"type":"message_delta","usage":{"output_tokens":20}}`, u)
	e := u.CacheUsageEvidence
	if e == nil || e.TotalInputTokens != 2000 || e.CacheReadTokens != 1600 {
		t.Fatalf("native raw input lost: %+v", e)
	}
	// This is the existing ForceCacheBilling transformation. The raw fact must
	// remain independent from these billable bucket mutations.
	u.CacheReadInputTokens += u.InputTokens
	u.InputTokens = 0
	if e.CacheReadTokens != 1600 || e.TotalInputTokens != 2000 {
		t.Fatalf("billing changed raw evidence")
	}
}

func TestCacheGuardGeminiRealParserPresence(t *testing.T) {
	known := extractGeminiUsage([]byte(`{"usageMetadata":{"promptTokenCount":2000,"cachedContentTokenCount":0}}`))
	unknown := extractGeminiUsage([]byte(`{"usageMetadata":{"promptTokenCount":2000}}`))
	if known.CacheUsageEvidence.Status != upstreamcacheusage.Known || unknown.CacheUsageEvidence.Status != upstreamcacheusage.Absent {
		t.Fatalf("Gemini missing and zero were conflated")
	}
}
