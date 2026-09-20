package service

import (
	"context"
	"errors"
	"testing"

	pluginv1 "github.com/Wei-Shaw/sub2api/pkg/pluginapi/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type runtimeAuthorityRepository struct {
	PluginRepository
	installation *PluginInstallation
	err          error
	reads        int
	onRead       func()
}

func (r *runtimeAuthorityRepository) GetByID(context.Context, int64) (*PluginInstallation, error) {
	r.reads++
	if r.onRead != nil {
		r.onRead()
	}
	return r.installation, r.err
}

type runtimeAuthorityHost struct {
	pluginv1.UnimplementedHostServiceServer
	writes, reads, releases, identities int
}

func (s *runtimeAuthorityHost) StateCompareAndSwap(context.Context, *pluginv1.StateCompareAndSwapRequest) (*pluginv1.StateCompareAndSwapResponse, error) {
	s.writes++
	return &pluginv1.StateCompareAndSwapResponse{}, nil
}
func (s *runtimeAuthorityHost) AcquireLease(context.Context, *pluginv1.LeaseRequest) (*pluginv1.LeaseResponse, error) {
	s.writes++
	return &pluginv1.LeaseResponse{}, nil
}
func (s *runtimeAuthorityHost) RenewLease(context.Context, *pluginv1.LeaseRequest) (*pluginv1.LeaseResponse, error) {
	s.writes++
	return &pluginv1.LeaseResponse{}, nil
}
func (s *runtimeAuthorityHost) StateGet(context.Context, *pluginv1.StateGetRequest) (*pluginv1.StateGetResponse, error) {
	s.reads++
	return &pluginv1.StateGetResponse{}, nil
}
func (s *runtimeAuthorityHost) ReleaseLease(context.Context, *pluginv1.LeaseRequest) (*pluginv1.LeaseResponse, error) {
	s.releases++
	return &pluginv1.LeaseResponse{}, nil
}

func (s *runtimeAuthorityHost) ResolveOutboundIdentity(context.Context, *pluginv1.ResolveOutboundIdentityRequest) (*pluginv1.ResolveOutboundIdentityResponse, error) {
	s.identities++
	return &pluginv1.ResolveOutboundIdentityResponse{}, nil
}

func runtimeAuthorityFixture() (*pluginRequestHostServiceServer, *runtimeAuthorityRepository, *runtimeAuthorityHost) {
	installation := &PluginInstallation{ID: 7, PluginKey: "test.authority", BinarySHA256: "fixed", ConfigRevision: 1, State: PluginStateEnabled}
	installation.Manifest.Requires = PluginRequirements{HostServiceAPI: 2, HostFeatures: []string{"scoped-routing.v1"}}
	installation.Bindings = []PluginBinding{{Capability: PluginCapabilityOpenAIOAuthOutbound, Platform: PlatformOpenAI, AccountType: AccountTypeOAuth, Enabled: true, RolloutPercent: 100}}
	runtime := &pluginRuntime{installation: installation}
	runtime.scopedConfig.Store(&pluginScopedConfig{revision: 4, active: true})
	current := *installation
	current.ConfigRevision = 4
	repo := &runtimeAuthorityRepository{installation: &current}
	host := &runtimeAuthorityHost{}
	return &pluginRequestHostServiceServer{HostServiceServer: host, runtime: runtime, repository: repo}, repo, host
}

func runtimeAuthorityCalls(host *pluginRequestHostServiceServer) []func() error {
	return []func() error{
		func() error {
			_, err := host.StateCompareAndSwap(context.Background(), &pluginv1.StateCompareAndSwapRequest{})
			return err
		},
		func() error { _, err := host.AcquireLease(context.Background(), &pluginv1.LeaseRequest{}); return err },
		func() error { _, err := host.RenewLease(context.Background(), &pluginv1.LeaseRequest{}); return err },
		func() error {
			_, err := host.ResolveOutboundIdentity(context.Background(), &pluginv1.ResolveOutboundIdentityRequest{AccountId: 42})
			return err
		},
	}
}

func TestPluginRuntimeAuthorityUsesAppliedRevisionAndAllowsMaintenanceReceipts(t *testing.T) {
	for _, state := range []string{PluginStateEnabled, PluginStateUpgrading} {
		t.Run(state, func(t *testing.T) {
			host, repo, delegate := runtimeAuthorityFixture()
			repo.installation.State = state
			host.runtime.scopedConfig.Store(&pluginScopedConfig{revision: 4, active: false})
			host.runtime.draining.Store(true)
			for _, call := range runtimeAuthorityCalls(host) {
				require.NoError(t, call())
			}
			require.Equal(t, 4, repo.reads, "authority is re-read for every protected operation")
			require.Equal(t, 3, delegate.writes)
			require.Equal(t, 1, delegate.identities)
			require.Equal(t, uint64(1), host.runtime.installation.ConfigRevision)
		})
	}
}

