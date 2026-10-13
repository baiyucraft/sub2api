//go:build unit

package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGPT61SolLongContextCatalogDefaults(t *testing.T) {
	account := newCodexModelsAPIKeyTestAccount("https://relay.example/v1")
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("enabled=%t", enabled), func(t *testing.T) {
			defaults := codexModelDefaults{gpt61SolNewLongContextEnabled: enabled}
			want := 272000
			if enabled {
				want = 500000
			}
			for _, body := range []string{
				`{"models":[{"slug":"gpt-6.1-sol"}]}`,
				`{"models":[{"slug":"gpt-6.1-sol","context_window":null}]}`,
			} {
				completed, err := completeAPIKeyCodexModelsManifestMetadata([]byte(body), true, account, defaults)
				require.NoError(t, err)
				model := decodeCodexManifestModels(t, completed)[0]
				require.EqualValues(t, want, model["context_window"])
				require.EqualValues(t, 872000, model["max_context_window"])
			}
			converted := convertOpenAIModelListToCodexManifestForAccount(
				[]byte(`{"object":"list","data":[{"id":"gpt-6.1-sol"}]}`), account, defaults)
			require.EqualValues(t, want, decodeCodexManifestModels(t, converted)[0]["context_window"])
			merged, _, err := mergeConfiguredCodexModelsManifest(
				[]byte(`{"models":[]}`), []string{"openai/GPT_6.1_SOL-max"}, nil, false, defaults)
			require.NoError(t, err)
			require.EqualValues(t, want, decodeCodexManifestModels(t, merged)[0]["context_window"])
			for _, window := range []int{272000, 300000, 1000000} {
				body := fmt.Sprintf(`{"models":[{"slug":"gpt-6.1-sol","context_window":%d,"max_context_window":1000000}]}`, window)
				completed, err := completeAPIKeyCodexModelsManifestMetadata([]byte(body), true, account, defaults)
				require.NoError(t, err)
				model := decodeCodexManifestModels(t, completed)[0]
				require.EqualValues(t, window, model["context_window"])
				require.EqualValues(t, 1000000, model["max_context_window"])
				list := fmt.Sprintf(`{"object":"list","data":[{"id":"gpt-6.1-sol","context_window":%d,"max_context_window":1000000}]}`, window)
				converted = convertOpenAIModelListToCodexManifestForAccount([]byte(list), account, defaults)
				model = decodeCodexManifestModels(t, converted)[0]
				require.EqualValues(t, window, model["context_window"])
				require.EqualValues(t, 1000000, model["max_context_window"])
			}
		})
	}
}

func TestGPT61SolLongContextGeneratedGroupCatalog(t *testing.T) {
	account := Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Credentials: map[string]any{
			"base_url":      "https://relay.example/v1",
			"model_mapping": map[string]any{"public-sol": "gpt-6.1-sol"},
		}}
	group := &Group{ID: 1, Platform: PlatformOpenAI}
	repo := codexModelsVisibilityAccountRepo{byGroup: map[int64][]Account{1: {account}}}
	svc := &OpenAIGatewayService{accountRepo: repo}
	var previousETag string
	for _, enabled := range []bool{false, true, false} {
		t.Run(fmt.Sprintf("enabled=%t", enabled), func(t *testing.T) {
			setGPT61SolLongContextForBillingTest(t, svc, enabled)
			want := 272000
			if enabled {
				want = 500000
			}
			manifest, configured, err := svc.BuildGroupConfiguredCodexModelsManifest(context.Background(), group, previousETag)
			require.NoError(t, err)
			require.True(t, configured)
			require.False(t, manifest.NotModified)
			model := decodeCodexManifestModels(t, manifest.Body)[0]
			require.Equal(t, "public-sol", model["slug"])
			require.EqualValues(t, want, model["context_window"])
			require.EqualValues(t, 872000, model["max_context_window"])
			previousETag = manifest.ETag
			same, _, err := svc.BuildGroupConfiguredCodexModelsManifest(context.Background(), group, previousETag)
			require.NoError(t, err)
			require.True(t, same.NotModified)
			gateway := &GatewayService{accountRepo: repo, settingService: svc.settingService}
			body, err := gateway.BuildCodexModelsManifestForGroup(context.Background(), group, "", []string{"public-sol"})
			require.NoError(t, err)
			require.EqualValues(t, want, decodeCodexManifestModels(t, body)[0]["context_window"])
		})
	}
}

