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
	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestGPT62SuffixReasoningEffortPolicy(t *testing.T) {
	suffix := []byte(`{"model":"gpt-6.2-sol-max"}`)
	_, _, err := ApplyOpenAIReasoningEffortPolicy(suffix, "high", nil, ReasoningEffortOverLimitDeny)
	require.True(t, IsReasoningEffortPolicyDenied(err))
	wire, changed, err := ApplyOpenAIReasoningEffortPolicy(suffix, "high", nil, "")
	require.NoError(t, err)
	require.True(t, changed)
	final, _, err := normalizeGPT62RequestEffort(wire, "gpt-6.2-sol", "reasoning.effort")
	require.NoError(t, err)
	require.Equal(t, "high", gjson.GetBytes(final, "reasoning.effort").String())
	_, err = applyOpenAIWSReasoningEffortPolicy(suffix, &OpenAIWSIngressHooks{
		MaxReasoningEffort: "high", MaxReasoningEffortOverLimit: ReasoningEffortOverLimitDeny,
	})
	require.True(t, IsReasoningEffortPolicyDenied(err))

	explicit := []byte(`{"model":"gpt-6.2-sol-max","reasoning":{"effort":"low"}}`)
	final, changed, err = ApplyOpenAIReasoningEffortPolicy(explicit, "high", nil, ReasoningEffortOverLimitDeny)
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, explicit, final)

	alias := []byte(`{"model":"public"}`)
	denyCtx := WithOpenAIReasoningEffortPolicy(context.Background(), "high", nil, ReasoningEffortOverLimitDeny)
	_, _, err = normalizeGPT62RequestEffortWithPolicy(denyCtx, alias, "gpt-6.2-astra-max", "reasoning.effort")
	require.True(t, IsReasoningEffortPolicyDenied(err))
	maps := []ReasoningEffortMapping{{From: "max", To: "high"}, {From: "high", To: "low"}}
	ctx := WithOpenAIReasoningEffortPolicy(context.Background(), "high", maps, "")
	final, _, err = normalizeGPT62RequestEffortWithPolicy(ctx, alias, "gpt-6.2-astra-max", "reasoning_effort")
	require.NoError(t, err)
	require.Equal(t, "high", gjson.GetBytes(final, "reasoning_effort").String())
	final, _, err = normalizeGPT62RequestEffortWithPolicy(ctx, final, "gpt-6.2-astra-max", "reasoning_effort")
	require.NoError(t, err)
	require.Equal(t, "high", gjson.GetBytes(final, "reasoning_effort").String())
	legacy := []byte(`{"model":"gpt-6.1-sol-max"}`)
	final, changed, err = ApplyOpenAIReasoningEffortPolicy(legacy, "high", nil, ReasoningEffortOverLimitDeny)
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, legacy, final)
}

func TestGPT62RequestEffort(t *testing.T) {
	for _, model := range []string{"gpt-6.2-sol", "gpt-6.2-astra"} {
		t.Run(model, func(t *testing.T) {
			for _, body := range []string{
				`{"reasoning":{"effort":"none"}}`,
				`{"reasoning_effort":"MINIMAL"}`,
				`{"output_config":{"effort":"ultra"}}`,
				`{"thinking":{"type":"disabled"}}`,
				`{"model":"openai/` + model + `-none"}`,
			} {
				_, _, err := normalizeGPT62RequestEffort([]byte(body), model, "reasoning.effort")
				require.ErrorContains(t, err, model, body)
			}
			for _, effort := range []string{"low", "medium", "high", "xhigh", "max"} {
				body := []byte(`{"model":"openai/` + model + "-" + effort + `"}`)
				converted, changed, err := normalizeGPT62RequestEffort(body, model, "reasoning.effort")
				require.NoError(t, err)
				require.True(t, changed)
				require.Equal(t, effort, gjson.GetBytes(converted, "reasoning.effort").String())
				require.Equal(t, model, normalizeCodexModel(gjson.GetBytes(body, "model").String()))
			}
			wire := []byte(`{"reasoning":{"effort":"max","mode":"pro"}}`)
			out, changed, err := normalizeGPT62RequestEffort(wire, model, "reasoning.effort")
			require.NoError(t, err)
			require.False(t, changed)
			require.Equal(t, wire, out)
			require.Equal(t, "max", normalizeOpenAIReasoningEffortForModel("max", model))
			_, err = apicompat.ChatCompletionsToResponses(&apicompat.ChatCompletionsRequest{Model: model, ReasoningEffort: "none"})
			require.ErrorContains(t, err, model)
		})
	}
}

