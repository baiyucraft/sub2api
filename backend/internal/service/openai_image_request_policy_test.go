package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func imagePolicyAccount(supported bool) *Account {
	now := time.Now()
	return &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, UpstreamImagePricing: &UpstreamKeyImagePricing{Supported: supported, Status: UpstreamKeyImagePricingStatusAvailable, ObservedAt: &now}}
}

func TestOpenAIImageRequestDescriptor(t *testing.T) {
	for _, tt := range []struct {
		name, body, source string
		required, strip    bool
	}{
		{"ordinary", `{"input":"draw a picture"}`, "ordinary", false, false},
		{"native auto", `{"tools":[{"type":"image_generation"}],"tool_choice":"auto"}`, "native", true, false},
		{"native none", `{"tools":[{"type":"image_generation"}],"tool_choice":"none"}`, "native", true, false},
		{"lite native", `{"input":[{"type":"additional_tools","tools":[{"type":"image_generation"}]}]}`, "native", true, false},
		{"passive", `{"tools":[{"type":"namespace","name":"image_gen"}]}`, "passive", false, true},
		{"flattened passive", `{"tools":[{"type":"function","name":"image_gen__imagegen"}]}`, "passive", false, true},
		{"selected function", `{"tool_choice":{"type":"function","name":"image_gen.imagegen"}}`, "explicit", true, false},
		{"continuation", `{"previous_response_id":"resp_x","tools":[{"type":"namespace","name":"image_gen"}]}`, "history", true, false},
		{"history call", `{"input":[{"type":"function_call","namespace":"image_gen","name":"imagegen"}]}`, "history", true, false},
		{"unresolved output", `{"tools":[{"type":"namespace","name":"image_gen"}],"input":[{"type":"function_call_output","call_id":"call_x","output":"done"}]}`, "history", true, false},
		{"duplicate", `{"tools":[{"type":"namespace","name":"image_gen"}],"tools":[]}`, "passive", false, false},
		{"nested duplicate", `{"tools":[{"type":"namespace","name":"image_gen","type":"image_generation"}]}`, "passive", false, false},
		{"chat history", `{"tools":[{"type":"namespace","name":"image_gen"}],"messages":[{"role":"assistant","tool_calls":[{"type":"function","function":{"name":"image_gen.imagegen"}}]}]}`, "history", true, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := DescribeOpenAIImageRequest("/v1/responses", "gpt-6.1-sol", []byte(tt.body), true)
			require.Equal(t, tt.source, r.Source)
			require.Equal(t, tt.required, r.RequiresCapability())
			require.Equal(t, tt.strip, r.CanStripPassive())
		})
	}
}

func TestOpenAIImagePermissionSnapshotRanks(t *testing.T) {
	a := imagePolicyAccount(true)
	require.Equal(t, OpenAIImagePermissionAllowed, OpenAIImagePermissionRank(a, 0))
	a.UpstreamImagePricing.Supported = false
	require.Equal(t, OpenAIImagePermissionDenied, OpenAIImagePermissionRank(a, 0))
	a.UpstreamImagePricing.Stale = true
	require.Equal(t, OpenAIImagePermissionDenied, OpenAIImagePermissionRank(a, 0))
	a.UpstreamImagePricing.Stale = false
	old := time.Now().Add(-2 * time.Hour)
	a.UpstreamImagePricing.ObservedAt = &old
	require.Equal(t, OpenAIImagePermissionDenied, OpenAIImagePermissionRank(a, 3600))
	require.Equal(t, OpenAIImagePermissionDenied, OpenAIImagePermissionRank(a, 86400))
	a.UpstreamImagePricing.Supported = true
	require.Equal(t, OpenAIImagePermissionUnknown, OpenAIImagePermissionRank(a, 3600))
	require.Equal(t, OpenAIImagePermissionAllowed, OpenAIImagePermissionRank(a, 86400))
	a.UpstreamImagePricing.Status = UpstreamKeyImagePricingStatusUnavailable
	require.Equal(t, OpenAIImagePermissionUnknown, OpenAIImagePermissionRank(a, 86400))
	a.UpstreamImagePricing = nil
	require.Equal(t, OpenAIImagePermissionUnknown, OpenAIImagePermissionRank(a, 86400))
}

