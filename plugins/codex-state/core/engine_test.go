package core

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testModel = "gpt-6-astra"

type fakeHost struct {
	mu                  sync.Mutex
	identities          map[int64]Identity
	state               map[string]StoredState
	leases              map[string]Lease
	fences              map[string]uint64
	revision            string
	failRead, failWrite bool
	conflicts           int
	identityReads       int
	actionFinalFailure  bool
}

func newHost() *fakeHost {
	return &fakeHost{identities: map[int64]Identity{123: {AccountID: 123, Revision: "identity-1", Eligible: true, Egresses: []Egress{{ProxyURL: "http://egress-one:8080"}, {ProxyURL: "http://egress-two:8080"}}, Headers: http.Header{"Authorization": {"Bearer secret-token"}}}}, state: map[string]StoredState{}, leases: map[string]Lease{}, fences: map[string]uint64{}, revision: "1"}
}

func (h *fakeHost) AvailableAccounts(context.Context) (map[int64]bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	available := map[int64]bool{}
	for id, identity := range h.identities {
		if identity.Eligible {
			available[id] = true
		}
	}
	return available, nil
}
func (h *fakeHost) valid(g Guard) error {
	identity := h.identities[g.AccountID]
	if h.revision != g.ConfigRevision || identity.Revision != g.IdentityRevision || !identity.Eligible {
		return ErrStale
	}
	return nil
}
func (h *fakeHost) Identity(ctx context.Context, id int64) (Identity, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.identityReads++
	if ctx.Err() != nil {
		return Identity{}, ctx.Err()
	}
	return h.identities[id], nil
}
func (h *fakeHost) StateGet(ctx context.Context, key string, g Guard) (StoredState, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if ctx.Err() != nil {
		return StoredState{}, ctx.Err()
	}
	if h.failRead {
		return StoredState{}, ErrUnavailable
	}
	if err := h.valid(g); err != nil {
		return StoredState{}, err
	}
	record := h.state[key]
	record.Data = append([]byte(nil), record.Data...)
	return record, nil
}
func (h *fakeHost) StateCAS(ctx context.Context, m Mutation) (uint64, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	if h.failWrite {
		return 0, ErrUnavailable
	}
	if err := h.valid(m.Guard); err != nil {
		return 0, err
	}
	lease := h.leases[m.Key]
	if m.Lease.Key != m.Key || lease.Owner != m.Lease.Owner || lease.Fence != m.Lease.Fence || !time.Now().Before(lease.ExpiresAt) {
		return 0, ErrStale
	}
	if h.conflicts > 0 {
		h.conflicts--
		return 0, ErrConflict
	}
	if h.state[m.Key].Version != m.ExpectedVersion {
		return 0, ErrConflict
	}
	if h.actionFinalFailure && strings.HasPrefix(m.Key, "actions/") {
		var r actionRecord
		_ = json.Unmarshal(m.Data, &r)
		if r.Done {
			h.actionFinalFailure = false
			return 0, ErrUnavailable
		}
	}
	version := m.ExpectedVersion + 1
	h.state[m.Key] = StoredState{Version: version, Data: append([]byte(nil), m.Data...)}
	return version, nil
}
func (h *fakeHost) LeaseAcquire(ctx context.Context, r LeaseRequest) (Lease, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if ctx.Err() != nil {
		return Lease{}, ctx.Err()
	}
	if err := h.valid(r.Guard); err != nil {
		return Lease{}, err
	}
	if old := h.leases[r.Key]; time.Now().Before(old.ExpiresAt) {
		return Lease{}, ErrLeaseBusy
	}
	h.fences[r.Key]++
	l := Lease{r.Key, r.Owner, h.fences[r.Key], time.Now().Add(r.TTL), r.Guard}
	h.leases[r.Key] = l
	return l, nil
}
func (h *fakeHost) LeaseRenew(ctx context.Context, l Lease, d time.Duration) (Lease, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	current := h.leases[l.Key]
	if current.Owner != l.Owner || current.Fence != l.Fence || !time.Now().Before(current.ExpiresAt) {
		return Lease{}, ErrStale
	}
	current.ExpiresAt = time.Now().Add(d)
	h.leases[l.Key] = current
	return current, nil
}
func (h *fakeHost) LeaseRelease(ctx context.Context, l Lease) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if old := h.leases[l.Key]; old.Owner == l.Owner && old.Fence == l.Fence {
		delete(h.leases, l.Key)
	}
	return nil
}
func (h *fakeHost) getSlot() Slot {
	h.mu.Lock()
	defer h.mu.Unlock()
	var s Slot
	_ = json.Unmarshal(h.state[slotKey(123, testModel)].Data, &s)
	return s
}
func (h *fakeHost) seed(slot Slot) {
	h.mu.Lock()
	defer h.mu.Unlock()
	raw, _ := json.Marshal(slot)
	key := slotKey(123, testModel)
	h.state[key] = StoredState{Version: h.state[key].Version + 1, Data: raw}
}

