package service

import (
	"context"
	"hash/fnv"
	"reflect"
	"sort"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/upstreamcacheguard"
)

type cacheGuardPreference struct {
	Active          bool
	KeepAccountID   int64
	Migration       bool
	Tiers           [][]int64
	ScopeIDs        map[int64]struct{}
	CandidateIDs    []int64
	CandidateKeyIDs map[int64]int64
}
type cacheGuardTentativeKey struct{}
type cacheGuardMigrationKey struct{}

func cacheGuardAdmittedMigration(ctx context.Context, accountID int64) bool {
	id, _ := ctx.Value(cacheGuardMigrationKey{}).(int64)
	return id > 0 && id == accountID
}
func withCacheGuardTentativeSelection(ctx context.Context) context.Context {
	return context.WithValue(ctx, cacheGuardTentativeKey{}, true)
}
func cacheGuardTentativeSelection(ctx context.Context) bool {
	v, _ := ctx.Value(cacheGuardTentativeKey{}).(bool)
	return v
}

// buildCacheGuardPreference produces soft candidate layers. The original
// scheduler remains responsible for every capability, quota, route and slot
// condition; callers must always retry its original hard candidate set.
func buildCacheGuardPreference(ctx context.Context, groupID *int64, sessionHash, model, protocol string, accounts []Account, ttftExcluded map[int64]struct{}) (preference cacheGuardPreference) {
	r := currentUpstreamCacheGuardRunner()
	p := cacheGuardPreference{}
	if r == nil || r.closed.Load() || protectionOrdinaryFallback(ctx) {
		return p
	}
	epoch := r.engine.PolicyVersion()
	defer func() {
		// Metadata, candidates and a cold session read can straddle a policy
		// publication. Never return a decision assembled from different epochs.
		if r.engine.PolicyVersion() != epoch || !protectionPolicyEvaluationCurrent(ctx) {
			preference = cacheGuardPreference{}
		}
	}()
	guard := forkCacheGuardReader()
	if guard == nil {
		return p
	}
	if groupID == nil {
		return p
	}
	if identity, ok := ctx.Value(cacheGuardSchedulingKey{}).(cacheGuardSchedulingIdentity); ok && identity.budget != nil {
		identity.budget.mu.Lock()
		identity.budget.groupID = *groupID
		identity.budget.mu.Unlock()
	}
	if !SchedulingProtectionGroupSelected(r.protectionPolicyReader(), *groupID) {
		return p
	}
	req, ok := r.logicalRequest(ctx, groupID, sessionHash, model, protocol)
	if !ok {
		return p
	}
	candidates := make([]upstreamcacheguard.Candidate, 0, len(accounts))
	for _, a := range accounts {
		candidates = append(candidates, upstreamcacheguard.Candidate{AccountID: a.ID, UpstreamKeyID: accountUpstreamKeyID(&a)})
	}
	view := r.engine.CachedCandidatesReadView(req, candidates)
	if !view.Consistent || !view.Snapshot.Available || view.PolicyVersion != epoch || view.Settings.Mode != upstreamcacheguard.Protect || !view.Settings.ContainsGroup(*groupID) {
		return p
	}
	if !view.SessionCached {
		var err error
		view, err = r.engine.CandidatesReadViewWithDatabaseContext(ctx, req, candidates, cacheGuardDatabaseContext)
		if err != nil || !view.Consistent || view.PolicyVersion != epoch {
			return p
		}
	}
	if view.Session.Known && view.Session.PreferredAccountID > 0 {
		for i := range accounts {
			account := &accounts[i]
			if account.ID == view.Session.PreferredAccountID {
				req.Model = canonicalOpenAIAccountSchedulingModel(account, model)
				req.Protocol = cacheGuardAccountProtocol(ctx, account, req.Model, protocol)
				req.AccountID, req.UpstreamKeyID = account.ID, accountUpstreamKeyID(account)
				view = r.engine.CachedCandidatesReadView(req, candidates)
				if !view.Consistent || !view.Snapshot.Available || view.PolicyVersion != epoch {
					return p
				}
				break
			}
		}
	}
	s, snapshot, session := view.Settings, view.Snapshot, view.Session
	if policy, ok := protectionPolicyForEvaluation(ctx, *groupID); ok && !reflect.DeepEqual(policy.Cache, s) {
		return p
	}
	if s.Mode != upstreamcacheguard.Protect || !s.ContainsGroup(*groupID) {
		return p
	}
	p.Active = true
	p.ScopeIDs = map[int64]struct{}{}
	p.CandidateKeyIDs = map[int64]int64{}
	states := map[int64]upstreamcacheguard.AccountSnapshot{}
	eligible := map[int64]Account{}
	for _, cached := range snapshot.Accounts {
		states[cached.AccountID] = cached
	}
	for _, a := range accounts {
		if !schedulingProtectionAccountSelected(r.protectionPolicyReader(), &a) {
			continue
		}
		p.CandidateIDs = append(p.CandidateIDs, a.ID)
		if a.IsUpstreamBound() && a.IsSchedulable() && a.IsModelSupported(model) {
			p.ScopeIDs[a.ID] = struct{}{}
			p.CandidateKeyIDs[a.ID] = accountUpstreamKeyID(&a)
			if _, usable := states[a.ID]; usable {
				eligible[a.ID] = a
			}
		}
	}
	if len(eligible) == 0 {
		return cacheGuardPreference{}
	}
	var firstPool *Account
	for i := range accounts {
		a := &accounts[i]
		if firstPool == nil || cacheGuardPoolBetter(a, firstPool, groupID) {
			firstPool = a
		}
	}
	if session.Known && session.PreferredAccountID > 0 {
		if _, usable := eligible[session.PreferredAccountID]; !usable {
			return cacheGuardPreference{}
		}
		if _, ok := eligible[session.PreferredAccountID]; ok {
			source := states[session.PreferredAccountID]
			if !snapshot.Fresh || !source.Fresh || !session.Fresh {
				// A stale snapshot may preserve an existing session affinity, but
				// must not create a new trial or migrate from old observations.
				p.KeepAccountID = session.PreferredAccountID
				return p
			}
			if !session.PendingLow || req.StartedAt.Before(session.ResidenceUntil) || !cacheGuardAccountProtected(source) {
				p.KeepAccountID = session.PreferredAccountID
				return p
			}
			// An expired window is unknown, rather than a zero-percent baseline
			// against which an account can be claimed to be five points better.
			if source.ValidRequests <= s.MinValidRequests {
				p.KeepAccountID = session.PreferredAccountID
				return p
			}
			var better []int64
			for id := range eligible {
				a := states[id]
				target := eligible[id]
				if a.Fresh && a.Phase != upstreamcacheguard.Unknown && id != session.PreferredAccountID && cacheGuardSamePool(&target, firstPool, groupID) && a.ValidRequests > s.MinValidRequests && a.CachePercent >= source.CachePercent+5 {
					better = append(better, id)
				}
			}
			if len(better) == 0 {
				p.KeepAccountID = session.PreferredAccountID
				return p
			}
			sort.Slice(better, func(i, j int) bool { return states[better[i]].CachePercent > states[better[j]].CachePercent })
			p.Migration = true
			p.KeepAccountID = session.PreferredAccountID
			p.Tiers = cacheGuardRateTiers(better, states, ttftExcluded)
			return p
		}
	}
	if !snapshot.Fresh {
		// No known session can safely use stale observations as a new routing
		// decision. Let the ordinary scheduler choose an account.
		return cacheGuardPreference{}
	}
	// Stale accounts remain valid only for their existing sessions. New
	// decisions use the independent fresh subset, so one account's failed
	// refresh neither attracts new sessions nor disables healthy candidates.
	for id := range eligible {
		if !states[id].Fresh {
			delete(eligible, id)
		}
	}
	if len(eligible) == 0 {
		return cacheGuardPreference{}
	}
	// Admission assigns a shared ordinal once per logical natural new session.
	// A later round, model switch or migration can never acquire a new trial slot.
	dbCtx, cancel := cacheGuardDatabaseContextForStage(ctx, "trial_plan")
	trial, err := guard.PlanTrial(dbCtx, req)
	cancel()
	if err != nil || !protectionPolicyEvaluationCurrent(ctx) {
		return cacheGuardPreference{}
	}
	if trial.TrialSlot {
		var recovery []int64
		for id := range eligible {
			a := states[id]
			target := eligible[id]
			if cacheGuardSamePool(&target, firstPool, groupID) && cacheGuardRecoveryReady(a, req.StartedAt) {
				recovery = append(recovery, id)
			}
		}
		sort.Slice(recovery, func(i, j int) bool { return recovery[i] < recovery[j] })
		if len(recovery) > 0 {
			index := int(trial.TrialOrdinal % int64(len(recovery)))
			p.Tiers = append(p.Tiers, []int64{recovery[index]})
		}
	}
	// Account priorities remain the outer pool boundary. Unknown cache evidence
	// gets a deterministic natural-session opportunity, never a fabricated rate.
	type pool struct {
		preferred bool
		priority  int
	}
	byPriority := map[pool][]int64{}
	priorities := []pool{}
	for id, a := range eligible {
		priority := pool{isAccountPreferredForGroup(&a, groupID), openAIAccountSchedulingPriority(&a)}
		if _, ok := byPriority[priority]; !ok {
			priorities = append(priorities, priority)
		}
		byPriority[priority] = append(byPriority[priority], id)
	}
	sort.Slice(priorities, func(i, j int) bool {
		if priorities[i].preferred != priorities[j].preferred {
			return priorities[i].preferred
		}
		return priorities[i].priority < priorities[j].priority
	})
	for _, priority := range priorities {
		var known, unknown, protected []int64
		for _, id := range byPriority[priority] {
			a := states[id]
			if cacheGuardAccountProtected(a) {
				protected = append(protected, id)
			} else if a.Phase != upstreamcacheguard.Unknown && a.ValidRequests > s.MinValidRequests {
				known = append(known, id)
			} else {
				unknown = append(unknown, id)
			}
		}
		if len(known) == 0 && len(protected) == 0 {
			p.Tiers = append(p.Tiers, byPriority[priority])
			continue
		}
		h := fnv.New32a()
		_, _ = h.Write([]byte(req.ID))
		unknownFirst := len(unknown) > 0 && h.Sum32()%10 == 0
		if unknownFirst {
			p.Tiers = append(p.Tiers, unknown)
		}
		p.Tiers = append(p.Tiers, cacheGuardRateTiers(known, states, ttftExcluded)...)
		if !unknownFirst && len(unknown) > 0 {
			p.Tiers = append(p.Tiers, unknown)
		}
		p.Tiers = append(p.Tiers, cacheGuardRateTiers(protected, states, ttftExcluded)...)
	}
	return p
}

