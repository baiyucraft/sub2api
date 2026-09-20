package rpcadapter

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
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

type wireHost struct {
	pluginv1.HostServiceClient
	mu               sync.Mutex
	states           map[string]*pluginv1.StateGetResponse
	leases           map[string]*pluginv1.LeaseResponse
	nextFence        uint64
	identityCalls    int
	identityRevision string
	casOwners        []string
	completed        []string
	onComplete       func(context.Context, string) error
}

func newWireHost() *wireHost {
	return &wireHost{states: map[string]*pluginv1.StateGetResponse{}, leases: map[string]*pluginv1.LeaseResponse{}, identityRevision: "identity-1"}
}

func (h *wireHost) CompleteRequest(ctx context.Context, r *pluginv1.CompleteRequestRequest, _ ...grpc.CallOption) (*pluginv1.CompleteRequestResponse, error) {
	h.mu.Lock()
	h.completed = append(h.completed, r.RequestId)
	callback := h.onComplete
	h.mu.Unlock()
	if callback != nil {
		if err := callback(ctx, r.RequestId); err != nil {
			return nil, err
		}
	}
	return &pluginv1.CompleteRequestResponse{}, nil
}

func (h *wireHost) ListResources(context.Context, *pluginv1.ListResourcesRequest, ...grpc.CallOption) (*pluginv1.ListResourcesResponse, error) {
	return &pluginv1.ListResourcesResponse{ResourcesJson: []byte(`{"accounts":[{"id":123,"platform":"openai","account_type":"setup-token"}]}`)}, nil
}
func (h *wireHost) ResolveOutboundIdentity(_ context.Context, r *pluginv1.ResolveOutboundIdentityRequest, _ ...grpc.CallOption) (*pluginv1.ResolveOutboundIdentityResponse, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.identityCalls++
	return &pluginv1.ResolveOutboundIdentityResponse{Found: true, AccountId: r.AccountId, Platform: "openai", AccountType: "setup-token", IdentityRevision: h.identityRevision, Token: "private-token", Egresses: []*pluginv1.OutboundEgress{{ProxyId: 3, ProxyUrl: "http://egress:80"}}}, nil
}
func (h *wireHost) StateGet(_ context.Context, r *pluginv1.StateGetRequest, _ ...grpc.CallOption) (*pluginv1.StateGetResponse, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	old := h.states[r.Key]
	if old == nil {
		return &pluginv1.StateGetResponse{}, nil
	}
	return &pluginv1.StateGetResponse{Found: old.Found, Value: append([]byte(nil), old.Value...), Version: old.Version}, nil
}
func (h *wireHost) StateCompareAndSwap(_ context.Context, r *pluginv1.StateCompareAndSwapRequest, _ ...grpc.CallOption) (*pluginv1.StateCompareAndSwapResponse, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	l := h.leases[r.Key]
	if l == nil || l.Owner != r.LeaseOwner || l.Fence != r.LeaseFence {
		return nil, status.Error(codes.FailedPrecondition, "lease lost")
	}
	h.casOwners = append(h.casOwners, r.LeaseOwner)
	old := h.states[r.Key]
	var v uint64
	if old != nil {
		v = old.Version
	}
	if v != r.ExpectedVersion {
		return nil, status.Error(codes.Aborted, "version conflict")
	}
	v++
	h.states[r.Key] = &pluginv1.StateGetResponse{Found: !r.Delete, Version: v, Value: append([]byte(nil), r.Value...)}
	return &pluginv1.StateCompareAndSwapResponse{Version: v}, nil
}
func (h *wireHost) AcquireLease(_ context.Context, r *pluginv1.LeaseRequest, _ ...grpc.CallOption) (*pluginv1.LeaseResponse, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.leases[r.Key] != nil {
		return &pluginv1.LeaseResponse{Acquired: false}, nil
	}
	h.nextFence++
	l := &pluginv1.LeaseResponse{Acquired: true, Owner: "host-generated-owner", Fence: h.nextFence, ExpiresAt: time.Now().Add(time.Duration(r.TtlSeconds) * time.Second).Unix()}
	h.leases[r.Key] = l
	return l, nil
}
func (h *wireHost) RenewLease(_ context.Context, r *pluginv1.LeaseRequest, _ ...grpc.CallOption) (*pluginv1.LeaseResponse, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	l := h.leases[r.Key]
	if l == nil || l.Owner != r.Owner || l.Fence != r.Fence {
		return nil, status.Error(codes.FailedPrecondition, "lease lost")
	}
	return l, nil
}
func (h *wireHost) ReleaseLease(_ context.Context, r *pluginv1.LeaseRequest, _ ...grpc.CallOption) (*pluginv1.LeaseResponse, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	l := h.leases[r.Key]
	if l != nil && l.Owner == r.Owner && l.Fence == r.Fence {
		delete(h.leases, r.Key)
	}
	return &pluginv1.LeaseResponse{}, nil
}