type probeFunc func(context.Context, ProbeRequest) (ProbeResult, error)

func (f probeFunc) Probe(ctx context.Context, r ProbeRequest) (ProbeResult, error) { return f(ctx, r) }

func stateAt(at time.Time, blocks int, marker byte) string {
	raw := make([]byte, 57+16*blocks)
	raw[0] = 0x80
	binary.BigEndian.PutUint64(raw[1:9], uint64(at.Unix()))
	raw[25] = marker
	return base64.URLEncoding.EncodeToString(raw)
}
func ticketAt(at time.Time, version uint64, marker byte) *Ticket {
	state := stateAt(at, 10, marker)
	env, _ := ParseEnvelope(state, "pro", at)
	return &Ticket{State: state, Fingerprint: env.Fingerprint, Version: version, CapturedAt: at, IssuedAt: env.IssuedAt, ExpiresAt: env.ExpiresAt}
}
func configJSON(proxy string) []byte {
	raw, _ := json.Marshal(Config{Version: 1, Enabled: true, HarvestProxyURL: proxy, Accounts: []AccountConfig{{AccountID: 123, Models: map[string]ModelConfig{testModel: {Enabled: true, TicketPlan: "pro"}, "gpt-5.6-sol": {Enabled: false, TicketPlan: "team"}}}}})
	return raw
}
func engineFor(t *testing.T, h *fakeHost, probe Prober, proxy string, opts Options) *Engine {
	t.Helper()
	if probe == nil {
		probe = probeFunc(func(context.Context, ProbeRequest) (ProbeResult, error) {
			return ProbeResult{}, errors.New("unexpected probe")
		})
	}
	if opts.PollInterval == 0 {
		opts.PollInterval = time.Hour
	}
	if opts.RetryDelay == 0 {
		opts.RetryDelay = time.Millisecond
	}
	e := New(h, probe, opts)
	if err := e.Apply(configJSON(proxy), "1", true); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := e.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return e
}
func baseSlot(now time.Time) Slot {
	return Slot{Guard: Guard{123, testModel, "1", "identity-1"}, Active: ticketAt(now, 1, 1), Ready: ticketAt(now, 2, 2), NextVersion: 2}
}
func eventually(t *testing.T, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition did not become true")
}

func TestValidateRejectsRevisionAndDoesNotStartWork(t *testing.T) {
	for _, raw := range []string{`{"version":1,"revision":"fake"}`, `{"version":1,"config_revision":1}`, `{"version":2}`, `{"version":1,"accounts":[{"account_id":1,"models":{"unknown":{"enabled":true,"ticket_plan":"pro"}}}]}`, `{"version":1} {}`} {
		if _, err := Validate([]byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	if _, err := Validate([]byte(`{"version":1,"enabled":false,"harvest_proxy_url":"http://user:secret@proxy:80","dial_proxy_url":"","accounts":[]}`)); err != nil {
		t.Fatal(err)
	}
	h := newHost()
	var calls atomic.Int32
	e := New(h, probeFunc(func(context.Context, ProbeRequest) (ProbeResult, error) { calls.Add(1); return ProbeResult{}, nil }), Options{})
	defer e.Close(context.Background())
	if err := e.Apply(configJSON("http://proxy:80"), "1", false); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 || e.Status(context.Background()).Running {
		t.Fatal("inactive apply started runtime")
	}
	if err := e.Apply(configJSON("http://changed:80"), "1", false); err == nil {
		t.Fatal("accepted revision reuse")
	}
}

func TestEnvelopeInternalTimeAndPlan(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	for _, tc := range []struct {
		name          string
		at            time.Time
		blocks        int
		valid, usable bool
	}{
		{"current", now, 10, true, true}, {"skew", now.Add(30 * time.Second), 10, true, true}, {"future", now.Add(31 * time.Second), 10, false, false}, {"cutoff", now.Add(-time.Hour + 30*time.Second), 10, true, false}, {"before_cutoff", now.Add(-time.Hour + 31*time.Second), 10, true, true}, {"expired", now.Add(-2 * time.Hour), 10, true, false}, {"wrong_plan", now, 12, false, false}, {"abnormal", now, 11, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, err := ParseEnvelope(stateAt(tc.at, tc.blocks, 1), "pro", now)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v", err)
			}
			if err == nil && env.Usable(now) != tc.usable {
				t.Fatal("wrong usability")
			}
		})
	}
	if abnormal(strings.Repeat("x", 312), now) {
		t.Fatal("length alone treated as abnormal")
	}
	if !abnormal(stateAt(now, 11, 1), now) {
		t.Fatal("missed decoded abnormal envelope")
	}
}

