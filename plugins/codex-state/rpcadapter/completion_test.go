package rpcadapter

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baiyucraft/codex-state-plugin/core"
	pluginv1 "github.com/baiyucraft/codex-state-plugin/sdk/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func seedCompletionSlot(t *testing.T, h *wireHost) string {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	raw := make([]byte, 57+16*10)
	raw[0] = 0x80
	binary.BigEndian.PutUint64(raw[1:9], uint64(now.Unix()))
	value := base64.URLEncoding.EncodeToString(raw)
	envelope, err := core.ParseEnvelope(value, "pro", now)
	if err != nil {
		t.Fatal(err)
	}
	guard := core.Guard{AccountID: 123, Model: "gpt-6-astra", ConfigRevision: "1", IdentityRevision: "identity-1"}
	slot := core.Slot{Guard: guard, NextVersion: 1, Strikes: 1, Active: &core.Ticket{State: value, Fingerprint: envelope.Fingerprint, Version: 1, CapturedAt: now, IssuedAt: envelope.IssuedAt, ExpiresAt: envelope.ExpiresAt}}
	data, err := json.Marshal(slot)
	if err != nil {
		t.Fatal(err)
	}
	key := remoteKey("slots/123/gpt-6-astra", guard)
	h.mu.Lock()
	h.states[key] = &pluginv1.StateGetResponse{Found: true, Value: data, Version: 1}
	h.mu.Unlock()
	return key
}

func completionStrikes(t *testing.T, h *wireHost, key string) int {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	var slot core.Slot
	if err := json.Unmarshal(h.states[key].Value, &slot); err != nil {
		t.Fatal(err)
	}
	return slot.Strikes
}

func completionStart(endpoint, id string) *pluginv1.ForwardRequestStart {
	return &pluginv1.ForwardRequestStart{RequestId: id, Method: "GET", Url: endpoint, AccountId: 123, OutboundModel: "gpt-6-astra", IdentityRevision: "identity-1", ConfigRevision: 1}
}

