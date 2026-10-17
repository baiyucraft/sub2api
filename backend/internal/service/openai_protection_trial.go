package service

import (
	"context"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/forkscheduling"
)

// One non-bankable credit is funded by twenty successful new allocations.
// A reservation keeps the credit until a transport actually sends it.
type openAITTFTTrialState struct {
	opportunities uint8
	sequence      uint64
	credit        bool
	lastAccountID int64
	lease         *openAITTFTTrial
}

type openAITTFTTrial struct {
	guard             *openAITTFTGuard
	key               openAITTFTGuardKey
	probeKey          openAITTFTGuardProbeKey
	probe             *openAITTFTGuardProbeState
	groupID           int64
	config            OpenAITTFTGuardConfigSnapshot
	runtimeID         string
	policyRevision    uint64
	degradedAt        time.Time
	cycle             uint64
	generation        uint64
	recoveryEpoch     uint64
	opportunityBudget *cacheGuardRequestBudget
	once              sync.Once
	sent              atomic.Bool
	finished          bool // guarded by guard.mu
}

func protectionOpportunityBudget(ctx context.Context) *cacheGuardRequestBudget {
	identity, _ := ctx.Value(cacheGuardSchedulingKey{}).(cacheGuardSchedulingIdentity)
	if identity.RequestID == "" {
		return nil
	}
	return identity.budget
}

func (g *openAITTFTGuard) trialStateLocked(k openAITTFTGuardProbeKey) *openAITTFTGuardProbeState {
	p := g.groupProbes[k]
	if p == nil {
		if len(g.groupProbes) >= g.maxEntries {
			return nil
		}
		p = &openAITTFTGuardProbeState{}
		g.groupProbes[k] = p
	}
	return p
}

func (g *openAITTFTGuard) budgetStateLocked(groupID int64, model string) *openAITTFTTrialState {
	if g.trialBudgets == nil {
		g.trialBudgets = make(map[openAITTFTGuardBudgetKey]*openAITTFTTrialState)
	}
	k := openAITTFTGuardBudgetKey{groupID: groupID, model: normalizeOpenAIAccountModelTransientModel(model)}
	if p := g.trialBudgets[k]; p != nil {
		return p
	}
	if len(g.trialBudgets) >= g.maxEntries {
		return nil
	}
	p := &openAITTFTTrialState{}
	g.trialBudgets[k] = p
	return p
}