func TestPluginRuntimeAuthorityRejectsStaleOrUnavailableRuntime(t *testing.T) {
	for _, scenario := range []string{"sha", "revision", "disabled", "starting", "error", "missing", "storage-error", "no-snapshot", "exited", "no-repository", "apply-raced", "no-binding", "disabled-binding", "maintenance-disabled-binding"} {
		t.Run(scenario, func(t *testing.T) {
			host, repo, delegate := runtimeAuthorityFixture()
			want := codes.FailedPrecondition
			switch scenario {
			case "sha":
				repo.installation.BinarySHA256 = "replacement"
			case "revision":
				repo.installation.ConfigRevision++
			case "disabled", "starting", "error":
				repo.installation.State = scenario
			case "missing":
				repo.installation = nil
			case "storage-error":
				repo.err = errors.New("postgres://user:secret@database")
				want = codes.Unavailable
			case "no-snapshot":
				host.runtime.scopedConfig.Store(nil)
			case "exited":
				host.runtime.exited.Store(true)
			case "no-repository":
				host.repository = nil
				want = codes.Unavailable
			case "apply-raced":
				repo.onRead = func() { host.runtime.scopedConfig.Store(&pluginScopedConfig{revision: 5, active: true}) }
			case "no-binding":
				repo.installation.Bindings = nil
			case "disabled-binding":
				repo.installation.Bindings[0].Enabled = false
			case "maintenance-disabled-binding":
				repo.installation.State = PluginStateUpgrading
				repo.installation.Bindings[0].Enabled = false
			}
			for _, call := range runtimeAuthorityCalls(host) {
				err := call()
				require.Equal(t, want, status.Code(err))
				require.NotContains(t, err.Error(), "secret")
			}
			require.Zero(t, delegate.writes)
			require.Zero(t, delegate.identities)
		})
	}
}

func TestPluginRuntimeAuthorityEnableCommitAllowsRetryWithoutReapply(t *testing.T) {
	host, repo, delegate := runtimeAuthorityFixture()
	applied := host.runtime.scopedConfig.Load()
	repo.installation.State = PluginStateStarting
	repo.installation.Bindings[0].Enabled = false
	for _, call := range runtimeAuthorityCalls(host) {
		require.Equal(t, codes.FailedPrecondition, status.Code(call()))
	}
	repo.installation.State = PluginStateEnabled
	for _, call := range runtimeAuthorityCalls(host) {
		require.Equal(t, codes.FailedPrecondition, status.Code(call()))
	}
	repo.installation.Bindings[0].Enabled = true
	for _, call := range runtimeAuthorityCalls(host) {
		require.NoError(t, call())
	}
	require.Same(t, applied, host.runtime.scopedConfig.Load())
	require.Equal(t, 3, delegate.writes)
	require.Equal(t, 1, delegate.identities)
}

func TestPluginRuntimeAuthorityUpgradeCandidateCannotWriteBeforeCommit(t *testing.T) {
	host, repo, delegate := runtimeAuthorityFixture()
	repo.installation.State = PluginStateUpgrading
	host.runtime.staged.Store(true)
	for _, call := range runtimeAuthorityCalls(host) {
		require.Equal(t, codes.FailedPrecondition, status.Code(call()))
	}
	require.Zero(t, delegate.writes)
	require.Zero(t, delegate.identities)
	repo.installation.State = PluginStateEnabled
	host.runtime.staged.Store(false)
	for _, call := range runtimeAuthorityCalls(host) {
		require.NoError(t, call())
	}
	require.Equal(t, 3, delegate.writes)
	require.Equal(t, 1, delegate.identities)
}

func TestPluginRuntimeAuthorityPreservesLegacyIdentityDirectory(t *testing.T) {
	host, repo, delegate := runtimeAuthorityFixture()
	host.runtime.installation.Manifest = PluginManifest{}
	host.runtime.scopedConfig.Store(nil)
	repo.err = errors.New("legacy directory does not require scoped authority")
	_, err := host.ResolveOutboundIdentity(context.Background(), &pluginv1.ResolveOutboundIdentityRequest{AccountId: 42})
	require.NoError(t, err)
	require.Zero(t, repo.reads)
	require.Equal(t, 1, delegate.identities)
}

func TestPluginRuntimeAuthorityDoesNotBlockReadReleaseOrCompletion(t *testing.T) {
	host, repo, delegate := runtimeAuthorityFixture()
	repo.err = errors.New("storage unavailable")
	require.True(t, host.runtime.beginRequest())
	cleaned := false
	require.NoError(t, host.runtime.registerRequestCompletion("receipt", func() { cleaned = true }))
	_, err := host.StateGet(context.Background(), &pluginv1.StateGetRequest{})
	require.NoError(t, err)
	_, err = host.ReleaseLease(context.Background(), &pluginv1.LeaseRequest{})
	require.NoError(t, err)
	_, err = host.CompleteRequest(context.Background(), &pluginv1.CompleteRequestRequest{RequestId: "receipt"})
	require.NoError(t, err)
	require.True(t, cleaned)
	require.Zero(t, repo.reads)
	require.Equal(t, 1, delegate.reads)
	require.Equal(t, 1, delegate.releases)
}
