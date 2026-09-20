package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	pluginv1 "github.com/Wei-Shaw/sub2api/pkg/pluginapi/v1"
	hcplugin "github.com/hashicorp/go-plugin"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

type modelsDiscoveryPluginRepo struct {
	PluginRepository
	installation *PluginInstallation
	err          error
}

func (r *modelsDiscoveryPluginRepo) GetByID(context.Context, int64) (*PluginInstallation, error) {
	return r.installation, r.err
}

func (r *modelsDiscoveryPluginRepo) List(context.Context) ([]*PluginInstallation, error) {
	return []*PluginInstallation{r.installation}, r.err
}

type modelsDiscoveryPluginClient struct {
	pluginv1.TransportPluginClient
	admissions int
	forwards   int
}

func (c *modelsDiscoveryPluginClient) AdmitBatch(context.Context, *pluginv1.AdmitBatchRequest, ...grpc.CallOption) (*pluginv1.AdmitBatchResponse, error) {
	c.admissions++
	return nil, errors.New("catalog must not use model admission")
}

func (c *modelsDiscoveryPluginClient) Forward(context.Context, ...grpc.CallOption) (pluginv1.TransportPlugin_ForwardClient, error) {
	c.forwards++
	return nil, errors.New("legacy catalog transport reached")
}

func newModelsDiscoveryPluginFixture(scoped bool) (*OpenAIGatewayService, *Account, *modelsDiscoveryPluginRepo, *modelsDiscoveryPluginClient) {
	account := &Account{ID: 42, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	installation := &PluginInstallation{
		ID: 7, State: PluginStateEnabled, ConfigRevision: 3, BinarySHA256: "catalog-test",
		Bindings: []PluginBinding{{Capability: PluginCapabilityOpenAIOAuthOutbound, Platform: PlatformOpenAI, AccountType: AccountTypeOAuth, Enabled: true, RolloutPercent: 100}},
	}
	if scoped {
		installation.ManagedScope = []PluginManagedTarget{{AccountID: account.ID, Models: []string{"managed-model"}}}
	}
	repo := &modelsDiscoveryPluginRepo{installation: installation}
	client := &modelsDiscoveryPluginClient{}
	runtime := &pluginRuntime{installation: installation, api: client, client: hcplugin.NewClient(&hcplugin.ClientConfig{})}
	manager := &PluginManager{repo: repo}
	manager.route.Store(scopedPluginRoute(installation, runtime, ""))
	return &OpenAIGatewayService{pluginManager: manager}, account, repo, client
}

func TestOpenAIModelsDiscoveryScopedCatalogUsesOrdinaryHTTP(t *testing.T) {
	for _, mode := range []string{"online", "offline", "storage-error", "revision", "fresh-instance"} {
		t.Run(mode, func(t *testing.T) {
			svc, account, repo, client := newModelsDiscoveryPluginFixture(true)
			switch mode {
			case "offline":
				svc.pluginManager.route.Load().runtime = nil
			case "storage-error":
				repo.err = errors.New("storage unavailable")
			case "revision":
				repo.installation.ConfigRevision++
			case "fresh-instance":
				svc.pluginManager.route.Store(nil)
			}
			seen := make(chan *http.Request, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				seen <- req.Clone(context.Background())
				w.Header().Set("ETag", `"catalog-current"`)
				_, _ = io.WriteString(w, `{"models":[{"slug":"managed-model"}]}`)
			}))
			defer server.Close()
			original := chatgptCodexModelsURL
			chatgptCodexModelsURL = server.URL + "/backend-api/codex/models"
			t.Cleanup(func() { chatgptCodexModelsURL = original })
			response, err := svc.fetchOpenAIModelsUpstream(context.Background(), openAIModelsRequest{
				url: chatgptCodexModelsURL + "?client_version=fixture", credentialAccount: account,
				headers: http.Header{"Authorization": {"Bearer catalog-fixture-token"}, "Chatgpt-Account-Id": {"catalog-account"}},
			}, `"catalog-old"`)
			require.NoError(t, err)
			require.JSONEq(t, `{"models":[{"slug":"managed-model"}]}`, string(response.Body))
			require.Equal(t, `"catalog-current"`, response.ETag)
			select {
			case req := <-seen:
				require.Equal(t, http.MethodGet, req.Method)
				require.Equal(t, "/backend-api/codex/models", req.URL.Path)
				require.Equal(t, "fixture", req.URL.Query().Get("client_version"))
				require.Equal(t, "Bearer catalog-fixture-token", req.Header.Get("Authorization"))
				require.Equal(t, "catalog-account", req.Header.Get("Chatgpt-Account-Id"))
				require.Equal(t, `"catalog-old"`, req.Header.Get("If-None-Match"))
				require.Empty(t, req.Header.Get("x-codex-turn-state"))
			default:
				t.Fatal("catalog request did not reach ordinary HTTP transport")
			}
			require.Zero(t, client.admissions)
			require.Zero(t, client.forwards)
		})
	}
}

