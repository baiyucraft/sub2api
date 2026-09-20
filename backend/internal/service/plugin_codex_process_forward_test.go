package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pluginv1 "github.com/Wei-Shaw/sub2api/pkg/pluginapi/v1"
	"github.com/stretchr/testify/require"
)

const (
	codexProcessPluginKey = "baiyu.codex-state"
	codexProcessNamespace = "codex-state-v1"
	codexProcessAccountID = int64(42)
	codexProcessRevision  = uint64(9)
	codexProcessIdentity  = "synthetic-identity-v1"
	codexProcessModel     = "gpt-6-astra"
	codexProcessHeader    = "X-Codex-Turn-State"
)

// This opt-in test crosses the real process/broker boundary without linking the
// independent plugin module. All tickets, identities and persistent state are
// synthetic. Both proxies and the target are loopback-only; no proxy forwards.
func TestPluginCodexStateRealProcessForwardAndCompletion(t *testing.T) {
	path := os.Getenv("SUB2API_TEST_CODEX_STATE_BINARY")
	if path == "" {
		t.Skip("SUB2API_TEST_CODEX_STATE_BINARY not supplied; independent process test skipped")
	}
	path, err := filepath.Abs(path)
	require.NoError(t, err)
	binaryBytes, err := os.ReadFile(path)
	require.NoError(t, err)
	binaryHash := sha256.Sum256(binaryBytes)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var directRequests, rejectedProxyRequests atomic.Int64
	target := codexProcessLocalServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		directRequests.Add(1)
		http.Error(w, "direct transport forbidden", http.StatusForbidden)
	}))
	targetURL, err := url.Parse(target.URL)
	require.NoError(t, err)
	type observedRequest struct {
		method, uri, host string
		headers           http.Header
		body              []byte
		contentLength     int64
	}
	observed := make(chan observedRequest, 4)
	sse := []byte(": synthetic fixture\r\n\r\nevent: response.output_text.delta\r\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"fixture\"}\r\n\r\nevent: response.completed\r\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"model\":\"gpt-6-astra\"}}\r\n\r\ndata: [DONE]\r\n\r\n")
	proxy := codexProcessLocalServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// In particular, never tunnel a harvest CONNECT to the real upstream.
		if r.Method != http.MethodPost || r.URL.Scheme != "http" || r.URL.Host != targetURL.Host ||
			r.Host != targetURL.Host || !strings.HasPrefix(r.URL.Path, "/synthetic/responses/") {
			rejectedProxyRequests.Add(1)
			http.Error(w, "unexpected proxy request", http.StatusForbidden)
			return
		}
		defer r.Body.Close()
		body, readErr := io.ReadAll(io.LimitReader(r.Body, 4097))
		if readErr != nil || len(body) > 4096 {
			rejectedProxyRequests.Add(1)
			http.Error(w, "invalid fixture body", http.StatusBadRequest)
			return
		}
		select {
		case observed <- observedRequest{r.Method, r.RequestURI, r.Host, r.Header.Clone(), body, r.ContentLength}:
		default:
			rejectedProxyRequests.Add(1)
			http.Error(w, "unexpected request replay", http.StatusConflict)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Synthetic-Fixture", "preserved")
		for _, chunk := range [][]byte{sse[:7], sse[7:51], sse[51:]} {
			if _, writeErr := w.Write(chunk); writeErr != nil {
				return
			}
			w.(http.Flusher).Flush()
		}
	}))

	issued := time.Now().UTC().Truncate(time.Second).Add(-time.Minute)
	active := codexProcessSyntheticTicket(issued, 0x31)
	clientState := codexProcessSyntheticTicket(issued, 0x52).State
	slot := codexProcessSlot{
		Guard:  codexProcessGuard{codexProcessAccountID, codexProcessModel, strconv.FormatUint(codexProcessRevision, 10), codexProcessIdentity},
		Active: active, NextVersion: 1, Strikes: 1, Paused: true,
	}
	slotJSON, err := json.Marshal(slot)
	require.NoError(t, err)
	guardHash := sha256.Sum256([]byte(slot.Guard.ConfigRevision + "\x00" + slot.Guard.IdentityRevision))
	store := &codexProcessStateStore{
		key:   fmt.Sprintf("slots/%d/%s/%x", codexProcessAccountID, codexProcessModel, guardHash),
		value: slotJSON, version: 1,
	}
	installation := &PluginInstallation{
		ID: 17, PluginKey: codexProcessPluginKey, Version: "0.1.0", BinaryPath: path,
		BinarySHA256: hex.EncodeToString(binaryHash[:]), ConfigRevision: codexProcessRevision, State: PluginStateEnabled,
		Manifest: PluginManifest{Requires: PluginRequirements{HostServiceAPI: 2, HostFeatures: append([]string(nil), pluginv1.HostFeatures...)}},
		Bindings: []PluginBinding{{Capability: PluginCapabilityOpenAIOAuthOutbound, Platform: PlatformOpenAI, AccountType: AccountTypeOAuth, Enabled: true, RolloutPercent: 100}},
	}
	repo := &codexProcessAuthorityRepository{installation: *installation, requests: make(map[string]struct{})}
	directory := &codexProcessDirectory{proxyURL: proxy.URL}
	host := newPluginHostServiceServer(installation.PluginKey, newFakePluginKVStore(), directory)
	host.stateStore, host.runtimeAuthorityRepo = store, repo
	host.allowSetupToken = true
	socketDir := filepath.Join(t.TempDir(), "runtime")
	require.NoError(t, os.MkdirAll(socketDir, 0o700))
	runtime, err := startPluginRuntime(ctx, installation, 15*time.Second, socketDir, host)
	require.NoError(t, err)
	defer runtime.kill()
	stopOnTimeout := context.AfterFunc(ctx, runtime.kill)
	defer stopOnTimeout()

	// A persisted pause blocks collection. Local-only harvest/dial proxies and
	// the store's mutation guard are independent defenses against fixture drift.
	config, err := json.Marshal(map[string]any{
		"version": 1, "enabled": true, "harvest_proxy_url": proxy.URL, "dial_proxy_url": proxy.URL,
		"accounts": []any{map[string]any{"account_id": codexProcessAccountID, "models": map[string]any{
			codexProcessModel: map[string]any{"enabled": true, "ticket_plan": "pro"},
		}}},
	})
	require.NoError(t, err)
	applyCtx, stopApply := context.WithTimeout(ctx, 10*time.Second)
	defer stopApply()
	canonical, scope, err := runtime.validateScopedConfig(applyCtx, config)
	require.NoError(t, err)
	require.Equal(t, []PluginManagedTarget{{AccountID: codexProcessAccountID, Models: []string{codexProcessModel}}}, scope)
	require.NoError(t, runtime.applyScopedConfig(applyCtx, canonical, codexProcessRevision, true))
	stopApply()

	for _, scenario := range []struct {
		name, clientState string
	}{
		// Start with a strike so an accidental receipt for client-owned state
		// would cause an observable CAS rather than a no-op strike reset.
		{name: "client-state-precedence", clientState: clientState},
		{name: "injected-state"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			requestCtx, stop := context.WithTimeout(ctx, 15*time.Second)
			defer stop()
			admitted, err := runtime.api.AdmitBatch(requestCtx, &pluginv1.AdmitBatchRequest{
				ConfigRevision: codexProcessRevision,
				Candidates:     []*pluginv1.AdmissionCandidate{{AccountId: codexProcessAccountID, OutboundModel: codexProcessModel, IdentityRevision: codexProcessIdentity}},
			})
			require.NoError(t, err)
			require.NotNil(t, admitted)
			require.Equal(t, codexProcessRevision, admitted.ConfigRevision)
			require.Len(t, admitted.Decisions, 1)
			require.True(t, admitted.Decisions[0].Allowed, admitted.Decisions[0].Reason)
			require.Equal(t, codexProcessAccountID, admitted.Decisions[0].AccountId)
			require.Equal(t, codexProcessIdentity, admitted.Decisions[0].IdentityRevision)
			require.Equal(t, codexProcessModel, admitted.Decisions[0].OutboundModel)

			before, writesBefore := store.snapshot()
			requestID := "synthetic-" + scenario.name
			require.Zero(t, runtime.inFlight.Load())
			require.True(t, runtime.beginRequest())
			require.NoError(t, repo.BeginPluginRequest(requestCtx, installation.ID, installation.BinarySHA256, codexProcessRevision, requestID))
			type completion struct {
				err     error
				strikes int
				writes  int
			}
			completed := make(chan completion, 1)
			var completions atomic.Int64
			require.NoError(t, runtime.registerRequestCompletion(requestID, func() {
				finishCtx, cancelFinish := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancelFinish()
				current, writes := store.snapshot()
				endErr := repo.EndPluginRequest(finishCtx, installation.ID, requestID)
				completions.Add(1)
				completed <- completion{endErr, current.Strikes, writes}
			}))
			require.EqualValues(t, 1, runtime.inFlight.Load())
			count, err := repo.PluginRequestsInFlight(requestCtx, installation.ID)
			require.NoError(t, err)
			require.EqualValues(t, 1, count)

			headers := http.Header{"Content-Type": {"application/json"}, "Authorization": {"Bearer synthetic-forward-only"}}
			if scenario.clientState != "" {
				headers.Set(codexProcessHeader, scenario.clientState)
			}
			body := []byte("{\n  \"model\": \"gpt-6-astra\", \"stream\": true, \"input\": \"synthetic-only\"\n}")
			requestURL := target.URL + "/synthetic/responses/" + scenario.name
			// Deliberately use raw Forward, not runtime.roundTrip: consuming End
			// here cannot release the registry. Only the real broker RPC can do so.
			stream, err := runtime.api.Forward(requestCtx)
			require.NoError(t, err)
			require.NoError(t, stream.Send(&pluginv1.ForwardRequest{Frame: &pluginv1.ForwardRequest_Start{Start: &pluginv1.ForwardRequestStart{
				RequestId: requestID, Method: http.MethodPost, Url: requestURL, Host: targetURL.Host, Headers: headersToPlugin(headers),
				ProxyUrl: proxy.URL, AccountId: codexProcessAccountID, Platform: PlatformOpenAI, AccountType: AccountTypeOAuth,
				HasBody: true, ContentLength: int64(len(body)), OutboundModel: codexProcessModel,
				IdentityRevision: codexProcessIdentity, ConfigRevision: codexProcessRevision,
			}}}))
			for _, part := range [][]byte{body[:11], body[11:]} {
				require.NoError(t, stream.Send(&pluginv1.ForwardRequest{Frame: &pluginv1.ForwardRequest_BodyChunk{BodyChunk: part}}))
			}
			require.NoError(t, stream.Send(&pluginv1.ForwardRequest{Frame: &pluginv1.ForwardRequest_BodyEnd{BodyEnd: true}}))
			require.NoError(t, stream.CloseSend())
			var received bytes.Buffer
			var responseStart *pluginv1.ForwardResponseStart
			var responseEnd *pluginv1.ForwardResponseEnd
			for frames := 0; ; frames++ {
				require.Less(t, frames, 128, "bounded synthetic response")
				frame, recvErr := stream.Recv()
				if recvErr == io.EOF {
					break
				}
				require.NoError(t, recvErr)
				require.NotNil(t, frame)
				switch value := frame.Frame.(type) {
				case *pluginv1.ForwardResponse_Start:
					require.Nil(t, responseStart)
					require.Nil(t, responseEnd)
					responseStart = value.Start
				case *pluginv1.ForwardResponse_BodyChunk:
					require.NotNil(t, responseStart)
					require.Nil(t, responseEnd)
					require.LessOrEqual(t, received.Len()+len(value.BodyChunk), len(sse))
					_, _ = received.Write(value.BodyChunk)
				case *pluginv1.ForwardResponse_End:
					require.NotNil(t, responseStart)
					require.Nil(t, responseEnd)
					responseEnd = value.End
				case *pluginv1.ForwardResponse_Error:
					t.Fatalf("synthetic Forward failed: %s", value.Error.GetCode())
				default:
					t.Fatal("unexpected Forward frame")
				}
			}
			require.NotNil(t, responseStart)
			require.EqualValues(t, http.StatusOK, responseStart.StatusCode)
			require.Equal(t, "text/event-stream", headersFromPlugin(responseStart.Headers).Get("Content-Type"))
			require.Equal(t, "preserved", headersFromPlugin(responseStart.Headers).Get("X-Synthetic-Fixture"))
			require.Equal(t, sse, received.Bytes(), "SSE framing, whitespace and bytes must be untouched")
			require.NotNil(t, responseEnd)
			require.EqualValues(t, len(sse), responseEnd.BytesReceived)
			select {
			case got := <-observed:
				require.Equal(t, http.MethodPost, got.method)
				require.Equal(t, requestURL, got.uri)
				require.Equal(t, targetURL.Host, got.host)
				require.Equal(t, body, got.body)
				require.EqualValues(t, len(body), got.contentLength)
				require.Equal(t, "Bearer synthetic-forward-only", got.headers.Get("Authorization"))
				wantState := scenario.clientState
				if wantState == "" {
					wantState = active.State
				}
				require.Equal(t, []string{wantState}, got.headers.Values(codexProcessHeader))
			case <-requestCtx.Done():
				t.Fatal("local proxy did not observe Forward")
			}
			select {
			case done := <-completed:
				require.NoError(t, done.err)
				if scenario.clientState == "" {
					require.Zero(t, done.strikes, "completion must follow synchronous receipt persistence")
					require.Equal(t, writesBefore+1, done.writes)
				} else {
					require.Equal(t, before.Strikes, done.strikes)
					require.Equal(t, writesBefore, done.writes, "client-owned state must not create a plugin receipt")
				}
			case <-requestCtx.Done():
				t.Fatal("real plugin did not acknowledge CompleteRequest through the broker")
			}
			require.Eventually(t, func() bool { return runtime.inFlight.Load() == 0 }, time.Second, 10*time.Millisecond)
			require.EqualValues(t, 1, completions.Load())
			require.False(t, runtime.requests.contains(requestID))
			count, err = repo.PluginRequestsInFlight(requestCtx, installation.ID)
			require.NoError(t, err)
			require.Zero(t, count)
			require.False(t, runtime.exited.Load(), "process exit must not substitute for completion RPC")
			require.False(t, runtime.client.Exited())
		})
	}

	// Per-request checks above proved completion while the process was alive.
	// Stop it before final safety counters so no supervisor work escapes review.
	runtime.kill()
	require.Zero(t, directRequests.Load(), "Forward must use the explicit local proxy")
	require.Zero(t, rejectedProxyRequests.Load(), "no harvest CONNECT, external destination or replay is allowed")
	require.Empty(t, observed)
	require.Zero(t, store.unexpected.Load(), "state access must stay in the synthetic paused slot")
	require.Zero(t, directory.unexpected.Load())
	require.Positive(t, repo.reads.Load(), "broker operations must pass the live authority wrapper")
	finalSlot, _ := store.snapshot()
	require.True(t, finalSlot.Paused)
	require.False(t, finalSlot.Harvesting)
	require.False(t, finalSlot.HarvestRequested)
}

func codexProcessLocalServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	server := httptest.NewUnstartedServer(handler)
	server.Config.ReadHeaderTimeout = 3 * time.Second
	server.Config.ReadTimeout = 5 * time.Second
	server.Config.WriteTimeout = 10 * time.Second
	server.Start()
	t.Cleanup(func() {
		server.CloseClientConnections()
		server.Close()
	})
	endpoint, err := url.Parse(server.URL)
	require.NoError(t, err)
	ip := net.ParseIP(endpoint.Hostname())
	require.NotNil(t, ip)
	require.True(t, ip.IsLoopback(), "synthetic servers must never bind an external address")
	return server
}

// These are serialized fixture shapes, not a dependency on plugin core types.
type codexProcessGuard struct {
	AccountID        int64  `json:"account_id"`
	Model            string `json:"model"`
	ConfigRevision   string `json:"config_revision"`
	IdentityRevision string `json:"identity_revision"`
}

type codexProcessTicket struct {
	State       string    `json:"state"`
	Fingerprint string    `json:"fingerprint"`
	Version     uint64    `json:"version"`
	CapturedAt  time.Time `json:"captured_at"`
	IssuedAt    time.Time `json:"issued_at"`
	ExpiresAt   time.Time `json:"expires_at"`
}

type codexProcessSlot struct {
	Guard            codexProcessGuard  `json:"guard"`
	Active           codexProcessTicket `json:"active"`
	NextVersion      uint64             `json:"next_version"`
	Strikes          int                `json:"strikes"`
	Paused           bool               `json:"paused"`
	Harvesting       bool               `json:"harvesting"`
	HarvestRequested bool               `json:"harvest_requested"`
}

