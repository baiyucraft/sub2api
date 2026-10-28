//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/gemini"
	"github.com/stretchr/testify/require"
)

func newGeminiModelsEndpointsTestService(accounts []Account, upstream HTTPUpstream) *GeminiMessagesCompatService {
	repo := &mockAccountRepoForGemini{listByGroupFunc: func(context.Context, int64, []string) ([]Account, error) {
		return accounts, nil
	}}
	return &GeminiMessagesCompatService{
		accountRepo:   repo,
		httpUpstream:  upstream,
		cfg:           &config.Config{},
		tokenProvider: NewGeminiTokenProvider(nil, &vertexModelsTokenCacheStub{token: "ya29.vertex-token"}, nil),
	}
}

func decodeGeminiModelNames(t *testing.T, body []byte) []string {
	t.Helper()
	var got gemini.ModelsListResponse
	require.NoError(t, json.Unmarshal(body, &got))
	names := make([]string, 0, len(got.Models))
	for _, model := range got.Models {
		require.Contains(t, model.SupportedGenerationMethods, "generateContent")
		names = append(names, model.Name)
	}
	return names
}

func TestGeminiDeclaredModelsPreserveMappingAndSelectionContracts(t *testing.T) {
	groupID := int64(8)
	first := Account{ID: 1, Platform: PlatformGemini, Type: AccountTypeAPIKey, Credentials: map[string]any{
		"api_key": "test", "model_mapping": map[string]any{"gemini-3.*": "gemini-3.8-flash", "gemini-2.5-flash": "gemini-2.5-flash"},
	}}
	second := Account{ID: 2, Platform: PlatformGemini, Type: AccountTypeAPIKey, Credentials: map[string]any{
		"api_key": "test", "model_mapping": map[string]any{"gemini-3.1-pro-preview": "gemini-3.1-pro-preview", "gemini-2.5-flash": "gemini-2.5-flash"},
	}}
	for _, tc := range []struct {
		name     string
		accounts []Account
		want     []string
	}{
		{"deduplicated_sorted_mapping_keys", []Account{second, first}, []string{"gemini-2.5-flash", "gemini-3.*", "gemini-3.1-pro-preview"}},
		{"unrestricted_account_keeps_upstream_fallback", []Account{first, {ID: 3, Platform: PlatformGemini, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "test"}}}, nil},
		{"other_platform_does_not_cancel_declared_list", []Account{first, {ID: 4, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}}, []string{"gemini-2.5-flash", "gemini-3.*"}},
		{"empty_group", nil, []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := newGeminiModelsEndpointsTestService(tc.accounts, nil)
			got, err := svc.DeclaredModelIDs(context.Background(), &groupID)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}

	failure := errors.New("model snapshot unavailable")
	svc := newGeminiModelsEndpointsTestService(nil, nil)
	svc.accountRepo = &mockAccountRepoForGemini{listByGroupFunc: func(context.Context, int64, []string) ([]Account, error) {
		return nil, failure
	}}
	got, err := svc.DeclaredModelIDs(context.Background(), &groupID)
	require.ErrorIs(t, err, failure)
	require.Nil(t, got)
}

func TestGeminiDeclaredModelsCoverExactWildcardAndAlias(t *testing.T) {
	for _, tc := range []struct {
		name     string
		declared []string
		model    string
		want     bool
	}{
		{"exact", []string{"gemini-2.5-flash"}, "gemini-2.5-flash", true},
		{"wildcard", []string{"gemini-3.*"}, "gemini-3.8-flash", true},
		{"normalized_customtools_alias", []string{"gemini-3.1-pro-preview"}, "gemini-3.1-pro-preview-customtools", true},
		{"distinct_model", []string{"gemini-3.1-pro-preview"}, "gemini-2.5-flash", false},
		{"no_declaration", nil, "gemini-2.5-flash", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, DeclaredModelsCover(tc.declared, tc.model))
		})
	}
}

func TestSelectAccountForModelsEndpoints_VertexServiceAccountOnlyGroup(t *testing.T) {
	groupID := int64(8)
	vertex := *vertexModelSyncTestAccount(nil)
	vertex.ID = 18
	svc := newGeminiModelsEndpointsTestService([]Account{vertex}, nil)

	account, err := svc.SelectAccountForModelsEndpoints(context.Background(), &groupID)
	require.NoError(t, err)
	require.Equal(t, int64(18), account.ID)
}