func TestPrepareStrictScopeClientStateAndPersistentPromotion(t *testing.T) {
	h := newHost()
	e := engineFor(t, h, nil, "", Options{})
	now := time.Now()
	slot := baseSlot(now)
	h.seed(slot)
	headers := make(http.Header)
	receipt, err := e.Prepare(context.Background(), 123, testModel, "identity-1", headers)
	if err != nil || receipt == nil || headers.Get(StateHeader) != slot.Active.State {
		t.Fatalf("prepare %v %v", receipt, err)
	}
	client := http.Header{StateHeader: {"client-original"}}
	r, err := e.Prepare(context.Background(), 123, testModel, "identity-1", client)
	if err != nil || r != nil || client.Get(StateHeader) != "client-original" {
		t.Fatal("client state replaced")
	}
	if _, err = e.Prepare(context.Background(), 123, testModel, "old-identity", make(http.Header)); !errors.Is(err, ErrStale) {
		t.Fatal("wrong identity accepted")
	}
	for _, scope := range []struct {
		id    int64
		model string
	}{{999, testModel}, {123, "gpt-5.6-sol"}, {123, "unknown"}} {
		if _, err = e.Prepare(context.Background(), scope.id, scope.model, "", make(http.Header)); err != nil {
			t.Fatal("unmanaged scope made strict")
		}
	}
	slot.Active = ticketAt(now.Add(-time.Hour), 1, 1)
	h.seed(slot)
	h.mu.Lock()
	h.failWrite = true
	h.mu.Unlock()
	if _, err = e.Prepare(context.Background(), 123, testModel, "identity-1", make(http.Header)); err == nil {
		t.Fatal("unpersisted ready promotion escaped")
	}
	h.mu.Lock()
	h.failWrite = false
	h.conflicts = 2
	h.mu.Unlock()
	headers = make(http.Header)
	r, err = e.Prepare(context.Background(), 123, testModel, "identity-1", headers)
	if err != nil || r == nil || r.TicketVersion != 3 || h.getSlot().Ready != nil {
		t.Fatalf("promotion: %v %v", r, err)
	}
	h.mu.Lock()
	h.failRead = true
	h.mu.Unlock()
	if _, err = e.Prepare(context.Background(), 123, testModel, "identity-1", make(http.Header)); err == nil {
		t.Fatal("cache fallback on read failure")
	}
}

func TestPrepareRequiresUsableEgressWithoutBlockingCompletion(t *testing.T) {
	for _, tc := range []struct {
		name     string
		egresses []Egress
	}{
		{"none", nil},
		{"empty_url", []Egress{{ProxyURL: ""}}},
		{"invalid_url", []Egress{{ProxyURL: "://invalid"}}},
		{"unsupported_scheme", []Egress{{ProxyURL: "ftp://egress:80"}}},
		{"invalid_port", []Egress{{ProxyURL: "http://egress:70000"}}},
		{"all_unusable", []Egress{{ProxyURL: ""}, {ProxyURL: "http://"}, {ProxyURL: " "}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHost()
			e := engineFor(t, h, nil, "", Options{})
			original := baseSlot(time.Now())
			original.Strikes = 1
			h.seed(original)
			receipt, err := e.Prepare(context.Background(), 123, testModel, "identity-1", make(http.Header))
			if err != nil || receipt == nil {
				t.Fatal("initial receipt unavailable", receipt, err)
			}
			h.mu.Lock()
			identity := h.identities[123]
			identity.Egresses = tc.egresses
			h.identities[123] = identity
			h.mu.Unlock()
			for _, clientState := range []string{"", "client-original"} {
				headers := make(http.Header)
				if clientState != "" {
					headers.Set(StateHeader, clientState)
				}
				if r, err := e.Prepare(context.Background(), 123, testModel, "identity-1", headers); !errors.Is(err, ErrUnavailable) || r != nil {
					t.Fatal("missing usable egress was admitted", r, err)
				}
				if headers.Get(StateHeader) != clientState {
					t.Fatal("rejected prepare changed client state")
				}
			}
			if err := e.Admit(context.Background(), 123, testModel, "identity-1"); !errors.Is(err, ErrUnavailable) {
				t.Fatal("admission ignored missing usable egress", err)
			}
			if h.getSlot().Strikes != 1 {
				t.Fatal("egress rejection changed watchdog strikes")
			}
			if err := e.RecordCompletion(*receipt, ""); err != nil || h.getSlot().Strikes != 0 {
				t.Fatal("egress removal prevented outstanding receipt cleanup", err)
			}
			h.mu.Lock()
			identity = h.identities[123]
			identity.Egresses = []Egress{{ProxyURL: ""}, {ProxyURL: "http://restored-egress:8080"}}
			h.identities[123] = identity
			h.mu.Unlock()
			headers := make(http.Header)
			restored, err := e.Prepare(context.Background(), 123, testModel, "identity-1", headers)
			if err != nil || restored == nil || *restored != *receipt || headers.Get(StateHeader) != original.Active.State {
				t.Fatal("restored egress did not reuse stable active", restored, err)
			}
			client := make(http.Header)
			client.Set(StateHeader, "client-original")
			if r, err := e.Prepare(context.Background(), 123, testModel, "identity-1", client); err != nil || r != nil || client.Get(StateHeader) != "client-original" {
				t.Fatal("restored egress lost client state priority", r, err)
			}
			if err := e.Admit(context.Background(), 123, testModel, "identity-1"); err != nil {
				t.Fatal("restored egress remained blocked", err)
			}
		})
	}
}