func TestOpenAIImagePolicyDerivesIndependentBodies(t *testing.T) {
	body := []byte(`{"model":"gpt-6.1-sol","seed":9007199254740993,"tools":[{"type":"namespace","name":"image_gen"},{"type":"function","name":"shell"}],"input":[{"type":"additional_tools","tools":[{"type":"namespace","name":"image_gen"}]},{"role":"user","content":"hello"}]}`)
	original := string(body)
	r := DescribeOpenAIImageRequest("/v1/responses", "gpt-6.1-sol", body, true)
	stripped, changed, err := DeriveOpenAIImageRequestBody(body, r, imagePolicyAccount(false))
	require.NoError(t, err)
	require.True(t, changed)
	require.False(t, gjson.GetBytes(stripped, `tools.#(name=="image_gen")`).Exists())
	require.Equal(t, "shell", gjson.GetBytes(stripped, "tools.0.name").String())
	require.Equal(t, "user", gjson.GetBytes(stripped, "input.0.role").String())
	require.Equal(t, "9007199254740993", gjson.GetBytes(stripped, "seed").Raw)
	require.Equal(t, original, string(body))
	allowedBody, changed, err := DeriveOpenAIImageRequestBody(body, r, imagePolicyAccount(true))
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, original, string(allowedBody))
	manual := imagePolicyAccount(false)
	manual.Extra = map[string]any{featureKeyCodexImageGenerationExplicitToolPolicy: "allow"}
	allowedBody, changed, err = DeriveOpenAIImageRequestBody(body, r, manual)
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, original, string(allowedBody))
	for _, bridge := range []bool{true, false} {
		manual.Extra = map[string]any{featureKeyCodexImageGenerationBridge: bridge}
		allowedBody, changed, err = DeriveOpenAIImageRequestBody(body, r, manual)
		require.NoError(t, err)
		require.False(t, changed, "a legacy bridge-only override is manual policy")
		require.Equal(t, original, string(allowedBody))
	}
}

func TestOpenAIImagePassiveChatOnlyCompatibility(t *testing.T) {
	a := imagePolicyAccount(true)
	a.Type = AccountTypeAPIKey
	a.Extra = map[string]any{openai_compat.ExtraKeyResponsesSupported: false}
	require.False(t, a.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityResponses))
	body := []byte(`{"tools":[{"type":"namespace","name":"image_gen"}]}`)
	ctx := WithOpenAIImageRequest(context.Background(), "/v1/responses", "gpt-6.1-sol", body, true)
	svc := newOpenAIImageGenerationControlTestService(&httpUpstreamRecorder{})
	require.True(t, svc.AccountRequiresOpenAIImageCapability(ctx, nil, a))
	require.False(t, svc.AccountRequiresOpenAIResponsesForImage(ctx, nil, a))
	r, _ := OpenAIImageRequestFromContext(ctx)
	require.NoError(t, CheckOpenAIImageRequestPermission(ctx, r, a))
}

func TestOpenAIImagePolicyRetainsExplicitAndHistory(t *testing.T) {
	for _, body := range []string{
		`{"tools":[{"type":"image_generation"}],"tool_choice":"none"}`,
		`{"tools":[{"type":"namespace","name":"image_gen"}],"previous_response_id":"resp_x"}`,
		`{"tools":[{"type":"namespace","name":"image_gen"}],"input":[{"type":"function_call","call_id":"call_x","name":"imagegen","namespace":"image_gen"}]}`,
	} {
		r := DescribeOpenAIImageRequest("/v1/responses", "gpt-6.1-sol", []byte(body), true)
		next, changed, err := DeriveOpenAIImageRequestBody([]byte(body), r, imagePolicyAccount(false))
		require.NoError(t, err)
		require.False(t, changed)
		require.Equal(t, body, string(next))
		var failure *UpstreamFailoverError
		require.ErrorAs(t, CheckOpenAIImageRequestPermission(context.Background(), r, imagePolicyAccount(false)), &failure)
		require.False(t, failure.RetryableOnSameAccount)
		require.Equal(t, GatewayFailureScopeAccountCapability, failure.Scope)
	}
}

func TestOpenAIImageOptionalBridgeGate(t *testing.T) {
	svc := newOpenAIImageGenerationControlTestService(&httpUpstreamRecorder{})
	svc.cfg.Gateway.CodexImageGenerationBridgeEnabled = true
	ctx := WithOpenAIImageRequest(context.Background(), "/v1/responses", "gpt-6.1-sol", []byte(`{"input":"hello"}`), true)
	a := imagePolicyAccount(false)
	require.False(t, svc.CanInjectOpenAIHostedImageGeneration(ctx, nil, a))
	require.False(t, svc.AccountRequiresOpenAIImageCapability(ctx, nil, a))
	require.NoError(t, CheckOpenAIImageRequestPermission(ctx, DescribeOpenAIImageRequest("/v1/responses", "gpt-6.1-sol", []byte(`{"input":"hello"}`), true), a))
	a.UpstreamImagePricing.Stale = true
	require.False(t, svc.CanInjectOpenAIHostedImageGeneration(ctx, nil, a))
	a = imagePolicyAccount(true)
	require.True(t, svc.CanInjectOpenAIHostedImageGeneration(ctx, nil, a))
	a.UpstreamImagePricing = nil
	require.True(t, svc.CanInjectOpenAIHostedImageGeneration(ctx, nil, a))
	a.Extra = map[string]any{featureKeyCodexImageGenerationBridge: false}
	require.False(t, svc.CanInjectOpenAIHostedImageGeneration(ctx, nil, a))
	a.Extra = nil
	groupCtx := WithOpenAIImageRequestGroup(ctx, &Group{AllowImageGeneration: false})
	require.False(t, svc.CanInjectOpenAIHostedImageGeneration(groupCtx, nil, a))
	require.False(t, svc.AccountRequiresOpenAIImageCapability(groupCtx, nil, a))
}

