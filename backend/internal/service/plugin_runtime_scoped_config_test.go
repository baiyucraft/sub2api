package service

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pluginv1 "github.com/Wei-Shaw/sub2api/pkg/pluginapi/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

type scopedConfigSnapshotClient struct {
	pluginv1.TransportPluginClient
	apply func(context.Context, *pluginv1.ApplyConfigRequest) (*pluginv1.ApplyConfigResponse, error)
}

func (c *scopedConfigSnapshotClient) ApplyConfig(ctx context.Context, request *pluginv1.ApplyConfigRequest, _ ...grpc.CallOption) (*pluginv1.ApplyConfigResponse, error) {
	return c.apply(ctx, request)
}

func TestPluginScopedConfigSnapshotPauseUsesLastAppliedConfig(t *testing.T) {
	var requests []*pluginv1.ApplyConfigRequest
	client := &scopedConfigSnapshotClient{apply: func(_ context.Context, req *pluginv1.ApplyConfigRequest) (*pluginv1.ApplyConfigResponse, error) {
		requests = append(requests, req)
		return &pluginv1.ApplyConfigResponse{Applied: true}, nil
	}}
	runtime := &pluginRuntime{api: client, installation: &PluginInstallation{ConfigRevision: 1}}
	raw := []byte(`{"version":2}`)
	require.NoError(t, runtime.applyAndRememberScopedConfig(context.Background(), raw, 2, true))
	raw[0] = 'x'
	requests[0].ConfigJson[0] = 'y'
	require.NoError(t, runtime.pauseScopedConfig(context.Background()))
	require.Len(t, requests, 2)
	require.Equal(t, uint64(2), requests[1].ConfigRevision)
	require.Equal(t, `{"version":2}`, string(requests[1].ConfigJson))
	require.False(t, requests[1].RuntimeActive)
	require.Equal(t, uint64(1), runtime.installation.ConfigRevision)
	require.False(t, runtime.scopedConfig.Load().active)
	require.NoError(t, runtime.pauseScopedConfig(context.Background()))
	require.Len(t, requests, 2, "already paused snapshots are idempotent")
}

func TestPluginScopedConfigSnapshotFailurePreservesPrevious(t *testing.T) {
	for _, mode := range []string{"rpc-error", "nil", "rejected"} {
		t.Run(mode, func(t *testing.T) {
			client := &scopedConfigSnapshotClient{apply: func(context.Context, *pluginv1.ApplyConfigRequest) (*pluginv1.ApplyConfigResponse, error) {
				return &pluginv1.ApplyConfigResponse{Applied: true}, nil
			}}
			runtime := &pluginRuntime{api: client}
			require.NoError(t, runtime.applyAndRememberScopedConfig(context.Background(), []byte(`{"old":true}`), 7, true))
			previous := runtime.scopedConfig.Load()
			client.apply = func(context.Context, *pluginv1.ApplyConfigRequest) (*pluginv1.ApplyConfigResponse, error) {
				switch mode {
				case "rpc-error":
					return nil, errors.New("sensitive plugin error")
				case "nil":
					return nil, nil
				default:
					return &pluginv1.ApplyConfigResponse{}, nil
				}
			}
			err := runtime.applyAndRememberScopedConfig(context.Background(), []byte(`{"new":true}`), 8, true)
			require.Error(t, err)
			require.NotContains(t, err.Error(), "sensitive")
			require.Same(t, previous, runtime.scopedConfig.Load())
			require.Error(t, runtime.pauseScopedConfig(context.Background()))
			require.Same(t, previous, runtime.scopedConfig.Load())
		})
	}
}

func TestPluginScopedConfigSnapshotPauseAllowsReceiptCompletion(t *testing.T) {
	client := &scopedConfigSnapshotClient{apply: func(context.Context, *pluginv1.ApplyConfigRequest) (*pluginv1.ApplyConfigResponse, error) {
		return &pluginv1.ApplyConfigResponse{Applied: true}, nil
	}}
	runtime := &pluginRuntime{api: client}
	require.Error(t, runtime.pauseScopedConfig(context.Background()))
	require.NoError(t, runtime.applyAndRememberScopedConfig(context.Background(), []byte(`{}`), 3, true))
	require.True(t, runtime.beginRequest())
	var cleanup atomic.Int32
	require.NoError(t, runtime.registerRequestCompletion("existing-request", func() { cleanup.Add(1) }))
	require.NoError(t, runtime.pauseScopedConfig(context.Background()))
	require.Equal(t, int64(1), runtime.inFlight.Load())
	require.Zero(t, cleanup.Load())
	host := &pluginRequestHostServiceServer{runtime: runtime}
	_, err := host.CompleteRequest(context.Background(), &pluginv1.CompleteRequestRequest{RequestId: "existing-request"})
	require.NoError(t, err)
	require.Equal(t, int32(1), cleanup.Load())
	require.Zero(t, runtime.inFlight.Load())
}

func TestPluginScopedConfigSnapshotPauseSerializesWithApply(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	var paused *pluginv1.ApplyConfigRequest
	client := &scopedConfigSnapshotClient{apply: func(ctx context.Context, req *pluginv1.ApplyConfigRequest) (*pluginv1.ApplyConfigResponse, error) {
		if req.RuntimeActive {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		} else {
			paused = req
		}
		return &pluginv1.ApplyConfigResponse{Applied: true}, nil
	}}
	runtime := &pluginRuntime{api: client}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	applied := make(chan error, 1)
	go func() { applied <- runtime.applyAndRememberScopedConfig(ctx, []byte(`{"current":true}`), 11, true) }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("apply did not start")
	}
	require.Nil(t, runtime.scopedConfig.Load(), "unacknowledged config must not be published")
	pauseDone := make(chan error, 1)
	go func() { pauseDone <- runtime.pauseScopedConfig(ctx) }()
	unblock()
	require.NoError(t, <-applied)
	require.NoError(t, <-pauseDone)
	require.Equal(t, uint64(11), paused.ConfigRevision)
	require.Equal(t, `{"current":true}`, string(paused.ConfigJson))
	require.False(t, runtime.scopedConfig.Load().active)
}
