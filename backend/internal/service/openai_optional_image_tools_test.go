package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpenAIImageHistoryResolvesOnlyKnownImageCalls(t *testing.T) {
	for _, tt := range []struct {
		name, input   string
		hard, unknown bool
	}{
		{"shell", `[{"type":"function_call_output","call_id":"a","output":"ok"},{"type":"function_call","call_id":"a","name":"exec_command"}]`, false, false},
		{"image", `[{"type":"function_call_output","call_id":"a","output":"ok"},{"type":"function_call","call_id":"a","name":"image_gen__imagegen"}]`, true, false},
		{"unknown", `[{"type":"function_call_output","call_id":"missing","output":"ok"}]`, false, true},
		{"missing id", `[{"type":"function_call_output","output":"ok"}]`, false, true},
		{"native history", `[{"type":"image_generation_call","id":"a","result":"old"}]`, true, false},
		{"image understanding", `[{"role":"user","content":[{"type":"input_image","image_url":"https://example.test/x.png"}]}]`, false, false},
		{"output cannot invent calls", `[{"type":"function_call_output","call_id":"a","output":{"type":"image_generation_call"}},{"type":"function_call","call_id":"a","name":"shell"}]`, false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(`{"tools":[{"type":"namespace","name":"image_gen"}],"input":` + tt.input + `}`)
			r := DescribeOpenAIImageRequest(openAIResponsesEndpoint, "text-model", body, false)
			require.Equal(t, tt.hard, r.RequiresCapability())
			require.Equal(t, tt.unknown, r.UnknownHistory)
		})
	}
	chat := []byte(`{"messages":[{"role":"tool","tool_call_id":"shell","content":"ok"},{"role":"assistant","tool_calls":[{"id":"shell","type":"function","function":{"name":"exec_command"}}]}]}`)
	r := DescribeOpenAIImageRequest(openAIResponsesEndpoint, "text-model", chat, false)
	require.False(t, r.History)
	require.False(t, r.UnknownHistory)
}