func TestWatchdogVersionSuccessResetAndFailClosed(t *testing.T) {
	h := newHost()
	e := engineFor(t, h, nil, "", Options{})
	h.seed(baseSlot(time.Now()))
	receipt, _ := e.Prepare(context.Background(), 123, testModel, "identity-1", make(http.Header))
	if err := e.RecordCompletion(*receipt, "model_mismatch"); err != nil {
		t.Fatal(err)
	}
	if h.getSlot().Strikes != 1 {
		t.Fatal("first strike")
	}
	if err := e.RecordCompletion(*receipt, ""); err != nil || h.getSlot().Strikes != 0 {
		t.Fatal("success did not reset")
	}
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := e.RecordCompletion(*receipt, "state_envelope"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	promoted := h.getSlot()
	if promoted.Active.Version != 3 || promoted.Strikes != 0 || promoted.Ready != nil {
		t.Fatalf("bad promotion %+v", promoted)
	}
	_ = e.RecordCompletion(*receipt, "model_mismatch")
	if h.getSlot().Strikes != 0 {
		t.Fatal("old observation hit new active")
	}
	current, _ := e.Prepare(context.Background(), 123, testModel, "identity-1", make(http.Header))
	h.mu.Lock()
	h.failWrite = true
	h.mu.Unlock()
	if err := e.RecordCompletion(*current, "model_mismatch"); err == nil {
		t.Fatal("watchdog failure lost")
	}
	h.mu.Lock()
	h.failWrite = false
	h.mu.Unlock()
	if _, err := e.Prepare(context.Background(), 123, testModel, "identity-1", make(http.Header)); err == nil {
		t.Fatal("failed watchdog allowed strict")
	}
}

func TestObserverCompletionOnlyTransparent(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name, body, header string
		want               int
	}{
		{"completed_mismatch", "event: response.completed\ndata: {\"response\":{\"status\":\"completed\",\"model\":\"other\"}}\n\n", "", 1},
		{"json_completed", `{"object":"response","status":"completed","model":"other"}`, "", 1},
		{"in_progress", `{"object":"response","status":"in_progress","model":"other"}`, stateAt(now, 11, 9), 0},
		{"header_partial", "data: {\"type\":\"response.created\",\"response\":{\"model\":\"other\"}}\n\n", stateAt(now, 11, 9), 0},
		{"completed_header", "data: {\"type\":\"response.completed\",\"response\":{\"model\":\"gpt-6-astra\"}}\n\n", stateAt(now, 11, 9), 1},
		{"unclosed_header", "data: {\"type\":\"response.completed\",\"response\":{\"model\":\"gpt-6-astra\"}}\n", stateAt(now, 11, 9), 0},
		{"unclosed_mismatch", "data: {\"type\":\"response.completed\",\"response\":{\"model\":\"other\"}}\n", "", 0},
		{"unclosed_no_lf", `data: {"type":"response.completed","response":{"model":"other"}}`, "", 0},
		{"truncated_sse_json", "data: {\"type\":\"response.completed\",\"response\":{\"model\":\"other\"}\n\n", "", 0},
		{"invalid_header_length", "data: {\"type\":\"response.completed\",\"response\":{\"model\":\"gpt-6-astra\"}}\n\n", strings.Repeat("x", 312), 0},
		{"incomplete", `{"object":"response","status":"completed","model":"other"`, stateAt(now, 11, 9), 0},
		{"empty_model", `{"object":"response","status":"completed","model":""}`, stateAt(now, 11, 9), 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHost()
			e := engineFor(t, h, nil, "", Options{})
			h.seed(baseSlot(now))
			r, _ := e.Prepare(context.Background(), 123, testModel, "identity-1", make(http.Header))
			response := &http.Response{StatusCode: 200, Header: http.Header{StateHeader: {tc.header}}, Body: io.NopCloser(strings.NewReader(tc.body))}
			e.ObserveResponse(r, response)
			if h.getSlot().Strikes != 0 {
				t.Fatal("header counted prematurely")
			}
			var got strings.Builder
			buf := make([]byte, 1)
			for {
				n, err := response.Body.Read(buf)
				got.Write(buf[:n])
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			_ = response.Body.Close()
			if got.String() != tc.body || h.getSlot().Strikes != tc.want {
				t.Fatalf("bytes changed or strikes=%d want=%d", h.getSlot().Strikes, tc.want)
			}
		})
	}
}