func TestGPT62RequestChatFallbackRejectsTools(t *testing.T) {
	for _, model := range []string{"gpt-6.2-sol", "gpt-6.2-astra"} {
		for protocol, body := range map[string]string{
			"chat":      `{"model":"public","messages":[{"role":"user","content":"hi"}],"functions":[{"name":"lookup","parameters":{"type":"object"}}]}`,
			"responses": `{"model":"public","input":"hi","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}]}`,
			"messages":  `{"model":"public","max_tokens":100,"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"lookup","input_schema":{"type":"object"}}]}`,
		} {
			t.Run(model+"/"+protocol, func(t *testing.T) {
				recorder := httptest.NewRecorder()
				ginContext, _ := gin.CreateTestContext(recorder)
				ginContext.Request = httptest.NewRequest(http.MethodPost, "/v1/"+protocol, strings.NewReader(body))
				account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"model_mapping": map[string]any{"public": model}}}
				service := &OpenAIGatewayService{cfg: &config.Config{}}
				var err error
				switch protocol {
				case "chat":
					_, err = service.forwardAsRawChatCompletions(context.Background(), ginContext, account, []byte(body), "")
				case "responses":
					_, err = service.forwardResponsesViaRawChatCompletions(context.Background(), ginContext, account, []byte(body), time.Now())
				case "messages":
					_, err = service.forwardAnthropicViaRawChatCompletions(context.Background(), ginContext, account, []byte(body), "")
				}
				require.ErrorContains(t, err, model+" requires Responses")
				require.Equal(t, http.StatusBadRequest, recorder.Code)
			})
		}
	}
}

func TestGPT62RequestNativeResponses(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		for _, effort := range []string{"none", "max"} {
			model := "gpt-6.2-astra"
			requestModel := model
			if !passthrough {
				requestModel = "public"
			}
			body := `{"model":"` + requestModel + `","input":"hi","stream":true,"reasoning":{"effort":"` + effort + `"},"temperature":0.5,"top_p":0.9,"top_logprobs":2,"include":["message.output_text.logprobs","reasoning.encrypted_content"]}`
			recorder := httptest.NewRecorder()
			ginContext, _ := gin.CreateTestContext(recorder)
			ginContext.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
			upstream := &httpUpstreamRecorder{resp: openAICompatSSECompletedResponse("resp_gpt62", model)}
			service := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
			account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
				Credentials: map[string]any{"api_key": "test", "model_mapping": map[string]any{"public": model}},
				Extra:       map[string]any{"openai_passthrough": passthrough},
			}
			_, err := service.Forward(context.Background(), ginContext, account, []byte(body))
			if effort == "none" {
				require.ErrorContains(t, err, model)
				require.Equal(t, http.StatusBadRequest, recorder.Code)
				require.Empty(t, upstream.requests)
				continue
			}
			require.NoError(t, err)
			require.Equal(t, model, gjson.GetBytes(upstream.lastBody, "model").String())
			require.Equal(t, effort, gjson.GetBytes(upstream.lastBody, "reasoning.effort").String())
			for _, field := range []string{"temperature", "top_p", "top_logprobs"} {
				require.False(t, gjson.GetBytes(upstream.lastBody, field).Exists(), field)
			}
			require.JSONEq(t, `["reasoning.encrypted_content"]`, gjson.GetBytes(upstream.lastBody, "include").Raw)
		}
	}
}

func TestGPT62RequestMessagesAppliesGroupPolicyOnce(t *testing.T) {
	mappings := []ReasoningEffortMapping{{From: "high", To: "low"}, {From: "low", To: "medium"}, {From: "max", To: "high"}, {From: "xhigh", To: "deny"}, {From: "medium", To: "low"}}
	for _, tc := range []struct{ requested, mapped, want string }{
		{"gpt-6.2-sol-high", "gpt-6.2-sol", "low"},
		{"gpt-6.2-sol-max", "gpt-6.2-sol", "high"},
		{"public", "gpt-6.2-sol-max", "high"},
	} {
		for _, raw := range []bool{false, true} {
			body := []byte(`{"model":"` + tc.requested + `","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`)
			ctx := context.Background()
			if legacy, changed := ApplyAnthropicOpenAIReasoningEffortPolicy(body, "", mappings); changed {
				require.Equal(t, "public", tc.requested)
				ctx = WithOriginalOpenAIMessagesEffort(ctx, body)
				body = legacy
			} else {
				require.NotEqual(t, "public", tc.requested)
			}
			ctx = WithOpenAIReasoningEffortPolicyForMessages(ctx, body, "", mappings, "")
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(string(body)))
			upstream := &httpUpstreamRecorder{resp: openAICompatSSECompletedResponse("resp_gpt62", "gpt-6.2-sol")}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
			account := rawGPT56ResponsesAPIKeyAccount(NormalizeOpenAICompatRequestedModel(tc.requested), tc.mapped)
			var err error
			path := "reasoning.effort"
			if raw {
				upstream.resp = &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"id":"chatcmpl_gpt62","object":"chat.completion","model":"gpt-6.2-sol","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))}
				_, err = svc.forwardAnthropicViaRawChatCompletions(ctx, c, account, body, "")
				path = "reasoning_effort"
			} else {
				_, err = svc.ForwardAsAnthropic(ctx, c, account, body, "", "")
			}
			require.NoError(t, err, "%s raw=%v", tc.requested, raw)
			require.Equal(t, "gpt-6.2-sol", gjson.GetBytes(upstream.lastBody, "model").String())
			require.Equal(t, tc.want, gjson.GetBytes(upstream.lastBody, path).String(), "%s raw=%v", tc.requested, raw)
		}
	}
}

