package service

import (
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

type pluginAdmissionBridgeUpstream struct {
	HTTPUpstream
	models []string
}

func (u *pluginAdmissionBridgeUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	metadata, _ := req.Context().Value(pluginRequestMetadataKey{}).(pluginRequestMetadata)
	u.models = append(u.models, metadata.Model)
	body := fmt.Sprintf("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_admission_bridge\",\"status\":\"completed\",\"model\":%q,\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n", metadata.Model)
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
}

func TestPluginAdmissionBridgeNewlyManagedModelRejectedBeforeLaterTurnOutput(t *testing.T) {
	gin.SetMode(gin.TestMode)
	account := &Account{ID: 41, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "test-token", "chatgpt_account_id": "test-account"},
	}
	manager := &PluginManager{}
	upstream := &pluginAdmissionBridgeUpstream{}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream, pluginManager: manager}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	var frames [][]byte
	forward := func(model string, turn int) error {
		payload := []byte(fmt.Sprintf(`{"type":"response.create","model":%q,"input":"test"}`, model))
		_, err := svc.proxyOpenAIWSHTTPBridgeTurn(context.Background(), c, account, "test-token", payload, len(payload), model, "", "", "", "", turn, func(frame []byte) error {
			frames = append(frames, append([]byte(nil), frame...))
			return nil
		})
		return err
	}
	require.NoError(t, forward("gpt-5.1", 1))
	require.NotEmpty(t, frames)
	require.Equal(t, "response.completed", gjson.GetBytes(frames[len(frames)-1], "type").String())
	frameCount := len(frames)

	manager.route.Store(&pluginRoute{pluginID: 1, scope: []PluginManagedTarget{{AccountID: account.ID, Models: []string{"gpt-5.2"}}}, unavailable: "runtime unavailable"})
	err := forward("gpt-5.2", 2)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.True(t, failoverErr.PluginAdmissionRejected)
	require.False(t, failoverErr.ShouldReportAccountScheduleFailure())
	require.True(t, failoverErr.ShouldRetryNextAccount())
	require.Len(t, frames, frameCount, "a rejected turn must not publish a client error before handler failover")
	require.Equal(t, []string{"gpt-5.1"}, upstream.models, "a newly managed model must not bypass the plugin via HTTP")
	_, recorded := c.Get(OpsUpstreamErrorsKey)
	require.False(t, recorded)

	// A different model on the same account remains outside the plugin scope.
	require.NoError(t, forward("gpt-5.1", 3))
	require.Equal(t, []string{"gpt-5.1", "gpt-5.1"}, upstream.models)
}
