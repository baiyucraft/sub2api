//go:build unit

package handler

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type pluginAdmissionExtendedEndpoint struct {
	name, path, body, accountType string
	countTokens                   bool
	run                           func(*OpenAIGatewayHandler, *gin.Context)
	forward                       func(*testing.T, *OpenAIGatewayHandler, *gin.Context, *service.Account, []byte) error
}

func pluginAdmissionExtendedEndpoints() []pluginAdmissionExtendedEndpoint {
	images := func(t *testing.T, h *OpenAIGatewayHandler, c *gin.Context, account *service.Account, body []byte) error {
		t.Helper()
		parsed, err := h.gatewayService.ParseOpenAIImagesRequest(c, body)
		require.NoError(t, err)
		_, err = h.gatewayService.ForwardImages(c.Request.Context(), c, account, body, parsed, "")
		return err
	}
	alpha := func(_ *testing.T, h *OpenAIGatewayHandler, c *gin.Context, account *service.Account, body []byte) error {
		_, err := h.gatewayService.ForwardAlphaSearch(c.Request.Context(), c, account, body)
		return err
	}
	return []pluginAdmissionExtendedEndpoint{
		{"alpha_apikey", "/v1/alpha/search", `{"model":"gpt-5.2","query":"test"}`, service.AccountTypeAPIKey, false, (*OpenAIGatewayHandler).AlphaSearch, alpha},
		{"alpha_oauth", "/v1/alpha/search", `{"model":"gpt-5.2","query":"test"}`, service.AccountTypeOAuth, false, (*OpenAIGatewayHandler).AlphaSearch, alpha},
		{"images_apikey", "/v1/images/generations", `{"model":"gpt-image-2","prompt":"test","size":"1024x1024"}`, service.AccountTypeAPIKey, false, (*OpenAIGatewayHandler).Images, images},
		{"images_oauth", "/v1/images/generations", `{"model":"gpt-image-2","prompt":"test","size":"1024x1024"}`, service.AccountTypeOAuth, false, (*OpenAIGatewayHandler).Images, images},
		{"embeddings", "/v1/embeddings", `{"model":"text-embedding-3-small","input":"test"}`, service.AccountTypeAPIKey, false, (*OpenAIGatewayHandler).Embeddings,
			func(_ *testing.T, h *OpenAIGatewayHandler, c *gin.Context, account *service.Account, body []byte) error {
				_, err := h.gatewayService.ForwardEmbeddings(c.Request.Context(), c, account, body, "")
				return err
			}},
		{"responses_input_tokens", "/v1/responses/input_tokens", `{"model":"gpt-5.2","input":"test"}`, service.AccountTypeOAuth, true, (*OpenAIGatewayHandler).ResponsesInputTokens,
			func(_ *testing.T, h *OpenAIGatewayHandler, c *gin.Context, account *service.Account, body []byte) error {
				return h.gatewayService.ForwardResponsesInputTokens(c.Request.Context(), c, account, body)
			}},
		{"messages_count_tokens", "/v1/messages/count_tokens", `{"model":"gpt-5.2","messages":[{"role":"user","content":"test"}]}`, service.AccountTypeOAuth, true, (*OpenAIGatewayHandler).CountTokens,
			func(_ *testing.T, h *OpenAIGatewayHandler, c *gin.Context, account *service.Account, body []byte) error {
				return h.gatewayService.ForwardCountTokensAsAnthropic(c.Request.Context(), c, account, body, "")
			}},
	}
}

func pluginAdmissionExtendedAccounts(accountType string) []service.Account {
	accounts := pluginAdmissionHandlerAccounts()
	for i := range accounts {
		accounts[i].Type = accountType
		accounts[i].Credentials["api_key"] = "test-api-key"
		accounts[i].Credentials["chatgpt_account_id"] = "test-chatgpt-account"
		accounts[i].Credentials["model_mapping"] = map[string]any{
			"gpt-5.2":                "gpt-5.2",
			"gpt-image-2":            "gpt-image-2",
			"text-embedding-3-small": "text-embedding-3-small",
		}
	}
	return accounts
}