func TestModelsEndpointAccountRank_Order(t *testing.T) {
	ordered := []*Account{
		{Platform: PlatformGemini, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "k"}},
		{Platform: PlatformGemini, Type: AccountTypeOAuth, Credentials: map[string]any{}},
		{Platform: PlatformGemini, Type: AccountTypeOAuth, Credentials: map[string]any{"project_id": "p", "oauth_type": "ai_studio"}},
		{Platform: PlatformGemini, Type: AccountTypeOAuth, Credentials: map[string]any{"project_id": "p", "oauth_type": "code_assist"}},
		{Platform: PlatformGemini, Type: AccountTypeUpstream, Credentials: map[string]any{"api_key": "k"}},
		vertexModelSyncTestAccount(nil),
		{Platform: PlatformGemini, Type: AccountTypeServiceAccount, Credentials: map[string]any{"auth_mode": "apikey", "api_key": "AQ.k"}},
	}
	for i := 1; i < len(ordered); i++ {
		require.Less(t, modelsEndpointAccountRank(ordered[i-1]), modelsEndpointAccountRank(ordered[i]), "position %d", i)
	}
}

func TestSelectAccountForModelsEndpoints_PrefersAPIKeyOverVertex(t *testing.T) {
	groupID := int64(8)
	vertex := *vertexModelSyncTestAccount(nil)
	vertex.ID = 18
	apiKey := Account{ID: 19, Platform: PlatformGemini, Type: AccountTypeAPIKey, Priority: 9, Credentials: map[string]any{"api_key": "k"}}
	svc := newGeminiModelsEndpointsTestService([]Account{vertex, apiKey}, nil)

	account, err := svc.SelectAccountForModelsEndpoints(context.Background(), &groupID)
	require.NoError(t, err)
	require.Equal(t, int64(19), account.ID)
}

func TestSelectAccountForModelsEndpoints_PreservesUpstreamHealthAndExactCost(t *testing.T) {
	groupID := int64(8)
	upstreamConfigID := int64(991070)
	cheapKeyID, expensiveKeyID := int64(991071), int64(991072)
	cheapRate, expensiveRate := 0.045, 0.05
	cheap := Account{ID: 991071, Platform: PlatformGemini, Type: AccountTypeAPIKey, Priority: 1, UpstreamConfigID: &upstreamConfigID, UpstreamKeyID: &cheapKeyID, RateMultiplier: &cheapRate, Credentials: map[string]any{"api_key": "test"}}
	expensive := Account{ID: 991072, Platform: PlatformGemini, Type: AccountTypeAPIKey, Priority: 1, UpstreamConfigID: &upstreamConfigID, UpstreamKeyID: &expensiveKeyID, RateMultiplier: &expensiveRate, Credentials: map[string]any{"api_key": "test"}}
	registry := GlobalUpstreamHealthRegistry()
	registry.Forget(cheapKeyID)
	registry.Forget(expensiveKeyID)
	t.Cleanup(func() { registry.Forget(cheapKeyID); registry.Forget(expensiveKeyID) })
	ctx := context.WithValue(context.Background(), ctxkey.ForcePlatform, PlatformGemini)
	svc := newGeminiModelsEndpointsTestService([]Account{expensive, cheap}, nil)

	selected, err := svc.SelectAccountForModelsEndpoints(ctx, &groupID)
	require.NoError(t, err)
	require.Equal(t, cheap.ID, selected.ID, "effective rates must not be rounded before comparison")

	registry.RecordFailure(cheapKeyID, "401", "authentication_failed", time.Now())
	selected, err = svc.SelectAccountForModelsEndpoints(ctx, &groupID)
	require.NoError(t, err)
	require.Equal(t, expensive.ID, selected.ID, "a healthy key must precede an excluded cheaper key")

	registry.RecordFailure(expensiveKeyID, "401", "authentication_failed", time.Now())
	selected, err = svc.SelectAccountForModelsEndpoints(ctx, &groupID)
	require.NoError(t, err)
	require.Equal(t, cheap.ID, selected.ID, "when every key is excluded retain the existing availability fallback")
}

type geminiModelsErrorAccountRepo struct {
	*mockAccountRepoForGemini
	getByID func(context.Context, int64) (*Account, error)
}

func (r *geminiModelsErrorAccountRepo) GetByID(ctx context.Context, id int64) (*Account, error) {
	return r.getByID(ctx, id)
}

func (r *geminiModelsErrorAccountRepo) ListSchedulableByGroupIDAndPlatform(ctx context.Context, groupID int64, platform string) ([]Account, error) {
	return r.ListSchedulableByGroupIDAndPlatforms(ctx, groupID, []string{platform})
}

