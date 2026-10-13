package service

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/upstreamcacheusage"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// Exercise the real forwarding gates, not just the shared usage parser: a
// successful terminal may preserve billing fallback without preserving a sample.
func TestCacheGuardWSTerminalPresenceThroughNativeAndBridge(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, eventType := range []string{"response.completed", "response.done"} {
		for _, zeroUsage := range []bool{false, true} {
			name := eventType + "/missing_usage"
			terminalUsage := ""
			wantStatus := upstreamcacheusage.Absent
			if zeroUsage {
				name = eventType + "/zero_usage"
				terminalUsage = `,"usage":{"input_tokens":0,"output_tokens":0,"input_tokens_details":{"cached_tokens":0}}`
				wantStatus = upstreamcacheusage.Known
			}
			progressive := []byte(`{"type":"response.in_progress","response":{"id":"resp_cache_terminal","model":"gpt-5.1","usage":{"input_tokens":100,"output_tokens":7,"input_tokens_details":{"cached_tokens":80}}}}`)
			terminal := []byte(`{"type":"` + eventType + `","response":{"id":"resp_cache_terminal","model":"gpt-5.1","status":"completed","output":[]` + terminalUsage + `}}`)
			for _, bridge := range []bool{false, true} {
				transport := "native"
				if bridge {
					transport = "bridge"
				}
				t.Run(transport+"/"+name, func(t *testing.T) {
					store := cacheGuardTestRunner(t)
					ctx := cacheGuardTestAdmittedContext()
					recorder := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(recorder)
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
					c.Request.Header.Set("User-Agent", "unit-test-agent/1.0")
					cfg := &config.Config{}
					cfg.Gateway.MaxLineSize = defaultMaxLineSize
					cfg.Gateway.OpenAIWS.Enabled = true
					cfg.Gateway.OpenAIWS.APIKeyEnabled = true
					cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
					cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
					cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
					account := &Account{
						ID: 32, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
						Status: StatusActive, Schedulable: true, Concurrency: 1,
						Credentials: map[string]any{"api_key": "sk-test"},
						Extra:       map[string]any{"responses_websockets_v2_enabled": true},
					}
					payload := []byte(`{"model":"gpt-5.1","stream":true,"input":"hi"}`)
					svc := &OpenAIGatewayService{
						cfg: cfg, cache: &stubGatewayCache{},
						openaiWSResolver: NewOpenAIWSProtocolResolver(cfg), toolCorrector: NewCodexToolCorrector(),
					}
					var result *OpenAIForwardResult
					var err error
					if bridge {
						sse := "data: " + string(progressive) + "\n\n" + "data: " + string(terminal) + "\n\n"
						svc.httpUpstream = &httpUpstreamRecorder{resp: &http.Response{
							StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}},
							Body: io.NopCloser(strings.NewReader(sse)),
						}}
						result, err = svc.proxyOpenAIWSHTTPBridgeTurn(ctx, c, account, "sk-test", payload, len(payload), "gpt-5.1", "", "", "", "", 1, func([]byte) error { return nil })
					} else {
						pool := newOpenAIWSConnPool(cfg)
						pool.setClientDialerForTest(&openAIWSCaptureDialer{conn: &openAIWSCaptureConn{events: [][]byte{progressive, terminal}}})
						t.Cleanup(pool.Close)
						svc.openaiWSPool = pool
						svc.httpUpstream = &httpUpstreamRecorder{}
						result, err = svc.Forward(ctx, c, account, payload)
					}
					require.NoError(t, err)
					require.NotNil(t, result)
					require.Equal(t, eventType, result.UpstreamTerminalEvent)
					require.Equal(t, 100, result.Usage.InputTokens, "existing billing fallback must survive")
					require.Equal(t, 7, result.Usage.OutputTokens)
					require.Equal(t, 80, result.Usage.CacheReadInputTokens)
					require.NotNil(t, result.CacheUsageEvidence)
					require.Equal(t, wantStatus, result.CacheUsageEvidence.Status)
					require.Zero(t, result.CacheUsageEvidence.TotalInputTokens)
					require.False(t, result.CacheUsageEvidence.Known())
					FinishOpenAIUpstreamCacheGuardAttempt(ctx, result, err)
					require.Len(t, store.events, 1)
					require.False(t, store.events[0].ValidSample(), "progressive high cache is not final evidence")
				})
			}
		}
	}
}

func TestCacheGuardWSBridgeDisconnectedDrainDoesNotSample(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := cacheGuardTestRunner(t)
	ctx := cacheGuardTestAdmittedContext()
	sse := "data: {\"type\":\"keepalive\"}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_drain\",\"usage\":{\"input_tokens\":100,\"output_tokens\":7,\"input_tokens_details\":{\"cached_tokens\":80}}}}\n\n"
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(sse)),
	}}}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	payload := []byte(`{"type":"response.create","model":"gpt-5.1","input":"hi"}`)
	result, err := svc.proxyOpenAIWSHTTPBridgeTurn(ctx, c,
		&Account{ID: 14, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 1},
		"sk-test", payload, len(payload), "gpt-5.1", "", "", "", "", 1, func([]byte) error { return io.EOF })
	require.NoError(t, err, "upstream billing is drained after a client disconnect")
	require.NotNil(t, result)
	require.True(t, result.ClientDisconnect)
	require.Equal(t, 100, result.Usage.InputTokens)
	require.Equal(t, 7, result.Usage.OutputTokens)
	require.Equal(t, 80, result.Usage.CacheReadInputTokens)
	require.True(t, result.CacheUsageEvidence.Known())
	FinishOpenAIUpstreamCacheGuardAttempt(ctx, result, err)
	require.Len(t, store.events, 1)
	require.False(t, store.events[0].Success)
	require.False(t, store.events[0].ValidSample())
}

func TestCacheGuardWSNormalizedFailureTerminalsDoNotSample(t *testing.T) {
	for _, terminal := range []string{"error", "response.failed", "response.incomplete", "response.canceled", "response.cancelled"} {
		t.Run(terminal, func(t *testing.T) {
			store := cacheGuardTestRunner(t)
			ctx := cacheGuardTestAdmittedContext()
			usage := OpenAIUsage{}
			parseOpenAIWSResponseUsageFromCompletedEvent([]byte(`{"type":"`+terminal+`","usage":{"input_tokens":100,"output_tokens":7,"input_tokens_details":{"cached_tokens":80}}}`), &usage)
			result := &OpenAIForwardResult{Usage: usage, OpenAIWSMode: true, UpstreamTerminalEvent: normalizeOpenAIWSTerminalEvent(terminal)}
			require.True(t, usage.CacheUsageEvidence.Known())
			require.Equal(t, 100, usage.InputTokens)
			require.Equal(t, 7, usage.OutputTokens)
			require.Equal(t, 80, usage.CacheReadInputTokens)
			require.False(t, result.SucceededForScheduling())
			FinishOpenAIUpstreamCacheGuardAttempt(ctx, result, nil)
			require.Len(t, store.events, 1)
			require.False(t, store.events[0].Success)
			require.False(t, store.events[0].ValidSample())
		})
	}
}
