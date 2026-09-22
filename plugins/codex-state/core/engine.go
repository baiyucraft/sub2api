package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"
)

const StateHeader = "X-Codex-Turn-State"

type Options struct {
	Owner          string
	Now            func() time.Time
	PollInterval   time.Duration
	LeaseTTL       time.Duration
	CleanupTimeout time.Duration
	ProbeTimeout   time.Duration
	RetryDelay     time.Duration
}

type snapshot struct {
	config     Config
	revision   string
	generation uint64
	active     bool
	drain      bool
	ctx        context.Context
}
type job struct {
	cancel     context.CancelFunc
	generation uint64
}

type Engine struct {
	host        Host
	prober      Prober
	opts        Options
	mu          sync.RWMutex
	current     snapshot
	cancel      context.CancelFunc
	closed      bool
	jobs        map[string]*job
	blocked     map[string]Guard
	logs        []Log
	nextLogID   uint64
	identities  map[int64]string
	bulkActions map[string]Action
	bulkResults map[string]ActionResult
	wg          sync.WaitGroup
}

func New(host Host, prober Prober, opts Options) *Engine {
	if opts.Owner == "" {
		opts.Owner = randomID()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = 5 * time.Second
	}
	if opts.LeaseTTL <= 0 {
		opts.LeaseTTL = 30 * time.Second
	}
	if opts.CleanupTimeout <= 0 {
		opts.CleanupTimeout = 5 * time.Second
	}
	if opts.ProbeTimeout <= 0 {
		opts.ProbeTimeout = 25 * time.Second
	}
	if opts.RetryDelay <= 0 {
		opts.RetryDelay = time.Second
	}
	return &Engine{host: host, prober: prober, opts: opts, jobs: map[string]*job{}, blocked: map[string]Guard{}, identities: map[int64]string{}, bulkActions: map[string]Action{}, bulkResults: map[string]ActionResult{}}
}

func randomID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

