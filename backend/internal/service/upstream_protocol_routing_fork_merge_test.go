//go:build unit

package service

import (
	"errors"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestForkAdaptiveRoutingRequiresEndpointEvidence(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const relay = "http://relay.example/v1"
	configID, keyID := int64(1), int64(2)
	for _, platform := range []string{PlatformKimi, PlatformZhipu, PlatformDeepseek, PlatformMiniMax} {
		for _, capability := range []openai_compat.CNProtocolCapability{
			openai_compat.CNProtocolCapabilityUnknown, openai_compat.CNProtocolCapabilityNotConfigured,
			openai_compat.CNProtocolCapabilityUnsupported, openai_compat.CNProtocolCapabilitySupported,
		} {
			for _, ingress := range routingMatrixIngresses() {
				t.Run(platform+"/"+string(capability)+"/"+ingress.name, func(t *testing.T) {
					account := &Account{
						ID: 902, Platform: platform, Type: AccountTypeAPIKey, Concurrency: 1,
						UpstreamConfigID: &configID, UpstreamKeyID: &keyID,
						Credentials: map[string]any{
							"api_key": "relay-test-key", "base_url": relay, "api_protocol": APIProtocolAdaptive,
							"api_base_urls": map[string]any{
								APIProtocolChatCompletions: "http://stale-vendor.example/v1",
								APIProtocolResponses:       "http://stale-vendor.example/v1",
								APIProtocolAnthropic:       "http://stale-vendor.example",
							},
						},
					}
					account.Extra = openai_compat.MergeCNProtocolCapability(nil, APIProtocolResponses, capability)
					account.Extra = openai_compat.MergeCNProtocolCapability(account.Extra, APIProtocolAnthropic, capability)
					tc := routingMatrixCase{platform: platform, ingress: ingress, model: "relay-model"}
					body := tc.body()
					upstream := &httpUpstreamRecorder{err: errors.New("stop after capture")}
					svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
					_ = ingress.forward(svc, adaptiveProtocolTestContext(ingress.path, body), account, body)
					require.Len(t, upstream.requests, 1)
					request := upstream.requests[0]
					require.Equal(t, "relay.example", request.URL.Host)
					wantPath := "/v1/chat/completions"
					if capability == openai_compat.CNProtocolCapabilitySupported {
						switch {
						case ingress.name == "messages":
							wantPath = "/v1/messages"
						case ingress.responsesBody && platform != PlatformZhipu:
							wantPath = "/v1/responses"
						}
					}
					require.Equal(t, wantPath, request.URL.Path)
					require.True(t, request.Header.Get("Authorization") == "Bearer relay-test-key" || request.Header.Get("X-Api-Key") == "relay-test-key")
				})
			}
		}
	}
}

func TestForkBoundProviderEndpointsNeverUseVendorDefaults(t *testing.T) {
	configID, keyID := int64(1), int64(2)
	for _, platform := range []string{PlatformKimi, PlatformZhipu, PlatformDeepseek, PlatformMiniMax, PlatformOpenCodeGo, PlatformCommandCode, PlatformCline} {
		for _, protocol := range []string{APIProtocolAdaptive, APIProtocolAnthropic, APIProtocolChatCompletions, APIProtocolResponses} {
			for _, origin := range []string{" https://relay.example/v1 ", ""} {
				account := &Account{
					Platform: platform, Type: AccountTypeAPIKey, UpstreamConfigID: &configID, UpstreamKeyID: &keyID,
					Credentials: map[string]any{"api_protocol": protocol, "base_url": origin,
						"api_base_urls": map[string]any{APIProtocolChatCompletions: "https://vendor.example/v1"}},
				}
				want := strings.TrimSpace(origin)
				require.Equal(t, want, account.GetOpenAIBaseURL(), platform+"/"+protocol)
				require.Equal(t, want, account.GetOpenAIFormatBaseURL(), platform+"/"+protocol)
				for _, endpoint := range []string{APIProtocolResponses, APIProtocolAnthropic, APIProtocolChatCompletions} {
					require.Equal(t, want, account.GetCNProtocolBaseURL(endpoint), platform+"/"+protocol+"/"+endpoint)
				}
				if account.IsAdaptiveAPIProtocol() || account.IsAnthropicProtocol() {
					require.Equal(t, want, account.GetAnthropicProtocolBaseURL(), platform+"/"+protocol)
				}
			}
		}
	}
}

func TestForkBoundAnthropicFinalRequestURL(t *testing.T) {
	gin.SetMode(gin.TestMode)
	configID, keyID := int64(1), int64(2)
	for _, tc := range []struct {
		base, want string
	}{
		{"http://relay.example/v1", "http://relay.example/v1/messages"},
		{"http://relay.example/provider/v1/", "http://relay.example/provider/v1/messages"},
		{"http://relay.example/provider/v1/messages", "http://relay.example/provider/v1/messages"},
		{"http://relay.example/provider/v1?tenant=one%2Ftwo", "http://relay.example/provider/v1/messages?tenant=one%2Ftwo"},
		{"http://relay.example/provider/v1/messages?tenant=one", "http://relay.example/provider/v1/messages?tenant=one"},
		{"http://relay.example/api/anthropic", "http://relay.example/api/anthropic/v1/messages"},
		{"http://relay.example/provider/v4", "http://relay.example/provider/v4/v1/messages"},
		{"http://relay.example/tenant%2Fv1", "http://relay.example/tenant%2Fv1/v1/messages"},
	} {
		for _, platform := range []string{PlatformKimi, PlatformZhipu, PlatformDeepseek, PlatformMiniMax} {
			t.Run(platform+"/"+tc.base, func(t *testing.T) {
				account := nativeAnthropicTestAccount()
				account.Platform = platform
				account.UpstreamConfigID, account.UpstreamKeyID = &configID, &keyID
				account.Credentials["base_url"] = tc.base
				body := []byte(`{"model":"relay-model","max_tokens":32,"stream":false,"messages":[{"role":"user","content":"hi"}]}`)
				upstream := &httpUpstreamRecorder{err: errors.New("stop after capture")}
				svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
				_, _ = svc.ForwardAsAnthropic(t.Context(), adaptiveProtocolTestContext("/v1/messages", body), account, body, "", "")
				require.Len(t, upstream.requests, 1)
				require.Equal(t, tc.want, upstream.requests[0].URL.String())
			})
		}
	}
	account := nativeAnthropicTestAccount()
	account.UpstreamConfigID, account.UpstreamKeyID = &configID, &keyID
	account.Credentials["base_url"] = "http://changed.example/v1"
	require.Equal(t, "http://validated.example/provider/v1/messages?tenant=one",
		nativeAnthropicMessagesURL(account, "http://validated.example/provider/v1?tenant=one"),
		"endpoint assembly must use the validated snapshot instead of rereading credentials")
}

func TestForkByModelRoutingIgnoresCNEvidence(t *testing.T) {
	for _, platform := range []string{PlatformOpenCodeGo, PlatformCommandCode} {
		account := routingTestAccount(platform, AccountTypeAPIKey, map[string]any{
			"api_protocol":   APIProtocolAdaptive,
			"protocol_rules": []any{map[string]any{"pattern": "test-*", "protocol": APIProtocolResponses}},
		}, openai_compat.MergeCNProtocolCapability(nil, APIProtocolResponses, openai_compat.CNProtocolCapabilityUnsupported))
		require.Equal(t, APIProtocolResponses, resolveUpstreamProtocol(account, APIProtocolAnthropic, "test-model", nil))
		require.True(t, account.UsesNativeCNResponses())
	}
	cline := routingTestAccount(PlatformCline, AccountTypeAPIKey, map[string]any{"api_protocol": APIProtocolAdaptive}, nil)
	require.Equal(t, APIProtocolChatCompletions, resolveUpstreamProtocol(cline, APIProtocolResponses, "", nil))
	require.Equal(t, APIProtocolChatCompletions, resolveUpstreamProtocol(cline, APIProtocolAnthropic, "", nil))
}