func TestOpenAIOptionalImageGroupPolicyPreservesContinuationAndChoices(t *testing.T) {
	for _, tt := range []struct {
		name, body    string
		hard, changed bool
		choice        string
	}{
		{"native auto", `{"tools":[{"type":"image_generation"}],"tool_choice":"auto"}`, false, true, `"auto"`},
		{"native none", `{"tools":[{"type":"image_generation"}],"tool_choice":"none"}`, false, true, `"none"`},
		{"native omitted", `{"tools":[{"type":"image_generation"}]}`, false, true, ``},
		{"sole required", `{"tools":[{"type":"image_generation"}],"tool_choice":"required"}`, true, false, `"required"`},
		{"mixed required", `{"tools":[{"type":"image_generation"},{"type":"function","name":"shell"}],"tool_choice":"required"}`, false, true, `"required"`},
		{"restricted required image", `{"tools":[{"type":"image_generation"},{"type":"function","name":"shell"}],"tool_choice":{"type":"allowed_tools","mode":"required","tools":[{"type":"image_generation"}]}}`, true, false, `{"type":"allowed_tools","mode":"required","tools":[{"type":"image_generation"}]}`},
		{"restricted auto image", `{"tools":[{"type":"image_generation"},{"type":"function","name":"shell"}],"tool_choice":{"type":"allowed_tools","mode":"auto","tools":[{"type":"image_generation"}]}}`, false, true, `"none"`},
		{"restriction without catalog", `{"tool_choice":{"type":"allowed_tools","mode":"auto","tools":[{"type":"image_generation"}]}}`, false, true, `"none"`},
		{"mixed restriction without catalog", `{"tool_choice":{"type":"allowed_tools","mode":"auto","tools":[{"type":"function","name":"image_gen__imagegen"},{"type":"function","name":"shell"}]}}`, false, true, `{"type":"allowed_tools","mode":"auto","tools":[{"type":"function","name":"shell"}]}`},
		{"empty unrelated restriction", `{"tool_choice":{"type":"allowed_tools","mode":"auto","tools":[]}}`, false, false, `{"type":"allowed_tools","mode":"auto","tools":[]}`},
		{"restricted mixed", `{"tools":[{"type":"image_generation"},{"type":"function","name":"shell"},{"type":"function","name":"forbidden"}],"tool_choice":{"type":"allowed_tools","mode":"required","tools":[{"type":"image_generation"},{"type":"function","name":"shell"}]}}`, false, true, `{"type":"allowed_tools","mode":"required","tools":[{"type":"function","name":"shell"}]}`},
		{"selected flattened", `{"tools":[{"type":"function","name":"image_gen.imagegen"}],"tool_choice":{"type":"function","name":"image_gen.imagegen"}}`, true, false, `{"type":"function","name":"image_gen.imagegen"}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(tt.body)
			r := DescribeOpenAIImageRequest(openAIResponsesEndpoint, "text-model", body, true)
			require.Equal(t, tt.hard, r.RequiresCapability())
			next, final, changed, err := ApplyOpenAIImageRequestGroupPolicy(body, r, &Group{AllowImageGeneration: false})
			require.NoError(t, err)
			require.Equal(t, tt.changed, changed)
			require.Equal(t, tt.hard, final.RequiresCapability())
			require.Equal(t, tt.choice, gjson.GetBytes(next, "tool_choice").Raw)
			if !changed {
				require.Equal(t, tt.body, string(next))
			}
			require.Equal(t, tt.body, string(body))
		})
	}
	body := []byte(`{"seed":9007199254740993,"previous_response_id":"resp_old","tools":[{"type":"function","name":"image_gen__imagegen"},{"type":"function","name":"shell"}],"input":[{"type":"function_call_output","call_id":"unknown","output":"keep me"},{"type":"additional_tools","tools":[{"type":"namespace","name":"image_gen"}]}]}`)
	r := DescribeOpenAIImageRequest(openAIResponsesEndpoint, "text-model", body, false)
	next, final, changed, err := ApplyOpenAIImageRequestGroupPolicy(body, r, &Group{AllowImageGeneration: true, StripOptionalImageTools: true})
	require.NoError(t, err)
	require.True(t, changed)
	require.False(t, final.RequiresCapability())
	require.True(t, final.UnknownHistory)
	require.Equal(t, "resp_old", gjson.GetBytes(next, "previous_response_id").String())
	require.Equal(t, "keep me", gjson.GetBytes(next, "input.0.output").String())
	require.Equal(t, "9007199254740993", gjson.GetBytes(next, "seed").Raw)
	require.Equal(t, "shell", gjson.GetBytes(next, "tools.0.name").String())
	require.Len(t, gjson.GetBytes(next, "input").Array(), 1)
	require.NoError(t, CheckOpenAIImageRequestPermission(WithOpenAIImageRequestDescriptor(context.Background(), r), final, imagePolicyAccount(false)))
}

func TestOpenAIOptionalImageGroupPolicySuppressesBridgeAndScheduling(t *testing.T) {
	svc := newOpenAIImageGenerationControlTestService(&httpUpstreamRecorder{})
	svc.cfg.Gateway.CodexImageGenerationBridgeEnabled = true
	body := []byte(`{"tools":[{"type":"image_generation"}],"previous_response_id":"resp_unknown"}`)
	ctx := WithOpenAIImageRequest(context.Background(), openAIResponsesEndpoint, "text-model", body, true)
	ctx = WithOpenAIImageRequestGroup(ctx, &Group{ID: 10, AllowImageGeneration: true, StripOptionalImageTools: true})
	ctx = svc.WithOpenAIImageRequestPolicy(ctx, nil)
	// Channel lookup recaching must not re-enable a group suppression flag.
	a := imagePolicyAccount(true)
	a.Extra = map[string]any{featureKeyCodexImageGenerationBridge: true}
	require.False(t, svc.CanInjectOpenAIHostedImageGeneration(ctx, nil, a))
	require.False(t, svc.AccountRequiresOpenAIImageCapability(ctx, nil, a))
	require.False(t, svc.AccountRequiresOpenAIResponsesForImage(ctx, nil, a))
}

func TestOpenAIOptionalImageGroupPolicyRejectsAmbiguousDeclarations(t *testing.T) {
	for _, body := range []string{
		`{"tools":[{"type":"image_generation"}],"tools":[]}`,
		`{"tools":[],"tools":[{"type":"image_generation"}]}`,
		`{"tools":[{"type":"namespace","name":"image_gen"}],"tools":[]}`,
		`{"tools":[],"tools":[{"type":"namespace","name":"image_gen"}]}`,
		`{"tools":[{"type":"namespace","name":"image_gen","type":"image_generation"}]}`,
		`{"input":"first","input":"second"}`,
	} {
		for _, group := range []*Group{
			{AllowImageGeneration: false},
			{AllowImageGeneration: true, StripOptionalImageTools: true},
		} {
			r := DescribeOpenAIImageRequest(openAIResponsesEndpoint, "text-model", []byte(body), false)
			require.False(t, r.Valid)
			next, _, changed, err := ApplyOpenAIImageRequestGroupPolicy([]byte(body), r, group)
			require.EqualError(t, err, "invalid image tool declarations")
			require.False(t, changed)
			require.Equal(t, body, string(next), "ambiguous input must remain unchanged")
		}
	}
	// Preserve the existing behavior when optional-tool cleanup is disabled.
	body := []byte(`{"tools":[{"type":"image_generation"}],"tools":[]}`)
	r := DescribeOpenAIImageRequest(openAIResponsesEndpoint, "text-model", body, false)
	_, legacy, changed, err := ApplyOpenAIImageRequestGroupPolicy(body, r, &Group{AllowImageGeneration: true})
	require.NoError(t, err)
	require.False(t, changed)
	require.True(t, legacy.RequiresCapability())
}

func TestOpenAIImageNativeLegacyPolicyOnlySurvivesExistingDeclarations(t *testing.T) {
	body := []byte(`{"tools":[{"type":"image_generation"}],"tool_choice":"auto"}`)
	r := DescribeOpenAIImageRequest(openAIResponsesEndpoint, "text-model", body, true)
	require.False(t, r.RequiresCapability())
	_, legacy, changed, err := ApplyOpenAIImageRequestGroupPolicy(body, r, &Group{AllowImageGeneration: true})
	require.NoError(t, err)
	require.False(t, changed)
	require.True(t, legacy.NativeRequired)
	require.True(t, legacy.RequiresCapability())
	retained := DescribeOpenAIImageRequest(openAIResponsesEndpoint, "text-model", body, true)
	require.True(t, ReconcileOpenAIImageRequestDescriptor(retained, legacy).RequiresCapability())
	removed := DescribeOpenAIImageRequest(openAIResponsesEndpoint, "text-model", []byte(`{"tools":[]}`), true)
	require.False(t, ReconcileOpenAIImageRequestDescriptor(removed, legacy).RequiresCapability())
	_, cleaned, changed, err := ApplyOpenAIImageRequestGroupPolicy(body, legacy, &Group{AllowImageGeneration: true, StripOptionalImageTools: true})
	require.NoError(t, err)
	require.True(t, changed)
	require.False(t, cleaned.RequiresCapability())
}
