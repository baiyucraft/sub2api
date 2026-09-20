package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	pluginv1 "github.com/Wei-Shaw/sub2api/pkg/pluginapi/v1"
	hcplugin "github.com/hashicorp/go-plugin"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type pluginActionRepo struct {
	PluginRepository
	installation *PluginInstallation
	err          error
	calls        int
}

func (r *pluginActionRepo) GetByID(context.Context, int64) (*PluginInstallation, error) {
	r.calls++
	return r.installation, r.err
}

type pluginActionClient struct {
	pluginv1.TransportPluginClient
	call func(context.Context, *pluginv1.RunActionRequest) (*pluginv1.RunActionResponse, error)
}

func (c *pluginActionClient) RunAction(ctx context.Context, request *pluginv1.RunActionRequest, _ ...grpc.CallOption) (*pluginv1.RunActionResponse, error) {
	return c.call(ctx, request)
}

func newPluginActionTestManager(call func(context.Context, *pluginv1.RunActionRequest) (*pluginv1.RunActionResponse, error)) *PluginManager {
	installation := &PluginInstallation{ID: 7, State: PluginStateEnabled, ConfigRevision: 12, BinarySHA256: "current"}
	running := *installation
	manager := &PluginManager{repo: &pluginActionRepo{installation: installation}, runtimes: map[int64]*pluginRuntime{
		7: {installation: &running, client: hcplugin.NewClient(&hcplugin.ClientConfig{}), api: &pluginActionClient{call: call}},
	}}
	manager.route.Store(&pluginRoute{pluginID: 7, runtime: manager.runtimes[7], configRevision: installation.ConfigRevision})
	return manager
}

func validPluginActionRequest() *pluginv1.RunActionRequest {
	return &pluginv1.RunActionRequest{ActionId: "action-123", Name: "refresh.account", PayloadJson: []byte(`{"account_id":7}`), ConfigRevision: 999}
}

func TestPluginManagerRunActionUsesInstalledRevisionAndJSONObject(t *testing.T) {
	calls := 0
	manager := newPluginActionTestManager(func(ctx context.Context, request *pluginv1.RunActionRequest) (*pluginv1.RunActionResponse, error) {
		calls++
		require.Equal(t, uint64(12), request.ConfigRevision)
		require.Equal(t, "action-123", request.ActionId)
		require.JSONEq(t, `{"account_id":7}`, string(request.PayloadJson))
		deadline, ok := ctx.Deadline()
		require.True(t, ok)
		require.LessOrEqual(t, time.Until(deadline), pluginActionTimeout)
		request.PayloadJson[0] = ' '
		return &pluginv1.RunActionResponse{ActionId: request.ActionId, Accepted: true, Status: "queued", ResultJson: []byte(`{"job_id":"job-1"}`)}, nil
	})
	request := validPluginActionRequest()
	manager.runtimes[7].installation.ConfigRevision = 1
	for range 2 {
		result, err := manager.RunAction(context.Background(), 7, request)
		require.NoError(t, err)
		raw, err := json.Marshal(result)
		require.NoError(t, err)
		require.JSONEq(t, `{"action_id":"action-123","accepted":true,"status":"queued","result":{"job_id":"job-1"}}`, string(raw))
	}
	require.Equal(t, 2, calls, "the engine owns idempotency")
	require.Equal(t, uint64(999), request.ConfigRevision)
	require.JSONEq(t, `{"account_id":7}`, string(request.PayloadJson))
	require.Zero(t, manager.runtimes[7].inFlight.Load())
}

func TestPluginManagerRunActionRejectsInvalidRequestsBeforeRepositoryRead(t *testing.T) {
	for name, request := range map[string]*pluginv1.RunActionRequest{
		"nil":          nil,
		"missing id":   {Name: "refresh", PayloadJson: []byte(`{}`)},
		"long id":      {ActionId: strings.Repeat("a", 129), Name: "refresh", PayloadJson: []byte(`{}`)},
		"invalid name": {ActionId: "id", Name: "refresh\nsecret", PayloadJson: []byte(`{}`)},
		"long name":    {ActionId: "id", Name: strings.Repeat("a", 129), PayloadJson: []byte(`{}`)},
		"invalid JSON": {ActionId: "id", Name: "refresh", PayloadJson: []byte(`{`)},
		"null":         {ActionId: "id", Name: "refresh", PayloadJson: []byte(`null`)},
		"array":        {ActionId: "id", Name: "refresh", PayloadJson: []byte(`[]`)},
		"oversized":    {ActionId: "id", Name: "refresh", PayloadJson: []byte(`{"data":"` + strings.Repeat("a", PluginActionMaxPayloadBytes) + `"}`)},
	} {
		t.Run(name, func(t *testing.T) {
			repo := &pluginActionRepo{}
			manager := &PluginManager{repo: repo}
			_, err := manager.RunAction(context.Background(), 7, request)
			require.Equal(t, http.StatusBadRequest, infraerrors.Code(err))
			require.Zero(t, repo.calls)
		})
	}
}

