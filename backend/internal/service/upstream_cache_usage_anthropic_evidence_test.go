package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/upstreamcacheusage"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAnthropicRawEvidenceDeltaAuthority(t *testing.T) {
	for _, tc := range []struct {
		name, delta string
		status      upstreamcacheusage.Status
		read        int64
	}{
		{"output_only", `{"output_tokens":7}`, upstreamcacheusage.Known, 80},
		{"empty", `{}`, upstreamcacheusage.Known, 80},
		{"full_zero", `{"input_tokens":100,"cache_read_input_tokens":0}`, upstreamcacheusage.Known, 0},
		{"input_revision", `{"input_tokens":200}`, upstreamcacheusage.Absent, 0},
		{"cache_revision", `{"cache_read_input_tokens":80}`, upstreamcacheusage.Absent, 80},
		{"create_revision", `{"cache_creation_input_tokens":20}`, upstreamcacheusage.Absent, 0},
		{"null", `null`, upstreamcacheusage.Invalid, 0},
		{"invalid_read", `{"input_tokens":20,"cache_read_input_tokens":-1}`, upstreamcacheusage.Invalid, 0},
		{"string_read", `{"input_tokens":20,"cache_read_input_tokens":"0"}`, upstreamcacheusage.Invalid, 0},
		{"fractional_read", `{"input_tokens":20,"cache_read_input_tokens":0.5}`, upstreamcacheusage.Invalid, 0},
		{"overflow_input", `{"input_tokens":9223372036854775807,"cache_read_input_tokens":1}`, upstreamcacheusage.Invalid, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := &ClaudeUsage{}
			observeAnthropicCacheUsageEvent(`{"type":"message_start","message":{"usage":{"input_tokens":20,"cache_read_input_tokens":80}}}`, u)
			previous := u.CacheUsageEvidence
			observeAnthropicCacheUsageEvent(`{"type":"message_delta","usage":`+tc.delta+`}`, u)
			require.Equal(t, tc.status, u.CacheUsageEvidence.Status)
			if tc.status != upstreamcacheusage.Invalid {
				require.Equal(t, tc.read, u.CacheUsageEvidence.CacheReadTokens)
			}
			require.Equal(t, int64(80), previous.CacheReadTokens, "previous evidence is immutable")
		})
	}
}

func TestAnthropicDefaultSSEObservesOriginalCacheUsage(t *testing.T) {
	for _, tc := range []struct {
		name, cache string
		status      upstreamcacheusage.Status
	}{
		{"hit", `,"cache_read_input_tokens":80`, upstreamcacheusage.Known},
		{"zero", `,"cache_read_input_tokens":0`, upstreamcacheusage.Known},
		{"missing", "", upstreamcacheusage.Absent},
		{"invalid", `,"cache_read_input_tokens":null`, upstreamcacheusage.Invalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			body := `event: message_start` + "\n" + `data: {"type":"message_start","message":{"model":"test","usage":{"input_tokens":20` + tc.cache + `}}}` + "\n\n" +
				`data: {"type":"message_delta","usage":{"output_tokens":7}}` + "\n\n" + `data: {"type":"message_stop"}` + "\n\n"
			resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
			svc := &GatewayService{cfg: &config.Config{}, rateLimitService: &RateLimitService{}}
			result, err := svc.handleStreamingResponse(context.Background(), resp, c, &Account{Type: AccountTypeAPIKey}, time.Now(), "test", "test", false)
			require.NoError(t, err)
			require.NotNil(t, result.usage.CacheUsageEvidence)
			require.Equal(t, tc.status, result.usage.CacheUsageEvidence.Status)
			result.usage.InputTokens = 999
			require.NotEqual(t, int64(999), result.usage.CacheUsageEvidence.TotalInputTokens)
		})
	}
}

func TestAntigravityActualClaudeStreamRetainsGeminiEvidence(t *testing.T) {
	for _, compat := range []bool{false, true} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		body := `data: {"response":{"candidates":[{"content":{"parts":[{"text":"reply"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":100,"cachedContentTokenCount":80}}}` + "\n\n"
		resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
		svc := &AntigravityGatewayService{settingService: &SettingService{cfg: &config.Config{}}}
		var result *antigravityStreamResult
		var err error
		if compat {
			result, err = svc.handleAntigravityCompatStream(c, resp, time.Now(), "test", newAntigravityResponsesStreamAdapter("test"), "test")
		} else {
			result, err = svc.handleClaudeStreamingResponse(c, resp, time.Now(), "test")
		}
		require.NoError(t, err)
		require.NotNil(t, result.usage.CacheUsageEvidence)
		require.True(t, result.usage.CacheUsageEvidence.Known())
		require.Equal(t, int64(100), result.usage.CacheUsageEvidence.TotalInputTokens)
		require.Equal(t, int64(80), result.usage.CacheUsageEvidence.CacheReadTokens)
		require.Equal(t, 20, result.usage.InputTokens)
	}
}