func TestGPT62RequestMessagesRejectsPolicyMappedNone(t *testing.T) {
	const body = `{"model":"public","max_tokens":100,"messages":[{"role":"user","content":"hi"}],"output_config":{"effort":"high"}}`
	for _, raw := range []bool{false, true} {
		recorder := httptest.NewRecorder()
		ginContext, _ := gin.CreateTestContext(recorder)
		ginContext.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
		upstream := &httpUpstreamRecorder{}
		service := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
		account := rawGPT56ResponsesOAuthAccount("public", "gpt-6.2-sol")
		ctx := WithOpenAIReasoningEffortPolicy(context.Background(), "", []ReasoningEffortMapping{{From: "high", To: "none"}}, "")
		var err error
		if raw {
			_, err = service.forwardAnthropicViaRawChatCompletions(ctx, ginContext, account, []byte(body), "")
		} else {
			_, err = service.ForwardAsAnthropic(ctx, ginContext, account, []byte(body), "", "")
		}
		require.ErrorContains(t, err, "gpt-6.2-sol does not support reasoning effort")
		require.Equal(t, http.StatusBadRequest, recorder.Code)
		require.Empty(t, upstream.requests)
	}
}

func TestGPT62RequestWebSocket(t *testing.T) {
	for _, effort := range []string{"none", "max"} {
		t.Run(effort, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Gateway.OpenAIWS.Enabled = true
			cfg.Gateway.OpenAIWS.APIKeyEnabled = true
			cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
			cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
			cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
			cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
			cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
			cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3
			capture := &openAIWSCaptureConn{events: [][]byte{[]byte(`{"type":"response.completed","response":{"id":"resp_gpt62","model":"gpt-6.2-astra","usage":{"input_tokens":1,"output_tokens":1}}}`)}}
			dialer := &openAIWSCaptureDialer{conn: capture}
			pool := newOpenAIWSConnPool(cfg)
			pool.setClientDialerForTest(dialer)
			service := &OpenAIGatewayService{cfg: cfg, cache: &stubGatewayCache{}, openaiWSResolver: NewOpenAIWSProtocolResolver(cfg), toolCorrector: NewCodexToolCorrector(), openaiWSPool: pool}
			account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1,
				Credentials: map[string]any{"api_key": "test", "model_mapping": map[string]any{"public": "gpt-6.2-astra"}},
				Extra:       map[string]any{"responses_websockets_v2_enabled": true},
			}
			serverErr := make(chan error, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := coderws.Accept(w, r, nil)
				if err != nil {
					serverErr <- err
					return
				}
				defer conn.CloseNow()
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = r
				payload := []byte(`{"type":"response.create","model":"public","input":"hi","reasoning":{"effort":"` + effort + `"},"temperature":0.5}`)
				serverErr <- service.ProxyResponsesWebSocketFromClient(r.Context(), c, conn, account, "test", payload, nil)
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			client, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
			require.NoError(t, err)
			defer client.CloseNow()
			if effort == "max" {
				_, event, err := client.Read(ctx)
				require.NoError(t, err)
				require.Equal(t, "response.completed", gjson.GetBytes(event, "type").String())
				_ = client.Close(coderws.StatusNormalClosure, "done")
			}
			select {
			case err := <-serverErr:
				if effort == "none" {
					require.ErrorContains(t, err, "gpt-6.2-astra does not support reasoning effort")
					require.Zero(t, dialer.DialCount())
					return
				}
				require.NoError(t, err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			require.Len(t, capture.writes, 1)
			require.Equal(t, "gpt-6.2-astra", capture.writes[0]["model"])
			require.Equal(t, map[string]any{"effort": "max"}, capture.writes[0]["reasoning"])
			require.NotContains(t, capture.writes[0], "temperature")
		})
	}
}
