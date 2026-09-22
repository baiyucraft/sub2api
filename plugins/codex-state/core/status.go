package core

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Log struct {
	ID        uint64    `json:"id"`
	At        time.Time `json:"at"`
	AccountID int64     `json:"account_id"`
	Model     string    `json:"model"`
	Level     string    `json:"level"`
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
	AccountID                int64                  `json:"account_id"`
	Name                     string                 `json:"name,omitempty"`
	AccountType              string                 `json:"account_type,omitempty"`
	GroupIDs                 []int64                `json:"group_ids,omitempty"`
	BusinessEgressConfigured bool                   `json:"business_egress_configured"`
	Configured               bool                   `json:"configured"`
	Available                bool                   `json:"available"`
	Models                   map[string]ModelStatus `json:"models"`
}
type Status struct {
	Version        string          `json:"version"`
	Running        bool            `json:"running"`
	Enabled        bool            `json:"enabled"`
	ConfigRevision uint64          `json:"config_revision"`
	Summary        StatusSummary   `json:"summary"`
	Accounts       []AccountStatus `json:"accounts"`
	Logs           []Log           `json:"logs"`
}

type StatusSummary struct {
	ConfiguredAccounts int            `json:"configured_accounts"`
	NoEgressAccounts   int            `json:"no_egress_accounts"`
	ReadyByModel       map[string]int `json:"ready_by_model"`
	CooldownByModel    map[string]int `json:"cooldown_by_model"`
	StrikeCount        int            `json:"strike_count"`
}

func (e *Engine) log(g Guard, code string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.nextLogID++
	e.logs = append(e.logs, Log{ID: e.nextLogID, At: e.opts.Now().UTC(), AccountID: g.AccountID, Model: g.Model, Level: logLevel(code), Code: code})
	if len(e.logs) > 100 {
		e.logs = append([]Log(nil), e.logs[len(e.logs)-100:]...)
	}
}

func logLevel(code string) string {
	lower := strings.ToLower(code)
	if strings.Contains(lower, "failed") || strings.Contains(lower, "rejected") || strings.Contains(lower, "error") || strings.Contains(lower, "unavailable") {
		return "error"
	}
	return "info"
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
	status := Status{Version: Version, Running: s.active, Enabled: s.config.Enabled, ConfigRevision: revision, Summary: StatusSummary{ConfiguredAccounts: len(s.config.Accounts), ReadyByModel: map[string]int{}, CooldownByModel: map[string]int{}}, Accounts: []AccountStatus{}, Logs: []Log{}}
	configured := make(map[int64]AccountConfig, len(s.config.Accounts))
	for _, account := range s.config.Accounts {
		configured[account.AccountID] = account
	}
	var directoryAccounts []DirectoryAccount
	var availabilityErr error
	directorySupported := false
	if directory, ok := e.host.(StatusDirectory); ok {
		directorySupported = true
		directoryAccounts, availabilityErr = directory.DirectoryAccounts(ctx)
	}
	e.mu.RLock()
	status.Logs = append(status.Logs, e.logs...)
	e.mu.RUnlock()
	seen := make(map[int64]bool, len(directoryAccounts))
	rows := make([]DirectoryAccount, 0, len(directoryAccounts)+len(s.config.Accounts))
	// A missing or failed directory is a best-effort status degradation. Keep
	// configured accounts usable in that case; only a successful directory
	// response is authoritative for deletion and live egress metadata.
	directoryAuthoritative := directorySupported && availabilityErr == nil
	if availabilityErr == nil {
		for _, account := range directoryAccounts {
			if account.AccountID > 0 && !seen[account.AccountID] {
				seen[account.AccountID] = true
				account.Present = true
				account.GroupIDs = normalizeGroupIDs(account.GroupIDs)
				rows = append(rows, account)
			}
		}
	}
	for _, account := range s.config.Accounts {
		if !seen[account.AccountID] {
			rows = append(rows, DirectoryAccount{AccountID: account.AccountID, Present: false})
		}
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].AccountID < rows[j].AccountID })
	for _, metadata := range rows {
		a, isConfigured := configured[metadata.AccountID]
		account := AccountStatus{AccountID: metadata.AccountID, Name: metadata.Name, AccountType: metadata.AccountType, GroupIDs: normalizeGroupIDs(metadata.GroupIDs), BusinessEgressConfigured: metadata.BusinessEgressConfigured, Configured: isConfigured, Available: !directoryAuthoritative || metadata.Present, Models: map[string]ModelStatus{}}
		if isConfigured && directoryAuthoritative && (!account.Available || !account.BusinessEgressConfigured) {
			status.Summary.NoEgressAccounts++
		}
		for _, model := range Models {
			cfg := ModelConfig{TicketPlan: "pro"}
			if isConfigured {
				if saved, ok := a.Models[model]; ok {
					cfg = saved
				}
			}
			m := ModelStatus{Enabled: cfg.Enabled, TicketPlan: cfg.TicketPlan, State: "disabled"}
			if s.active && isConfigured && cfg.Enabled {
				e.mu.RLock()
				revision := e.identities[metadata.AccountID]
				e.mu.RUnlock()
				g := Guard{metadata.AccountID, model, s.revision, revision}
				var err error
				if revision == "" {
					err = ErrUnavailable
				}
				if directoryAuthoritative && (!account.Available || !account.BusinessEgressConfigured) {
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
					status.Summary.StrikeCount += slot.Strikes
					m.Attempts = slot.Attempts
					m.Refreshing = slot.Harvesting || slot.HarvestRequested
					m.LastError = publicError(slot.LastError)
					m.State = "waiting"
					if usable(slot.Active, cfg.TicketPlan, now) {
						m.State = "ready"
						status.Summary.ReadyByModel[model]++
					}
					if now.Before(slot.CooldownUntil) {
						m.State = "cooldown"
						status.Summary.CooldownByModel[model]++
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
					blocked := e.blocked[slotKey(metadata.AccountID, model)] == g
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

func normalizeGroupIDs(input []int64) []int64 {
	if len(input) == 0 {
		return nil
	}
	seen := make(map[int64]struct{}, len(input))
	ids := make([]int64, 0, len(input))
	for _, id := range input {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}
