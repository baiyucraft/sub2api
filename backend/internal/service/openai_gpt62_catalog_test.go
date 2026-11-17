//go:build unit

package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGPT62GeneratedCatalog(t *testing.T) {
	for _, tc := range []struct {
		model  string
		window int64
		name   string
	}{
		{"gpt-6.2-sol", 500000, "GPT-6.2 Sol"},
		{"gpt-6.2-astra", 272000, "GPT-6.2 Astra"},
	} {
		t.Run(tc.model, func(t *testing.T) {
			d := newConfiguredCodexModelDescriptor(tc.model)
			require.Equal(t, tc.name, d.DisplayName)
			require.Equal(t, "low", *d.DefaultReasoningLevel)
			require.Equal(t, tc.window, d.ContextWindow)
			require.EqualValues(t, 1050000, d.MaxContextWindow)
			require.Equal(t, []string{"text", "image"}, d.InputModalities)
			require.False(t, d.UseResponsesLite)
			require.Equal(t, "xhigh", *d.MultiAgentReasoningEffort)
			require.Equal(t, "v2", d.MultiAgentVersion)
			body, err := json.Marshal(map[string]any{"models": []configuredCodexModelDescriptor{d}})
			require.NoError(t, err)
			models := decodeCodexManifestModels(t, body)
			require.Equal(t, []string{"low", "medium", "high", "xhigh", "max", "ultra"}, effortsFromManifestModel(t, models[0]))
			require.True(t, isOpenAICodexReasoningGPTModel(tc.model))
			require.True(t, isOpenAICodexImageInputModel(tc.model))
			require.NotEmpty(t, d.ServiceTiers)
		})
	}
	unknown := newConfiguredCodexModelDescriptor("gpt-6.2-sol-preview")
	require.Equal(t, "none", *unknown.DefaultReasoningLevel)
}

func TestGPT62CatalogRespectsLiveCapabilities(t *testing.T) {
	for _, model := range []string{"gpt-6.2-sol", "gpt-6.2-astra"} {
		account := Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
		reasoning := true
		account.SetUpstreamModelMetadataSnapshot(UpstreamModelMetadataSnapshot{Models: map[string]UpstreamModelMetadata{
			model: {ID: model, Reasoning: &reasoning, DefaultReasoningLevel: "high", SupportedReasoningLevels: []string{"high", "max", "ultra"}, InputModalities: []string{"text"}, ContextWindow: 160000},
		}})
		s := &GatewayService{accountRepo: codexModelsVisibilityAccountRepo{byGroup: map[int64][]Account{1: {account}}}}
		body, err := s.BuildCodexModelsManifestForGroup(context.Background(), &Group{ID: 1, Platform: PlatformOpenAI}, "", []string{model})
		require.NoError(t, err)
		models := decodeCodexManifestModels(t, body)
		require.Len(t, models, 1)
		require.Equal(t, "high", models[0]["default_reasoning_level"])
		require.Equal(t, []string{"high", "max", "ultra"}, effortsFromManifestModel(t, models[0]))
		require.EqualValues(t, 160000, models[0]["context_window"])
		require.Equal(t, []any{"text"}, models[0]["input_modalities"])
	}
}

func TestGPT62OrdinaryModelsConversionPreservesExplicitWindows(t *testing.T) {
	for _, model := range []string{"gpt-6.2-sol", "gpt-6.2-astra"} {
		body := convertOpenAIModelListToCodexManifest([]byte(`{"object":"list","data":[{"id":"` + model + `","context_window":128000,"max_context_window":256000}]}`))
		models := decodeCodexManifestModels(t, body)
		require.Equal(t, model, models[0]["slug"])
		require.EqualValues(t, 128000, models[0]["context_window"])
		require.EqualValues(t, 256000, models[0]["max_context_window"])
		require.Equal(t, []string{"low", "medium", "high", "xhigh", "max", "ultra"}, effortsFromManifestModel(t, models[0]))
		require.Equal(t, codexModelsManifestBodyETag(body), CodexModelsManifestETag(body))
	}
}