func TestObserverCloseAfterCancelledClientRequiresClosedSSEEvent(t *testing.T) {
	for _, tc := range []struct {
		name, suffix string
		want         int
	}{
		{"closed", "\n\n", 1},
		{"missing_blank_line", "\n", 0},
		{"missing_lf", "", 0},
		{"partial_crlf_delimiter", "\r\n\r", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHost()
			e := engineFor(t, h, nil, "", Options{})
			h.seed(baseSlot(time.Now()))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			receipt, err := e.Prepare(ctx, 123, testModel, "identity-1", make(http.Header))
			if err != nil {
				t.Fatal(err)
			}
			body := `data: {"type":"response.completed","response":{"model":"other"}}` + tc.suffix
			response := &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
			e.ObserveResponse(receipt, response)
			buf := make([]byte, len(body))
			if n, err := response.Body.Read(buf); n != len(body) || err != nil {
				t.Fatal(n, err)
			}
			cancel()
			if err := response.Body.Close(); err != nil {
				t.Fatal(err)
			}
			if string(buf) != body || h.getSlot().Strikes != tc.want {
				t.Fatal("Close changed bytes or counted an unclosed event")
			}
		})
	}
}

func TestHarvestPersistsStableActiveAndOrderedRepresentatives(t *testing.T) {
	h := newHost()
	now := time.Now()
	slot := baseSlot(now)
	slot.Ready = nil
	h.seed(slot)
	var mu sync.Mutex
	var probes []ProbeRequest
	probe := probeFunc(func(ctx context.Context, r ProbeRequest) (ProbeResult, error) {
		mu.Lock()
		probes = append(probes, r)
		mu.Unlock()
		if r.State == "" {
			return ProbeResult{State: stateAt(now, 10, 3), StatusCode: 200, Completed: true, Model: testModel}, nil
		}
		if r.ProxyURL == "http://egress-one:8080" {
			return ProbeResult{StatusCode: 500}, nil
		}
		return ProbeResult{StatusCode: 200, Completed: true, Model: testModel}, nil
	})
	e := engineFor(t, h, probe, "http://user-{sid}:secret@harvest:8080", Options{})
	eventually(t, func() bool { s := h.getSlot(); return s.Ready != nil && !s.Harvesting })
	saved := h.getSlot()
	if saved.Active.Fingerprint != slot.Active.Fingerprint || saved.Active.Version != 1 || saved.Ready.Fingerprint == slot.Active.Fingerprint {
		t.Fatal("stable active replaced")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(probes) != 3 || probes[1].ProxyURL != "http://egress-one:8080" || probes[2].ProxyURL != "http://egress-two:8080" || strings.Contains(probes[0].ProxyURL, "{sid}") {
		t.Fatal("proxy order or dynamic session")
	}
	statusJSON, err := e.StatusJSON(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{slot.Active.State, "secret", "Bearer", "harvest:8080", "egress-one"} {
		if strings.Contains(string(statusJSON), secret) {
			t.Fatal("status leaked secret")
		}
	}
}

func TestHarvestAttemptBudgetCooldownAndCrossInstance(t *testing.T) {
	h := newHost()
	var calls atomic.Int32
	probe := probeFunc(func(context.Context, ProbeRequest) (ProbeResult, error) {
		calls.Add(1)
		return ProbeResult{StatusCode: 200}, errors.New("secret upstream body")
	})
	e1 := engineFor(t, h, probe, "http://harvest:80", Options{})
	e2 := engineFor(t, h, probe, "http://harvest:80", Options{})
	eventually(t, func() bool { s := h.getSlot(); return s.Attempts == 8 && !s.Harvesting && !s.CooldownUntil.IsZero() })
	if calls.Load() != 8 {
		t.Fatalf("duplicate harvests: %d", calls.Load())
	}
	if err := e1.startHarvest(context.Background(), e1.snap(), 123, testModel, true); err != nil {
		t.Fatal(err)
	}
	if err := e2.startHarvest(context.Background(), e2.snap(), 123, testModel, true); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 8 {
		t.Fatal("cooldown bypassed")
	}
	if until := h.getSlot().CooldownUntil; time.Until(until) < RetryCooldown-time.Second {
		t.Fatal("wrong cooldown")
	}
}

func TestCancelAndIdentityChangeIgnoreLateProbe(t *testing.T) {
	for _, change := range []string{"cancel", "identity", "config"} {
		t.Run(change, func(t *testing.T) {
			h := newHost()
			entered := make(chan struct{})
			release := make(chan struct{})
			var once sync.Once
			probe := probeFunc(func(context.Context, ProbeRequest) (ProbeResult, error) {
				once.Do(func() { close(entered) })
				<-release
				return ProbeResult{State: stateAt(time.Now(), 10, 9), StatusCode: 200, Completed: true, Model: testModel}, nil
			})
			e := engineFor(t, h, probe, "http://harvest:80", Options{})
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("probe not entered")
			}
			switch change {
			case "cancel":
				_, err := e.RunAction(context.Background(), Action{Name: "cancel", ActionID: "cancel-1", Payload: ActionPayload{123, testModel}})
				if err != nil {
					t.Fatal(err)
				}
			case "identity":
				h.mu.Lock()
				id := h.identities[123]
				id.Revision = "identity-2"
				h.identities[123] = id
				h.mu.Unlock()
			case "config":
				h.mu.Lock()
				h.revision = "2"
				h.mu.Unlock()
				if err := e.Apply(configJSON(""), "2", true); err != nil {
					t.Fatal(err)
				}
			}
			close(release)
			eventually(t, func() bool { e.mu.RLock(); defer e.mu.RUnlock(); return len(e.jobs) == 0 })
			if h.getSlot().Active != nil {
				t.Fatal("late result was published")
			}
		})
	}
}

func TestActionsIdempotentAfterPartialJournalAndRestart(t *testing.T) {
	h := newHost()
	e := engineFor(t, h, nil, "", Options{})
	h.seed(baseSlot(time.Now()))
	action := Action{Name: "cancel", ActionID: "durable-id", Payload: ActionPayload{123, testModel}}
	h.mu.Lock()
	h.actionFinalFailure = true
	h.mu.Unlock()
	if _, err := e.RunAction(context.Background(), action); err == nil {
		t.Fatal("expected journal finalize failure")
	}
	epoch := h.getSlot().JobEpoch
	e2 := engineFor(t, h, nil, "", Options{})
	result, err := e2.RunAction(context.Background(), action)
	if err != nil || !result.Duplicate || h.getSlot().JobEpoch != epoch {
		t.Fatalf("retry changed action %v %v", result, err)
	}
	result, err = e2.RunAction(context.Background(), action)
	if err != nil || !result.Duplicate || h.getSlot().JobEpoch != epoch {
		t.Fatal("duplicate reran action")
	}
	action.Name = "harvest"
	if _, err = e2.RunAction(context.Background(), action); !errors.Is(err, ErrActionConflict) {
		t.Fatal("ID reuse accepted")
	}
	h.mu.Lock()
	before := h.identityReads
	h.mu.Unlock()
	_ = e2.Status(context.Background())
	h.mu.Lock()
	after := h.identityReads
	h.mu.Unlock()
	if before != after {
		t.Fatal("health resolved credentials")
	}
}

func TestSameRevisionDraftActivationAndPausedCompletion(t *testing.T) {
	h := newHost()
	e := engineFor(t, h, nil, "", Options{})
	original := baseSlot(time.Now())
	h.seed(original)
	receipt, err := e.Prepare(context.Background(), 123, testModel, "identity-1", make(http.Header))
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Apply(configJSON(""), "1", false); err != nil {
		t.Fatal(err)
	}
	if e.RuntimeActive() {
		t.Fatal("runtime still active")
	}
	if err = e.RecordCompletion(*receipt, "model_mismatch"); err != nil {
		t.Fatal("paused completion failed", err)
	}
	if h.getSlot().Strikes != 1 {
		t.Fatal("paused completion lost")
	}
	if err = e.Apply(configJSON(""), "1", true); err != nil {
		t.Fatal(err)
	}
	slot := h.getSlot()
	if !e.RuntimeActive() || slot.Active.Version != original.Active.Version || slot.Active.State != original.Active.State || slot.Strikes != 1 {
		t.Fatal("same revision activation reset state")
	}
}

func TestCompletedSuccessSurvivesClientCancelAndRuntimePause(t *testing.T) {
	for _, body := range []string{
		`{"object":"response","status":"completed","model":"gpt-6-astra"}`,
		"data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"model\":\"gpt-6-astra\"}}\n\n",
	} {
		t.Run(body[:4], func(t *testing.T) {
			h := newHost()
			e := engineFor(t, h, nil, "", Options{})
			slot := baseSlot(time.Now())
			slot.Strikes = 1
			h.seed(slot)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			receipt, err := e.Prepare(ctx, 123, testModel, "identity-1", make(http.Header))
			if err != nil {
				t.Fatal(err)
			}
			response := &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
			e.ObserveResponse(receipt, response)
			buffer := make([]byte, len(body))
			if n, readErr := response.Body.Read(buffer); n != len(body) || readErr != nil {
				t.Fatal(n, readErr)
			}
			cancel()
			if err = e.Apply(configJSON(""), "1", false); err != nil {
				t.Fatal(err)
			}
			if err = response.Body.Close(); err != nil {
				t.Fatal(err)
			}
			current := h.getSlot()
			if current.Strikes != 0 || current.Active.Version != slot.Active.Version || current.Active.State != slot.Active.State {
				t.Fatal("successful completion lost on cancel/pause")
			}
		})
	}
}

func TestPausedApplyCancelsHarvestButPreservesOutstandingReceipt(t *testing.T) {
	h := newHost()
	initial := baseSlot(time.Now())
	initial.Ready = nil
	initial.Strikes = 1
	h.seed(initial)
	entered, cancelled := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	probe := probeFunc(func(ctx context.Context, r ProbeRequest) (ProbeResult, error) {
		calls.Add(1)
		close(entered)
		<-ctx.Done()
		close(cancelled)
		// A late implementation may return success after cancellation. It must
		// neither start verification nor publish this candidate.
		return ProbeResult{State: stateAt(time.Now(), 10, 8), StatusCode: 200, Completed: true, Model: testModel}, nil
	})
	config := configJSON("http://harvest:80")
	e := engineFor(t, h, probe, "http://harvest:80", Options{})
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("harvest not started")
	}
	receipt, err := e.Prepare(context.Background(), 123, testModel, "identity-1", make(http.Header))
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Apply(config, "1", false); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("Apply pause did not cancel active harvest")
	}
	eventually(t, func() bool { e.mu.RLock(); defer e.mu.RUnlock(); return len(e.jobs) == 0 })
	if err = e.RecordCompletion(*receipt, ""); err != nil {
		t.Fatal("paused receipt accounting failed", err)
	}
	stored := h.getSlot()
	if e.RuntimeActive() || calls.Load() != 1 || stored.Ready != nil || stored.Active.State != initial.Active.State || stored.Active.Version != initial.Active.Version || stored.Strikes != 0 {
		t.Fatal("pause either published late harvest or lost completion")
	}
}

