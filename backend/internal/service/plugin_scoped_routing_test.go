package service

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	pluginv1 "github.com/Wei-Shaw/sub2api/pkg/pluginapi/v1"
	hcplugin "github.com/hashicorp/go-plugin"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

type scopedAdmissionClient struct {
	pluginv1.TransportPluginClient
	response *pluginv1.AdmitBatchResponse
	calls    int
}

func (a *scopedAdmissionClient) AdmitBatch(_ context.Context, r *pluginv1.AdmitBatchRequest, _ ...grpc.CallOption) (*pluginv1.AdmitBatchResponse, error) {
	a.calls++
	if a.response != nil {
		return a.response, nil
	}
	return &pluginv1.AdmitBatchResponse{ConfigRevision: r.ConfigRevision, Decisions: []*pluginv1.AdmissionDecision{{AccountId: r.Candidates[0].AccountId, OutboundModel: r.Candidates[0].OutboundModel, IdentityRevision: r.Candidates[0].IdentityRevision, Allowed: true}}}, nil
}

func scopedAdmissionFixture() (*PluginManager, *pluginTokenRepository, *scopedAdmissionClient) {
	installation := &PluginInstallation{ID: 7, State: PluginStateEnabled, ConfigRevision: 2, BinarySHA256: "fixed", Manifest: PluginManifest{Requires: PluginRequirements{HostServiceAPI: 2, HostFeatures: []string{"scoped-routing.v1", "admission.v1", "oauth-like.v1"}}}, ManagedScope: []PluginManagedTarget{{AccountID: 42, Models: []string{"model-a"}}}, Bindings: []PluginBinding{{Capability: PluginCapabilityOpenAIOAuthOutbound, Platform: PlatformOpenAI, AccountType: AccountTypeOAuth, Enabled: true, RolloutPercent: 100}}}
	repo := &pluginTokenRepository{installation: installation}
	client := &scopedAdmissionClient{}
	runtime := &pluginRuntime{installation: installation, api: client, client: &hcplugin.Client{}}
	m := &PluginManager{repo: repo}
	m.route.Store(scopedPluginRoute(installation, runtime, ""))
	return m, repo, client
}

func TestPluginScopedAdmissionIsolatesModelsAndAccounts(t *testing.T) {
	m, _, client := scopedAdmissionFixture()
	account := &Account{ID: 42, Platform: PlatformOpenAI, Type: AccountTypeSetupToken}
	require.True(t, m.AdmitOpenAIAccount(context.Background(), account, "model-a"))
	require.Equal(t, 1, client.calls)
	client.response = &pluginv1.AdmitBatchResponse{ConfigRevision: 1}
	require.False(t, m.AdmitOpenAIAccount(context.Background(), account, "model-a"))
	require.True(t, m.AdmitOpenAIAccount(context.Background(), account, "model-b"))
	other := *account
	other.ID = 43
	require.True(t, m.AdmitOpenAIAccount(context.Background(), &other, "model-a"))
	require.Equal(t, 2, client.calls)
}

func TestPluginScopedAdmissionPersistedDisableAndMaintenance(t *testing.T) {
	m, repo, _ := scopedAdmissionFixture()
	account := &Account{ID: 42, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	for _, state := range []string{PluginStateStarting, PluginStateUpgrading, PluginStateError} {
		repo.installation.State = state
		require.False(t, m.AdmitOpenAIAccount(context.Background(), account, "model-a"), state)
		require.True(t, m.AdmitOpenAIAccount(context.Background(), account, "unmanaged"), state)
	}
	repo.installation.Bindings[0].Enabled = false
	require.True(t, m.AdmitOpenAIAccount(context.Background(), account, "model-a"))
}

func TestPluginScopedAdmissionFreshInstanceCannotBypassPersistedScope(t *testing.T) {
	m, repo, _ := scopedAdmissionFixture()
	m.route.Store(nil)
	account := &Account{ID: 42, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	require.False(t, m.AdmitOpenAIAccount(context.Background(), account, "model-a"))
	require.True(t, m.AdmitOpenAIAccount(context.Background(), account, "model-b"))
	repo.installation.ManagedScope = append(repo.installation.ManagedScope, PluginManagedTarget{AccountID: 99, Models: []string{"model-b"}})
	repo.installation.ConfigRevision++
	account.ID = 99
	require.False(t, m.AdmitOpenAIAccount(context.Background(), account, "model-b"))
}

func TestPluginScopedTransportAllowsUnmanagedWithoutOnlineRuntime(t *testing.T) {
	m, _, _ := scopedAdmissionFixture()
	m.route.Load().runtime = nil
	account := &Account{ID: 42, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	svc := &OpenAIGatewayService{pluginManager: m}
	for _, model := range []string{"model-a", "model-b"} {
		req, _ := http.NewRequest(http.MethodPost, "https://example.invalid", nil)
		require.NoError(t, svc.preparePluginRequest(context.Background(), account, model, req))
		response, handled, err := m.RoundTripOpenAIOAuth(req.Context(), req, "", account)
		require.Nil(t, response)
		if model == "model-a" {
			require.True(t, handled)
			var denial *PluginAdmissionError
			require.ErrorAs(t, err, &denial)
		} else {
			require.False(t, handled)
			require.NoError(t, err)
		}
	}
}

func TestNormalizePluginScopeRejectsAmbiguousTargets(t *testing.T) {
	_, err := normalizePluginScope([]*pluginv1.ManagedTarget{{AccountId: 0, Models: []string{"x"}}})
	require.Error(t, err)
	scope, err := normalizePluginScope([]*pluginv1.ManagedTarget{{AccountId: 2, Models: []string{"b", "a", "b"}}, {AccountId: 2, Models: []string{"a"}}})
	require.NoError(t, err)
	require.Equal(t, []PluginManagedTarget{{AccountID: 2, Models: []string{"a", "b"}}}, scope)
	_, err = normalizePluginScope([]*pluginv1.ManagedTarget{{AccountId: 1, Models: []string{" x"}}})
	require.Error(t, err)
}

type drainGuardRepo struct {
	PluginRequestGuardRepository
	count int64
	err   error
}

func (r drainGuardRepo) PluginRequestsInFlight(context.Context, int64) (int64, error) {
	return r.count, r.err
}
func TestPluginMaintenanceDrainCannotAssumeMissingRequestsFinished(t *testing.T) {
	require.NoError(t, waitPluginRequestsDrained(context.Background(), drainGuardRepo{}, 1))
	require.Error(t, waitPluginRequestsDrained(context.Background(), drainGuardRepo{err: errors.New("storage unavailable")}, 1))
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	require.ErrorContains(t, waitPluginRequestsDrained(ctx, drainGuardRepo{count: 1}, 1), "incomplete")
}

func TestPluginAdmissionErrorDoesNotReportUpstreamAccountFailure(t *testing.T) {
	svc := &OpenAIGatewayService{}
	err := svc.handleOpenAIUpstreamTransportError(context.Background(), nil, &Account{ID: 42}, &PluginAdmissionError{AccountID: 42, Model: "model-a"}, false)
	var failover *UpstreamFailoverError
	require.ErrorAs(t, err, &failover)
	require.True(t, failover.PluginAdmissionRejected)
	require.False(t, failover.ShouldReportAccountScheduleFailure())
	require.False(t, failover.RetryableOnSameAccount)
}