func TestGPT62APIKeyCatalogDisablesLiteForMappedAliases(t *testing.T) {
	account := newCodexModelsAPIKeyTestAccount("https://models.example/v1")
	account.Credentials["model_mapping"] = map[string]any{"public-sol": "openai/gpt-6.2-sol-max"}
	body, err := adjustAPIKeyCodexModelsManifest([]byte(`{"models":[{"slug":"public-sol","use_responses_lite":true},{"slug":"gpt-6.2-astra","use_responses_lite":true}]}`), account)
	require.NoError(t, err)
	for _, model := range decodeCodexManifestModels(t, body) {
		require.Equal(t, false, model["use_responses_lite"])
	}
}

const legacyGPT62Catalog = `{"models":[{"slug":"gpt-6.2-sol","display_name":"gpt-6.2-sol","description":"OpenAI GPT coding model routed through Sub2API.","default_reasoning_level":"none","supported_reasoning_levels":[{"effort":"none","description":"Use the model's default behavior without configurable reasoning"}],"context_window":272000,"max_context_window":272000,"input_modalities":["text"],"truncation_policy":{"mode":"bytes","limit":10000}}]}`

func TestGPT62StaleUpstreamFallbackIsRebuilt(t *testing.T) {
	account := newCodexModelsAPIKeyTestAccount("https://models.example/v1")
	manifest := &OpenAIModelsResponse{Body: []byte(legacyGPT62Catalog)}
	s := &OpenAIGatewayService{}
	require.NoError(t, s.CompleteAPIKeyCodexModelsManifestForClient(manifest, account))
	body, changed, err := mergeConfiguredCodexModelsManifest(manifest.Body, []string{"gpt-6.2-sol"}, nil, false)
	require.NoError(t, err)
	require.True(t, changed)
	models := decodeCodexManifestModels(t, body)
	require.Len(t, models, 1)
	require.Equal(t, "low", models[0]["default_reasoning_level"])
	require.EqualValues(t, 500000, models[0]["context_window"])
	require.Equal(t, []string{"low", "medium", "high", "xhigh", "max", "ultra"}, effortsFromManifestModel(t, models[0]))
}

func TestGPT62PinnedOlderFallbackCannotHideLiveEntry(t *testing.T) {
	live := []byte(`{"models":[{"slug":"gpt-6.2-sol","description":"Live upstream","default_reasoning_level":"high","context_window":160000,"input_modalities":["text"]}]}`)
	stale := []byte(legacyGPT62Catalog)
	for _, bodies := range [][][]byte{{stale, live}, {live, stale}} {
		body, err := mergeCodexModelsManifestBodies(bodies)
		require.NoError(t, err)
		models := decodeCodexManifestModels(t, body)
		require.Len(t, models, 1)
		require.Equal(t, "Live upstream", models[0]["description"])
		require.Equal(t, "high", models[0]["default_reasoning_level"])
		require.EqualValues(t, 160000, models[0]["context_window"])
		require.Equal(t, []any{"text"}, models[0]["input_modalities"])
	}
	body, err := mergeCodexModelsManifestBodies([][]byte{stale})
	require.NoError(t, err)
	require.Equal(t, "low", decodeCodexManifestModels(t, body)[0]["default_reasoning_level"])
}

func TestGPT62OfficialAPIKeyPreservesExplicitImageFields(t *testing.T) {
	account := newCodexModelsAPIKeyTestAccount("https://api.openai.com/v1")
	body := []byte(`{"models":[{"slug":"gpt-6.2-sol","input_modalities":["text"],"supports_image_detail_original":false},{"slug":"gpt-6.2-astra"}]}`)
	completed, err := completeAPIKeyCodexModelsManifestMetadata(body, true, account)
	require.NoError(t, err)
	models := decodeCodexManifestModels(t, completed)
	require.Equal(t, []any{"text"}, models[0]["input_modalities"])
	require.Equal(t, false, models[0]["supports_image_detail_original"])
	require.Equal(t, []any{"text", "image"}, models[1]["input_modalities"])
	require.Equal(t, false, models[1]["supports_image_detail_original"])
}