func TestSelectAccountForModelsEndpoints_QueryAndHydrationErrorsKeepHealthExclusions(t *testing.T) {
	for _, failure := range []string{"query", "hydration"} {
		t.Run(failure, func(t *testing.T) {
			groupID, excludedKeyID := int64(8), int64(991073)
			excluded := Account{ID: 991073, Platform: PlatformGemini, Type: AccountTypeAPIKey, Priority: 0, UpstreamKeyID: &excludedKeyID, Credentials: map[string]any{"api_key": "test"}}
			healthy := Account{ID: 991074, Platform: PlatformGemini, Type: AccountTypeAPIKey, Priority: 1, Credentials: map[string]any{"api_key": "test"}}
			registry := GlobalUpstreamHealthRegistry()
			registry.Forget(excludedKeyID)
			t.Cleanup(func() { registry.Forget(excludedKeyID) })
			registry.RecordFailure(excludedKeyID, "401", "authentication_failed", time.Now())
			// The wording must not classify an infrastructure failure as candidate exhaustion.
			failureErr := errors.New("no available " + failure + " connection")
			listCalls := 0
			hydratedIDs := []int64{}
			repo := &geminiModelsErrorAccountRepo{
				mockAccountRepoForGemini: &mockAccountRepoForGemini{listByGroupFunc: func(context.Context, int64, []string) ([]Account, error) {
					listCalls++
					if failure == "query" && listCalls == 2 {
						return nil, failureErr
					}
					return []Account{excluded, healthy}, nil
				}},
				getByID: func(_ context.Context, id int64) (*Account, error) {
					hydratedIDs = append(hydratedIDs, id)
					if id == healthy.ID {
						return nil, failureErr
					}
					return &excluded, nil
				},
			}
			svc := &GeminiMessagesCompatService{accountRepo: repo}
			if failure == "hydration" {
				svc.schedulerSnapshot = NewSchedulerSnapshotService(nil, nil, repo, nil, nil)
			}
			ctx := context.WithValue(context.Background(), ctxkey.ForcePlatform, PlatformGemini)
			selected, err := svc.SelectAccountForModelsEndpoints(ctx, &groupID)
			require.ErrorIs(t, err, failureErr)
			require.Nil(t, selected)
			require.Equal(t, 2, listCalls, "health discovery and the first selection must not be followed by an unfiltered retry")
			require.NotContains(t, hydratedIDs, excluded.ID)
			if failure == "hydration" {
				require.Equal(t, []int64{healthy.ID}, hydratedIDs)
			}
		})
	}
}

func TestForwardListModels_VertexServiceAccountReturnsAIStudioFormat(t *testing.T) {
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		vertexModelsJSONResponse(http.StatusOK, `{"publisherModels":[
			{"name":"publishers/google/models/gemini-3.8-flash"},
			{"name":"publishers/google/models/gemma4"}
		],"nextPageToken":"page-2"}`),
		vertexModelsJSONResponse(http.StatusOK, `{"publisherModels":[{"name":"publishers/google/models/gemini-2.5-flash"}]}`),
	}}
	svc := newGeminiModelsEndpointsTestService(nil, upstream)

	res, err := svc.ForwardListModels(context.Background(), vertexModelSyncTestAccount(map[string]any{"location": "europe-west4"}))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.StatusCode)
	require.Equal(t, []string{"models/gemini-2.5-flash", "models/gemini-3.8-flash"}, decodeGeminiModelNames(t, res.Body))

	require.Len(t, upstream.requests, 2)
	require.Equal(t, "https://europe-west4-aiplatform.googleapis.com/v1beta1/publishers/google/models?pageSize=300", upstream.requests[0].URL.String())
	require.Equal(t, "https://europe-west4-aiplatform.googleapis.com/v1beta1/publishers/google/models?pageSize=300&pageToken=page-2", upstream.requests[1].URL.String())
	for _, req := range upstream.requests {
		require.Equal(t, "Bearer ya29.vertex-token", req.Header.Get("Authorization"))
	}
}

func TestForwardListModels_VertexUpstreamErrorPassesThrough(t *testing.T) {
	body := `{"error":{"code":403,"status":"PERMISSION_DENIED"}}`
	upstream := &httpUpstreamRecorder{resp: vertexModelsJSONResponse(http.StatusForbidden, body)}
	svc := newGeminiModelsEndpointsTestService(nil, upstream)

	res, err := svc.ForwardListModels(context.Background(), vertexModelSyncTestAccount(map[string]any{"location": "global"}))
	require.NoError(t, err)
	require.Equal(t, http.StatusForbidden, res.StatusCode)
	require.JSONEq(t, body, string(res.Body))
}

func TestForwardListModels_VertexAPIKeyReturnsBuiltinList(t *testing.T) {
	upstream := &httpUpstreamRecorder{}
	svc := newGeminiModelsEndpointsTestService(nil, upstream)
	account := &Account{ID: 174, Platform: PlatformGemini, Type: AccountTypeServiceAccount, Credentials: map[string]any{
		"auth_mode": "apikey",
		"api_key":   "AQ.test-key",
	}}

	res, err := svc.ForwardListModels(context.Background(), account)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.StatusCode)
	expected, err := json.Marshal(gemini.FallbackModelsList())
	require.NoError(t, err)
	require.JSONEq(t, string(expected), string(res.Body))
	require.Empty(t, upstream.requests)
}