func codexProcessSyntheticTicket(issued time.Time, seed byte) codexProcessTicket {
	// Observable envelope framing: marker, big-endian time and 10 pro blocks.
	// No real ciphertext, signature, credentials or provider state is involved.
	raw := bytes.Repeat([]byte{seed}, 57+16*10)
	raw[0] = 0x80
	binary.BigEndian.PutUint64(raw[1:9], uint64(issued.Unix()))
	digest := sha256.Sum256(raw)
	return codexProcessTicket{base64.RawURLEncoding.EncodeToString(raw), hex.EncodeToString(digest[:]), 1, issued, issued, issued.Add(time.Hour)}
}

type codexProcessAuthorityRepository struct {
	PluginRepository
	installation PluginInstallation
	reads        atomic.Int64
	mu           sync.Mutex
	requests     map[string]struct{}
}

func (r *codexProcessAuthorityRepository) GetByID(ctx context.Context, id int64) (*PluginInstallation, error) {
	r.reads.Add(1)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if id != r.installation.ID {
		return nil, ErrPluginStateChanged
	}
	copy := r.installation
	copy.Bindings = append([]PluginBinding(nil), copy.Bindings...)
	return &copy, nil
}

func (r *codexProcessAuthorityRepository) BeginPluginRequest(ctx context.Context, id int64, sha string, revision uint64, requestID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if id != r.installation.ID || sha != r.installation.BinarySHA256 || revision != r.installation.ConfigRevision || r.installation.State != PluginStateEnabled {
		return ErrPluginStateChanged
	}
	if _, exists := r.requests[requestID]; exists {
		return ErrPluginStateChanged
	}
	r.requests[requestID] = struct{}{}
	return nil
}