func cacheGuardAccountProtected(account upstreamcacheguard.AccountSnapshot) bool {
	return account.Phase == upstreamcacheguard.Degraded || account.Phase == upstreamcacheguard.Cooling || account.Phase == upstreamcacheguard.Trial
}

func cacheGuardRecoveryReady(account upstreamcacheguard.AccountSnapshot, at time.Time) bool {
	// All executing groups share the account cohort and cooldown.
	if cacheGuardAccountProtected(account) {
		return !at.Before(account.CooldownUntil)
	}
	return false
}

func cacheGuardPoolBetter(a, b *Account, groupID *int64) bool {
	pa, pb := isAccountPreferredForGroup(a, groupID), isAccountPreferredForGroup(b, groupID)
	if pa != pb {
		return pa
	}
	return openAIAccountSchedulingPriority(a) < openAIAccountSchedulingPriority(b)
}
func cacheGuardSamePool(a, b *Account, groupID *int64) bool {
	return a != nil && b != nil && isAccountPreferredForGroup(a, groupID) == isAccountPreferredForGroup(b, groupID) && openAIAccountSchedulingPriority(a) == openAIAccountSchedulingPriority(b)
}
func cacheGuardRateTiers(ids []int64, states map[int64]upstreamcacheguard.AccountSnapshot, ttftExcluded map[int64]struct{}) [][]int64 {
	ids = append([]int64(nil), ids...)
	sort.SliceStable(ids, func(i, j int) bool {
		a, b := states[ids[i]], states[ids[j]]
		if a.CachePercent == b.CachePercent {
			return ids[i] < ids[j]
		}
		return a.CachePercent > b.CachePercent
	})
	var tiers [][]int64
	for len(ids) > 0 {
		best := states[ids[0]].CachePercent
		end := 0
		var normal, slow []int64
		for end < len(ids) && best-states[ids[end]].CachePercent <= 5 {
			id := ids[end]
			if _, bad := ttftExcluded[id]; bad {
				slow = append(slow, id)
			} else {
				normal = append(normal, id)
			}
			end++
		}
		if len(normal) > 0 {
			tiers = append(tiers, normal)
		}
		if len(slow) > 0 {
			tiers = append(tiers, slow)
		}
		ids = ids[end:]
	}
	return tiers
}