const rpcConfig = `{"version":1,"enabled":true,"harvest_proxy_url":"","dial_proxy_url":"","accounts":[{"account_id":123,"models":{"gpt-6-astra":{"enabled":true,"ticket_plan":"pro"},"gpt-5.6-sol":{"enabled":false,"ticket_plan":"pro"}}}]}`

func testServer(t *testing.T) (*Server, *wireHost) {
	t.Helper()
	s := New()
	h := newWireHost()
	s.host.set(h)
	r, err := s.ApplyConfig(context.Background(), &pluginv1.ApplyConfigRequest{ConfigJson: []byte(rpcConfig), ConfigRevision: 1, RuntimeActive: true})
	if err != nil || !r.Applied {
		t.Fatal(r, err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := s.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return s, h
}

func TestRPCValidateManagedTargetsHealthAndNegotiation(t *testing.T) {
	s, h := testServer(t)
	r, err := s.ValidateConfig(context.Background(), &pluginv1.ValidateConfigRequest{ConfigJson: []byte(rpcConfig)})
	if err != nil || !r.Valid || !r.ScopedRouting || len(r.ManagedTargets) != 1 || len(r.ManagedTargets[0].Models) != 1 || r.ManagedTargets[0].Models[0] != "gpt-6-astra" {
		t.Fatal(r, err)
	}
	off := strings.Replace(rpcConfig, `"enabled":true`, `"enabled":false`, 1)
	r, _ = s.ValidateConfig(context.Background(), &pluginv1.ValidateConfigRequest{ConfigJson: []byte(off)})
	if len(r.ManagedTargets) != 0 {
		t.Fatal("disabled managed targets")
	}
	h.mu.Lock()
	calls := h.identityCalls
	h.mu.Unlock()
	health, err := s.Health(context.Background(), &pluginv1.HealthRequest{})
	if err != nil || !health.Healthy {
		t.Fatal(health, err)
	}
	h.mu.Lock()
	after := h.identityCalls
	h.mu.Unlock()
	if calls != after {
		t.Fatal("health fetched identity")
	}
	var payload map[string]any
	if json.Unmarshal([]byte(health.StatusJson), &payload) != nil || payload["config_revision"] != float64(1) {
		t.Fatal("revision JSON is not numeric")
	}
	result, err := s.InitHostServices(context.Background(), &pluginv1.InitHostServicesRequest{HostServiceApiVersion: 1, HostFeatures: RequiredHostFeatures})
	if err != nil || result.Ready || result.Message != "host service API 2 required" {
		t.Fatal("incorrect incompatible-host rejection", result, err)
	}
	info, _ := s.GetInfo(context.Background(), nil)
	if info.PluginId != "baiyu.codex-state" || info.PluginVersion != "0.1.0" || info.ProtocolVersion != 1 || info.TransportApiVersion != 1 {
		t.Fatal(info)
	}
}

func TestRequiredHostFeaturesMatchSDKAndSourceLock(t *testing.T) {
	want := []string{
		"scoped-routing.v1", "admission.v1", "resources.v1", "actions.v1",
		"state-cas.v1", "leases.v1", "oauth-like.v1",
		"request-completion.v1", "config-secrets.v1",
	}
	if !slices.Equal(RequiredHostFeatures, want) {
		t.Fatalf("adapter host features differ: %v", RequiredHostFeatures)
	}
	// The package tool emits pluginv1.HostFeatures into manifest.requires.
	if !slices.Equal(pluginv1.HostFeatures, want) {
		t.Fatalf("SDK/manifest host features differ: %v", pluginv1.HostFeatures)
	}
	raw, err := os.ReadFile("../sources.lock.json")
	if err != nil {
		t.Fatal(err)
	}
	var lock struct {
		HostRequirements struct {
			HostServiceAPI int      `json:"host_service_api"`
			HostFeatures   []string `json:"host_features"`
		} `json:"host_requirements"`
	}
	if err = json.Unmarshal(raw, &lock); err != nil {
		t.Fatal(err)
	}
	if lock.HostRequirements.HostServiceAPI != 2 || !slices.Equal(lock.HostRequirements.HostFeatures, want) {
		t.Fatalf("source lock host requirements differ: %+v", lock.HostRequirements)
	}
}

func TestInitHostServicesRequiresEveryHostFeature(t *testing.T) {
	s := New()
	for i, missing := range RequiredHostFeatures {
		t.Run(missing, func(t *testing.T) {
			features := append([]string(nil), RequiredHostFeatures[:i]...)
			features = append(features, RequiredHostFeatures[i+1:]...)
			// Matching the expected feature count must not substitute for membership.
			features = append(features, "unrelated-future-feature.v1")
			result, err := s.InitHostServices(context.Background(), &pluginv1.InitHostServicesRequest{HostServiceApiVersion: 2, HostFeatures: features})
			if err != nil || result.Ready || result.Message != "required host feature missing: "+missing {
				t.Fatal("missing feature did not fail before broker initialization", result, err)
			}
		})
	}
	t.Run("full_set_reaches_broker_check", func(t *testing.T) {
		features := append([]string(nil), RequiredHostFeatures...)
		slices.Reverse(features)
		features = append(features, "unrelated-future-feature.v1")
		result, err := s.InitHostServices(context.Background(), &pluginv1.InitHostServicesRequest{HostServiceApiVersion: 2, HostFeatures: features})
		if err != nil || result.Ready || result.Message != "host broker unavailable" {
			t.Fatal("complete feature set did not pass negotiation", result, err)
		}
	})
}

func TestHostCASUsesReturnedOwnerAndRetainsTombstoneVersion(t *testing.T) {
	s, h := testServer(t)
	g := core.Guard{AccountID: 123, Model: "gpt-6-astra", ConfigRevision: "1", IdentityRevision: "identity-1"}
	key := "slots/123/gpt-6-astra"
	l, err := s.host.LeaseAcquire(context.Background(), core.LeaseRequest{Key: key, Owner: "plugin-owner", TTL: time.Second, Guard: g})
	if err != nil || l.Owner != "host-generated-owner" {
		t.Fatal(l, err)
	}
	v, err := s.host.StateCAS(context.Background(), core.Mutation{Key: key, ExpectedVersion: 0, Data: []byte(`{"ticket":"private"}`), Guard: g, Lease: l})
	if err != nil || v != 1 {
		t.Fatal(v, err)
	}
	v, err = s.host.StateCAS(context.Background(), core.Mutation{Key: key, ExpectedVersion: v, Data: nil, Guard: g, Lease: l})
	if err != nil || v != 2 {
		t.Fatal(v, err)
	}
	stored, err := s.host.StateGet(context.Background(), key, g)
	if err != nil || stored.Version != 2 || len(stored.Data) != 0 {
		t.Fatal(stored, err)
	}
	if _, err = s.host.StateCAS(context.Background(), core.Mutation{Key: key, Data: []byte(`{}`), Guard: g, Lease: l}); err != core.ErrConflict {
		t.Fatal("recreated tombstone without version", err)
	}
	_ = s.host.LeaseRelease(context.Background(), l)
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, owner := range h.casOwners {
		if owner != "host-generated-owner" {
			t.Fatal("wrong owner")
		}
	}
	changed := g
	changed.IdentityRevision = "identity-2"
	if remoteKey(key, g) == remoteKey(key, changed) {
		t.Fatal("identity keys collide")
	}
	changed = g
	changed.ConfigRevision = "2"
	if remoteKey(key, g) == remoteKey(key, changed) {
		t.Fatal("config keys collide")
	}
}

func TestRunActionQueuedHealthAndDuplicate(t *testing.T) {
	s, _ := testServer(t)
	request := &pluginv1.RunActionRequest{ActionId: "action-1", Name: "harvest", ConfigRevision: 1, PayloadJson: []byte(`{"account_id":123,"model":"gpt-6-astra"}`)}
	r, err := s.RunAction(context.Background(), request)
	if err != nil || !r.Accepted || r.Status != "queued" {
		t.Fatal(r, err)
	}
	health, _ := s.Health(context.Background(), nil)
	var state core.Status
	_ = json.Unmarshal([]byte(health.StatusJson), &state)
	m := state.Accounts[0].Models["gpt-6-astra"]
	if m.State != "queued" || !m.Refreshing {
		t.Fatalf("queue not visible %+v", m)
	}
	r, err = s.RunAction(context.Background(), request)
	if err != nil || !bytes.Contains(r.ResultJson, []byte(`"duplicate":true`)) {
		t.Fatal(r, err)
	}
	request.ConfigRevision = 2
	if _, err = s.RunAction(context.Background(), request); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("wrong revision accepted")
	}
}

type fakeStream struct {
	grpc.ServerStream
	ctx       context.Context
	frames    []*pluginv1.ForwardRequest
	index     int
	responses []*pluginv1.ForwardResponse
	onSend    func(*pluginv1.ForwardResponse) error
}

func (s *fakeStream) Context() context.Context { return s.ctx }
func (s *fakeStream) Recv() (*pluginv1.ForwardRequest, error) {
	if s.index >= len(s.frames) {
		return nil, io.EOF
	}
	v := s.frames[s.index]
	s.index++
	return v, nil
}
func (s *fakeStream) Send(r *pluginv1.ForwardResponse) error {
	s.responses = append(s.responses, r)
	if s.onSend != nil {
		return s.onSend(r)
	}
	return nil
}

func forwardFrames(start *pluginv1.ForwardRequestStart, body string) []*pluginv1.ForwardRequest {
	frames := []*pluginv1.ForwardRequest{{Frame: &pluginv1.ForwardRequest_Start{Start: start}}}
	for i := 0; i < len(body); i += 3 {
		end := i + 3
		if end > len(body) {
			end = len(body)
		}
		frames = append(frames, &pluginv1.ForwardRequest{Frame: &pluginv1.ForwardRequest_BodyChunk{BodyChunk: []byte(body[i:end])}})
	}
	return append(frames, &pluginv1.ForwardRequest{Frame: &pluginv1.ForwardRequest_BodyEnd{BodyEnd: true}})
}

func TestForwardStreamsOriginalBodyAndResponseViaSelectedProxy(t *testing.T) {
	s, _ := testServer(t)
	payload := "arbitrary streaming body: no model inspection"
	responseBody := "data: {\"type\":\"response.created\"}\n\ndata: [DONE]\n\n"
	var calls atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		raw, _ := io.ReadAll(r.Body)
		if string(raw) != payload || r.Header.Get(core.StateHeader) != "client-state" || r.URL.Host != "unresolvable.invalid" {
			t.Error("request changed", string(raw), r.Header, r.URL)
		}
		w.Header().Add("X-Multi", "one")
		w.Header().Add("X-Multi", "two")
		w.WriteHeader(201)
		_, _ = io.WriteString(w, responseBody)
	}))
	defer proxy.Close()
	start := &pluginv1.ForwardRequestStart{Method: "POST", Url: "http://unresolvable.invalid/responses", ProxyUrl: proxy.URL, AccountId: 123, OutboundModel: "gpt-6-astra", IdentityRevision: "identity-1", ConfigRevision: 1, HasBody: true, ContentLength: -1, Headers: map[string]*pluginv1.HeaderValues{core.StateHeader: {Values: []string{"client-state"}}}}
	stream := &fakeStream{ctx: context.Background(), frames: forwardFrames(start, payload)}
	if err := s.Forward(stream); err != nil {
		t.Fatal(err)
	}
	var received strings.Builder
	var responseStart *pluginv1.ForwardResponseStart
	var end *pluginv1.ForwardResponseEnd
	for _, frame := range stream.responses {
		if e := frame.GetError(); e != nil {
			t.Fatal(e)
		}
		if v := frame.GetStart(); v != nil {
			responseStart = v
		}
		received.Write(frame.GetBodyChunk())
		if v := frame.GetEnd(); v != nil {
			end = v
		}
	}
	if calls.Load() != 1 || received.String() != responseBody || responseStart == nil || responseStart.StatusCode != 201 || len(responseStart.Headers["X-Multi"].Values) != 2 || end == nil || end.BytesReceived != int64(len(responseBody)) {
		t.Fatalf("nontransparent forward calls=%d start=%v end=%v body=%q", calls.Load(), responseStart, end, received.String())
	}
}