func (r *codexProcessAuthorityRepository) EndPluginRequest(ctx context.Context, id int64, requestID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if id != r.installation.ID {
		return ErrPluginStateChanged
	}
	delete(r.requests, requestID)
	return nil
}

func (r *codexProcessAuthorityRepository) PluginRequestsInFlight(ctx context.Context, id int64) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	if id != r.installation.ID {
		return 0, ErrPluginStateChanged
	}
	return int64(len(r.requests)), nil
}

type codexProcessDirectory struct {
	proxyURL   string
	unexpected atomic.Int64
}

func (d *codexProcessDirectory) ListPluginAccounts(context.Context, string, string) ([]int64, error) {
	return []int64{codexProcessAccountID}, nil
}

func (d *codexProcessDirectory) ResolvePluginOutboundIdentity(_ context.Context, id int64) (*PluginOutboundIdentity, error) {
	if id != codexProcessAccountID {
		d.unexpected.Add(1)
		return nil, errors.New("unexpected synthetic identity")
	}
	return &PluginOutboundIdentity{
		AccountID: id, Platform: PlatformOpenAI, AccountType: AccountTypeOAuth,
		Token: "synthetic-directory-only", IdentityRevision: codexProcessIdentity,
		ProxyURL: d.proxyURL, Egresses: []PluginOutboundEgress{{ProxyID: 1, ProxyURL: d.proxyURL}},
	}, nil
}