type blockedCompletionHost struct {
	*fakeHost
	entered chan struct{}
	release chan struct{}
}

func (h *blockedCompletionHost) Identity(ctx context.Context, id int64) (Identity, error) {
	if IsCompletionContext(ctx) {
		close(h.entered)
		select {
		case <-h.release:
		case <-ctx.Done():
			return Identity{}, ctx.Err()
		}
	}
	return h.fakeHost.Identity(ctx, id)
}

func TestApplyPauseDuringCompletionDoesNotDropAccounting(t *testing.T) {
	stored := newHost()
	slot := baseSlot(time.Now())
	slot.Strikes = 1
	stored.seed(slot)
	h := &blockedCompletionHost{fakeHost: stored, entered: make(chan struct{}), release: make(chan struct{})}
	e := New(h, probeFunc(func(context.Context, ProbeRequest) (ProbeResult, error) { return ProbeResult{}, ErrUnavailable }), Options{PollInterval: time.Hour})
	t.Cleanup(func() { _ = e.Close(context.Background()) })
	if err := e.Apply(configJSON(""), "1", true); err != nil {
		t.Fatal(err)
	}
	receipt, err := e.Prepare(context.Background(), 123, testModel, "identity-1", make(http.Header))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- e.RecordCompletion(*receipt, "") }()
	select {
	case <-h.entered:
	case <-time.After(time.Second):
		t.Fatal("completion not entered")
	}
	if err = e.Apply(configJSON(""), "1", false); err != nil {
		t.Fatal(err)
	}
	close(h.release)
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("completion stuck")
	}
	if stored.getSlot().Strikes != 0 {
		t.Fatal("pause discarded in-progress completion")
	}
}