func TestForwardNoReplayAndStrictFailureBeforeUpstream(t *testing.T) {
	s, _ := testServer(t)
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Redirect(w, r, "/again", http.StatusTemporaryRedirect)
	}))
	defer upstream.Close()
	start := &pluginv1.ForwardRequestStart{Method: "GET", Url: upstream.URL, AccountId: 123, OutboundModel: "gpt-6-astra", IdentityRevision: "identity-1", ConfigRevision: 1}
	stream := &fakeStream{ctx: context.Background(), frames: forwardFrames(start, "")}
	if err := s.Forward(stream); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 || len(stream.responses) != 1 || stream.responses[0].GetError().RequestSent {
		t.Fatal("strict missing ticket sent upstream")
	}
	start.Headers = map[string]*pluginv1.HeaderValues{core.StateHeader: {Values: []string{"client-state"}}}
	stream = &fakeStream{ctx: context.Background(), frames: forwardFrames(start, "")}
	if err := s.Forward(stream); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || stream.responses[0].GetStart().StatusCode != 307 {
		t.Fatal("redirect replayed")
	}
}

func TestSameRevisionDraftThenActiveAndPauseRejectsNewForward(t *testing.T) {
	s, h := testServer(t)
	paused, err := s.ApplyConfig(context.Background(), &pluginv1.ApplyConfigRequest{ConfigJson: []byte(rpcConfig), ConfigRevision: 1, RuntimeActive: false})
	if err != nil || !paused.Applied {
		t.Fatal(paused, err)
	}
	request := &pluginv1.ForwardRequestStart{Method: "GET", Url: "http://invalid.invalid", AccountId: 123, OutboundModel: "gpt-6-astra", IdentityRevision: "identity-1", ConfigRevision: 1}
	stream := &fakeStream{ctx: context.Background(), frames: forwardFrames(request, "")}
	if err = s.Forward(stream); err != nil {
		t.Fatal(err)
	}
	if stream.responses[0].GetError().Code != "plugin_admission_runtime_inactive" {
		t.Fatal("paused forward allowed")
	}
	admit, _ := s.AdmitBatch(context.Background(), &pluginv1.AdmitBatchRequest{ConfigRevision: 1, Candidates: []*pluginv1.AdmissionCandidate{{AccountId: 123, OutboundModel: "gpt-6-astra", IdentityRevision: "identity-1"}}})
	if admit.Decisions[0].Allowed || admit.Decisions[0].Reason != "runtime_inactive" {
		t.Fatal("paused admission allowed")
	}
	h.mu.Lock()
	before := len(h.states)
	h.mu.Unlock()
	active, err := s.ApplyConfig(context.Background(), &pluginv1.ApplyConfigRequest{ConfigJson: []byte(rpcConfig), ConfigRevision: 1, RuntimeActive: true})
	if err != nil || !active.Applied || !s.engine.RuntimeActive() {
		t.Fatal(active, err)
	}
	h.mu.Lock()
	after := len(h.states)
	h.mu.Unlock()
	if before != after {
		t.Fatal("activation mutated persistent state")
	}
}