func pluginAdmissionExtendedResponse(attempt pluginAdmissionHandlerAttempt) *http.Response {
	body := `{"results":[]}`
	switch {
	case strings.HasSuffix(attempt.path, "/input_tokens"):
		body = `{"object":"response.input_tokens","input_tokens":9}`
	case strings.Contains(attempt.path, "/images/"):
		body = `{"created":1,"data":[{"b64_json":"aW1hZ2U=","size":"1024x1024"}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`
	case strings.HasSuffix(attempt.path, "/embeddings"):
		body = `{"object":"list","data":[{"object":"embedding","embedding":[0.1],"index":0}],"model":"text-embedding-3-small","usage":{"prompt_tokens":1,"total_tokens":1}}`
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
}

func pluginAdmissionExtendedContext(endpoint pluginAdmissionExtendedEndpoint) (*gin.Context, *httptest.ResponseRecorder) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, endpoint.path, bytes.NewBufferString(endpoint.body))
	c.Request.Header.Set("Content-Type", "application/json")
	setPluginAdmissionHandlerAuth(c)
	apiKey, _ := middleware2.GetAPIKeyFromContext(c)
	apiKey.Group.AllowImageGeneration = true
	return c, recorder
}

func TestPluginAdmissionExtendedHandlerFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, endpoint := range pluginAdmissionExtendedEndpoints() {
		for _, scenario := range []struct {
			name                     string
			rejectThrough            int64
			budget                   int
			writeOutput, failSecond  bool
			wantIDs                  []int64
			wantStatus, wantSwitches int
		}{
			{name: "zero budget", rejectThrough: 1, wantIDs: []int64{1, 2}, wantStatus: 200},
			{name: "multiple rejections preserve budget", rejectThrough: 2, budget: 1, wantIDs: []int64{1, 2, 3}, wantStatus: 200},
			{name: "all rejected", rejectThrough: 3, wantIDs: []int64{1, 2, 3}, wantStatus: 503},
			{name: "no replay after output", rejectThrough: 1, writeOutput: true, wantIDs: []int64{1}, wantStatus: 200},
			{name: "ordinary failure retains budget", rejectThrough: 1, budget: 1, failSecond: true, wantIDs: []int64{1, 2, 3}, wantStatus: 200, wantSwitches: 1},
		} {
			t.Run(endpoint.name+"/"+scenario.name, func(t *testing.T) {
				wantIDs, wantStatus, wantSwitches := scenario.wantIDs, scenario.wantStatus, scenario.wantSwitches
				if endpoint.countTokens && scenario.failSecond {
					// Token-count upstream failures still do not fail over.
					wantIDs, wantStatus, wantSwitches = []int64{1, 2}, 502, 0
				}
				upstream := &pluginAdmissionHandlerUpstream{
					reject:  func(a pluginAdmissionHandlerAttempt) bool { return a.accountID <= scenario.rejectThrough },
					failIDs: map[int64]bool{2: scenario.failSecond}, respond: pluginAdmissionExtendedResponse,
				}
				h := newPluginAdmissionHandler(t, upstream, pluginAdmissionExtendedAccounts(endpoint.accountType), scenario.budget)
				c, recorder := pluginAdmissionExtendedContext(endpoint)
				switches := 0
				c.Request = c.Request.WithContext(service.WithMonitorSwitchReporter(c.Request.Context(), func(count int) { switches = count }))
				if scenario.writeOutput {
					upstream.onReject = func() { _, _ = c.Writer.WriteString(`{"visible":true}`) }
				}
				endpoint.run(h, c)
				require.Equal(t, wantStatus, recorder.Code, recorder.Body.String())
				requirePluginAdmissionAttempts(t, upstream, wantIDs...)
				require.Equal(t, wantSwitches, switches)
				require.False(t, service.IsForceCacheBilling(c.Request.Context()))
				if !scenario.failSecond {
					_, recorded := c.Get(service.OpsUpstreamErrorsKey)
					require.False(t, recorded, "admission is not an upstream failure")
				}
				if scenario.rejectThrough == 3 {
					require.Equal(t, "api_error", gjson.GetBytes(recorder.Body.Bytes(), "error.type").String())
					message := "Service temporarily unavailable"
					if strings.HasPrefix(endpoint.name, "images_") {
						message = "No available compatible accounts"
					}
					require.Equal(t, message, gjson.GetBytes(recorder.Body.Bytes(), "error.message").String())
				}
				if endpoint.countTokens && wantStatus == 200 && !scenario.writeOutput {
					require.Equal(t, int64(9), gjson.GetBytes(recorder.Body.Bytes(), "input_tokens").Int())
				}
			})
		}
	}
}