func TestPausedPrepareRejectsManagedButLeavesUnmanagedScope(t *testing.T) {
	h := newHost()
	e := engineFor(t, h, nil, "", Options{})
	h.seed(baseSlot(time.Now()))
	if err := e.Apply(configJSON(""), "1", false); err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"", "client-original"} {
		headers := make(http.Header)
		if state != "" {
			headers.Set(StateHeader, state)
		}
		if receipt, err := e.Prepare(context.Background(), 123, testModel, "identity-1", headers); !errors.Is(err, ErrDisabled) || receipt != nil {
			t.Fatal("paused managed target was allowed", receipt, err)
		}
		if headers.Get(StateHeader) != state {
			t.Fatal("paused prepare changed client state")
		}
		if receipt, err := e.Prepare(context.Background(), 123, "gpt-5.6-sol", "identity-1", headers); err != nil || receipt != nil {
			t.Fatal("unmanaged target became strict", receipt, err)
		}
	}
}

func TestPausedReceiptCannotMutateNewOwnership(t *testing.T) {
	for _, change := range []string{"configuration", "identity"} {
		t.Run(change, func(t *testing.T) {
			h := newHost()
			e := engineFor(t, h, nil, "", Options{})
			original := baseSlot(time.Now())
			original.Strikes = 1
			h.seed(original)
			receipt, err := e.Prepare(context.Background(), 123, testModel, "identity-1", make(http.Header))
			if err != nil || receipt == nil {
				t.Fatal("receipt missing", err)
			}
			if err = e.Apply(configJSON(""), "1", false); err != nil {
				t.Fatal(err)
			}
			replacement := original
			if change == "configuration" {
				h.mu.Lock()
				h.revision = "2"
				h.mu.Unlock()
				if err = e.Apply(configJSON(""), "2", false); err != nil {
					t.Fatal(err)
				}
				replacement.Guard.ConfigRevision = "2"
			} else {
				h.mu.Lock()
				identity := h.identities[123]
				identity.Revision = "identity-2"
				h.identities[123] = identity
				h.mu.Unlock()
				replacement.Guard.IdentityRevision = "identity-2"
			}
			// Reusing ticket bytes/version must not bridge ownership revisions.
			h.seed(replacement)
			for _, reason := range []string{"", "model_mismatch"} {
				if err = e.RecordCompletion(*receipt, reason); err != nil {
					t.Fatal("stale observation should be ignored", err)
				}
				current := h.getSlot()
				if current.Guard != replacement.Guard || current.Strikes != 1 || current.Active == nil || current.Active.Version != original.Active.Version || current.Active.Fingerprint != original.Active.Fingerprint {
					t.Fatal("paused receipt mutated new ownership")
				}
			}
		})
	}
}

