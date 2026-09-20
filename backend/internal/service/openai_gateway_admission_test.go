package service

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	pluginv1 "github.com/Wei-Shaw/sub2api/pkg/pluginapi/v1"
	hcplugin "github.com/hashicorp/go-plugin"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

type schedulingAdmissionRepo struct {
	PluginRepository
	PluginRequestGuardRepository
	installation *PluginInstallation
	reads        int
	err          error
	guardBegins  int
}

func (r *schedulingAdmissionRepo) GetByID(context.Context, int64) (*PluginInstallation, error) {
	r.reads++
	return r.installation, r.err
}

func (r *schedulingAdmissionRepo) BeginPluginRequest(context.Context, int64, string, uint64, string) error {
	r.guardBegins++
	return errors.New("unexpected dispatch after admission rejection")
}

type schedulingAdmissionClient struct {
	pluginv1.TransportPluginClient
	requests []*pluginv1.AdmitBatchRequest
	denied   map[int64]bool
	respond  func(context.Context, *pluginv1.AdmitBatchRequest) (*pluginv1.AdmitBatchResponse, error)
}

func (c *schedulingAdmissionClient) AdmitBatch(ctx context.Context, req *pluginv1.AdmitBatchRequest, _ ...grpc.CallOption) (*pluginv1.AdmitBatchResponse, error) {
	c.requests = append(c.requests, req)
	if c.respond != nil {
		return c.respond(ctx, req)
	}
	response := schedulingAdmissionResponse(req)
	for _, decision := range response.Decisions {
		decision.Allowed = !c.denied[decision.AccountId]
	}
	return response, nil
}

func schedulingAdmissionResponse(req *pluginv1.AdmitBatchRequest) *pluginv1.AdmitBatchResponse {
	response := &pluginv1.AdmitBatchResponse{ConfigRevision: req.ConfigRevision}
	for _, candidate := range req.Candidates {
		response.Decisions = append(response.Decisions, &pluginv1.AdmissionDecision{
			AccountId: candidate.AccountId, OutboundModel: candidate.OutboundModel,
			IdentityRevision: candidate.IdentityRevision, Allowed: true,
		})
	}
	return response
}

func newSchedulingAdmissionFixture() (*OpenAIGatewayService, *schedulingAdmissionRepo, *schedulingAdmissionClient, []Account) {
	accounts := []Account{
		{ID: 42, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1},
		{ID: 43, Platform: PlatformOpenAI, Type: AccountTypeSetupToken, Status: StatusActive, Schedulable: true, Concurrency: 1},
	}
	installation := &PluginInstallation{
		ID: 7, State: PluginStateEnabled, ConfigRevision: 2, BinarySHA256: "fixed",
		Manifest:     PluginManifest{Requires: PluginRequirements{HostServiceAPI: 2, HostFeatures: []string{"scoped-routing.v1", "admission.v1", "oauth-like.v1"}}},
		ManagedScope: []PluginManagedTarget{{AccountID: 42, Models: []string{"gpt-5.4"}}, {AccountID: 43, Models: []string{"gpt-5.4"}}},
		Bindings:     []PluginBinding{{Capability: PluginCapabilityOpenAIOAuthOutbound, Platform: PlatformOpenAI, AccountType: AccountTypeOAuth, Enabled: true, RolloutPercent: 100}},
	}
	repo := &schedulingAdmissionRepo{installation: installation}
	client := &schedulingAdmissionClient{denied: make(map[int64]bool)}
	runtime := &pluginRuntime{installation: installation, api: client, client: &hcplugin.Client{}}
	manager := &PluginManager{repo: repo}
	manager.route.Store(scopedPluginRoute(installation, runtime, ""))
	return &OpenAIGatewayService{
		pluginManager: manager, cfg: &config.Config{},
		accountRepo:        schedulerTestOpenAIAccountRepo{accounts: accounts},
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
	}, repo, client, accounts
}