func (g *openAITTFTGuard) recordAllocationOpportunity(ctx context.Context, groupID int64, model string, cfg OpenAITTFTGuardConfigSnapshot) {
	b := protectionOpportunityBudget(ctx)
	if g == nil || b == nil || !cfg.Enabled {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.acceptRequestConfigLocked(groupID, cfg) {
		return
	}
	scope, ok := openAITTFTGuardScopeFor(groupID, cfg)
	model = normalizeOpenAIAccountModelTransientModel(model)
	if !ok || model == "" {
		return
	}
	p := g.budgetStateLocked(groupID, model)
	if p == nil || !b.ttftOpportunity.CompareAndSwap(false, true) {
		return
	}
	probe := g.trialStateLocked(openAITTFTGuardProbeKey{scopeID: scope, model: model})
	if probe != nil {
		probe.sequence++
	}
	p.sequence++
	if p.credit {
		return
	}
	p.opportunities++
	if p.opportunities >= openAITTFTGuardProbeEvery {
		p.opportunities = 0
		p.credit = true
	}
}

func (g *openAITTFTGuard) reserveTrialOpportunity(ctx context.Context, groupID int64, accounts []Account, model string, cfg OpenAITTFTGuardConfigSnapshot, quality map[int64]forkscheduling.TTFTQuality) *openAITTFTTrial {
	b := protectionOpportunityBudget(ctx)
	if g == nil || b == nil || !cfg.Enabled || b.ttftOpportunity.Load() || b.ttftTrialAttempted.Load() {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.acceptRequestConfigLocked(groupID, cfg) {
		return nil
	}
	scope, ok := openAITTFTGuardScopeFor(groupID, cfg)
	if !ok {
		return nil
	}
	// Keep canonical model budgets independent even when accounts map aliases.
	sorted := append([]Account(nil), accounts...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	for _, candidate := range sorted {
		m := canonicalOpenAIAccountSchedulingModel(&candidate, model)
		pk := openAITTFTGuardProbeKey{scopeID: scope, model: normalizeOpenAIAccountModelTransientModel(m)}
		p := g.groupProbes[pk]
		budget := g.budgetStateLocked(groupID, pk.model)
		if p == nil || budget == nil || !budget.credit || budget.lease != nil {
			continue
		}
		var targets []Account
		for _, a := range sorted {
			if normalizeOpenAIAccountModelTransientModel(canonicalOpenAIAccountSchedulingModel(&a, model)) == pk.model && jointTTFTClass(a.ID, quality, cfg.MinSamples) != 0 {
				targets = append(targets, a)
			}
		}
		if len(targets) == 0 {
			continue
		}
		start := sort.Search(len(targets), func(i int) bool { return targets[i].ID > budget.lastAccountID })
		for step := 0; step < len(targets); step++ {
			a := &targets[(start+step)%len(targets)]
			raw, valid := g.sampleKeyLocked(a.ID, accountUpstreamKeyID(a), pk.model)
			if !valid {
				continue
			}
			if g.trialLeases[raw] != nil || len(g.trialLeases) >= g.maxEntries {
				continue
			}
			if !b.ttftTrialAttempted.CompareAndSwap(false, true) {
				return nil
			}
			k := raw
			k.scopeID = scope
			p.cycle++
			t := &openAITTFTTrial{guard: g, key: k, probeKey: pk, probe: p, groupID: groupID, config: cfg, runtimeID: g.runtimeID, cycle: p.cycle, opportunityBudget: b}
			if g.policySnapshot != nil {
				t.policyRevision = g.policySnapshot.revision
			}
			if e := g.entries[k]; e != nil {
				t.generation = e.generation
				t.degradedAt = e.degradedAt
				t.recoveryEpoch = e.recoveryEpoch
			}
			budget.lease = t
			if g.trialLeases == nil {
				g.trialLeases = make(map[openAITTFTGuardKey]*openAITTFTTrial)
			}
			g.trialLeases[raw] = t
			budget.lastAccountID = a.ID
			return t
		}
	}
	return nil
}

func (t *openAITTFTTrial) validLocked() bool {
	g := t.guard
	if t.finished || t.runtimeID != g.runtimeID || !g.acceptRequestConfigLocked(t.groupID, t.config) {
		return false
	}
	if g.policySnapshot != nil && t.policyRevision != g.policySnapshot.revision {
		return false
	}
	p := g.groupProbes[t.probeKey]
	budget := g.budgetStateLocked(t.groupID, t.probeKey.model)
	if p == nil || p != t.probe || budget == nil || budget.lease != t {
		return false
	}
	if e := g.entries[t.key]; t.generation > 0 && (e == nil || e.generation != t.generation) {
		return false
	}
	return true
}

func (t *openAITTFTTrial) validate() bool {
	if t == nil || t.guard == nil {
		return false
	}
	t.guard.mu.Lock()
	defer t.guard.mu.Unlock()
	return t.validLocked()
}

func (t *openAITTFTTrial) markSent() bool {
	if t == nil || t.guard == nil {
		return false
	}
	g := t.guard
	g.mu.Lock()
	defer g.mu.Unlock()
	if !t.validLocked() {
		return false
	}
	if t.sent.Load() {
		return true
	}
	budget := g.budgetStateLocked(t.groupID, t.probeKey.model)
	if budget == nil || !budget.credit {
		return false
	}
	budget.credit = false
	// A sent trial may fund the next period once per logical request. Ordinary
	// retries and account reselections share the same opportunity CAS.
	budget.opportunities = 0
	if t.opportunityBudget != nil && t.opportunityBudget.ttftOpportunity.CompareAndSwap(false, true) {
		budget.opportunities = 1
		budget.sequence++
		if probe := g.groupProbes[t.probeKey]; probe != nil {
			probe.sequence++
		}
	}
	t.sent.Store(true)
	return true
}

func (t *openAITTFTTrial) finish(_ bool, _ *int) {
	if t == nil || t.guard == nil {
		return
	}
	t.once.Do(func() {
		g := t.guard
		g.mu.Lock()
		defer g.mu.Unlock()
		t.finished = true
		if budget := g.budgetStateLocked(t.groupID, t.probeKey.model); budget != nil && budget.lease == t {
			budget.lease = nil
		}
		raw := t.key
		raw.scopeID = 0
		if g.trialLeases[raw] == t {
			delete(g.trialLeases, raw)
		}
	})
}

// MarkSchedulingProtectionTrialSent is called at the real transport boundary,
// after scope, database and cache cohort admission have all succeeded.
func MarkSchedulingProtectionTrialSent(ctx context.Context, account *Account, actualModel string) error {
	if ctx == nil {
		return nil
	}
	selection, _ := ctx.Value(protectionSelectionKey{}).(*protectionSelection)
	if selection == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !schedulingProtectionSendSelected(ctx, account) {
		return nil
	}
	// No observation holder is required for funding. When a transport did
	// prepare one, its actual outbound model remains authoritative.
	if strings.TrimSpace(actualModel) == "" {
		if attempt, _ := ctx.Value(cacheUsageAttemptContextKey{}).(*cacheUsageAttempt); attempt != nil {
			attempt.mu.Lock()
			actualModel = attempt.model
			attempt.mu.Unlock()
		}
	}
	if actualModel == "" {
		metadata, _ := ctx.Value(pluginRequestMetadataKey{}).(pluginRequestMetadata)
		actualModel = metadata.Model
	}
	model := normalizeOpenAIAccountModelTransientModel(actualModel)
	if model == "" {
		model = selection.allocationModel
	}
	allocationIdentityMatches := selection.newAllocation && account != nil && account.ID == selection.allocationAccountID && accountUpstreamKeyID(account) == selection.allocationUpstreamKeyID && model == selection.allocationModel
	t := selection.trial
	if t == nil {
		// Funding is committed only after final admission succeeds. This also
		// covers ordinary selections that did not reserve a trial lease.
		if allocationIdentityMatches && selection.service != nil {
			selection.service.getOpenAITTFTGuard().recordAllocationOpportunity(ctx, selection.groupID, selection.allocationModel, selection.allocationConfig)
		}
		return nil
	}
	// A trial must match the prepared actual model, never its ticket's model.
	// `model` is the normalized model actually admitted for this send. It may
	// come from the prepared cache/TTFT holder or plugin metadata; when those
	// holders are absent, the selection's frozen allocation model is the only
	// request-local prepared identity. Compare that resolved identity so a real
	// send cannot lose its trial credit merely because the transport did not
	// expose a second model field.
	if account == nil || account.ID != t.key.accountID || accountUpstreamKeyID(account) != t.key.upstreamKeyID || model != t.key.model || !t.markSent() {
		return schedulingProtectionScopeChangedError()
	}
	return nil
}