func TestGPT61SolLongContextCachedCatalogSwitch(t *testing.T) {
	for _, source := range []struct {
		name         string
		body         string
		wantExplicit int
	}{
		{"missing", `{"models":[{"slug":"gpt-6.1-sol"}]}`, 0},
		{"null", `{"models":[{"slug":"gpt-6.1-sol","context_window":null}]}`, 0},
		{"standard-list", `{"object":"list","data":[{"id":"gpt-6.1-sol"}]}`, 0},
		{"explicit", `{"models":[{"slug":"gpt-6.1-sol","context_window":300000,"max_context_window":900000}]}`, 300000},
	} {
		t.Run(source.name, func(t *testing.T) {
			var calls atomic.Int32
			upstream := &codexModelsHTTPUpstreamStub{do: func(_ *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
				calls.Add(1)
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Etag": []string{`"provider"`}},
					Body: io.NopCloser(strings.NewReader(source.body))}, nil
			}}
			svc := newCodexModelsAPIKeyTestService(upstream)
			account := newCodexModelsAPIKeyTestAccount("https://relay.example/v1")
			svc.accountRepo = codexModelsVisibilityAccountRepo{}
			group := &Group{ID: 1, Platform: PlatformOpenAI}
			group.CodexModelsManifestConfig.Enabled = true
			var previousETag string
			for i, enabled := range []bool{false, true, false} {
				setGPT61SolLongContextForBillingTest(t, svc, enabled)
				manifest, err := svc.FetchCodexModelsManifest(context.Background(), account, "0.150.0", "")
				require.NoError(t, err)
				require.NoError(t, svc.CompleteAPIKeyCodexModelsManifestForClient(manifest, account))
				model := decodeCodexManifestModels(t, manifest.Body)[0]
				want := 272000
				if enabled {
					want = 500000
				}
				if source.wantExplicit != 0 {
					want = source.wantExplicit
				}
				require.EqualValues(t, want, model["context_window"])
				require.NoError(t, svc.MergeGroupConfiguredCodexModels(context.Background(), group, manifest, previousETag))
				if source.wantExplicit == 0 {
					require.False(t, manifest.NotModified, "switch must return changed content")
				} else if i > 0 {
					require.True(t, manifest.NotModified, "explicit upstream windows are policy-independent")
				}
				previousETag = manifest.ETag
			}
			require.Equal(t, int32(1), calls.Load(), "policy changes must reuse the untouched upstream source cache")
		})
	}
}

func TestGPT61SolLongContextCatalogCompletionAndAliasMergeSharePolicy(t *testing.T) {
	account := newCodexModelsAPIKeyTestAccount("https://relay.example/v1")
	account.Credentials["model_mapping"] = map[string]any{"gpt-6.1-sol": "gpt-6.1-sol"}
	group := &Group{ID: 1, Platform: PlatformOpenAI}
	svc := &OpenAIGatewayService{accountRepo: codexModelsVisibilityAccountRepo{
		byGroup: map[int64][]Account{1: {*account}},
	}}
	setGPT61SolLongContextForBillingTest(t, svc, false)
	manifest := &OpenAIModelsResponse{Body: []byte(`{"models":[{"slug":"gpt-5.6-sol"}]}`)}
	require.NoError(t, svc.CompleteAPIKeyCodexModelsManifestForClient(manifest, account))
	// A save during the response must not split metadata completion and alias
	// supplementation across two policies.
	setGPT61SolLongContextForBillingTest(t, svc, true)
	require.NoError(t, svc.MergeGroupConfiguredCodexModels(context.Background(), group, manifest, ""))
	models := decodeCodexManifestModels(t, manifest.Body)
	require.Len(t, models, 2)
	require.EqualValues(t, 272000, models[1]["context_window"])
}

func TestGPT61SolLongContextLiveWindowsOverrideSyncedMetadata(t *testing.T) {
	for _, shape := range []string{"manifest", "standard-list"} {
		// Even a value equal to a generated default is explicit provider data.
		for _, window := range []int{0, 272000, 300000, 500000} {
			t.Run(fmt.Sprintf("%s/window=%d", shape, window), func(t *testing.T) {
				fields := `,"context_window":null`
				if window > 0 {
					fields = fmt.Sprintf(`,"context_window":%d,"max_context_window":900000`, window)
				}
				source := fmt.Sprintf(`{"models":[{"slug":"gpt-6.1-sol"%s}]}`, fields)
				if shape == "standard-list" {
					source = fmt.Sprintf(`{"object":"list","data":[{"id":"gpt-6.1-sol"%s}]}`, fields)
				}
				var calls atomic.Int32
				upstream := &codexModelsHTTPUpstreamStub{do: func(_ *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
					calls.Add(1)
					return &http.Response{StatusCode: http.StatusOK, Header: http.Header{},
						Body: io.NopCloser(strings.NewReader(source))}, nil
				}}
				svc := newCodexModelsAPIKeyTestService(upstream)
				account := newCodexModelsAPIKeyTestAccount("https://relay.example/v1")
				account.SetUpstreamModelMetadataSnapshot(UpstreamModelMetadataSnapshot{Models: map[string]UpstreamModelMetadata{
					"gpt-6.1-sol": {ID: "gpt-6.1-sol", ContextWindow: 999000},
				}})
				svc.accountRepo = codexModelsVisibilityAccountRepo{}
				group := &Group{ID: 1, Platform: PlatformOpenAI}
				group.CodexModelsManifestConfig.Enabled = true
				var previousETag string
				for i, enabled := range []bool{false, true, false} {
					setGPT61SolLongContextForBillingTest(t, svc, enabled)
					manifest, err := svc.FetchCodexModelsManifest(context.Background(), account, "0.150.0", "")
					require.NoError(t, err)
					require.NoError(t, svc.CompleteAPIKeyCodexModelsManifestForClient(manifest, account))
					model := decodeCodexManifestModels(t, manifest.Body)[0]
					want, wantMax := window, 900000
					if window == 0 {
						want, wantMax = 999000, 999000
					}
					require.EqualValues(t, want, model["context_window"])
					require.EqualValues(t, wantMax, model["max_context_window"])
					require.NoError(t, svc.MergeGroupConfiguredCodexModels(context.Background(), group, manifest, previousETag))
					require.Equal(t, i > 0, manifest.NotModified, "provider/synced windows do not change with local defaults")
					previousETag = manifest.ETag
				}
				require.EqualValues(t, 1, calls.Load(), "all policies reuse cached provider source")
			})
		}
	}
}