func (d *codexProcessDirectory) ListPluginResources(context.Context) (*PluginResources, error) {
	resources := newPluginResources()
	resources.Accounts = []PluginResourceAccount{{ID: codexProcessAccountID, Name: "synthetic-only", Platform: PlatformOpenAI, AccountType: AccountTypeOAuth, GroupIDs: []int64{}, BusinessEgressConfigured: true}}
	return resources, nil
}

func (d *codexProcessDirectory) ResolvePluginProxy(_ context.Context, id int64) (string, error) {
	if id != 1 {
		d.unexpected.Add(1)
		return "", errors.New("unexpected synthetic proxy")
	}
	return d.proxyURL, nil
}

type codexProcessStateStore struct {
	mu         sync.Mutex
	key        string
	value      []byte
	version    int64
	lease      PluginLease
	fence      int64
	writes     int
	unexpected atomic.Int64
}

func (s *codexProcessStateStore) check(ctx context.Context, pluginKey, namespace, key string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if pluginKey != codexProcessPluginKey || namespace != codexProcessNamespace || key != s.key {
		s.unexpected.Add(1)
		return errors.New("unexpected synthetic state access")
	}
	return nil
}

func (s *codexProcessStateStore) snapshot() (codexProcessSlot, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var slot codexProcessSlot
	_ = json.Unmarshal(s.value, &slot)
	return slot, s.writes
}

