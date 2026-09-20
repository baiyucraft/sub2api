package core

import (
	"context"
	"encoding/json"
	"strconv"
	"time"
)

type Log struct {
	At        time.Time `json:"at"`
	AccountID int64     `json:"account_id"`
	Model     string    `json:"model"`
	Code      string    `json:"code"`
}
type TicketStatus struct {
	RemainingSeconds int64     `json:"remaining_seconds"`
	IssuedAt         time.Time `json:"issued_at"`
	CapturedAt       time.Time `json:"captured_at"`
	ExpiresAt        time.Time `json:"expires_at"`
	Usable           bool      `json:"usable"`
	Version          uint64    `json:"version"`
}
type ModelStatus struct {
	Enabled       bool          `json:"enabled"`
	TicketPlan    string        `json:"ticket_plan"`
	State         string        `json:"state"`
	Active        *TicketStatus `json:"active"`
	Ready         *TicketStatus `json:"ready"`
	Strikes       int           `json:"strikes"`
	Attempts      int           `json:"attempts"`
	Refreshing    bool          `json:"refreshing"`
	CooldownUntil *time.Time    `json:"cooldown_until"`
	LastError     string        `json:"last_error"`
}
type AccountStatus struct {
	AccountID int64                  `json:"account_id"`
	Models    map[string]ModelStatus `json:"models"`
}
type Status struct {
	Version        string          `json:"version"`
	Running        bool            `json:"running"`
	Enabled        bool            `json:"enabled"`
	ConfigRevision uint64          `json:"config_revision"`
	Accounts       []AccountStatus `json:"accounts"`
	Logs           []Log           `json:"logs"`
}

func (e *Engine) log(g Guard, code string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.logs = append(e.logs, Log{e.opts.Now().UTC(), g.AccountID, g.Model, code})
	if len(e.logs) > 100 {
		e.logs = append([]Log(nil), e.logs[len(e.logs)-100:]...)
	}
}

func publicError(code string) string {
	switch code {
	case "harvest_failed", "upstream_rejected", "harvest_persistence_failed":
		return code
	}
	if code != "" {
		return "state_unavailable"
	}
	return ""
}

func ticketStatus(ticket *Ticket, plan string, now time.Time) *TicketStatus {
	if ticket == nil || !usable(ticket, plan, now) {
		return nil
	}
	remaining := int64(ticket.ExpiresAt.Sub(now).Seconds())
	if remaining < 0 {
		remaining = 0
	}
	return &TicketStatus{remaining, ticket.IssuedAt, ticket.CapturedAt, ticket.ExpiresAt, usable(ticket, plan, now), ticket.Version}
}

// Status exposes only allowlisted metadata. Host/proxy errors and raw state,
// headers, fingerprints, URLs, action IDs and credentials are never serialized.
func (e *Engine) Status(ctx context.Context) Status {
	s := e.snap()
	revision, _ := strconv.ParseUint(s.revision, 10, 64)
	status := Status{Version: Version, Running: s.active, Enabled: s.config.Enabled, ConfigRevision: revision, Accounts: []AccountStatus{}, Logs: []Log{}}
	var available map[int64]bool
	var availabilityErr error
	if directory, ok := e.host.(StatusDirectory); ok && s.active {
		available, availabilityErr = directory.AvailableAccounts(ctx)
	}
	e.mu.RLock()
	status.Logs = append(status.Logs, e.logs...)
	e.mu.RUnlock()
	for _, a := range s.config.Accounts {
		account := AccountStatus{AccountID: a.AccountID, Models: map[string]ModelStatus{}}
		for _, model := range Models {
			cfg, ok := a.Models[model]
			if !ok {
				continue
			}
			m := ModelStatus{Enabled: cfg.Enabled, TicketPlan: cfg.TicketPlan, State: "disabled"}
			if s.active && cfg.Enabled {
				e.mu.RLock()
				revision := e.identities[a.AccountID]
				e.mu.RUnlock()
				g := Guard{a.AccountID, model, s.revision, revision}
				var err error
				if revision == "" {
					err = ErrUnavailable
				}
				if availabilityErr != nil || (available != nil && !available[a.AccountID]) {
					err = ErrUnavailable
				}
				var slot Slot
				if err == nil {
					slot, _, err = e.read(ctx, g)
				}
				if err != nil {
					m.State = "unavailable"
					m.LastError = "state_unavailable"
				} else {
					now := e.opts.Now()
					m.Active = ticketStatus(slot.Active, cfg.TicketPlan, now)
					m.Ready = ticketStatus(slot.Ready, cfg.TicketPlan, now)
					m.Strikes = slot.Strikes
					m.Attempts = slot.Attempts
					m.Refreshing = slot.Harvesting || slot.HarvestRequested
					m.LastError = publicError(slot.LastError)
					m.State = "waiting"
					if usable(slot.Active, cfg.TicketPlan, now) {
						m.State = "ready"
					}
					if now.Before(slot.CooldownUntil) {
						m.State = "cooldown"
						cooldown := slot.CooldownUntil
						m.CooldownUntil = &cooldown
					}
					if slot.Harvesting {
						m.State = "harvesting"
					}
					if slot.HarvestRequested && !slot.Harvesting {
						m.State = "queued"
					}
					if slot.Paused {
						m.State = "cancelled"
					}
					e.mu.RLock()
					blocked := e.blocked[slotKey(a.AccountID, model)] == g
					e.mu.RUnlock()
					if blocked {
						m.State = "unavailable"
						m.LastError = "watchdog_persistence_failed"
						if m.Active != nil {
							m.Active.Usable = false
						}
					}
				}
			}
			account.Models[model] = m
		}
		status.Accounts = append(status.Accounts, account)
	}
	return status
}

func (e *Engine) StatusJSON(ctx context.Context) ([]byte, error) { return json.Marshal(e.Status(ctx)) }
