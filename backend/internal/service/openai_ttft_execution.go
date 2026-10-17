package service

import (
	"context"
	"sort"
	"sync"
	"time"
)

// Only bounded, allowlisted execution metadata is exposed. No session IDs,
// attempt IDs, headers, request bodies or credentials enter this view.
type TTFTProtectionExecutionView struct {
	Protocol        string    `json:"protocol"`
	RuntimeID       string    `json:"runtime_id"`
	SelectionPath   string    `json:"selection_path"`
	FallbackReason  string    `json:"fallback_reason,omitempty"`
	SendAttempted   bool      `json:"send_attempted"`
	Completed       bool      `json:"completed"`
	Succeeded       bool      `json:"succeeded"`
	Trial           bool      `json:"trial"`
	ObservedAt      time.Time `json:"observed_at"`
	WindowStartedAt time.Time `json:"window_started_at"`
	SentCount       uint64    `json:"sent_count"`
	CompletedCount  uint64    `json:"completed_count"`
	SuccessCount    uint64    `json:"success_count"`
	TrialCount      uint64    `json:"trial_count"`
	RecoveryCounted *bool     `json:"recovery_counted,omitempty"`
	RecoveryReason  string    `json:"recovery_reason,omitempty"`
}

type ttftExecutionKey struct {
	accountID, keyID, groupID  int64
	model, protocol, runtimeID string
}
type ttftExecutionStore struct {
	mu      sync.Mutex
	entries map[ttftExecutionKey]TTFTProtectionExecutionView
}

type ttftRecoveryDecision struct {
	counted bool
	reason  string
}

// The decision belongs to a threshold projection and is captured while the
// owner evaluates the accepted terminal fact under its runtime lock.
type ttftRecoveryEvaluation struct {
	attemptID string
	decision  *ttftRecoveryDecision
}

func (r ttftRecoveryEvaluation) record(attemptID string, counted bool, reason string) {
	if r.decision != nil && r.decision.reason == "" && r.attemptID == attemptID {
		*r.decision = ttftRecoveryDecision{counted: counted, reason: reason}
	}
}

func (s *OpenAIGatewayService) recordTTFTExecution(ctx context.Context, o OpenAITTFTGuardObservation, protocol string, decision ...ttftRecoveryDecision) {
	if s == nil || o.SentAt.IsZero() {
		return
	}
	path, fallback := "ordinary", ""
	if p, _ := ctx.Value(protectionSelectionKey{}).(*protectionSelection); p != nil {
		if p.selectionPath != "" {
			path = p.selectionPath
		}
		fallback = p.fallbackReason
	}
	if o.Trial {
		path = "trial"
	}
	if protectionOrdinaryFallback(ctx) {
		path, fallback = "budget_fallback", "protection_budget_exhausted"
	}
	key := ttftExecutionKey{o.AccountID, o.UpstreamKeyID, o.GroupID, o.Model, protocol, o.RuntimeID}
	at := time.Now()
	store := &s.ttftExecutions
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.entries == nil {
		store.entries = make(map[ttftExecutionKey]TTFTProtectionExecutionView)
	}
	for k, v := range store.entries {
		if at.Sub(v.ObservedAt) >= openAITTFTGuardEntryTTL {
			delete(store.entries, k)
		}
	}
	v, exists := store.entries[key]
	if v.WindowStartedAt.IsZero() || at.Sub(v.WindowStartedAt) >= openAITTFTGuardEntryTTL {
		v = TTFTProtectionExecutionView{WindowStartedAt: at}
	}
	if !exists && len(store.entries) >= openAITTFTGuardDefaultMaxEntries {
		var oldest ttftExecutionKey
		var oldestAt time.Time
		for k, existing := range store.entries {
			if oldestAt.IsZero() || existing.ObservedAt.Before(oldestAt) {
				oldest, oldestAt = k, existing.ObservedAt
			}
		}
		delete(store.entries, oldest)
	}
	v.Protocol, v.RuntimeID, v.SelectionPath, v.FallbackReason = protocol, o.RuntimeID, path, fallback
	v.SendAttempted, v.Completed, v.Succeeded, v.Trial, v.ObservedAt = true, !o.CompletedAt.IsZero(), o.Success, o.Trial, at
	if o.CompletedAt.IsZero() {
		v.SentCount++
		if o.Trial {
			v.TrialCount++
		}
		// A new physical send is the start of a new execution row. Do not
		// leave the previous terminal decision attached to it.
		v.RecoveryCounted, v.RecoveryReason = nil, ""
	} else {
		v.CompletedCount++
		if o.Success {
			v.SuccessCount++
		}
		if len(decision) > 0 {
			counted := decision[0].counted
			v.RecoveryCounted, v.RecoveryReason = &counted, decision[0].reason
		}
	}
	store.entries[key] = v
}

func (s *OpenAIGatewayService) ttftExecutionViews(accountID, keyID, groupID int64, model string, now time.Time) []TTFTProtectionExecutionView {
	store := &s.ttftExecutions
	store.mu.Lock()
	defer store.mu.Unlock()
	views := []TTFTProtectionExecutionView{}
	for k, v := range store.entries {
		if now.Sub(v.ObservedAt) >= openAITTFTGuardEntryTTL {
			continue
		}
		if k.accountID == accountID && k.keyID == keyID && k.groupID == groupID && k.model == model {
			views = append(views, v)
		}
	}
	sort.Slice(views, func(i, j int) bool {
		if views[i].Protocol == views[j].Protocol {
			return views[i].RuntimeID < views[j].RuntimeID
		}
		return views[i].Protocol < views[j].Protocol
	})
	return views
}