func TestPluginManagerRunActionRequiresEnabledRunningCurrentRuntime(t *testing.T) {
	for _, name := range []string{"disabled", "absent", "draining", "stale revision", "stale binary", "no client", "absent route", "replaced runtime"} {
		t.Run(name, func(t *testing.T) {
			manager := newPluginActionTestManager(func(context.Context, *pluginv1.RunActionRequest) (*pluginv1.RunActionResponse, error) {
				t.Fatal("action dispatched to an unavailable runtime")
				return nil, nil
			})
			switch name {
			case "disabled":
				manager.repo.(*pluginActionRepo).installation.State = PluginStateDisabled
			case "absent":
				delete(manager.runtimes, 7)
			case "draining":
				manager.runtimes[7].draining.Store(true)
			case "stale revision":
				manager.route.Store(&pluginRoute{pluginID: 7, runtime: manager.runtimes[7], configRevision: 10})
			case "stale binary":
				manager.runtimes[7].installation.BinarySHA256 = "old"
			case "no client":
				manager.runtimes[7].client = nil
			case "absent route":
				manager.route.Store(nil)
			case "replaced runtime":
				manager.route.Store(&pluginRoute{pluginID: 7, runtime: &pluginRuntime{}, configRevision: 12})
			}
			_, err := manager.RunAction(context.Background(), 7, validPluginActionRequest())
			require.Equal(t, http.StatusConflict, infraerrors.Code(err))
		})
	}
}

func TestPluginManagerRunActionCancellationAndErrorRedaction(t *testing.T) {
	manager := newPluginActionTestManager(func(ctx context.Context, _ *pluginv1.RunActionRequest) (*pluginv1.RunActionResponse, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err := manager.RunAction(ctx, 7, validPluginActionRequest())
	require.Equal(t, http.StatusGatewayTimeout, infraerrors.Code(err))
	require.Zero(t, manager.runtimes[7].inFlight.Load())
	manager = newPluginActionTestManager(func(context.Context, *pluginv1.RunActionRequest) (*pluginv1.RunActionResponse, error) {
		return nil, status.Error(codes.Internal, "http://user:secret-password@proxy token=secret-token")
	})
	_, err = manager.RunAction(context.Background(), 7, validPluginActionRequest())
	require.Equal(t, http.StatusServiceUnavailable, infraerrors.Code(err))
	require.NotContains(t, err.Error(), "secret")
}

func TestPluginManagerRunActionDoesNotWaitForLifecycleMutation(t *testing.T) {
	manager := newPluginActionTestManager(func(context.Context, *pluginv1.RunActionRequest) (*pluginv1.RunActionResponse, error) {
		t.Fatal("action dispatched during lifecycle mutation")
		return nil, nil
	})
	manager.operationMu.Lock()
	defer manager.operationMu.Unlock()
	_, err := manager.RunAction(context.Background(), 7, validPluginActionRequest())
	require.Equal(t, http.StatusConflict, infraerrors.Code(err))
	require.Zero(t, manager.repo.(*pluginActionRepo).calls)
}

func TestPluginManagerRunActionRejectsInvalidResults(t *testing.T) {
	for name, result := range map[string]*pluginv1.RunActionResponse{
		"nil":           nil,
		"wrong id":      {ActionId: "other", Status: "queued", ResultJson: []byte(`{}`)},
		"invalid JSON":  {ActionId: "action-123", Status: "queued", ResultJson: []byte(`{"secret-token":`)},
		"array":         {ActionId: "action-123", Status: "queued", ResultJson: []byte(`[]`)},
		"unsafe status": {ActionId: "action-123", Status: "http://user:secret-password@proxy", ResultJson: []byte(`{}`)},
		"oversized":     {ActionId: "action-123", Status: "queued", ResultJson: []byte(strings.Repeat("x", pluginActionMaxResultBytes+1))},
	} {
		t.Run(name, func(t *testing.T) {
			manager := newPluginActionTestManager(func(context.Context, *pluginv1.RunActionRequest) (*pluginv1.RunActionResponse, error) {
				return result, nil
			})
			_, err := manager.RunAction(context.Background(), 7, validPluginActionRequest())
			require.Equal(t, http.StatusBadGateway, infraerrors.Code(err))
			require.NotContains(t, err.Error(), "secret")
		})
	}
}

func TestPluginManagerResourcesReadsDisabledInstallationWithoutRuntime(t *testing.T) {
	manager := &PluginManager{repo: &pluginActionRepo{installation: &PluginInstallation{ID: 7, State: PluginStateDisabled}},
		accountDirectory: &OpenAIGatewayService{accountRepo: &pluginResourceAccountRepo{accounts: []Account{pluginTestAccount(1, AccountTypeSetupToken)}}}}
	resources, err := manager.Resources(context.Background(), 7)
	require.NoError(t, err)
	require.Len(t, resources.Accounts, 1)
	require.Nil(t, manager.runtimes)
	manager.repo.(*pluginActionRepo).err = sql.ErrNoRows
	_, err = manager.Resources(context.Background(), 7)
	require.Equal(t, http.StatusNotFound, infraerrors.Code(err))
	manager.repo.(*pluginActionRepo).err = errors.New("secret-proxy-password")
	_, err = manager.Resources(context.Background(), 7)
	require.NotContains(t, err.Error(), "secret")
}
