package service

import (
	"context"
	"sort"
	"sync"
	"time"
)

type protectionEvaluationKey struct{}

func withProtectionEvaluation(ctx context.Context) context.Context {
	if _, ok := ctx.Value(protectionEvaluationKey{}).(time.Time); ok {
		return ctx
	}
	return context.WithValue(ctx, protectionEvaluationKey{}, time.Now().UTC())
}
func protectionEvaluationTime(ctx context.Context, fallback func() time.Time) time.Time {
	if at, ok := ctx.Value(protectionEvaluationKey{}).(time.Time); ok {
		return at
	}
	return fallback().UTC()
}

type protectionCandidateReadsKey struct{}
type protectionCandidatePoolKey struct {
	owner    any
	groupID  int64
	hasGroup bool
	platform string
	forced   bool
}
type protectionCandidatePool struct {
	accounts []Account
	mixed    bool
	index    map[int64]int
}

// Only immutable selection hints are reused. Slot acquisition and the existing
// fresh account/Key/database checks remain authoritative on every attempt.
type protectionCandidateReads struct {
	mu    sync.Mutex
	pools map[protectionCandidatePoolKey]*protectionCandidatePool
}
type protectionCandidateTierKey struct{}
type protectionCandidateScopeKey struct{}
type protectionCandidateScope struct {
	ids     map[int64]struct{}
	mu      sync.Mutex
	outside map[*protectionCandidatePool][]int
}
type protectionCandidateTier struct {
	scope    map[int64]struct{}
	ids      []int64
	prepared *protectionCandidateScope
}

func withProtectionCandidateReads(ctx context.Context) context.Context {
	ctx = withProtectionEvaluation(ctx)
	if _, ok := ctx.Value(protectionCandidateReadsKey{}).(*protectionCandidateReads); ok {
		return ctx
	}
	return context.WithValue(ctx, protectionCandidateReadsKey{}, &protectionCandidateReads{pools: make(map[protectionCandidatePoolKey]*protectionCandidatePool)})
}

// A rejected protection hint must not become an ordinary selection through the
// same old directory. Ordinary fallback performs its original current read.
func withoutProtectionCandidateReads(ctx context.Context) context.Context {
	if reads, _ := ctx.Value(protectionCandidateReadsKey{}).(*protectionCandidateReads); reads == nil {
		return ctx
	}
	return context.WithValue(ctx, protectionCandidateReadsKey{}, (*protectionCandidateReads)(nil))
}

func protectionCandidateKey(owner any, groupID *int64, platform string, forced bool) protectionCandidatePoolKey {
	return protectionCandidatePoolKey{owner: owner, groupID: derefGroupID(groupID), hasGroup: groupID != nil, platform: platform, forced: forced}
}

func readProtectionCandidates(ctx context.Context, key protectionCandidatePoolKey) ([]Account, bool, bool) {
	reads, _ := ctx.Value(protectionCandidateReadsKey{}).(*protectionCandidateReads)
	if reads == nil {
		return nil, false, false
	}
	reads.mu.Lock()
	pool := reads.pools[key]
	reads.mu.Unlock()
	if pool == nil {
		return nil, false, false
	}
	tier, scoped := ctx.Value(protectionCandidateTierKey{}).(protectionCandidateTier)
	if !scoped {
		return append([]Account(nil), pool.accounts...), pool.mixed, true
	}
	// Retain source order, including ordinary accounts outside the soft scope.
	// Those accounts remain eligible under the previous exclusion semantics.
	indices := make([]int, 0, len(tier.ids))
	for _, id := range tier.ids {
		if i, ok := pool.index[id]; ok {
			indices = append(indices, i)
		}
	}
	if tier.prepared != nil {
		tier.prepared.mu.Lock()
		outside, known := tier.prepared.outside[pool]
		if !known {
			for i := range pool.accounts {
				if _, protected := tier.prepared.ids[pool.accounts[i].ID]; !protected {
					outside = append(outside, i)
				}
			}
			tier.prepared.outside[pool] = outside
		}
		tier.prepared.mu.Unlock()
		indices = append(indices, outside...)
	} else {
		for i := range pool.accounts {
			if _, protected := tier.scope[pool.accounts[i].ID]; !protected {
				indices = append(indices, i)
			}
		}
	}
	sort.Ints(indices)
	accounts := make([]Account, 0, len(indices))
	last := -1
	for _, i := range indices {
		if i != last {
			accounts = append(accounts, pool.accounts[i])
			last = i
		}
	}
	return accounts, pool.mixed, true
}

func rememberProtectionCandidates(ctx context.Context, key protectionCandidatePoolKey, accounts []Account, mixed bool) {
	reads, _ := ctx.Value(protectionCandidateReadsKey{}).(*protectionCandidateReads)
	if reads == nil {
		return
	}
	pool := &protectionCandidatePool{accounts: append([]Account(nil), accounts...), mixed: mixed, index: make(map[int64]int, len(accounts))}
	for i := range pool.accounts {
		pool.index[pool.accounts[i].ID] = i
	}
	reads.mu.Lock()
	if reads.pools[key] == nil {
		reads.pools[key] = pool
	}
	reads.mu.Unlock()
}

func withProtectionCandidateTier(ctx context.Context, scope map[int64]struct{}, ids []int64) context.Context {
	prepared, _ := ctx.Value(protectionCandidateScopeKey{}).(*protectionCandidateScope)
	return context.WithValue(ctx, protectionCandidateTierKey{}, protectionCandidateTier{scope: scope, ids: ids, prepared: prepared})
}

func withProtectionCandidateScope(ctx context.Context, scope map[int64]struct{}) context.Context {
	return context.WithValue(ctx, protectionCandidateScopeKey{}, &protectionCandidateScope{ids: scope, outside: make(map[*protectionCandidatePool][]int)})
}

func (p cacheGuardPreference) candidateScope() map[int64]struct{} {
	if !p.Migration {
		return p.ScopeIDs
	}
	scope := make(map[int64]struct{}, len(p.ScopeIDs)+len(p.CandidateIDs))
	for id := range p.ScopeIDs {
		scope[id] = struct{}{}
	}
	for _, id := range p.CandidateIDs {
		scope[id] = struct{}{}
	}
	return scope
}

// Tier attempts are synchronous. Build the exclusion union once and move only
// the previous/current tier, retaining caller exclusions even for allowed IDs.
func (p cacheGuardPreference) tierExclusions(base map[int64]struct{}) func([]int64) map[int64]struct{} {
	scope := p.candidateScope()
	excluded := make(map[int64]struct{}, len(scope)+len(base))
	for id := range base {
		excluded[id] = struct{}{}
	}
	for id := range scope {
		excluded[id] = struct{}{}
	}
	var previous []int64
	return func(tier []int64) map[int64]struct{} {
		for _, id := range previous {
			if _, inScope := scope[id]; inScope {
				excluded[id] = struct{}{}
			}
		}
		for _, id := range tier {
			if _, hard := base[id]; !hard {
				delete(excluded, id)
			}
		}
		previous = tier
		return excluded
	}
}
