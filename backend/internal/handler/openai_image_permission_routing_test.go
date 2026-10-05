//go:build unit

package handler

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func imageRoutingAccounts(n int) []service.Account {
	accounts := make([]service.Account, n)
	for i := range accounts {
		accounts[i] = service.Account{ID: int64(i + 1), Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
			Status: service.StatusActive, Schedulable: true, Priority: i,
			Credentials: map[string]any{"api_key": "fixture-token", "base_url": "https://fixture.invalid", "pool_mode": true, "pool_mode_retry_count": 2},
			Extra:       map[string]any{"openai_responses_supported": true},
		}
	}
	return accounts
}

func imageRoutingContext(t *testing.T, body string) (*gin.Context, *httptest.ResponseRecorder) {
	c, rec := newOpenAIResponsesFailoverTestContext(t, nil)
	c.Request.Body = io.NopCloser(bytes.NewBufferString(body))
	c.Request.Header.Set("User-Agent", "codex_cli_rs/0.100.0")
	c.Request.Header.Set("originator", "codex_cli_rs")
	key, _ := c.Get(string(middleware2.ContextKeyAPIKey))
	key.(*service.APIKey).Group.AllowImageGeneration = true
	return c, rec
}

func image403() *http.Response {
	return &http.Response{StatusCode: 403, Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(bytes.NewBufferString(`{"error":{"message":"Image generation is not enabled for this group"}}`))}
}

func TestResponsesImagePermission403DoesNotAmplifyRetries(t *testing.T) {
	gin.SetMode(gin.TestMode)
	accounts := imageRoutingAccounts(6)
	upstream := newAstraProCapturedUpstream(image403(), image403(), image403(), image403(), image403(), image403())
	h := newOpenAIResponsesFailoverTestHandler(t, upstream, accounts)
	h.maxAccountSwitches = 2
	c, rec := imageRoutingContext(t, `{"model":"gpt-6.1-sol","stream":false,"input":"hello","tools":[{"type":"image_generation"}],"tool_choice":"none"}`)
	h.Responses(c)
	_, ids, _ := upstream.snapshot()
	require.Equal(t, []int64{1, 2, 3}, ids)
	require.Equal(t, http.StatusServiceUnavailable, c.Writer.Status())
	require.Equal(t, "image_generation_unavailable", gjson.GetBytes(rec.Body.Bytes(), "error.code").String())
}

func TestResponsesNativeImageExhaustedWithoutUpstreamAttempt(t *testing.T) {
	accounts := imageRoutingAccounts(3)
	old := time.Now().Add(-48 * time.Hour)
	for i := range accounts {
		accounts[i].UpstreamImagePricing = &service.UpstreamKeyImagePricing{Status: "disabled", Supported: false, Stale: true, ObservedAt: &old}
	}
	upstream := newAstraProCapturedUpstream(astra200())
	h := newOpenAIResponsesFailoverTestHandler(t, upstream, accounts)
	c, rec := imageRoutingContext(t, `{"model":"gpt-6.1-sol","stream":true,"input":"hello","tools":[{"type":"image_generation"}],"tool_choice":"auto"}`)
	h.Responses(c)
	_, ids, _ := upstream.snapshot()
	require.Empty(t, ids)
	require.Equal(t, http.StatusServiceUnavailable, c.Writer.Status())
	require.Equal(t, "image_generation_unavailable", gjson.GetBytes(rec.Body.Bytes(), "error.code").String())
}

func TestResponsesImagePermissionExhaustedAfterSSEStarted(t *testing.T) {
	h := newOpenAIResponsesFailoverTestHandler(t, newAstraProCapturedUpstream())
	c, rec := imageRoutingContext(t, `{"model":"gpt-6.1-sol","stream":true,"input":"hello","tools":[{"type":"image_generation"}]}`)
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	_, err := c.Writer.WriteString(": keepalive\n\n")
	require.NoError(t, err)
	c.Writer.Flush()
	failure := &service.UpstreamFailoverError{StatusCode: http.StatusForbidden, Scope: service.GatewayFailureScopeAccountCapability, Reason: service.OpenAIImagePermissionDeniedReason}
	h.handleFailoverExhausted(c, failure, true)
	require.Equal(t, http.StatusOK, c.Writer.Status(), "committed transport status cannot be rewritten")
	require.Contains(t, rec.Body.String(), "event: response.failed")
	require.Contains(t, rec.Body.String(), `"code":"image_generation_unavailable"`)
	_, marked := c.Get(service.OpsStreamErrorKey)
	require.True(t, marked, "a committed HTTP 200 must retain the stream failure marker")
}