func TestForwardEndWaitsForCompletedWatchdog(t *testing.T) {
	s, h := testServer(t)
	key := seedCompletionSlot(t, h)
	body := `{"object":"response","status":"completed","model":"gpt-6-astra"}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, body) }))
	defer upstream.Close()
	ended := false
	h.onComplete = func(ctx context.Context, id string) error {
		assertCompletionContext(t, ctx)
		if !ended || id != "complete-normal" || completionStrikes(t, h, key) != 0 {
			t.Error("acknowledgement preceded End/watchdog")
		}
		return nil
	}
	stream := &fakeStream{ctx: context.Background(), frames: forwardFrames(completionStart(upstream.URL, "complete-normal"), ""), onSend: func(frame *pluginv1.ForwardResponse) error {
		if frame.GetEnd() != nil {
			ended = true
			if completionStrikes(t, h, key) != 0 {
				t.Error("End was sent before watchdog persistence")
			}
		}
		return nil
	}}
	if err := s.Forward(stream); err != nil {
		t.Fatal(err)
	}
	if !ended {
		t.Fatal("normal End missing")
	}
	assertCompletedOnce(t, h, "complete-normal")
}

func TestForwardCancelledBodyCompletesWatchdogAfterPause(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		strikes    int
	}{
		{"complete", "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"model\":\"gpt-6-astra\"}}\n\n", 0},
		{"json_complete", `{"object":"response","status":"completed","model":"gpt-6-astra"}`, 0},
		{"missing_blank_line", "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"model\":\"gpt-6-astra\"}}\n", 1},
		{"missing_lf", `data: {"type":"response.completed","response":{"status":"completed","model":"gpt-6-astra"}}`, 1},
		{"truncated_json", "data: {\"type\":\"response.completed\",\"response\":{\"model\":\"gpt-6-astra\"}\n\n", 1},
		{"partial", `data: {"type":"response.created","response":{"model":"gpt-6-astra"}}`, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, h := testServer(t)
			key := seedCompletionSlot(t, h)
			upstreamDone := make(chan struct{})
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(upstreamDone)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, tc.body)
				w.(http.Flusher).Flush()
				<-r.Context().Done()
			}))
			defer upstream.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			h.onComplete = func(ackCtx context.Context, id string) error {
				assertCompletionContext(t, ackCtx)
				if ctx.Err() == nil || s.engine.RuntimeActive() || completionStrikes(t, h, key) != tc.strikes {
					t.Error("acknowledgement preceded cancellation/watchdog cleanup")
				}
				select {
				case <-upstreamDone:
				case <-time.After(time.Second):
					t.Error("acknowledgement before upstream socket closed")
				}
				return nil
			}
			var seen strings.Builder
			paused := false
			stream := &fakeStream{ctx: ctx, frames: forwardFrames(completionStart(upstream.URL, "complete-cancel-"+tc.name), ""), onSend: func(frame *pluginv1.ForwardResponse) error {
				if chunk := frame.GetBodyChunk(); len(chunk) > 0 {
					seen.Write(chunk)
					if seen.Len() >= len(tc.body) {
						applied, err := s.ApplyConfig(context.Background(), &pluginv1.ApplyConfigRequest{ConfigJson: []byte(rpcConfig), ConfigRevision: 1, RuntimeActive: false})
						if err != nil || !applied.Applied {
							t.Error("pause failed", applied, err)
						}
						paused = true
						cancel()
						return context.Canceled
					}
				}
				if frame.GetEnd() != nil {
					t.Error("cancelled stream sent End")
				}
				return nil
			}}
			if err := s.Forward(stream); !errors.Is(err, context.Canceled) {
				t.Fatal("cancellation was not preserved", err)
			}
			if !paused || seen.String() != tc.body || completionStrikes(t, h, key) != tc.strikes {
				t.Fatal("cancelled stream lost completion or changed bytes")
			}
			select {
			case <-upstreamDone:
			case <-time.After(time.Second):
				t.Fatal("upstream body not closed")
			}
			assertCompletedOnce(t, h, "complete-cancel-"+tc.name)
		})
	}
}

func assertCompletionContext(t *testing.T, ctx context.Context) {
	t.Helper()
	if ctx.Err() != nil {
		t.Error("acknowledgement inherited downstream cancellation")
	}
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 5*time.Second {
		t.Error("acknowledgement requires independent five-second deadline")
	}
}

func assertCompletedOnce(t *testing.T, h *wireHost, id string) {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.completed) != 1 || h.completed[0] != id {
		t.Fatalf("wrong completion acknowledgements: %v", h.completed)
	}
}

func TestForwardAdmissionRejectionAcknowledgedAfterDownstreamCancel(t *testing.T) {
	s, h := testServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errorSent := false
	h.onComplete = func(ackCtx context.Context, id string) error {
		assertCompletionContext(t, ackCtx)
		if !errorSent || ctx.Err() == nil {
			t.Error("missing original rejection/cancellation")
		}
		return nil
	}
	start := completionStart("http://invalid.invalid", "complete-rejected")
	start.ConfigRevision = 2
	stream := &fakeStream{ctx: ctx, frames: forwardFrames(start, ""), onSend: func(frame *pluginv1.ForwardResponse) error {
		failure := frame.GetError()
		if failure == nil || failure.Code != "plugin_admission_config_revision_mismatch" || failure.RequestSent {
			t.Error("wrong admission failure", failure)
		}
		errorSent = true
		cancel()
		return context.Canceled
	}}
	if err := s.Forward(stream); !errors.Is(err, context.Canceled) {
		t.Fatal("acknowledgement replaced stream error", err)
	}
	assertCompletedOnce(t, h, "complete-rejected")
}

func TestReverseCompletionFailurePreservesNormalEnd(t *testing.T) {
	s, h := testServer(t)
	h.onComplete = func(ctx context.Context, id string) error {
		assertCompletionContext(t, ctx)
		return status.Error(codes.Unimplemented, "legacy host")
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "original response") }))
	defer upstream.Close()
	start := completionStart(upstream.URL, "complete-legacy")
	start.Headers = map[string]*pluginv1.HeaderValues{core.StateHeader: {Values: []string{"client-state"}}}
	stream := &fakeStream{ctx: context.Background(), frames: forwardFrames(start, "")}
	if err := s.Forward(stream); err != nil {
		t.Fatal("ack failure replaced successful response", err)
	}
	if len(stream.responses) == 0 || stream.responses[len(stream.responses)-1].GetEnd() == nil {
		t.Fatal("legacy completion proof missing")
	}
	assertCompletedOnce(t, h, "complete-legacy")
}

type blockedWatchdogHost struct {
	*wireHost
	entered chan context.Context
	release chan struct{}
}

func (h *blockedWatchdogHost) StateCompareAndSwap(ctx context.Context, _ *pluginv1.StateCompareAndSwapRequest, _ ...grpc.CallOption) (*pluginv1.StateCompareAndSwapResponse, error) {
	h.entered <- ctx
	select {
	case <-h.release:
		return nil, status.Error(codes.Unavailable, "storage unavailable")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestForwardCompletionWaitsForFailedWatchdogCleanup(t *testing.T) {
	s, h := testServer(t)
	key := seedCompletionSlot(t, h)
	blocked := &blockedWatchdogHost{wireHost: h, entered: make(chan context.Context, 1), release: make(chan struct{})}
	s.host.set(blocked)
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(blocked.release) }) }
	defer unblock()
	body := `{"object":"response","status":"completed","model":"gpt-6-astra"}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, body) }))
	defer upstream.Close()
	ended, acknowledged := make(chan struct{}), make(chan struct{})
	h.onComplete = func(ctx context.Context, id string) error {
		assertCompletionContext(t, ctx)
		h.mu.Lock()
		leases := len(h.leases)
		h.mu.Unlock()
		if leases != 0 {
			t.Error("acknowledgement preceded watchdog lease release")
		}
		close(acknowledged)
		return nil
	}
	stream := &fakeStream{ctx: context.Background(), frames: forwardFrames(completionStart(upstream.URL, "complete-write-failed"), ""), onSend: func(frame *pluginv1.ForwardResponse) error {
		if frame.GetEnd() != nil {
			close(ended)
		}
		return nil
	}}
	done := make(chan error, 1)
	go func() { done <- s.Forward(stream) }()
	select {
	case ctx := <-blocked.entered:
		assertCompletionContext(t, ctx)
	case <-time.After(time.Second):
		t.Fatal("watchdog persistence was not reached")
	}
	select {
	case <-ended:
		t.Error("End preceded blocked watchdog exit")
	default:
	}
	select {
	case <-acknowledged:
		t.Error("reverse acknowledgement preceded blocked watchdog exit")
	default:
	}
	unblock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal("watchdog failure changed successful response", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Forward did not finish after watchdog exit")
	}
	var received strings.Builder
	for _, frame := range stream.responses {
		received.Write(frame.GetBodyChunk())
		if frame.GetError() != nil {
			t.Error("watchdog failure replaced business result", frame.GetError())
		}
	}
	if received.String() != body || stream.responses[len(stream.responses)-1].GetEnd() == nil || completionStrikes(t, h, key) != 1 {
		t.Fatal("failed watchdog altered response or published unpersisted state")
	}
	assertCompletedOnce(t, h, "complete-write-failed")
	if _, err := s.engine.Prepare(context.Background(), 123, "gpt-6-astra", "identity-1", make(http.Header)); !errors.Is(err, core.ErrUnavailable) {
		t.Fatal("watchdog storage failure did not fail closed", err)
	}
}

