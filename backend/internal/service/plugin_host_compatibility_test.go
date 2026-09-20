package service

import (
	"context"
	"strconv"
	"testing"
	"time"

	pluginv1 "github.com/Wei-Shaw/sub2api/pkg/pluginapi/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestPluginHostCompatibilityLegacyAndDeclaredRequirements(t *testing.T) {
	for _, tc := range []struct {
		name       string
		version    int
		features   []string
		compatible bool
	}{
		{name: "legacy omitted", compatible: true},
		{name: "legacy API1", version: 1, compatible: true},
		{name: "current all features", version: pluginv1.HostServiceAPIVersion, features: append([]string(nil), pluginv1.HostFeatures...), compatible: true},
		{name: "future API", version: pluginv1.HostServiceAPIVersion + 1},
		{name: "negative API", version: -1},
		{name: "missing feature", version: pluginv1.HostServiceAPIVersion, features: []string{"future.required.feature.v99"}},
		{name: "partially supported", version: pluginv1.HostServiceAPIVersion, features: []string{"resources.v1", "future.required.feature.v99"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manifest := testPluginManifest(nil)
			manifest.Requires.HostServiceAPI = tc.version
			manifest.Requires.HostFeatures = tc.features
			err := manifest.Validate()
			compatibility := EvaluatePluginCompatibility(manifest, PluginHostInfo{Version: "0.1.179"})
			require.Equal(t, tc.compatible, compatibility.Compatible)
			if tc.compatible {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				require.Equal(t, "incompatible", compatibility.Status)
			}
		})
	}
}

func TestPluginHostCompatibilitySetupTokenRequiresOAuthLikeFeature(t *testing.T) {
	manifest := testPluginManifest(nil)
	manifest.Requires.HostServiceAPI = pluginv1.HostServiceAPIVersion
	manifest.Capabilities[0].AccountType = AccountTypeSetupToken
	require.Error(t, manifest.Validate())
	manifest.Requires.HostFeatures = []string{"oauth-like.v1"}
	require.NoError(t, manifest.Validate())
}

func TestPluginHostCompatibilityCapabilityGrants(t *testing.T) {
	for _, tc := range []struct {
		name, platform, accountType string
		oauthLike, allowed          bool
	}{
		{name: "legacy OAuth", platform: PlatformOpenAI, accountType: AccountTypeOAuth, allowed: true},
		{name: "legacy setup rejected", platform: PlatformOpenAI, accountType: AccountTypeSetupToken},
		{name: "declared OAuth-like setup", platform: PlatformOpenAI, accountType: AccountTypeSetupToken, oauthLike: true, allowed: true},
		{name: "declared OAuth-like OAuth", platform: PlatformOpenAI, accountType: AccountTypeOAuth, oauthLike: true, allowed: true},
		{name: "API key rejected", platform: PlatformOpenAI, accountType: AccountTypeAPIKey, oauthLike: true},
		{name: "other platform rejected", platform: PlatformAnthropic, accountType: AccountTypeOAuth, oauthLike: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manifest := testPluginManifest(nil)
			manifest.Capabilities[0].Platform = tc.platform
			manifest.Capabilities[0].AccountType = tc.accountType
			if tc.oauthLike {
				manifest.Requires.HostServiceAPI = pluginv1.HostServiceAPIVersion
				manifest.Requires.HostFeatures = []string{"oauth-like.v1"}
			}
			if tc.allowed {
				require.NoError(t, manifest.Validate())
			} else {
				require.Error(t, manifest.Validate())
			}
			require.Equal(t, tc.allowed, pluginDeclaresOpenAIOAuthCapability(manifest))
			manager := &PluginManager{kvStore: newFakePluginKVStore(), accountDirectory: &fakeAccountDirectory{}}
			server, ok := manager.buildHostServices(&PluginInstallation{PluginKey: "test.capability", Manifest: manifest}).(*pluginHostServiceServer)
			require.True(t, ok)
			require.Equal(t, tc.allowed, server.directory != nil)
			require.Equal(t, tc.oauthLike, server.allowSetupToken)
		})
	}
}