func TestForwardAdmissionErrorCodesNeverSpendFailoverBudget(t *testing.T) {
	s, _ := testServer(t)
	for _, tc := range []struct {
		name     string
		revision uint64
		identity string
		code     string
	}{
		{"configuration", 2, "identity-1", "plugin_admission_config_revision_mismatch"},
		{"missing_state", 1, "identity-1", "plugin_admission_state_unavailable"},
		{"identity_changed", 1, "old-identity", "plugin_admission_state_unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start := &pluginv1.ForwardRequestStart{Method: "POST", Url: "http://invalid.invalid", AccountId: 123, OutboundModel: "gpt-6-astra", IdentityRevision: tc.identity, ConfigRevision: tc.revision, HasBody: true, ContentLength: -1}
			stream := &fakeStream{ctx: context.Background(), frames: forwardFrames(start, "unconsumed body")}
			if err := s.Forward(stream); err != nil {
				t.Fatal(err)
			}
			if len(stream.responses) != 1 || stream.responses[0].GetError() == nil {
				t.Fatal("missing admission error")
			}
			errFrame := stream.responses[0].GetError()
			if errFrame.Code != tc.code || errFrame.RequestSent || stream.index != 1 {
				t.Fatalf("wrong admission semantics: %v, frames read=%d", errFrame, stream.index)
			}
		})
	}
}