func TestPluginAdmissionExtendedServiceErrorBoundary(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, endpoint := range pluginAdmissionExtendedEndpoints() {
		for _, kind := range []string{"admission", "request_sent", "ordinary_transport"} {
			t.Run(endpoint.name+"/"+kind, func(t *testing.T) {
				upstream := &pluginAdmissionHandlerUpstream{
					reject: func(pluginAdmissionHandlerAttempt) bool { return kind == "admission" },
					transportError: func(pluginAdmissionHandlerAttempt) error {
						if kind == "ordinary_transport" {
							return errors.New("upstream request failed")
						}
						return &service.PluginTransportError{Code: "plugin_admission_unavailable", Message: "already sent", RequestSent: true}
					},
				}
				accounts := pluginAdmissionExtendedAccounts(endpoint.accountType)
				h := newPluginAdmissionHandler(t, upstream, accounts, 1)
				c, recorder := pluginAdmissionExtendedContext(endpoint)
				err := endpoint.forward(t, h, c, &accounts[0], []byte(endpoint.body))
				require.Error(t, err)
				requirePluginAdmissionAttempts(t, upstream, 1)
				var failoverErr *service.UpstreamFailoverError
				if kind == "admission" {
					require.ErrorAs(t, err, &failoverErr)
					require.True(t, failoverErr.PluginAdmissionRejected)
					require.True(t, failoverErr.ShouldRetryNextAccount())
					require.False(t, failoverErr.ShouldReportAccountScheduleFailure())
					require.False(t, c.Writer.Written())
					require.Empty(t, recorder.Body.String())
					_, recorded := c.Get(service.OpsUpstreamErrorsKey)
					require.False(t, recorded)
				} else if kind == "ordinary_transport" && strings.HasPrefix(endpoint.name, "alpha_") {
					require.ErrorAs(t, err, &failoverErr, "alpha search retains ordinary transport failover")
					require.False(t, failoverErr.PluginAdmissionRejected)
					require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
				} else {
					require.False(t, errors.As(err, &failoverErr), "sent errors must not acquire admission retry semantics")
					if endpoint.countTokens || endpoint.name == "embeddings" {
						require.Equal(t, http.StatusBadGateway, recorder.Code)
						require.Equal(t, "upstream_error", gjson.GetBytes(recorder.Body.Bytes(), "error.type").String())
					}
				}
			})
		}
	}
}

func TestPluginAdmissionTokenCountReleasesSlotsBeforeRetry(t *testing.T) {
	for _, endpoint := range pluginAdmissionExtendedEndpoints() {
		if !endpoint.countTokens {
			continue
		}
		t.Run(endpoint.name, func(t *testing.T) {
			upstream := &pluginAdmissionHandlerUpstream{reject: func(a pluginAdmissionHandlerAttempt) bool { return a.accountID == 1 }, respond: pluginAdmissionExtendedResponse}
			h := newPluginAdmissionHandler(t, upstream, pluginAdmissionExtendedAccounts(endpoint.accountType), 0)
			var acquired []int64
			cache := &concurrencyCacheMock{}
			cache.acquireAccountSlotFn = func(_ context.Context, id int64, _ int, _ string) (bool, error) {
				require.Equal(t, int32(len(acquired)), atomic.LoadInt32(&cache.releaseAccountCalled), "previous slot must be released before reselection")
				acquired = append(acquired, id)
				return true, nil
			}
			h.concurrencyHelper = NewConcurrencyHelper(service.NewConcurrencyService(cache), SSEPingFormatNone, 0)
			c, recorder := pluginAdmissionExtendedContext(endpoint)
			endpoint.run(h, c)
			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			require.Equal(t, []int64{1, 2}, acquired)
			require.Equal(t, int32(len(acquired)), atomic.LoadInt32(&cache.releaseAccountCalled))
		})
	}
}

func TestPluginAdmissionExtendedHandlerRequestSentDoesNotRetry(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, endpoint := range pluginAdmissionExtendedEndpoints() {
		t.Run(endpoint.name, func(t *testing.T) {
			upstream := &pluginAdmissionHandlerUpstream{transportError: func(pluginAdmissionHandlerAttempt) error {
				return &service.PluginTransportError{Code: "plugin_admission_unavailable", Message: "already sent", RequestSent: true}
			}}
			h := newPluginAdmissionHandler(t, upstream, pluginAdmissionExtendedAccounts(endpoint.accountType), 3)
			c, recorder := pluginAdmissionExtendedContext(endpoint)
			endpoint.run(h, c)
			require.Equal(t, http.StatusBadGateway, recorder.Code, recorder.Body.String())
			requirePluginAdmissionAttempts(t, upstream, 1)
		})
	}
}
