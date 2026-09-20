package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type unavailableScopedGatewayPluginRepo struct {
	PluginRepository
	installation atomic.Pointer[PluginInstallation]
}

func (r *unavailableScopedGatewayPluginRepo) GetByID(_ context.Context, id int64) (*PluginInstallation, error) {
	installation := r.installation.Load()
	if installation == nil || installation.ID != id {
		return nil, errors.New("plugin installation not found")
	}
	return installation, nil
}

func (r *unavailableScopedGatewayPluginRepo) List(context.Context) ([]*PluginInstallation, error) {
	if installation := r.installation.Load(); installation != nil {
		return []*PluginInstallation{installation}, nil
	}
	return nil, nil
}

func (r *unavailableScopedGatewayPluginRepo) setBindingEnabled(enabled bool) {
	updated := *r.installation.Load()
	updated.Bindings = append([]PluginBinding(nil), updated.Bindings...)
	updated.Bindings[0].Enabled = enabled
	r.installation.Store(&updated)
}

func newUnavailableScopedGatewayPlugin(accountID int64, model string) *PluginManager {
	installation := &PluginInstallation{
		ID: 1, State: PluginStateEnabled, ConfigRevision: 1,
		ManagedScope: []PluginManagedTarget{{AccountID: accountID, Models: []string{model}}},
		Bindings: []PluginBinding{{Capability: PluginCapabilityOpenAIOAuthOutbound, Platform: PlatformOpenAI,
			AccountType: AccountTypeOAuth, Enabled: true, RolloutPercent: 100}},
	}
	repo := &unavailableScopedGatewayPluginRepo{}
	repo.installation.Store(installation)
	manager := &PluginManager{repo: repo}
	manager.route.Store(scopedPluginRoute(installation, nil, "runtime unavailable"))
	return manager
}

func TestUnavailableScopedGatewayPluginRepoListsDisabledBindings(t *testing.T) {
	manager := newUnavailableScopedGatewayPlugin(41, "managed-model")
	repo := manager.repo.(*unavailableScopedGatewayPluginRepo)
	repo.setBindingEnabled(false)
	installations, err := repo.List(context.Background())
	require.NoError(t, err)
	require.Len(t, installations, 1)
	require.False(t, installations[0].Bindings[0].Enabled)
	route, err := manager.currentScopedRoute(context.Background())
	require.NoError(t, err)
	require.Nil(t, route, "the real routing code, not the fake repository, must filter disabled bindings")
	manager.route.Store(nil)
	repo.setBindingEnabled(true)
	route, err = manager.currentScopedRoute(context.Background())
	require.NoError(t, err)
	require.NotNil(t, route)
	require.True(t, pluginScopeContains(route.scope, 41, "managed-model"))
}

func TestExtractOpenAIOutboundModel(t *testing.T) {
	for _, tt := range []struct{ body, model string }{
		{`{"model":" actual-outbound "}`, "actual-outbound"},
		{`{"input":[],"model":"actual-outbound"}`, "actual-outbound"},
		{`{}`, ""}, {`invalid`, ""}, {`{"model":null}`, ""},
	} {
		require.Equal(t, tt.model, extractOpenAIOutboundModel([]byte(tt.body)))
	}
}

func TestOpenAIAccountOutboundModelPreservesMappingOrder(t *testing.T) {
	svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{OpenAICompactModel: "fallback"}}}
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{
		"model_mapping":         map[string]any{"channel-alias": "actual-outbound", "fallback": "mapped-fallback"},
		"compact_model_mapping": map[string]any{"compact-alias": "account-compact"},
	}}
	require.Equal(t, "actual-outbound", svc.openAIAccountOutboundModel(account, " channel-alias ", false))
	require.Equal(t, "mapped-fallback", svc.openAIAccountOutboundModel(account, "channel-alias", true))
	require.Equal(t, "account-compact", svc.openAIAccountOutboundModel(account, "compact-alias", true))
	require.Equal(t, "", svc.openAIAccountOutboundModel(account, " ", true))
	require.Equal(t, "alias", svc.openAIAccountOutboundModel(nil, " alias ", true))
}

func TestOpenAIAccountRuntimePluginGateUsesOutboundModel(t *testing.T) {
	account := &Account{ID: 41, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{
		"model_mapping": map[string]any{"client-alias": "managed-model"},
	}}
	svc := &OpenAIGatewayService{
		cfg:           &config.Config{Gateway: config.GatewayConfig{OpenAICompactModel: "unmanaged-compact"}},
		pluginManager: newUnavailableScopedGatewayPlugin(account.ID, "managed-model"),
	}
	require.True(t, svc.isOpenAIAccountRequestRuntimeBlocked(account, "client-alias", false))
	require.False(t, svc.isOpenAIAccountRequestRuntimeBlocked(account, "client-alias", true))
	require.False(t, svc.isOpenAIAccountRequestRuntimeBlocked(account, "unmanaged", false))
	other := *account
	other.ID++
	require.False(t, svc.isOpenAIAccountRequestRuntimeBlocked(&other, "client-alias", false))
	other = *account
	other.Type = AccountTypeAPIKey
	require.False(t, svc.isOpenAIAccountRequestRuntimeBlocked(&other, "client-alias", false))
}

func TestOpenAIRequestBuildersCarryOutboundMetadata(t *testing.T) {
	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	account := &Account{ID: 41, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "token", "chatgpt_account_id": "account"},
	}
	ctx := context.Background()
	body := []byte(`{"model":"actual-outbound","input":[]}`)
	builders := map[string]func(*gin.Context) (*http.Request, error){
		"responses": func(c *gin.Context) (*http.Request, error) {
			return svc.buildUpstreamRequest(ctx, c, account, body, "token", true, "", true)
		},
		"passthrough": func(c *gin.Context) (*http.Request, error) {
			return svc.buildUpstreamRequestOpenAIPassthrough(ctx, c, account, body, "token")
		},
		"input_tokens": func(c *gin.Context) (*http.Request, error) {
			return svc.buildInputTokensUpstreamRequest(ctx, c, account, body, "token")
		},
		"native_anthropic": func(c *gin.Context) (*http.Request, error) {
			req, _, err := svc.buildNativeAnthropicUpstreamRequest(ctx, c, account, body, "key", "https://example.com/v1/messages")
			return req, err
		},
	}
	for name, build := range builders {
		t.Run(name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			req, err := build(c)
			require.NoError(t, err)
			metadata, ok := req.Context().Value(pluginRequestMetadataKey{}).(pluginRequestMetadata)
			require.True(t, ok)
			require.Equal(t, "actual-outbound", metadata.Model)
			require.Equal(t, PluginAccountIdentityRevision(account), metadata.IdentityRevision)
		})
	}
}
