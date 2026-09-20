package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestPluginAdmissionAlphaSearchBuildersUseOutboundModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, responses := range []bool{false, true} {
		for _, model := range []string{"gpt-5.2", "gpt-5.1"} {
			t.Run(fmt.Sprintf("responses=%t/model=%s", responses, model), func(t *testing.T) {
				account := &Account{ID: 42, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
					Credentials: map[string]any{"access_token": "test-token", "chatgpt_account_id": "test-account"},
				}
				manager := &PluginManager{}
				manager.route.Store(&pluginRoute{pluginID: 1, configRevision: 7,
					scope:       []PluginManagedTarget{{AccountID: account.ID, Models: []string{"gpt-5.2"}}},
					unavailable: "runtime unavailable",
				})
				svc := &OpenAIGatewayService{cfg: &config.Config{}, pluginManager: manager}
				alphaBody := []byte(`{"model":"client-alias","commands":{"search_query":[{"q":"test"}]}}`)
				body := []byte(fmt.Sprintf(`{"model":%q,"input":"test"}`, model))
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/alpha/search", bytes.NewReader(alphaBody))
				var req *http.Request
				var err error
				if responses {
					req, err = svc.buildOpenAIAlphaSearchResponsesWebSearchRequest(c.Request.Context(), c, account, alphaBody, body, "test-token")
				} else {
					req, err = svc.buildOpenAIAlphaSearchRequest(c.Request.Context(), c, account, body, "test-token")
				}
				require.NoError(t, err)
				defer req.Body.Close()
				metadata, ok := req.Context().Value(pluginRequestMetadataKey{}).(pluginRequestMetadata)
				require.True(t, ok)
				require.Equal(t, model, metadata.Model)
				require.Equal(t, PluginAccountIdentityRevision(account), metadata.IdentityRevision)
				require.Equal(t, uint64(7), metadata.ConfigRevision)

				response, handled, err := manager.RoundTripOpenAIOAuth(req.Context(), req, "", account)
				require.Nil(t, response)
				if model == "gpt-5.2" {
					var admissionErr *PluginAdmissionError
					require.ErrorAs(t, err, &admissionErr)
					require.True(t, handled)
					require.Equal(t, model, admissionErr.Model)
				} else {
					require.NoError(t, err)
					require.False(t, handled, "an unmanaged model on the same account must retain HTTP routing")
				}
			})
		}
	}
}

func TestPluginAdmissionAlphaSearchMappedModelRouting(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, personalAccessToken := range []bool{false, true} {
		for _, managedModel := range []string{"gpt-5.2", "client-alias"} {
			t.Run(fmt.Sprintf("pat=%t/scope=%s", personalAccessToken, managedModel), func(t *testing.T) {
				credentials := map[string]any{
					"access_token": "test-token", "chatgpt_account_id": "test-account",
					"chatgpt_account_is_fedramp": false,
					"model_mapping":              map[string]any{"client-alias": "gpt-5.2"},
				}
				responseBody := `{"output":"search result"}`
				contentType := "application/json"
				if personalAccessToken {
					credentials["access_token"] = "at-test-token"
					credentials["auth_mode"] = OpenAIAuthModePersonalAccessToken
					responseBody = alphaSearchResponsesSSE("search result")
					contentType = "text/event-stream"
				}
				account := &Account{ID: 42, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: credentials}
				manager := &PluginManager{}
				manager.route.Store(&pluginRoute{pluginID: 1,
					scope:       []PluginManagedTarget{{AccountID: account.ID, Models: []string{managedModel}}},
					unavailable: "runtime unavailable",
				})
				upstream := &httpUpstreamRecorder{resp: &http.Response{
					StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {contentType}},
					Body: io.NopCloser(strings.NewReader(responseBody)),
				}}
				svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream, pluginManager: manager}
				body := []byte(`{"model":"client-alias","commands":{"search_query":[{"q":"test"}]}}`)
				recorder := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(recorder)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/alpha/search", bytes.NewReader(body))
				result, err := svc.ForwardAlphaSearch(context.Background(), c, account, body)
				if managedModel == "gpt-5.2" {
					var failoverErr *UpstreamFailoverError
					require.ErrorAs(t, err, &failoverErr)
					require.True(t, failoverErr.PluginAdmissionRejected)
					require.Nil(t, result)
					require.Empty(t, upstream.requests)
					require.False(t, c.Writer.Written())
					require.False(t, failoverErr.ShouldReportAccountScheduleFailure())
					_, recorded := c.Get(OpsUpstreamErrorsKey)
					require.False(t, recorded)
					return
				}
				require.NoError(t, err)
				require.NotNil(t, result)
				require.Equal(t, http.StatusOK, recorder.Code)
				require.Equal(t, 1, result.WebSearchCalls)
				require.Len(t, upstream.requests, 1)
				require.Equal(t, "gpt-5.2", gjson.GetBytes(upstream.lastBody, "model").String())
				metadata, ok := upstream.lastReq.Context().Value(pluginRequestMetadataKey{}).(pluginRequestMetadata)
				require.True(t, ok)
				require.Equal(t, "gpt-5.2", metadata.Model)
			})
		}
	}
}

func TestPluginAdmissionFailoverOriginIsolation(t *testing.T) {
	configID, keyID := int64(11), int64(12)
	boundAccount := &Account{ID: 42, Platform: PlatformOpenAI, UpstreamConfigID: &configID, UpstreamKeyID: &keyID}
	for _, tc := range []struct {
		name      string
		admission bool
		status    int
		account   *Account
		capacity  bool
	}{
		{name: "admission_503", admission: true, status: http.StatusServiceUnavailable, account: boundAccount},
		{name: "admission_429", admission: true, status: http.StatusTooManyRequests, account: boundAccount},
		{name: "upstream_429", status: http.StatusTooManyRequests, account: boundAccount, capacity: true},
		{name: "upstream_503", status: http.StatusServiceUnavailable, account: boundAccount},
		{name: "unbound_429", status: http.StatusTooManyRequests, account: &Account{ID: 43, Platform: PlatformOpenAI}},
		{name: "nil_account", status: http.StatusTooManyRequests},
	} {
		t.Run(tc.name, func(t *testing.T) {
			failoverErr := &UpstreamFailoverError{PluginAdmissionRejected: tc.admission, StatusCode: tc.status}
			failoverErr.BindOriginAccount(tc.account)
			if tc.admission || tc.account == nil {
				require.Zero(t, failoverErr.OriginAccountID)
				require.Empty(t, failoverErr.OriginPlatform)
				require.False(t, failoverErr.OriginUpstreamBound)
			} else {
				require.Equal(t, tc.account.ID, failoverErr.OriginAccountID)
				require.Equal(t, tc.account.Platform, failoverErr.OriginPlatform)
				require.Equal(t, tc.account.IsUpstreamBound(), failoverErr.OriginUpstreamBound)
			}
			require.Equal(t, tc.capacity, failoverErr.IsUpstreamBoundRateLimit())
		})
	}

	// Classification must reject admission even if a producer supplied origin metadata.
	prebound := &UpstreamFailoverError{PluginAdmissionRejected: true, StatusCode: http.StatusTooManyRequests,
		OriginAccountID: boundAccount.ID, OriginPlatform: boundAccount.Platform, OriginUpstreamBound: true,
	}
	require.False(t, prebound.IsUpstreamBoundRateLimit())
	var nilError *UpstreamFailoverError
	require.NotPanics(t, func() { nilError.BindOriginAccount(boundAccount) })
	require.False(t, nilError.IsUpstreamBoundRateLimit())
}
