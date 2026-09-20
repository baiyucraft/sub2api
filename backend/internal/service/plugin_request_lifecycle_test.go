package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	pluginv1 "github.com/Wei-Shaw/sub2api/pkg/pluginapi/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type lifecycleForwardStream struct {
	grpc.ClientStream
	ctx     context.Context
	frames  chan *pluginv1.ForwardResponse
	sendErr error
	start   atomic.Pointer[pluginv1.ForwardRequestStart]
}

func (s *lifecycleForwardStream) Context() context.Context { return s.ctx }
func (s *lifecycleForwardStream) CloseSend() error         { return nil }
func (s *lifecycleForwardStream) Send(req *pluginv1.ForwardRequest) error {
	if start := req.GetStart(); start != nil {
		s.start.Store(start)
		return s.sendErr
	}
	return nil
}
func (s *lifecycleForwardStream) Recv() (*pluginv1.ForwardResponse, error) {
	select {
	case frame, ok := <-s.frames:
		if !ok {
			return nil, io.EOF
		}
		return frame, nil
	case <-s.ctx.Done():
		return nil, s.ctx.Err()
	}
}

type lifecycleForwardClient struct {
	pluginv1.TransportPluginClient
	stream *lifecycleForwardStream
	err    error
}

func (c *lifecycleForwardClient) Forward(ctx context.Context, _ ...grpc.CallOption) (pluginv1.TransportPlugin_ForwardClient, error) {
	c.stream.ctx = ctx
	return c.stream, c.err
}

func lifecycleRuntime(t *testing.T) (*pluginRuntime, *lifecycleForwardStream, *atomic.Int32, context.Context) {
	t.Helper()
	stream := &lifecycleForwardStream{frames: make(chan *pluginv1.ForwardResponse, 4)}
	runtime := &pluginRuntime{api: &lifecycleForwardClient{stream: stream}}
	cleanups := &atomic.Int32{}
	require.True(t, runtime.beginRequest())
	require.NoError(t, runtime.registerRequestCompletion("guard-request-id", func() { cleanups.Add(1) }))
	return runtime, stream, cleanups, withPluginRequestID(context.Background(), "guard-request-id")
}

func lifecycleRoundTrip(t *testing.T, runtime *pluginRuntime, ctx context.Context) (*http.Response, error) {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://example.invalid", nil)
	require.NoError(t, err)
	return runtime.roundTrip(ctx, request, "", &Account{ID: 42, Platform: PlatformOpenAI, Type: AccountTypeOAuth})
}

func lifecycleStart() *pluginv1.ForwardResponse {
	return &pluginv1.ForwardResponse{Frame: &pluginv1.ForwardResponse_Start{Start: &pluginv1.ForwardResponseStart{StatusCode: http.StatusOK}}}
}

func lifecycleEnd() *pluginv1.ForwardResponse {
	return &pluginv1.ForwardResponse{Frame: &pluginv1.ForwardResponse_End{End: &pluginv1.ForwardResponseEnd{}}}
}

func TestPluginRequestLifecycleCloseWaitsForCompletion(t *testing.T) {
	runtime, stream, cleanups, ctx := lifecycleRuntime(t)
	stream.frames <- lifecycleStart()
	response, err := lifecycleRoundTrip(t, runtime, ctx)
	require.NoError(t, err)
	require.Equal(t, "guard-request-id", stream.start.Load().RequestId)
	require.NoError(t, response.Body.Close())
	require.NoError(t, response.Body.Close())
	require.Equal(t, int64(1), runtime.inFlight.Load())
	require.Zero(t, cleanups.Load(), "HTTP close is not plugin completion")

	host := &pluginRequestHostServiceServer{runtime: runtime}
	_, err = host.CompleteRequest(context.Background(), &pluginv1.CompleteRequestRequest{RequestId: "guard-request-id"})
	require.NoError(t, err)
	require.Equal(t, int32(1), cleanups.Load())
	require.Zero(t, runtime.inFlight.Load())
}

func TestPluginRequestLifecycleEndAcknowledgesBeforeBodyClose(t *testing.T) {
	runtime, stream, cleanups, ctx := lifecycleRuntime(t)
	stream.frames <- lifecycleStart()
	stream.frames <- lifecycleEnd()
	response, err := lifecycleRoundTrip(t, runtime, ctx)
	require.NoError(t, err)
	_, err = io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Equal(t, int32(1), cleanups.Load())
	require.Zero(t, runtime.inFlight.Load())
	require.NoError(t, response.Body.Close())
	host := &pluginRequestHostServiceServer{runtime: runtime}
	_, err = host.CompleteRequest(context.Background(), &pluginv1.CompleteRequestRequest{RequestId: "guard-request-id"})
	require.NoError(t, err)
	runtime.confirmProcessExit()
	require.Equal(t, int32(1), cleanups.Load())
	require.Zero(t, runtime.inFlight.Load())
}