// Apply starts work only for host-active AND configuration-enabled runtime.
// Reapplying identical revision/config/runtime is a no-op; revision reuse with
// different JSON semantics is rejected. All old workers lose their context.
func (e *Engine) Apply(raw []byte, revision string, runtimeActive bool) error {
	cfg, err := Validate(raw)
	if err != nil {
		return err
	}
	if revision == "" {
		return errors.New("host configuration revision is required")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return ErrDisabled
	}
	if e.current.revision == revision {
		a, _ := json.Marshal(e.current.config)
		b, _ := json.Marshal(cfg)
		if string(a) != string(b) {
			return errors.New("configuration revision was reused")
		}
		if e.current.active == (runtimeActive && cfg.Enabled) {
			return nil
		}
	}
	if e.cancel != nil {
		e.cancel()
	}
	for _, j := range e.jobs {
		j.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	e.cancel = cancel
	e.current = snapshot{config: cfg, revision: revision, generation: e.current.generation + 1, active: runtimeActive && cfg.Enabled, ctx: ctx}
	if e.current.active {
		if e.host == nil || e.prober == nil {
			e.current.active = false
			cancel()
			return ErrUnavailable
		}
		snap := e.current
		e.wg.Add(1)
		go func() { defer e.wg.Done(); e.supervise(snap) }()
	}
	return nil
}

func (e *Engine) Close(ctx context.Context) error {
	e.mu.Lock()
	e.closed = true
	e.current.active = false
	if e.cancel != nil {
		e.cancel()
	}
	for _, j := range e.jobs {
		j.cancel()
	}
	e.mu.Unlock()
	done := make(chan struct{})
	go func() { e.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (e *Engine) snap() snapshot { e.mu.RLock(); defer e.mu.RUnlock(); return e.current }
func (e *Engine) live(s snapshot) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.liveLocked(s)
}

func (e *Engine) liveLocked(s snapshot) bool {
	if e.closed || !s.active || s.ctx.Err() != nil {
		return false
	}
	if s.drain {
		return e.current.config.Enabled && e.current.revision == s.revision
	}
	return e.current.active && e.current.generation == s.generation
}

func slotKey(id int64, model string) string { return fmt.Sprintf("slots/%d/%s", id, model) }

func (e *Engine) guard(ctx context.Context, s snapshot, id int64, model string) (Guard, Identity, error) {
	if !e.live(s) {
		return Guard{}, Identity{}, ErrDisabled
	}
	cfg, ok := s.config.model(id, model)
	if !ok || !cfg.Enabled {
		return Guard{}, Identity{}, ErrDisabled
	}
	identity, err := e.host.Identity(ctx, id)
	if err != nil || identity.AccountID != id || identity.Revision == "" || !identity.Eligible {
		return Guard{}, Identity{}, ErrUnavailable
	}
	e.mu.Lock()
	e.identities[id] = identity.Revision
	e.mu.Unlock()
	return Guard{id, model, s.revision, identity.Revision}, identity, nil
}

func (e *Engine) read(ctx context.Context, g Guard) (Slot, uint64, error) {
	stored, err := e.host.StateGet(ctx, slotKey(g.AccountID, g.Model), g)
	if err != nil {
		return Slot{}, 0, err
	}
	var slot Slot
	if len(stored.Data) > 0 && json.Unmarshal(stored.Data, &slot) != nil {
		return Slot{}, 0, ErrUnavailable
	}
	if slot.Guard != g {
		slot = Slot{Guard: g, NextVersion: slot.NextVersion, JobEpoch: slot.JobEpoch + 1, Actions: slot.Actions}
	}
	if slot.Strikes < 0 || slot.Attempts < 0 || slot.Attempts > MaxAttempts {
		return Slot{}, 0, ErrUnavailable
	}
	return slot, stored.Version, nil
}

func (e *Engine) release(lease Lease) {
	ctx, cancel := context.WithTimeout(context.Background(), e.opts.CleanupTimeout)
	defer cancel()
	_ = e.host.LeaseRelease(ctx, lease)
}

// Each state mutation takes the exact-key host lease. The persisted job epoch
// excludes superseded harvests while leaving watchdog mutations unblocked.
func (e *Engine) mutate(ctx context.Context, s snapshot, g Guard, held *Lease, fn func(*Slot) (bool, error)) (Slot, error) {
	key := slotKey(g.AccountID, g.Model)
	var lease Lease
	if held != nil {
		lease = *held
	} else {
		var err error
		for attempt := 0; attempt < 32; attempt++ {
			lease, err = e.host.LeaseAcquire(ctx, LeaseRequest{key, e.opts.Owner + ":" + randomID(), e.opts.CleanupTimeout, g})
			if !errors.Is(err, ErrLeaseBusy) {
				break
			}
			if err = pause(ctx, 10*time.Millisecond); err != nil {
				return Slot{}, err
			}
		}
		if err != nil {
			return Slot{}, err
		}
		defer e.release(lease)
	}
	for attempt := 0; attempt < 16; attempt++ {
		if ctx.Err() != nil {
			return Slot{}, ctx.Err()
		}
		if !e.live(s) {
			return Slot{}, ErrStale
		}
		slot, version, err := e.read(ctx, g)
		if err != nil {
			return Slot{}, err
		}
		changed, err := fn(&slot)
		if err != nil || !changed {
			return slot, err
		}
		data, err := json.Marshal(slot)
		if err != nil {
			return Slot{}, ErrUnavailable
		}
		e.mu.RLock()
		if !e.liveLocked(s) || ctx.Err() != nil {
			e.mu.RUnlock()
			return Slot{}, ErrStale
		}
		_, err = e.host.StateCAS(ctx, Mutation{key, version, data, g, lease})
		e.mu.RUnlock()
		if errors.Is(err, ErrConflict) {
			continue
		}
		return slot, err
	}
	return Slot{}, ErrConflict
}

func pause(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func normalizeSlot(slot *Slot, plan string, now time.Time) bool {
	changed := false
	if slot.Active != nil && !usable(slot.Active, plan, now) {
		slot.Active = nil
		slot.Strikes = 0
		changed = true
	}
	if slot.Ready != nil && !usable(slot.Ready, plan, now) {
		slot.Ready = nil
		changed = true
	}
	if slot.Active == nil && slot.Ready != nil {
		slot.Active, slot.Ready = slot.Ready, nil
		slot.NextVersion++
		slot.Active.Version = slot.NextVersion
		slot.Strikes = 0
		changed = true
	}
	return changed
}

// Prepare preserves an existing client STATE verbatim. The host has already
// removed known cross-account STATE. Only plugin-injected STATE gets a receipt.
func (e *Engine) Prepare(ctx context.Context, accountID int64, outboundModel, identityRevision string, headers http.Header) (*Receipt, error) {
	s := e.snap()
	cfg, ok := s.config.model(accountID, outboundModel)
	if !s.config.Enabled || !ok || !cfg.Enabled {
		return nil, nil
	}
	if !s.active {
		return nil, ErrDisabled
	}
	if headers == nil {
		return nil, ErrUnavailable
	}
	g, identity, err := e.guard(ctx, s, accountID, outboundModel)
	if err != nil {
		return nil, err
	}
	if identityRevision == "" || identityRevision != g.IdentityRevision {
		return nil, ErrStale
	}
	if !e.live(s) {
		return nil, ErrStale
	}
	// Proxy availability gates new requests, not ownership or receipt cleanup.
	hasEgress := false
	for _, egress := range identity.Egresses {
		if egress.ProxyURL != "" && ValidateProxy(egress.ProxyURL, false) == nil {
			hasEgress = true
			break
		}
	}
	if !hasEgress {
		return nil, ErrUnavailable
	}
	if headers.Get(StateHeader) != "" {
		return nil, nil
	}
	e.mu.RLock()
	blocked := e.blocked[slotKey(accountID, outboundModel)] == g
	e.mu.RUnlock()
	if blocked {
		return nil, ErrUnavailable
	}
	slot, _, err := e.read(ctx, g)
	if err != nil {
		return nil, ErrUnavailable
	}
	if !usable(slot.Active, cfg.TicketPlan, e.opts.Now()) {
		slot, err = e.mutate(ctx, s, g, nil, func(slot *Slot) (bool, error) { return normalizeSlot(slot, cfg.TicketPlan, e.opts.Now()), nil })
		if err != nil {
			return nil, ErrUnavailable
		}
	}
	if !e.live(s) || !usable(slot.Active, cfg.TicketPlan, e.opts.Now()) {
		return nil, ErrUnavailable
	}
	headers.Set(StateHeader, slot.Active.State)
	return &Receipt{g, slot.Active.Version, slot.Active.Fingerprint}, nil
}

// RecordCompletion is intentionally detached from the client's context. It is
// called only after complete terminal evidence and is bounded independently.
func (e *Engine) RecordCompletion(receipt Receipt, reason string) error {
	if reason != "" && reason != "model_mismatch" && reason != "state_envelope" {
		return errors.New("invalid completion reason")
	}
	s := e.snap()
	if !s.config.Enabled || s.revision != receipt.Guard.ConfigRevision {
		return nil
	}
	// Runtime pause stops new work, but the same configuration may finish the
	// observations already bound to requests before the pause.
	ctx, cancel := context.WithTimeout(context.Background(), e.opts.CleanupTimeout)
	defer cancel()
	ctx = context.WithValue(ctx, completionContextKey{}, true)
	s.active = true
	s.drain = true
	s.ctx = ctx
	g, _, err := e.guard(ctx, s, receipt.Guard.AccountID, receipt.Guard.Model)
	if err != nil {
		e.block(receipt.Guard)
		return err
	}
	if g != receipt.Guard {
		return nil
	}
	cfg, _ := s.config.model(g.AccountID, g.Model)
	matched := false
	_, err = e.mutate(ctx, s, g, nil, func(slot *Slot) (bool, error) {
		matched = false
		if slot.Active == nil || slot.Active.Version != receipt.TicketVersion || slot.Active.Fingerprint != receipt.Fingerprint {
			return false, nil
		}
		matched = true
		if reason == "" {
			changed := slot.Strikes != 0
			slot.Strikes = 0
			return changed, nil
		}
		slot.Strikes++
		if slot.Strikes >= 2 {
			slot.Active = nil
			normalizeSlot(slot, cfg.TicketPlan, e.opts.Now())
		}
		return true, nil
	})
	if err != nil {
		e.block(g)
		e.log(g, "watchdog_persistence_failed")
		return err
	}
	if !matched {
		return nil
	}
	e.mu.Lock()
	if e.blocked[slotKey(g.AccountID, g.Model)] == g {
		delete(e.blocked, slotKey(g.AccountID, g.Model))
	}
	e.mu.Unlock()
	if reason != "" {
		e.log(g, reason)
	}
	return nil
}

func (e *Engine) block(g Guard) {
	e.mu.Lock()
	e.blocked[slotKey(g.AccountID, g.Model)] = g
	e.mu.Unlock()
}

func (e *Engine) ConfigRevision() string { return e.snap().revision }

func (e *Engine) RuntimeActive() bool { return e.snap().active }

// Admit performs the same persisted strict readiness check without injecting
// headers or starting probes. Unmanaged targets retain host routing behavior.
func (e *Engine) Admit(ctx context.Context, accountID int64, model, identityRevision string) error {
	_, err := e.Prepare(ctx, accountID, model, identityRevision, make(http.Header))
	return err
}
