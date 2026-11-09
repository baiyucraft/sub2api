package service

import (
	"context"
	"reflect"
	"sort"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/upstreamcacheguard"
)

type cacheGuardPreference struct {
	Active          bool
	ExistingSession bool
	AffinityExpired bool
	KeepAccountID   int64
	Migration       bool
	// CacheMigrationTriggered records the source's low-cache evidence even if
	// no target has enough samples to prove a five-point improvement.
	CacheMigrationTriggered bool
	PriorityReallocation    bool
	PriorityTargetIDs       map[int64]struct{}
	Tiers                   [][]int64
	ScopeIDs                map[int64]struct{}
	CandidateIDs            []int64
	CandidateKeyIDs         map[int64]int64
	// QualifiedIDs are fresh, unprotected cache candidates. They may share one
	// native scheduling pool even when their measured cache percentages differ.
	QualifiedIDs                []int64
	TrialAccountID              int64
	PriorityReallocationAllowed bool
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
	var existingSession, affinityExpired bool
	defer func() {
		// Metadata, candidates and a cold session read can straddle a policy
		// publication. Never return a decision assembled from different epochs.
		if r.engine.PolicyVersion() != epoch || !protectionPolicyEvaluationCurrent(ctx) {
			preference = cacheGuardPreference{}
			return
		}
		// Ordinary fallbacks must not rehydrate an already expired soft binding.
		preference.ExistingSession = existingSession
		preference.AffinityExpired = affinityExpired
		if affinityExpired && groupID != nil {
			rememberExpiredProtectionAffinity(ctx, *groupID)
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
	existingSession, affinityExpired = session.Known, session.AffinityExpired
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
	if snapshot.Fresh {
		for id := range eligible {
			if states[id].Fresh && !cacheGuardAccountProtected(states[id]) {
				p.QualifiedIDs = append(p.QualifiedIDs, id)
			}
		}
		sort.Slice(p.QualifiedIDs, func(i, j int) bool { return p.QualifiedIDs[i] < p.QualifiedIDs[j] })
	}
	if session.Known && !session.AffinityExpired && session.PreferredAccountID > 0 {
		if _, usable := eligible[session.PreferredAccountID]; !usable {
			return cacheGuardPreference{}
		}
		if _, ok := eligible[session.PreferredAccountID]; ok {
			source := states[session.PreferredAccountID]
			sourceAccount := eligible[session.PreferredAccountID]
			sourceModel := canonicalOpenAIAccountSchedulingModel(&sourceAccount, model)
			p.PriorityReallocationAllowed = snapshot.Fresh && source.Fresh && session.Fresh && !req.StartedAt.Before(session.ResidenceUntil)
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
			p.CacheMigrationTriggered = true
			var better []int64
			for id := range eligible {
				a := states[id]
				target := eligible[id]
				if a.Fresh && a.Phase != upstreamcacheguard.Unknown && id != session.PreferredAccountID && canonicalOpenAIAccountSchedulingModel(&target, model) == sourceModel && a.ValidRequests > s.MinValidRequests && a.CachePercent >= source.CachePercent+5 {
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
	var trial upstreamcacheguard.TrialPlan
	// A known session without a confirmed owner may be waiting for a terminal
	// binding callback or its first completion. Its natural plan already exists;
	// neither pending-owner reads nor retries need another planning transaction.
	if !session.AffinityExpired && !session.Known {
		dbCtx, cancel := cacheGuardDatabaseContextForStage(ctx, "trial_plan")
		var err error
		trial, err = guard.PlanTrial(dbCtx, req)
		cancel()
		if err != nil || !protectionPolicyEvaluationCurrent(ctx) {
			return cacheGuardPreference{}
		}
	}
	if trial.TrialSlot {
		var recovery []int64
		for id := range eligible {
			a := states[id]
			if cacheGuardRecoveryReady(a, req.StartedAt) {
				recovery = append(recovery, id)
			}
		}
		sort.Slice(recovery, func(i, j int) bool { return recovery[i] < recovery[j] })
		if len(recovery) > 0 {
			index := int(trial.TrialOrdinal % int64(len(recovery)))
			p.TrialAccountID = recovery[index]
			p.Tiers = append(p.Tiers, []int64{recovery[index]})
		}
	}
	// Protection quality is the outer boundary. The original scheduler applies
	// preferred pools and account priorities within each tier. A fresh account
	// without sufficient evidence is neutral, never a fabricated cache rate.
	var known, unknown, protected []int64
	for id := range eligible {
		a := states[id]
		if cacheGuardAccountProtected(a) {
			protected = append(protected, id)
		} else if a.Phase != upstreamcacheguard.Unknown && a.ValidRequests > s.MinValidRequests {
			known = append(known, id)
		} else {
			unknown = append(unknown, id)
		}
	}
	p.Tiers = append(p.Tiers, cacheGuardOrdinaryRateTiers(known, unknown, states, ttftExcluded)...)
	p.Tiers = append(p.Tiers, cacheGuardRateTiers(protected, states, ttftExcluded)...)
	return p
}

// Build rate bands from measured evidence only. Neutral candidates join the
// first ordinary band without changing the known bands or a recovery trial.
// Keeping bands explicit avoids a non-transitive "unknown equals any rate"
// comparator, and never assigns a made-up percentage to an unknown account.
func cacheGuardOrdinaryRateTiers(known, cold []int64, states map[int64]upstreamcacheguard.AccountSnapshot, ttftExcluded map[int64]struct{}) [][]int64 {
	tiers := cacheGuardRateTiers(known, states, ttftExcluded)
	if len(cold) == 0 {
		return tiers
	}
	cold = append([]int64(nil), cold...)
	sort.Slice(cold, func(i, j int) bool { return cold[i] < cold[j] })
	if len(tiers) == 0 {
		return [][]int64{cold}
	}
	tiers[0] = append(tiers[0], cold...)
	return tiers
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
	return cacheGuardLatencyEvidenceTiers(tiers, func(id int64) (float64, cacheGuardLatencyEvidence) {
		value, known := latency(id)
		if known {
			return value, cacheGuardLatencyKnown
		}
		return 0, cacheGuardLatencyUnavailable
	})
}

type cacheGuardLatencyEvidence uint8

const (
	cacheGuardLatencyUnavailable cacheGuardLatencyEvidence = iota
	cacheGuardLatencyKnown
	cacheGuardLatencyCold
)

// Cold TTFT is neutral inside its existing cache band. Inaccessible evidence
// keeps the previous conservative no-refinement behavior instead of borrowing
// the cold-start exception. Known-only bands retain their exact EWMA ordering.
func cacheGuardLatencyEvidenceTiers(tiers [][]int64, latency func(int64) (float64, cacheGuardLatencyEvidence)) [][]int64 {
	var result [][]int64
	for _, tier := range tiers {
		values := map[int64]float64{}
		complete := true
		var ordered, cold []int64
		for _, id := range tier {
			value, evidence := latency(id)
			if evidence == cacheGuardLatencyUnavailable {
				complete = false
				break
			}
			if evidence == cacheGuardLatencyCold {
				cold = append(cold, id)
				continue
			}
			ordered = append(ordered, id)
			values[id] = value
		}
		if !complete || len(tier) < 2 {
			result = append(result, tier)
			continue
		}
		if len(ordered) == 0 {
			result = append(result, tier)
			continue
		}
		sort.SliceStable(ordered, func(i, j int) bool { return values[ordered[i]] < values[ordered[j]] })
		for start := 0; start < len(ordered); {
			end := start + 1
			for end < len(ordered) && values[ordered[end]] == values[ordered[start]] {
				end++
			}
			part := ordered[start:end]
			if start == 0 && len(cold) > 0 {
				part = append(append([]int64(nil), part...), cold...)
			}
			result = append(result, part)
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