func TestForwardSetupFailureStillAcknowledged(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		configure  func(*pluginv1.ForwardRequestStart)
	}{
		{"method", "invalid_forward_request", func(s *pluginv1.ForwardRequestStart) { s.Method = "bad method" }},
		{"scheme", "invalid_forward_request", func(s *pluginv1.ForwardRequestStart) { s.Url = "ftp://invalid.invalid" }},
		{"proxy", "invalid_forward_proxy", func(s *pluginv1.ForwardRequestStart) { s.ProxyUrl = "://invalid" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, h := testServer(t)
			start := completionStart("http://invalid.invalid", "complete-setup-"+tc.name)
			start.Headers = map[string]*pluginv1.HeaderValues{core.StateHeader: {Values: []string{"client-state"}}}
			tc.configure(start)
			h.onComplete = func(ctx context.Context, _ string) error { assertCompletionContext(t, ctx); return nil }
			stream := &fakeStream{ctx: context.Background(), frames: forwardFrames(start, "")}
			if err := s.Forward(stream); err != nil {
				t.Fatal(err)
			}
			if len(stream.responses) != 1 || stream.responses[0].GetError() == nil || stream.responses[0].GetError().Code != tc.code || stream.responses[0].GetError().RequestSent {
				t.Fatal("setup failure changed admission/transport semantics", stream.responses)
			}
			assertCompletedOnce(t, h, start.RequestId)
		})
	}
}

type transitionContextKey struct{}

type transitionIdentityHost struct {
	*wireHost
	onIdentity func()
}

func (h *transitionIdentityHost) ResolveOutboundIdentity(ctx context.Context, r *pluginv1.ResolveOutboundIdentityRequest, opts ...grpc.CallOption) (*pluginv1.ResolveOutboundIdentityResponse, error) {
	identity, err := h.wireHost.ResolveOutboundIdentity(ctx, r, opts...)
	if ctx.Value(transitionContextKey{}) == true {
		h.onIdentity()
	}
	return identity, err
}

func TestForwardClientStateCannotBypassConcurrentApply(t *testing.T) {
	for _, transition := range []string{"pause", "revision"} {
		t.Run(transition, func(t *testing.T) {
			s, h := testServer(t)
			var applied atomic.Bool
			s.host.set(&transitionIdentityHost{wireHost: h, onIdentity: func() {
				revision, active := uint64(1), false
				if transition == "revision" {
					revision, active = 2, true
				}
				result, err := s.ApplyConfig(context.Background(), &pluginv1.ApplyConfigRequest{ConfigJson: []byte(rpcConfig), ConfigRevision: revision, RuntimeActive: active})
				if err != nil || !result.Applied {
					t.Error("concurrent transition failed", result, err)
				}
				applied.Store(true)
			}})
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
			defer upstream.Close()
			start := completionStart(upstream.URL, "complete-transition-"+transition)
			start.Headers = map[string]*pluginv1.HeaderValues{core.StateHeader: {Values: []string{"client-state"}}}
			ctx := context.WithValue(context.Background(), transitionContextKey{}, true)
			stream := &fakeStream{ctx: ctx, frames: forwardFrames(start, "")}
			if err := s.Forward(stream); err != nil {
				t.Fatal(err)
			}
			if !applied.Load() || calls.Load() != 0 || len(stream.responses) != 1 {
				t.Fatal("concurrent Apply bypassed strict admission")
			}
			failure := stream.responses[0].GetError()
			if failure == nil || failure.RequestSent || !strings.HasPrefix(failure.Code, "plugin_admission_") {
				t.Fatal("concurrent Apply consumed upstream budget", failure)
			}
			assertCompletedOnce(t, h, start.RequestId)
		})
	}
}