func (s *codexProcessStateStore) StateGet(ctx context.Context, pluginKey, namespace, key string) (PluginStateRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(ctx, pluginKey, namespace, key); err != nil {
		return PluginStateRecord{}, err
	}
	return PluginStateRecord{Key: key, Value: bytes.Clone(s.value), Version: s.version, Found: true}, nil
}

func (s *codexProcessStateStore) StateCAS(ctx context.Context, pluginKey, namespace, key string, mutation PluginStateMutation) (PluginStateRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(ctx, pluginKey, namespace, key); err != nil {
		return PluginStateRecord{}, err
	}
	if mutation.ExpectedVersion != s.version {
		return PluginStateRecord{}, ErrPluginStateConflict
	}
	if mutation.Lease == nil || !s.validLease(*mutation.Lease) {
		return PluginStateRecord{}, ErrPluginLeaseLost
	}
	var slot codexProcessSlot
	if json.Unmarshal(mutation.Value, &slot) != nil || !slot.Paused || slot.Harvesting || slot.HarvestRequested {
		s.unexpected.Add(1)
		return PluginStateRecord{}, errors.New("synthetic fixture forbids collection")
	}
	s.value, s.version = bytes.Clone(mutation.Value), s.version+1
	s.writes++
	return PluginStateRecord{Key: key, Value: bytes.Clone(s.value), Version: s.version, Found: true}, nil
}

func (s *codexProcessStateStore) StateDelete(context.Context, string, string, string, PluginStateMutation) (PluginStateRecord, error) {
	s.unexpected.Add(1)
	return PluginStateRecord{}, errors.New("synthetic fixture forbids deletion")
}

func (s *codexProcessStateStore) StateList(context.Context, string, string, string, string, int) ([]PluginStateRecord, string, error) {
	s.unexpected.Add(1)
	return nil, "", errors.New("synthetic fixture forbids listing state")
}

func (s *codexProcessStateStore) validLease(lease PluginLease) bool {
	return lease.Owner != "" && lease.Owner == s.lease.Owner && lease.Fence == s.lease.Fence && time.Now().Before(s.lease.ExpiresAt)
}

func (s *codexProcessStateStore) LeaseAcquire(ctx context.Context, pluginKey, namespace, key string, ttl time.Duration) (PluginLease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(ctx, pluginKey, namespace, key); err != nil {
		return PluginLease{}, err
	}
	if s.validLease(s.lease) {
		return PluginLease{}, ErrPluginStateConflict
	}
	s.fence++
	s.lease = PluginLease{Owner: fmt.Sprintf("synthetic-owner-%d", s.fence), Fence: s.fence, ExpiresAt: time.Now().Add(ttl)}
	return s.lease, nil
}

func (s *codexProcessStateStore) LeaseRenew(ctx context.Context, pluginKey, namespace, key string, lease PluginLease, ttl time.Duration) (PluginLease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(ctx, pluginKey, namespace, key); err != nil {
		return PluginLease{}, err
	}
	if !s.validLease(lease) {
		return PluginLease{}, ErrPluginLeaseLost
	}
	s.lease.ExpiresAt = time.Now().Add(ttl)
	return s.lease, nil
}

func (s *codexProcessStateStore) LeaseRelease(ctx context.Context, pluginKey, namespace, key string, lease PluginLease) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(ctx, pluginKey, namespace, key); err != nil {
		return err
	}
	if !s.validLease(lease) {
		return ErrPluginLeaseLost
	}
	s.lease = PluginLease{}
	return nil
}

var _ PluginStateStore = (*codexProcessStateStore)(nil)
var _ PluginRequestGuardRepository = (*codexProcessAuthorityRepository)(nil)
