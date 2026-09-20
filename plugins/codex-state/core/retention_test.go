package core

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Migrates the legacy service retention suite, including its cold-start,
// original-expiry, live-status, redaction and cooldown assertions.
func TestRejectionStopsRoundAndRetainsDurableTicket(t *testing.T) {
	for _, code := range []int{401, 403, 429} {
		for _, stage := range []string{"harvest", "fixed"} {
			t.Run(strconv.Itoa(code)+"-"+stage, func(t *testing.T) {
				h := newHost()
				old := ticketAt(time.Now().Add(-52*time.Minute), 1, 1)
				h.seed(Slot{Guard: Guard{123, testModel, "1", "identity-1"}, Active: old, NextVersion: 1})
				var calls atomic.Int32
				entered, release := make(chan struct{}), make(chan struct{})
				probe := probeFunc(func(ctx context.Context, r ProbeRequest) (ProbeResult, error) {
					if calls.Add(1) == 1 {
						close(entered)
						select {
						case <-release:
						case <-ctx.Done():
							return ProbeResult{}, ctx.Err()
						}
					}
					if stage == "harvest" || r.State != "" {
						return ProbeResult{StatusCode: code}, nil
					}
					return ProbeResult{StatusCode: 200, Completed: true, Model: testModel, State: stateAt(time.Now(), 10, 7)}, nil
				})
				e := engineFor(t, h, probe, "http://harvest:80", Options{})
				select {
				case <-entered:
				case <-time.After(time.Second):
					t.Fatal("probe not entered")
				}
				status := e.Status(context.Background()).Accounts[0].Models[testModel]
				if status.State != "harvesting" || !status.Refreshing || status.Active == nil || !status.Active.Usable || !status.Active.CapturedAt.Equal(old.CapturedAt) || !status.Active.ExpiresAt.Equal(old.ExpiresAt) {
					t.Fatalf("old ticket lost during refresh: %+v", status)
				}
				close(release)
				eventually(t, func() bool { s := h.getSlot(); return !s.Harvesting && s.LastError == "upstream_rejected" })
				want := int32(1)
				if stage == "fixed" {
					want = 2
				}
				if calls.Load() != want {
					t.Fatalf("rotated exits after rejection: %d calls, want %d", calls.Load(), want)
				}
				status = e.Status(context.Background()).Accounts[0].Models[testModel]
				if status.Active == nil || !status.Active.Usable || status.Refreshing || status.CooldownUntil == nil || !status.CooldownUntil.After(time.Now().Add(4*time.Minute)) || status.LastError != "upstream_rejected" {
					t.Fatalf("bad retained status: %+v", status)
				}
				if !status.Active.CapturedAt.Equal(old.CapturedAt) || !status.Active.ExpiresAt.Equal(old.ExpiresAt) {
					t.Fatal("failure extended ticket lifetime")
				}
				if err := e.startHarvest(context.Background(), e.snap(), 123, testModel, false); err != nil {
					t.Fatal(err)
				}
				if calls.Load() != want {
					t.Fatal("cooldown was bypassed")
				}
				raw, _ := json.Marshal(status)
				if strings.Contains(string(raw), old.State) || strings.Contains(string(raw), "secret") {
					t.Fatal("status exposed ticket or credential")
				}
				restart := engineFor(t, h, probe, "http://harvest:80", Options{})
				headers := make(http.Header)
				if _, err := restart.Prepare(context.Background(), 123, testModel, "identity-1", headers); err != nil {
					t.Fatal("cold-start allocation failed", err)
				}
				if headers.Get(StateHeader) != old.State || !h.getSlot().Active.ExpiresAt.Equal(old.ExpiresAt) || !h.getSlot().Active.CapturedAt.Equal(old.CapturedAt) {
					t.Fatal("cold-start did not retain original ticket")
				}
			})
		}
	}
}

func TestRetentionSaveFailurePreservesOriginalTimesAfterRestart(t *testing.T) {
	h := newHost()
	old := ticketAt(time.Now().Add(-52*time.Minute), 1, 1)
	h.seed(Slot{Guard: Guard{123, testModel, "1", "identity-1"}, Active: old, NextVersion: 1})
	probe := probeFunc(func(ctx context.Context, r ProbeRequest) (ProbeResult, error) {
		if r.State != "" {
			h.mu.Lock()
			h.failWrite = true
			h.mu.Unlock()
		}
		return ProbeResult{StatusCode: 200, Completed: true, Model: testModel, State: stateAt(time.Now(), 10, 9)}, nil
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
	status := e.Status(context.Background()).Accounts[0].Models[testModel]
	if status.Active == nil || !status.Active.Usable || !status.Active.CapturedAt.Equal(old.CapturedAt) || !status.Active.ExpiresAt.Equal(old.ExpiresAt) {
		t.Fatalf("save failure changed durable ticket: %+v", status)
	}
	restart := engineFor(t, h, nil, "", Options{})
	headers := make(http.Header)
	if _, err := restart.Prepare(context.Background(), 123, testModel, "identity-1", headers); err != nil || headers.Get(StateHeader) != old.State {
		t.Fatal("cold-start lost durable ticket", err)
	}
	if h.getSlot().Ready != nil {
		t.Fatal("unpersisted candidate exposed")
	}
}

func TestStatusNeverAdvertisesUnusableSavedTicket(t *testing.T) {
	for _, scenario := range []string{"expired", "cutoff", "disabled", "global-disabled", "inactive", "missing", "deleted"} {
		t.Run(scenario, func(t *testing.T) {
			h := newHost()
			e := engineFor(t, h, nil, "", Options{})
			slot := baseSlot(time.Now())
			slot.Ready = nil
			if scenario == "expired" {
				slot.Active = ticketAt(time.Now().Add(-2*time.Hour), 1, 1)
			}
			if scenario == "cutoff" {
				slot.Active = ticketAt(time.Now().Add(-time.Hour+20*time.Second), 1, 1)
			}
			if scenario != "missing" {
				h.seed(slot)
			}
			_, _, _ = e.guard(context.Background(), e.snap(), 123, testModel)
			switch scenario {
			case "disabled", "global-disabled":
				cfg, _ := Validate(configJSON(""))
				if scenario == "disabled" {
					cfg.Accounts[0].Models[testModel] = ModelConfig{Enabled: false, TicketPlan: "pro"}
				} else {
					cfg.Enabled = false
				}
				raw, _ := json.Marshal(cfg)
				h.mu.Lock()
				h.revision = "2"
				h.mu.Unlock()
				if err := e.Apply(raw, "2", true); err != nil {
					t.Fatal(err)
				}
			case "inactive":
				h.mu.Lock()
				identity := h.identities[123]
				identity.Eligible = false
				h.identities[123] = identity
				h.mu.Unlock()
			case "deleted":
				h.mu.Lock()
				delete(h.identities, 123)
				h.mu.Unlock()
			}
			status := e.Status(context.Background()).Accounts[0].Models[testModel]
			if status.Active != nil || status.Ready != nil || status.Refreshing {
				t.Fatalf("unusable saved ticket advertised: %+v", status)
			}
		})
	}
}