// Latency refines a cache/health band only when its candidates have real TTFT
// evidence. An unknown latency keeps the original load selection in that band.
func cacheGuardLatencyTiers(tiers [][]int64, latency func(int64) (float64, bool)) [][]int64 {
	var result [][]int64
	for _, tier := range tiers {
		values := map[int64]float64{}
		complete := true
		for _, id := range tier {
			value, known := latency(id)
			if !known {
				complete = false
				break
			}
			values[id] = value
		}
		if !complete || len(tier) < 2 {
			result = append(result, tier)
			continue
		}
		ordered := append([]int64(nil), tier...)
		sort.SliceStable(ordered, func(i, j int) bool { return values[ordered[i]] < values[ordered[j]] })
		for start := 0; start < len(ordered); {
			end := start + 1
			for end < len(ordered) && values[ordered[end]] == values[ordered[start]] {
				end++
			}
			result = append(result, ordered[start:end])
			start = end
		}
	}
	return result
}
func (p cacheGuardPreference) exclusionsFor(tier []int64, base map[int64]struct{}) map[int64]struct{} {
	allowed := map[int64]struct{}{}
	for _, id := range tier {
		allowed[id] = struct{}{}
	}
	excluded := cloneExcludedAccountIDs(base)
	if excluded == nil {
		excluded = map[int64]struct{}{}
	}
	for id := range p.ScopeIDs {
		if _, ok := allowed[id]; !ok {
			excluded[id] = struct{}{}
		}
	}
	if p.Migration {
		for _, id := range p.CandidateIDs {
			if _, ok := allowed[id]; !ok {
				excluded[id] = struct{}{}
			}
		}
	}
	return excluded
}

// Protocol is the warm-context family, not the downstream compatibility format.
func cacheGuardOpenAIProtocol(transport OpenAIUpstreamTransport) string {
	if transport == OpenAIUpstreamTransportResponsesWebsocketV2 || transport == OpenAIUpstreamTransportResponsesWebsocketV2Ingress {
		return "openai_ws"
	}
	return "openai"
}

func cacheGuardNow() time.Time { return time.Now().UTC() }
