package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func imagePolicyTextUpstream() *httpUpstreamRecorder {
	return &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"id":"resp_text","model":"gpt-6.1-sol","usage":{"input_tokens":1,"output_tokens":1}}`))}}
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
		`{"model":"gpt-6.1-sol","tools":[{"type":"namespace","name":"image_gen"}],"previous_response_id":"resp_x","input":"hello"}`,
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
