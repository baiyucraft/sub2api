package service

import (
	"context"
	"errors"
	"sync"
	"time"

	pluginv1 "github.com/Wei-Shaw/sub2api/pkg/pluginapi/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Only opaque IDs and completion callbacks are retained, never request data.
type pluginRequestRegistry struct {
	mu      sync.Mutex
	closed  bool
	pending map[string]func()
}

func validPluginRequestID(id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func (r *pluginRequestRegistry) register(id string, finish func()) error {
	if !validPluginRequestID(id) || finish == nil {
		return errors.New("invalid plugin request completion registration")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return errors.New("plugin runtime has exited")
	}
	if r.pending == nil {
		r.pending = make(map[string]func())
	}
	if _, exists := r.pending[id]; exists {
		return errors.New("plugin request already registered")
	}
	r.pending[id] = finish
	return nil
}

func (r *pluginRequestRegistry) contains(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, exists := r.pending[id]
	return exists
}

func (r *pluginRequestRegistry) complete(id string) {
	r.mu.Lock()
	finish := r.pending[id]
	delete(r.pending, id)
	r.mu.Unlock()
	if finish != nil {
		finish()
	}
}

func (r *pluginRequestRegistry) closeAfterExit() {
	r.mu.Lock()
	r.closed = true
	pending := r.pending
	r.pending = nil
	r.mu.Unlock()
	for _, finish := range pending {
		finish()
	}
}

type pluginCompletionRequestIDKey struct{}

func withPluginRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, pluginCompletionRequestIDKey{}, requestID)
}

// Call after beginRequest and the distributed BeginPluginRequest succeed.
// Success transfers cleanup ownership to this runtime. Failure transfers none.
// cleanup must use a bounded independent context; failed DB deletion must leave
// the marker intact. HTTP cancellation and Body.Close are not acknowledgements.
func (r *pluginRuntime) registerRequestCompletion(requestID string, cleanup func()) error {
	if r == nil || cleanup == nil {
		return errors.New("plugin request completion unavailable")
	}
	return r.requests.register(requestID, func() {
		defer r.finishRequest()
		cleanup()
	})
}

func (r *pluginRuntime) watchProcessExit() {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if r.client.Exited() {
			r.confirmProcessExit()
			return
		}
		<-ticker.C
	}
}

func (r *pluginRuntime) confirmProcessExit() {
	r.exited.Store(true)
	r.draining.Store(true)
	r.requests.closeAfterExit()
}

// Wrapping the broker endpoint binds confirmations to the exact process, even
// when multiple runtimes share a plugin key during an upgrade.
type pluginRequestHostServiceServer struct {
	pluginv1.HostServiceServer
	runtime    *pluginRuntime
	repository PluginRepository
}

func (s *pluginRequestHostServiceServer) CompleteRequest(_ context.Context, req *pluginv1.CompleteRequestRequest) (*pluginv1.CompleteRequestResponse, error) {
	if s == nil || s.runtime == nil {
		return nil, status.Error(codes.Unavailable, "plugin request completion unavailable")
	}
	if req == nil || !validPluginRequestID(req.RequestId) {
		return nil, status.Error(codes.InvalidArgument, "invalid request id")
	}
	// Unknown and repeated IDs are indistinguishable and idempotent. A different
	// runtime cannot clear this runtime's markers through its own broker endpoint.
	s.runtime.requests.complete(req.RequestId)
	return &pluginv1.CompleteRequestResponse{}, nil
}