func TestResponsesImagePermissionAfterSemanticOutputDoesNotReplay(t *testing.T) {
	sse := "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"partial answer\"}\n\n" +
		"event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"message\":\"Image generation is not enabled for this group\"}}}\n\n"
	upstream := newAstraProCapturedUpstream(&http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(bytes.NewBufferString(sse))}, astra200())
	h := newOpenAIResponsesFailoverTestHandler(t, upstream, imageRoutingAccounts(3))
	c, rec := imageRoutingContext(t, `{"model":"gpt-6.1-sol","stream":true,"input":"hello","tools":[{"type":"image_generation"}]}`)
	h.Responses(c)
	_, ids, _ := upstream.snapshot()
	require.Equal(t, []int64{1}, ids)
	require.Contains(t, rec.Body.String(), "partial answer")
	require.Contains(t, rec.Body.String(), "response.failed")
}

func TestResponsesNativeImageSkipsDeniedAndPrefersKnownAllowed(t *testing.T) {
	accounts := imageRoutingAccounts(3)
	now := time.Now()
	accounts[0].UpstreamImagePricing = &service.UpstreamKeyImagePricing{Status: "available", Supported: false, ObservedAt: &now}
	accounts[2].UpstreamImagePricing = &service.UpstreamKeyImagePricing{Status: "available", Supported: true, ObservedAt: &now}
	upstream := newAstraProCapturedUpstream(astra200())
	h := newOpenAIResponsesFailoverTestHandler(t, upstream, accounts)
	c, _ := imageRoutingContext(t, `{"model":"gpt-6.1-sol","stream":false,"input":"hello","tools":[{"type":"image_generation"}]}`)
	h.Responses(c)
	_, ids, bodies := upstream.snapshot()
	require.Equal(t, []int64{3}, ids)
	require.Equal(t, "image_generation", gjson.GetBytes(bodies[0], "tools.0.type").String())
	require.Equal(t, http.StatusOK, c.Writer.Status())
}

func TestResponsesPassiveImageDeclarationCanUseDeniedTextAccount(t *testing.T) {
	accounts := imageRoutingAccounts(1)
	old := time.Now().Add(-48 * time.Hour)
	accounts[0].UpstreamImagePricing = &service.UpstreamKeyImagePricing{Status: "disabled", Supported: false, Stale: true, ObservedAt: &old}
	upstream := newAstraProCapturedUpstream(astra200())
	h := newOpenAIResponsesFailoverTestHandler(t, upstream, accounts)
	c, _ := imageRoutingContext(t, `{"model":"gpt-6.1-sol","stream":false,"input":"hello","tools":[{"type":"namespace","name":"image_gen","tools":[{"type":"function","name":"imagegen","parameters":{"type":"object"}}]}]}`)
	h.Responses(c)
	_, ids, bodies := upstream.snapshot()
	require.Equal(t, []int64{1}, ids)
	require.NotContains(t, string(bodies[0]), "image_gen")
	require.Equal(t, http.StatusOK, c.Writer.Status())
}

func TestResponsesPoolRetryStaysOnOriginalAccount(t *testing.T) {
	accounts := imageRoutingAccounts(2)
	upstream := newAstraProCapturedUpstream(astra403(), astra200())
	h := newOpenAIResponsesFailoverTestHandler(t, upstream, accounts)
	c, _ := imageRoutingContext(t, `{"model":"gpt-6.1-sol","stream":false,"input":"hello"}`)
	h.Responses(c)
	_, ids, _ := upstream.snapshot()
	require.Equal(t, []int64{1, 1}, ids)
	require.Equal(t, http.StatusOK, c.Writer.Status())
}