func TestForwardListModels_APIKeyForwardsToBaseURL(t *testing.T) {
	upstream := &httpUpstreamRecorder{resp: vertexModelsJSONResponse(http.StatusOK, `{"models":[{"name":"models/gemini-native"}]}`)}
	svc := newGeminiModelsEndpointsTestService(nil, upstream)
	account := &Account{ID: 261, Platform: PlatformGemini, Type: AccountTypeAPIKey, Credentials: map[string]any{
		"api_key":  "sk-pool",
		"base_url": "https://relay.example.com",
	}}

	res, err := svc.ForwardListModels(context.Background(), account)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.StatusCode)
	require.JSONEq(t, `{"models":[{"name":"models/gemini-native"}]}`, string(res.Body))
	require.Equal(t, "https://relay.example.com/v1beta/models", upstream.lastReq.URL.String())
	require.Equal(t, "sk-pool", upstream.lastReq.Header.Get("x-goog-api-key"))
}

func TestForwardGetModel_VertexServiceAccountLooksUpModelList(t *testing.T) {
	listBody := `{"publisherModels":[{"name":"publishers/google/models/gemini-3.8-flash"}]}`

	t.Run("listed model", func(t *testing.T) {
		upstream := &httpUpstreamRecorder{resp: vertexModelsJSONResponse(http.StatusOK, listBody)}
		svc := newGeminiModelsEndpointsTestService(nil, upstream)

		res, err := svc.ForwardGetModel(context.Background(), vertexModelSyncTestAccount(nil), "gemini-3.8-flash")
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, res.StatusCode)
		var model gemini.Model
		require.NoError(t, json.Unmarshal(res.Body, &model))
		require.Equal(t, "models/gemini-3.8-flash", model.Name)
	})

	t.Run("unlisted model", func(t *testing.T) {
		upstream := &httpUpstreamRecorder{resp: vertexModelsJSONResponse(http.StatusOK, listBody)}
		svc := newGeminiModelsEndpointsTestService(nil, upstream)

		res, err := svc.ForwardGetModel(context.Background(), vertexModelSyncTestAccount(nil), "gemini-unknown")
		require.NoError(t, err)
		require.Equal(t, http.StatusNotFound, res.StatusCode)
		require.JSONEq(t, `{"error":{"code":404,"message":"models/gemini-unknown is not found","status":"NOT_FOUND"}}`, string(res.Body))
	})
}

func TestForwardGetModel_VertexUsesModelLocation(t *testing.T) {
	upstream := &httpUpstreamRecorder{resp: vertexModelsJSONResponse(http.StatusOK, `{"publisherModels":[{"name":"publishers/google/models/gemini-3.8-flash"}]}`)}
	svc := newGeminiModelsEndpointsTestService(nil, upstream)
	account := vertexModelSyncTestAccount(map[string]any{
		"location":               "europe-west4",
		"vertex_model_locations": map[string]any{"gemini-3.8-flash": "global"},
	})

	res, err := svc.ForwardGetModel(context.Background(), account, "gemini-3.8-flash")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.StatusCode)
	require.Equal(t, "https://aiplatform.googleapis.com/v1beta1/publishers/google/models?pageSize=300", upstream.lastReq.URL.String())
}

func TestForwardListModels_VertexMidPaginationErrorPassesThrough(t *testing.T) {
	body := `{"error":{"code":503,"status":"UNAVAILABLE"}}`
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		vertexModelsJSONResponse(http.StatusOK, `{"publisherModels":[{"name":"publishers/google/models/gemini-3.8-flash"}],"nextPageToken":"page-2"}`),
		vertexModelsJSONResponse(http.StatusServiceUnavailable, body),
	}}
	svc := newGeminiModelsEndpointsTestService(nil, upstream)

	res, err := svc.ForwardListModels(context.Background(), vertexModelSyncTestAccount(nil))
	require.NoError(t, err)
	require.Equal(t, http.StatusServiceUnavailable, res.StatusCode)
	require.JSONEq(t, body, string(res.Body))
	require.Len(t, upstream.requests, 2)
}

func TestForwardListModels_ResponseOverReadLimitFails(t *testing.T) {
	upstream := &httpUpstreamRecorder{resp: vertexModelsJSONResponse(http.StatusOK, `{"models":[{"name":"models/gemini-native"}]}`)}
	svc := newGeminiModelsEndpointsTestService(nil, upstream)
	svc.cfg.Gateway.ModelsListReadMaxBytes = 16
	account := &Account{ID: 261, Platform: PlatformGemini, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "sk-pool"}}

	_, err := svc.ForwardListModels(context.Background(), account)
	require.ErrorContains(t, err, "exceeds 16 bytes")
}
