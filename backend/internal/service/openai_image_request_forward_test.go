package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func imagePolicyTextUpstream() *httpUpstreamRecorder {
	return &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"id":"resp_text","model":"gpt-6.1-sol","usage":{"input_tokens":1,"output_tokens":1}}`))}}
}

func TestOpenAIOptionalImageHTTPForwardPreservesOrdinaryRequests(t *testing.T) {
	for _, allow := range []bool{false, true} {
		for _, passthrough := range []bool{false, true} {
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("allow=%t/passthrough=%t/stream=%t", allow, passthrough, stream), func(t *testing.T) {
					upstream := imagePolicyTextUpstream()
					if stream {
						upstream.resp.Header.Set("Content-Type", "text/event-stream")
						upstream.resp.Body = io.NopCloser(strings.NewReader("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_new\"}}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_new\",\"model\":\"gpt-6.1-sol\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\ndata: [DONE]\n\n"))
					}
					svc := newOpenAIImageGenerationControlTestService(upstream)
					svc.cfg.Gateway.CodexImageGenerationBridgeEnabled = true
					c, _ := newOpenAIImageGenerationControlTestContext(allow, "codex_cli_rs/0.98.0")
					getAPIKeyFromContext(c).Group.StripOptionalImageTools = allow
					SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)
					a := newOpenAIImageGenerationControlTestAccount()
					a.Extra = map[string]any{"openai_passthrough": passthrough, featureKeyCodexImageGenerationBridge: true}
					a.UpstreamImagePricing = imagePolicyAccount(false).UpstreamImagePricing
					body := []byte(`{"model":"gpt-6.1-sol","previous_response_id":"resp_existing","tools":[{"type":"image_generation"},{"type":"function","name":"shell","parameters":{"type":"object"}}],"tool_choice":{"type":"allowed_tools","mode":"auto","tools":[{"type":"image_generation"}]},"input":[{"type":"function_call","name":"shell","call_id":"call_shell","arguments":"{}"},{"type":"function_call_output","call_id":"call_shell","output":"ok"}]}`)
					body, err := sjson.SetBytes(body, "stream", stream)
					require.NoError(t, err)
					original := string(body)
					result, err := svc.Forward(context.Background(), c, a, body)
					require.NoError(t, err)
					require.NotNil(t, result)
					require.Equal(t, original, string(body))
					require.NotNil(t, upstream.lastReq)
					require.False(t, gjson.GetBytes(upstream.lastBody, `tools.#(type=="image_generation")`).Exists())
					require.Equal(t, "shell", gjson.GetBytes(upstream.lastBody, "tools.0.name").String())
					require.Equal(t, "none", gjson.GetBytes(upstream.lastBody, "tool_choice").String())
					require.Equal(t, "resp_existing", gjson.GetBytes(upstream.lastBody, "previous_response_id").String())
					require.Contains(t, string(upstream.lastBody), "call_shell")
				})
			}
		}
	}
}

func TestOpenAIImagePolicyForwardIndependentAttempts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-6.1-sol","input":"hello","stream":false,"tools":[{"type":"namespace","name":"image_gen","tools":[{"type":"function","name":"imagegen","parameters":{"type":"object"}}]}]}`)
	original := string(body)
	for _, passthrough := range []bool{false, true} {
		c, _ := newOpenAIImageGenerationControlTestContext(true, "codex_cli_rs/0.98.0")
		SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)
		SetOpenAIImageIntentHint(c, true)
		for _, supported := range []bool{false, true} {
			upstream := imagePolicyTextUpstream()
			svc := newOpenAIImageGenerationControlTestService(upstream)
			a := newOpenAIImageGenerationControlTestAccount()
			a.UpstreamImagePricing = imagePolicyAccount(supported).UpstreamImagePricing
			a.Extra = map[string]any{"openai_passthrough": passthrough}
			result, err := svc.Forward(context.Background(), c, a, body)
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, supported, strings.Contains(string(upstream.lastBody), "image_gen"))
			require.Equal(t, original, string(body))
			cached, known := getOpenAIImageIntentHint(c)
			require.True(t, known)
			require.True(t, cached, "account stripping must not rewrite the canonical hint")
		}
	}
}

func TestOpenAIImagePolicyForwardDenialKeepsTextAndStopsNative(t *testing.T) {
	for _, body := range []string{
		`{"model":"gpt-6.1-sol","input":"hello","tools":[{"type":"image_generation"}],"tool_choice":"auto"}`,
		`{"model":"gpt-6.1-sol","input":"hello","tools":[{"type":"image_generation"}],"tool_choice":"none"}`,
		`{"model":"gpt-6.1-sol","tools":[{"type":"namespace","name":"image_gen"}],"previous_response_id":"resp_x","input":[{"type":"function_call","name":"image_gen__imagegen","call_id":"image_call"},{"type":"function_call_output","call_id":"image_call","output":"old image"}]}`,
	} {
		upstream := &httpUpstreamRecorder{}
		svc := newOpenAIImageGenerationControlTestService(upstream)
		c, _ := newOpenAIImageGenerationControlTestContext(true, "codex_cli_rs/0.98.0")
		a := newOpenAIImageGenerationControlTestAccount()
		a.UpstreamImagePricing = imagePolicyAccount(false).UpstreamImagePricing
		_, err := svc.Forward(context.Background(), c, a, []byte(body))
		var failure *UpstreamFailoverError
		require.ErrorAs(t, err, &failure)
		require.Equal(t, GatewayFailureReason("image_permission_denied"), failure.Reason)
		require.False(t, failure.RetryableOnSameAccount)
		require.Nil(t, upstream.lastReq)
	}
	upstream := imagePolicyTextUpstream()
	svc := newOpenAIImageGenerationControlTestService(upstream)
	svc.cfg.Gateway.CodexImageGenerationBridgeEnabled = true
	c, _ := newOpenAIImageGenerationControlTestContext(true, "codex_cli_rs/0.98.0")
	a := newOpenAIImageGenerationControlTestAccount()
	a.UpstreamImagePricing = imagePolicyAccount(false).UpstreamImagePricing
	_, err := svc.Forward(context.Background(), c, a, []byte(`{"model":"gpt-6.1-sol","input":"hello"}`))
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(upstream.lastBody, `tools.#(type=="image_generation")`).Exists())
}

func TestOpenAIImagePolicyForwardLocalDisabledPassiveDoesNotDeny(t *testing.T) {
	upstream := imagePolicyTextUpstream()
	svc := newOpenAIImageGenerationControlTestService(upstream)
	svc.cfg.Gateway.CodexImageGenerationBridgeEnabled = true
	c, recorder := newOpenAIImageGenerationControlTestContext(false, "codex_cli_rs/0.98.0")
	_, err := svc.Forward(context.Background(), c, newOpenAIImageGenerationControlTestAccount(), []byte(`{"model":"gpt-6.1-sol","input":"hello","tools":[{"type":"namespace","name":"image_gen","tools":[{"type":"function","name":"imagegen","parameters":{"type":"object"}}]}]}`))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.False(t, gjson.GetBytes(upstream.lastBody, `tools.#(type=="image_generation")`).Exists())
}