func TestOpenAISchedulingAdmissionBatchesAndReusesOnlyWithinPass(t *testing.T) {
	svc, repo, client, accounts := newSchedulingAdmissionFixture()
	client.denied[42] = true
	accounts = append(accounts, accounts[0], Account{ID: 99, Platform: PlatformOpenAI, Type: AccountTypeOAuth})
	ctx := svc.prefetchOpenAISchedulingAdmission(context.Background(), accounts, "gpt-5.1", false)
	for repeat := 0; repeat < 3; repeat++ {
		require.True(t, svc.isOpenAIAccountRequestRuntimeBlockedWithContext(ctx, &accounts[0], "gpt-5.1", false))
		require.False(t, svc.isOpenAIAccountRequestRuntimeBlockedWithContext(ctx, &accounts[1], "gpt-5.1", false))
		require.False(t, svc.isOpenAIAccountRequestRuntimeBlockedWithContext(ctx, &accounts[3], "gpt-5.1", false))
	}
	require.Len(t, client.requests, 1)
	require.Len(t, client.requests[0].Candidates, 2)
	require.Equal(t, "gpt-5.4", client.requests[0].Candidates[0].OutboundModel, "client aliases must be normalized before admission")
	require.Equal(t, 1, repo.reads)
	client.denied[43] = true
	next := svc.prefetchOpenAISchedulingAdmission(context.Background(), accounts, "gpt-5.1", false)
	require.True(t, svc.isOpenAIAccountRequestRuntimeBlockedWithContext(next, &accounts[1], "gpt-5.1", false))
	require.Len(t, client.requests, 2)
	changed := accounts[1]
	changed.Credentials = map[string]any{"chatgpt_account_id": "changed-identity"}
	require.True(t, svc.isOpenAIAccountRequestRuntimeBlockedWithContext(ctx, &changed, "gpt-5.1", false))
	require.Len(t, client.requests, 3)
	require.Equal(t, PluginAccountIdentityRevision(&changed), client.requests[2].Candidates[0].IdentityRevision)
}

func TestOpenAISchedulingAdmissionUsesPassCompactMapping(t *testing.T) {
	svc, repo, client, accounts := newSchedulingAdmissionFixture()
	accounts[0].Credentials = map[string]any{
		"model_mapping":         map[string]any{"alias": "normal-outbound"},
		"compact_model_mapping": map[string]any{"alias": "gpt-5.4"},
	}
	ctx := svc.prefetchOpenAISchedulingAdmission(context.Background(), accounts[:1], "alias", true)
	require.Len(t, client.requests, 1)
	require.Equal(t, "gpt-5.4", client.requests[0].Candidates[0].OutboundModel)
	require.False(t, svc.isOpenAIAccountRequestRuntimeBlockedWithContext(ctx, &accounts[0], "alias", false))
	require.Equal(t, 1, repo.reads, "capability deferral must not re-admit the non-compact model")
	accounts[0].Credentials = map[string]any{"compact_model_mapping": map[string]any{"alias": "changed-model"}}
	require.False(t, svc.isOpenAIAccountRequestRuntimeBlockedWithContext(ctx, &accounts[0], "alias", false))
	require.Equal(t, 2, repo.reads, "changed outbound model must not reuse another model's decision")
}

func TestOpenAISchedulingAdmissionRejectsMalformedBatch(t *testing.T) {
	for _, mode := range []string{"revision", "missing", "extra", "duplicate", "identity", "model", "account", "nil-decision", "nil-response", "rpc-error"} {
		t.Run(mode, func(t *testing.T) {
			svc, _, client, accounts := newSchedulingAdmissionFixture()
			client.respond = func(_ context.Context, req *pluginv1.AdmitBatchRequest) (*pluginv1.AdmitBatchResponse, error) {
				response := schedulingAdmissionResponse(req)
				switch mode {
				case "revision":
					response.ConfigRevision++
				case "missing":
					response.Decisions = response.Decisions[:1]
				case "extra":
					response.Decisions = append(response.Decisions, response.Decisions[0])
				case "duplicate":
					response.Decisions[1] = response.Decisions[0]
				case "identity":
					response.Decisions[1].IdentityRevision = "incorrect"
				case "model":
					response.Decisions[1].OutboundModel = "incorrect"
				case "account":
					response.Decisions[1].AccountId = 99
				case "nil-decision":
					response.Decisions[1] = nil
				case "nil-response":
					return nil, nil
				case "rpc-error":
					return nil, errors.New("unavailable")
				}
				return response, nil
			}
			accounts = append(accounts, Account{ID: 99, Platform: PlatformOpenAI, Type: AccountTypeOAuth})
			ctx := svc.prefetchOpenAISchedulingAdmission(context.Background(), accounts, "gpt-5.1", false)
			require.True(t, svc.isOpenAIAccountRequestRuntimeBlockedWithContext(ctx, &accounts[0], "gpt-5.1", false))
			require.True(t, svc.isOpenAIAccountRequestRuntimeBlockedWithContext(ctx, &accounts[1], "gpt-5.1", false))
			require.False(t, svc.isOpenAIAccountRequestRuntimeBlockedWithContext(ctx, &accounts[2], "gpt-5.1", false))
			require.Len(t, client.requests, 1)
		})
	}
}