func TestPluginRequestLifecycleUncertainStreamTerminationRetainsGuard(t *testing.T) {
	for _, mode := range []string{"eof", "error", "cancel", "send-start-error", "header-eof"} {
		t.Run(mode, func(t *testing.T) {
			runtime, stream, cleanups, ctx := lifecycleRuntime(t)
			if mode != "header-eof" && mode != "send-start-error" {
				stream.frames <- lifecycleStart()
			}
			switch mode {
			case "eof", "header-eof":
				close(stream.frames)
			case "error":
				stream.frames <- &pluginv1.ForwardResponse{Frame: &pluginv1.ForwardResponse_Error{Error: &pluginv1.ForwardResponseError{Code: "failed"}}}
			case "send-start-error":
				stream.sendErr = errors.New("send failed")
			}
			response, err := lifecycleRoundTrip(t, runtime, ctx)
			if response != nil {
				if mode != "cancel" {
					_, _ = io.ReadAll(response.Body)
				}
				require.NoError(t, response.Body.Close())
			} else {
				require.Error(t, err)
			}
			require.Zero(t, cleanups.Load())
			require.Equal(t, int64(1), runtime.inFlight.Load())
			// nil client is not evidence of process exit, even if kill is called.
			runtime.kill()
			require.Zero(t, cleanups.Load())
			runtime.confirmProcessExit()
			require.Equal(t, int32(1), cleanups.Load())
			require.Zero(t, runtime.inFlight.Load())
		})
	}
}

func TestPluginRequestLifecycleBeforeStartFailureCanRelease(t *testing.T) {
	runtime, stream, cleanups, ctx := lifecycleRuntime(t)
	runtime.api.(*lifecycleForwardClient).err = errors.New("stream unavailable")
	_, err := lifecycleRoundTrip(t, runtime, ctx)
	require.Error(t, err)
	require.Nil(t, stream.start.Load())
	require.Equal(t, int32(1), cleanups.Load())
	require.Zero(t, runtime.inFlight.Load())
}

func TestPluginRequestLifecycleConcurrentCompletionRunsOnce(t *testing.T) {
	runtime, _, cleanups, _ := lifecycleRuntime(t)
	host := &pluginRequestHostServiceServer{runtime: runtime}
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = host.CompleteRequest(context.Background(), &pluginv1.CompleteRequestRequest{RequestId: "guard-request-id"})
		}()
	}
	wg.Add(1)
	go func() { defer wg.Done(); runtime.confirmProcessExit() }()
	wg.Wait()
	require.Equal(t, int32(1), cleanups.Load())
	require.Zero(t, runtime.inFlight.Load())
	require.False(t, runtime.beginRequest())
	require.Error(t, runtime.registerRequestCompletion("new-request", func() {}))
}

func TestPluginRequestLifecycleRuntimeIsolationAndValidation(t *testing.T) {
	first, _, firstCleanups, _ := lifecycleRuntime(t)
	second := &pluginRuntime{}
	host := &pluginRequestHostServiceServer{runtime: second}
	_, err := host.CompleteRequest(context.Background(), &pluginv1.CompleteRequestRequest{RequestId: "guard-request-id"})
	require.NoError(t, err)
	require.Zero(t, firstCleanups.Load())
	for _, id := range []string{"", strings.Repeat("x", 129), "http://user:secret@proxy", "bad\nrequest"} {
		_, err := host.CompleteRequest(context.Background(), &pluginv1.CompleteRequestRequest{RequestId: id})
		require.Equal(t, codes.InvalidArgument, status.Code(err))
		require.NotContains(t, err.Error(), "secret")
	}
	require.Error(t, first.registerRequestCompletion("guard-request-id", func() { t.Error("duplicate callback ran") }))
	first.confirmProcessExit()
	require.Equal(t, int32(1), firstCleanups.Load())
}

func TestPluginRequestLifecycleLegacyRetainsBodyCloseAccounting(t *testing.T) {
	stream := &lifecycleForwardStream{frames: make(chan *pluginv1.ForwardResponse, 2)}
	runtime := &pluginRuntime{api: &lifecycleForwardClient{stream: stream}}
	require.True(t, runtime.beginRequest())
	stream.frames <- lifecycleStart()
	stream.frames <- lifecycleEnd()
	response, err := lifecycleRoundTrip(t, runtime, context.Background())
	require.NoError(t, err)
	_, err = io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Equal(t, int64(1), runtime.inFlight.Load())
	require.NoError(t, response.Body.Close())
	require.NoError(t, response.Body.Close())
	require.Zero(t, runtime.inFlight.Load())
}