func TestOpenAIImageFinalGuardRetainsHistoryAfterTransform(t *testing.T) {
	for _, body := range []string{
		`{"input":[{"type":"image_generation_call","result":"old"}]}`,
		`{"previous_response_id":"resp_x","tools":[{"type":"namespace","name":"image_gen"}]}`,
	} {
		ctx := WithOpenAIImageRequest(context.Background(), "/v1/responses", "gpt-6.1-sol", []byte(body), true)
		final := DescribeOpenAIImageRequest("/v1/responses", "gpt-6.1-sol", []byte(`{"input":"hello"}`), true)
		require.Error(t, CheckOpenAIImageRequestPermission(ctx, final, imagePolicyAccount(false)))
	}
}

func TestOpenAIImagePolicyRawSnapshotFallback(t *testing.T) {
	now := time.Now()
	a := &Account{Platform: PlatformOpenAI, Extra: map[string]any{Sub2APIImagePricingSnapshotExtraKey: sub2APIImagePricingSnapshotMap(sub2APIImagePricingSnapshot{Version: 1, Status: UpstreamKeyImagePricingStatusAvailable, AllowImageGeneration: true, ObservedAt: &now})}}
	require.Equal(t, OpenAIImagePermissionAllowed, OpenAIImagePermissionRank(a, 0))
	a.Extra = map[string]any{LCodexImageCapabilitySnapshotExtraKey: lcodexImageCapabilitySnapshotMap(lcodexImageCapabilitySnapshot{Version: 1, Status: UpstreamKeyImagePricingStatusDisabled, AllowImageGeneration: false, Stale: true})}
	require.Equal(t, OpenAIImagePermissionDenied, OpenAIImagePermissionRank(a, 0))
}

func TestOpenAIImageRequestDescriptorHeaderMetadata(t *testing.T) {
	svc := newOpenAIImageGenerationControlTestService(&httpUpstreamRecorder{})
	svc.cfg.Gateway.CodexImageGenerationBridgeEnabled = true
	r := DescribeOpenAIImageRequest("/v1/responses", "gpt-6.1-sol", []byte(`{"input":"hello"}`), true)
	r.Lite = true
	ctx := WithOpenAIImageRequestDescriptor(context.Background(), r)
	got, ok := OpenAIImageRequestFromContext(ctx)
	require.True(t, ok)
	require.True(t, got.Lite)
	require.False(t, svc.CanInjectOpenAIHostedImageGeneration(ctx, nil, imagePolicyAccount(true)))
	r.Lite = false
	r.Endpoint = "/v1/responses/compact"
	ctx = WithOpenAIImageRequestDescriptor(context.Background(), r)
	require.False(t, svc.CanInjectOpenAIHostedImageGeneration(ctx, nil, imagePolicyAccount(true)))
}

func TestOpenAIImagePermissionLocalFailureScope(t *testing.T) {
	body := []byte(`{"tools":[{"type":"image_generation"}]}`)
	r := DescribeOpenAIImageRequest(openAIResponsesEndpoint, "gpt-6.1-sol", body, false)
	for _, reason := range []GatewayFailureReason{"image_permission_denied", "image_capability_cooldown", "image_responses_unsupported"} {
		t.Run(string(reason), func(t *testing.T) {
			a := imagePolicyAccount(true)
			switch reason {
			case "image_permission_denied":
				a = imagePolicyAccount(false)
			case "image_capability_cooldown":
				setAccountModelRateLimitSnapshot(a, openAIImageGenerationRateLimitKey, time.Now().Add(time.Hour), "test", time.Now())
			case "image_responses_unsupported":
				a.Type = AccountTypeAPIKey
				a.Extra = map[string]any{openai_compat.ExtraKeyResponsesSupported: false}
			}
			var failure *UpstreamFailoverError
			require.ErrorAs(t, CheckOpenAIImageRequestPermission(context.Background(), r, a), &failure)
			require.Equal(t, GatewayFailureScopeAccountCapability, failure.Scope)
			require.Equal(t, reason, failure.Reason)
			require.False(t, failure.RetryableOnSameAccount)
		})
	}
}