func TestOpenAIModelsDiscoveryPreservesLegacyTransport(t *testing.T) {
	for _, online := range []bool{true, false} {
		svc, account, _, client := newModelsDiscoveryPluginFixture(false)
		if !online {
			svc.pluginManager.route.Load().runtime = nil
			svc.pluginManager.route.Load().unavailable = "legacy-runtime-offline"
		}
		response, err := svc.fetchOpenAIModelsUpstream(context.Background(), openAIModelsRequest{
			url: chatgptCodexModelsURL, credentialAccount: account,
		}, "")
		require.Nil(t, response)
		require.Error(t, err)
		if online {
			require.Equal(t, 1, client.forwards)
		} else {
			require.Zero(t, client.forwards)
			require.ErrorContains(t, err, "legacy-runtime-offline")
		}
		require.Zero(t, client.admissions)
	}
}

func TestOpenAIModelsDiscoveryMarkerCannotAuthorizeBusinessRequests(t *testing.T) {
	for _, mode := range []string{"unmarked-catalog", "unmarked-generation", "forged-header", "changed-method", "changed-path", "changed-host", "host-header", "changed-query", "body", "content-length", "transfer-encoding", "clone", "changed-account", "changed-identity", "wrong-endpoint-at-mark", "generation-metadata"} {
		t.Run(mode, func(t *testing.T) {
			svc, account, _, client := newModelsDiscoveryPluginFixture(true)
			req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, chatgptCodexModelsURL, nil)
			require.NoError(t, err)
			if mode == "wrong-endpoint-at-mark" {
				req.URL.Path = "/backend-api/codex/responses"
			}
			if mode != "unmarked-catalog" && mode != "unmarked-generation" && mode != "forged-header" {
				markOpenAIModelsDiscoveryRequest(req, account)
			}
			switch mode {
			case "unmarked-generation":
				req.Method = http.MethodPost
				req.URL.Path = "/backend-api/codex/responses"
			case "forged-header":
				req.Header.Set("X-Plugin-Request-Purpose", "models_discovery")
			case "changed-method":
				req.Method = http.MethodPost
			case "changed-path":
				req.URL.Path = "/backend-api/codex/responses"
			case "changed-host":
				req.URL.Host = "other.invalid"
			case "host-header":
				req.Host = "other.invalid"
			case "changed-query":
				req.URL.RawQuery = "model=managed-model"
			case "body":
				req.Body = io.NopCloser(strings.NewReader(`{"model":"managed-model"}`))
			case "content-length":
				req.ContentLength = 1
			case "transfer-encoding":
				req.TransferEncoding = []string{"chunked"}
			case "clone":
				req = req.Clone(req.Context())
			case "changed-account":
				account.ID++
			case "changed-identity":
				account.Credentials = map[string]any{"chatgpt_account_id": "changed"}
			case "generation-metadata":
				require.NoError(t, svc.preparePluginRequest(req.Context(), account, "managed-model", req))
			}
			response, handled, err := svc.roundTripOpenAIModelsDiscovery(req, "", account)
			require.Nil(t, response)
			require.True(t, handled)
			var denial *PluginAdmissionError
			require.ErrorAs(t, err, &denial)
			require.Zero(t, client.admissions)
			require.Zero(t, client.forwards)
		})
	}
}

func TestOpenAIModelsDiscoveryDoesNotChangeOrdinaryScopedAdmission(t *testing.T) {
	svc, account, _, client := newModelsDiscoveryPluginFixture(true)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, chatgptCodexModelsURL, nil)
	require.NoError(t, err)
	markOpenAIModelsDiscoveryRequest(req, account)
	response, handled, err := svc.pluginManager.RoundTripOpenAIOAuth(req.Context(), req, "", account)
	require.Nil(t, response)
	require.True(t, handled)
	var denial *PluginAdmissionError
	require.ErrorAs(t, err, &denial)
	require.Zero(t, client.admissions)
	require.Zero(t, client.forwards)
}

func TestOpenAIModelsDiscoveryCancellationAndUnknownRouteFailClosed(t *testing.T) {
	for _, cancelled := range []bool{true, false} {
		svc, account, repo, client := newModelsDiscoveryPluginFixture(true)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		if cancelled {
			cancel()
		} else {
			svc.pluginManager.route.Store(nil)
			repo.err = errors.New("unknown plugin mode")
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, chatgptCodexModelsURL, nil)
		require.NoError(t, err)
		markOpenAIModelsDiscoveryRequest(req, account)
		response, handled, err := svc.roundTripOpenAIModelsDiscovery(req, "", account)
		require.Nil(t, response)
		require.True(t, handled)
		if cancelled {
			require.ErrorIs(t, err, context.Canceled)
		} else {
			var denial *PluginAdmissionError
			require.ErrorAs(t, err, &denial)
		}
		require.Zero(t, client.admissions)
		require.Zero(t, client.forwards)
	}
}