func TestStaleObservationCannotClearNewVersionFailureBlock(t *testing.T) {
	h := newHost()
	e := engineFor(t, h, nil, "", Options{})
	h.seed(baseSlot(time.Now()))
	old, _ := e.Prepare(context.Background(), 123, testModel, "identity-1", make(http.Header))
	_ = e.RecordCompletion(*old, "model_mismatch")
	_ = e.RecordCompletion(*old, "model_mismatch")
	current, _ := e.Prepare(context.Background(), 123, testModel, "identity-1", make(http.Header))
	h.mu.Lock()
	h.failWrite = true
	h.mu.Unlock()
	_ = e.RecordCompletion(*current, "model_mismatch")
	h.mu.Lock()
	h.failWrite = false
	h.mu.Unlock()
	_ = e.RecordCompletion(*old, "")
	if _, err := e.Prepare(context.Background(), 123, testModel, "identity-1", make(http.Header)); err == nil {
		t.Fatal("old observation cleared failure block")
	}
}

func TestExpiredLeaseCannotMutateAndExpiredHarvestResumesBudget(t *testing.T) {
	h := newHost()
	g := Guard{123, testModel, "1", "identity-1"}
	key := slotKey(123, testModel)
	old, err := h.LeaseAcquire(context.Background(), LeaseRequest{Key: key, Owner: "old", TTL: time.Second, Guard: g})
	if err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	expired := h.leases[key]
	expired.ExpiresAt = time.Now().Add(-time.Second)
	h.leases[key] = expired
	h.mu.Unlock()
	newLease, err := h.LeaseAcquire(context.Background(), LeaseRequest{Key: key, Owner: "new", TTL: time.Second, Guard: g})
	if err != nil || newLease.Fence <= old.Fence {
		t.Fatal(newLease, err)
	}
	if _, err = h.StateCAS(context.Background(), Mutation{Key: key, Guard: g, Lease: old, Data: []byte(`{}`)}); !errors.Is(err, ErrStale) {
		t.Fatal("stale fence committed")
	}
	_ = h.LeaseRelease(context.Background(), old)
	h.mu.Lock()
	current := h.leases[key]
	h.mu.Unlock()
	if current.Owner != "new" {
		t.Fatal("old release removed new owner")
	}
	_ = h.LeaseRelease(context.Background(), newLease)
	h.seed(Slot{Guard: g, Harvesting: true, JobEpoch: 4, Attempts: 7, HarvestUntil: time.Now().Add(-time.Second)})
	var calls atomic.Int32
	engineFor(t, h, probeFunc(func(context.Context, ProbeRequest) (ProbeResult, error) {
		calls.Add(1)
		return ProbeResult{}, ErrUnavailable
	}), "http://harvest:80", Options{})
	eventually(t, func() bool { s := h.getSlot(); return s.Attempts == 8 && !s.Harvesting && !s.CooldownUntil.IsZero() })
	if calls.Load() != 1 || h.getSlot().JobEpoch != 5 {
		t.Fatal("expired job reset attempt budget")
	}
}

func TestHarvestWriteFailureDoesNotPublishCandidate(t *testing.T) {
	h := newHost()
	now := time.Now()
	slot := baseSlot(now)
	slot.Ready = nil
	h.seed(slot)
	probe := probeFunc(func(ctx context.Context, r ProbeRequest) (ProbeResult, error) {
		if r.State != "" {
			h.mu.Lock()
			h.failWrite = true
			h.mu.Unlock()
		}
		return ProbeResult{State: stateAt(now, 10, 8), StatusCode: 200, Completed: true, Model: testModel}, nil
	})
	e := engineFor(t, h, probe, "http://harvest:80", Options{})
	eventually(t, func() bool {
		h.mu.Lock()
		failed := h.failWrite
		h.mu.Unlock()
		e.mu.RLock()
		done := len(e.jobs) == 0
		e.mu.RUnlock()
		return failed && done
	})
	saved := h.getSlot()
	if saved.Ready != nil || saved.Active.Fingerprint != slot.Active.Fingerprint {
		t.Fatal("unpersisted candidate became available")
	}
}