func TestOpenAISchedulingAdmissionPropagatesCancellationAndAcceptsReorderedDecisions(t *testing.T) {
	svc, _, client, accounts := newSchedulingAdmissionFixture()
	type requestKey struct{}
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), requestKey{}, "request-value"))
	defer cancel()
	client.respond = func(checkCtx context.Context, req *pluginv1.AdmitBatchRequest) (*pluginv1.AdmitBatchResponse, error) {
		require.Equal(t, "request-value", checkCtx.Value(requestKey{}))
		deadline, ok := checkCtx.Deadline()
		require.True(t, ok)
		require.LessOrEqual(t, time.Until(deadline), 3*time.Second)
		response := schedulingAdmissionResponse(req)
		response.Decisions[0], response.Decisions[1] = response.Decisions[1], response.Decisions[0]
		return response, nil
	}
	pass := svc.prefetchOpenAISchedulingAdmission(ctx, accounts, "gpt-5.1", false)
	for i := range accounts {
		require.False(t, svc.isOpenAIAccountRequestRuntimeBlockedWithContext(pass, &accounts[i], "gpt-5.1", false))
	}
	client.respond = func(checkCtx context.Context, req *pluginv1.AdmitBatchRequest) (*pluginv1.AdmitBatchResponse, error) {
		cancel()
		<-checkCtx.Done()
		return schedulingAdmissionResponse(req), nil
	}
	pass = svc.prefetchOpenAISchedulingAdmission(ctx, accounts, "gpt-5.1", false)
	for i := range accounts {
		require.True(t, svc.isOpenAIAccountRequestRuntimeBlockedWithContext(pass, &accounts[i], "gpt-5.1", false))
	}
	require.Len(t, client.requests, 2)
	pass = svc.prefetchOpenAISchedulingAdmission(ctx, accounts, "gpt-5.1", false)
	require.True(t, svc.isOpenAIAccountRequestRuntimeBlockedWithContext(pass, &accounts[0], "gpt-5.1", false))
	require.Len(t, client.requests, 2, "cancelled requests must not begin another RPC")
}

func TestOpenAISchedulingAdmissionSkipsAuxiliaryAndNonOAuthAccounts(t *testing.T) {
	svc, repo, client, accounts := newSchedulingAdmissionFixture()
	repo.err = errors.New("storage unavailable")
	ctx := svc.prefetchOpenAISchedulingAdmission(context.Background(), accounts, "", false)
	require.False(t, svc.isOpenAIAccountRequestRuntimeBlockedWithContext(ctx, &accounts[0], "", false))
	accounts[0].Type = AccountTypeAPIKey
	parentID := int64(99)
	accounts[1].ParentAccountID = &parentID
	ctx = svc.prefetchOpenAISchedulingAdmission(context.Background(), accounts, "gpt-5.1", false)
	for i := range accounts {
		require.False(t, svc.isOpenAIAccountRequestRuntimeBlockedWithContext(ctx, &accounts[i], "gpt-5.1", false))
	}
	require.Zero(t, repo.reads)
	require.Empty(t, client.requests)
}

func TestOpenAISchedulingAdmissionSendRechecksAfterCachedAllow(t *testing.T) {
	svc, repo, client, accounts := newSchedulingAdmissionFixture()
	ctx := svc.prefetchOpenAISchedulingAdmission(context.Background(), accounts, "gpt-5.1", false)
	require.False(t, svc.isOpenAIAccountRequestRuntimeBlockedWithContext(ctx, &accounts[0], "gpt-5.1", false))
	client.denied[accounts[0].ID] = true
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://example.invalid/v1/responses", nil)
	require.NoError(t, err)
	require.NoError(t, svc.preparePluginRequest(ctx, &accounts[0], "gpt-5.4", req))
	response, handled, err := svc.pluginManager.roundTripScoped(req.Context(), req, "", &accounts[0])
	require.Nil(t, response)
	require.True(t, handled)
	var denied *PluginAdmissionError
	require.ErrorAs(t, err, &denied)
	require.Len(t, client.requests, 2)
	require.Len(t, client.requests[0].Candidates, 2)
	require.Len(t, client.requests[1].Candidates, 1, "send must perform independent authoritative admission")
	require.Zero(t, repo.guardBegins, "admission denial must occur before any dispatch guard begins")
}