// Emulate an already shipped plugin which rejects every version except API 1.
type pluginStrictLegacyHostReceiver struct {
	pluginv1.UnimplementedTransportPluginServer
	versions chan uint32
}

func (p *pluginStrictLegacyHostReceiver) InitHostServices(_ context.Context, request *pluginv1.InitHostServicesRequest) (*pluginv1.InitHostServicesResponse, error) {
	p.versions <- request.HostServiceApiVersion
	if request.HostServiceApiVersion != 1 {
		return nil, status.Error(codes.FailedPrecondition, "legacy plugin only supports host API 1")
	}
	return &pluginv1.InitHostServicesResponse{Ready: true}, nil
}

func TestPluginHostCompatibilityNegotiatesAPI1WithLegacyPlugin(t *testing.T) {
	for _, declared := range []int{0, 1} {
		t.Run(strconv.Itoa(declared), func(t *testing.T) {
			plugin := &pluginStrictLegacyHostReceiver{versions: make(chan uint32, 1)}
			client := dispenseTransportClient(t, plugin)
			installation := &PluginInstallation{PluginKey: "test.legacy", Manifest: PluginManifest{Requires: PluginRequirements{HostServiceAPI: declared}}}
			host := newPluginHostServiceServer(installation.PluginKey, newFakePluginKVStore(), nil)
			require.NoError(t, offerPluginHostServices(context.Background(), installation, client.TransportPluginClient, client.Broker, host, 5*time.Second))
			select {
			case version := <-plugin.versions:
				require.Equal(t, uint32(1), version)
			default:
				t.Fatal("legacy plugin did not receive host service negotiation")
			}
		})
	}
}

type pluginRequiredHostReceiver struct {
	pluginv1.UnimplementedTransportPluginServer
	ready    bool
	err      error
	requests chan *pluginv1.InitHostServicesRequest
}

func (p *pluginRequiredHostReceiver) InitHostServices(_ context.Context, request *pluginv1.InitHostServicesRequest) (*pluginv1.InitHostServicesResponse, error) {
	p.requests <- request
	return &pluginv1.InitHostServicesResponse{Ready: p.ready, Message: "http://user:secret-password@proxy"}, p.err
}

func TestPluginHostCompatibilityRequiredHandshakeFailsClosed(t *testing.T) {
	installation := &PluginInstallation{PluginKey: "test.required", Manifest: PluginManifest{Requires: PluginRequirements{
		HostServiceAPI: pluginv1.HostServiceAPIVersion, HostFeatures: []string{"resources.v1", "state-cas.v1"},
	}}}
	ctx := context.Background()
	require.Error(t, offerPluginHostServices(ctx, installation, nil, nil, nil, time.Second))
	require.NoError(t, offerPluginHostServices(ctx, &PluginInstallation{PluginKey: "test.legacy"}, nil, nil, nil, time.Second))
	for _, tc := range []struct {
		name     string
		ready    bool
		rpcError error
	}{
		{name: "accepted", ready: true},
		{name: "not ready"},
		{name: "unimplemented", rpcError: status.Error(codes.Unimplemented, "secret-token")},
		{name: "unavailable", rpcError: status.Error(codes.Unavailable, "http://user:secret-password@proxy")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plugin := &pluginRequiredHostReceiver{ready: tc.ready, err: tc.rpcError, requests: make(chan *pluginv1.InitHostServicesRequest, 1)}
			client := dispenseTransportClient(t, plugin)
			host := newPluginHostServiceServer(installation.PluginKey, newFakePluginKVStore(), nil)
			err := offerPluginHostServices(ctx, installation, client.TransportPluginClient, client.Broker, host, 5*time.Second)
			if tc.ready {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				require.NotContains(t, err.Error(), "secret")
			}
			select {
			case request := <-plugin.requests:
				require.Equal(t, uint32(pluginv1.HostServiceAPIVersion), request.HostServiceApiVersion)
				for _, feature := range installation.Manifest.Requires.HostFeatures {
					require.Contains(t, request.HostFeatures, feature)
				}
			default:
				t.Fatal("plugin did not receive host service negotiation")
			}
		})
	}
}