func TestOpenAISchedulingAdmissionSendRechecksNewManagedScope(t *testing.T) {
	svc, repo, client, _ := newSchedulingAdmissionFixture()
	account := Account{ID: 99, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	ctx := svc.prefetchOpenAISchedulingAdmission(context.Background(), []Account{account}, "gpt-5.1", false)
	require.False(t, svc.isOpenAIAccountRequestRuntimeBlockedWithContext(ctx, &account, "gpt-5.1", false))
	repo.installation.ManagedScope = append(repo.installation.ManagedScope, PluginManagedTarget{AccountID: 99, Models: []string{"gpt-5.4"}})
	repo.installation.ConfigRevision++
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://example.invalid/v1/responses", nil)
	require.NoError(t, err)
	require.NoError(t, svc.preparePluginRequest(ctx, &account, "gpt-5.4", req))
	response, handled, err := svc.pluginManager.roundTripScoped(req.Context(), req, "", &account)
	require.Nil(t, response)
	require.True(t, handled)
	var denied *PluginAdmissionError
	require.ErrorAs(t, err, &denied)
	require.Empty(t, client.requests, "an unsynchronized new scope must fail before dispatch")
	require.Zero(t, repo.guardBegins)
}

func TestOpenAISchedulingAdmissionUnavailableScopeIsolated(t *testing.T) {
	for _, mode := range []string{"offline", "storage", "revision", "draining"} {
		t.Run(mode, func(t *testing.T) {
			svc, repo, client, accounts := newSchedulingAdmissionFixture()
			switch mode {
			case "offline":
				svc.pluginManager.route.Load().runtime = nil
			case "storage":
				repo.err = errors.New("storage unavailable")
			case "revision":
				repo.installation.ConfigRevision++
			case "draining":
				svc.pluginManager.route.Load().runtime.draining.Store(true)
			}
			accounts = append(accounts, Account{ID: 99, Platform: PlatformOpenAI, Type: AccountTypeOAuth})
			ctx := svc.prefetchOpenAISchedulingAdmission(context.Background(), accounts, "gpt-5.1", false)
			require.True(t, svc.isOpenAIAccountRequestRuntimeBlockedWithContext(ctx, &accounts[0], "gpt-5.1", false))
			require.True(t, svc.isOpenAIAccountRequestRuntimeBlockedWithContext(ctx, &accounts[1], "gpt-5.1", false))
			require.False(t, svc.isOpenAIAccountRequestRuntimeBlockedWithContext(ctx, &accounts[2], "gpt-5.1", false))
			require.Equal(t, 1, repo.reads)
			require.Empty(t, client.requests)
		})
	}
}

func TestOpenAISchedulingAdmissionCandidatePoolsUseOneBatch(t *testing.T) {
	for _, mode := range []string{"legacy", "load-batch", "advanced", "weighted-sticky"} {
		t.Run(mode, func(t *testing.T) {
			svc, repo, client, accounts := newSchedulingAdmissionFixture()
			client.denied[accounts[0].ID] = true
			req := OpenAIAccountScheduleRequest{Platform: PlatformOpenAI, RequestedModel: "gpt-5.1", RequiredTransport: OpenAIUpstreamTransportAny}
			scheduler := &defaultOpenAIAccountScheduler{service: svc, stats: newOpenAIAccountRuntimeStats()}
			var selection *AccountSelectionResult
			var err error
			switch mode {
			case "legacy":
				var account *Account
				account, err = svc.selectAccountForModelWithExclusions(context.Background(), nil, PlatformOpenAI, "", "gpt-5.1", nil, false, 0, "", false)
				selection = &AccountSelectionResult{Account: account}
			case "load-batch":
				svc.cfg.Gateway.Scheduling.LoadBatchEnabled = true
				selection, err = svc.selectAccountWithLoadAwareness(context.Background(), nil, PlatformOpenAI, "", "gpt-5.1", nil, false, "", false)
			case "advanced":
				selection, _, _, _, _, _, err = scheduler.selectByLoadBalance(context.Background(), req)
			case "weighted-sticky":
				req.StickyWeighted = true
				req.StickyPreviousAccountID, req.StickyAccountID = accounts[0].ID, accounts[1].ID
				selection, err = scheduler.tryFallbackToWeightedSticky(context.Background(), req)
			}
			require.NoError(t, err)
			require.NotNil(t, selection)
			if selection.ReleaseFunc != nil {
				defer selection.ReleaseFunc()
			}
			require.NotNil(t, selection.Account)
			require.Equal(t, accounts[1].ID, selection.Account.ID)
			require.Len(t, client.requests, 1)
			require.Len(t, client.requests[0].Candidates, 2)
			require.Equal(t, 1, repo.reads, "candidate filtering and DB rechecks must share the batch result")
		})
	}
}
