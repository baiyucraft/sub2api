package upstreamcacheguard

import "time"

// Published account states are immutable. Copy handles while locked, then own
// nested maps outside the lock for management callers projecting many groups.
func (e *Engine) CachedSharedAccountsAt(candidates []Candidate, at time.Time) map[int64]AccountSnapshot {
	out := make(map[int64]AccountSnapshot, len(candidates))
	e.mu.RLock()
	active := e.AnyPolicyActive()
	for _, candidate := range candidates {
		id := accountIdentity(candidate.AccountID, candidate.UpstreamKeyID)
		a, exists := e.accounts[id]
		if !exists {
			a.AccountState = AccountState{AccountID: candidate.AccountID, UpstreamKeyID: candidate.UpstreamKeyID, Phase: Healthy}
		}
		a = e.accountSnapshotAtLocked(id, a, at)
		a.Available = a.Available && active
		a.Fresh = a.Fresh && active
		out[candidate.AccountID] = a
	}
	e.mu.RUnlock()
	for id, a := range out {
		out[id] = cloneAccountSnapshot(a)
	}
	return out
}

func (e *Engine) CachedAccountForGroupAt(groupID, accountID, keyID int64, at time.Time) (AccountSnapshot, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	a := e.cachedAccountForGroupAtLocked(Request{GroupID: groupID, AccountID: accountID, UpstreamKeyID: keyID}, e.ResolvedSettings(groupID), at)
	return a, a.Available
}

// CachedMetadata deliberately does not enumerate accounts. Health checks and
// session reads must have constant cost irrespective of the account directory.
func (e *Engine) CachedMetadata(groupID int64) Snapshot {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.cachedMetadataAtLocked(groupID, e.ResolvedSettings(groupID), e.now().UTC())
}
func (e *Engine) CachedMetadataAt(groupID int64, at time.Time) Snapshot {
	e.mu.RLock()
	g := e.cachedMetadataAtLocked(groupID, e.ResolvedSettings(groupID), at)
	e.mu.RUnlock()
	return g
}

// CachedCandidates copies only the requested policy projections. Returned
// states own their mutable slices and maps; unrelated policies are not copied.
func (e *Engine) CachedCandidates(groupID int64, candidates []Candidate) Snapshot {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.cachedCandidatesAtLocked(groupID, candidates, e.now().UTC())
}
func (e *Engine) CachedCandidatesAt(groupID int64, candidates []Candidate, at time.Time) Snapshot {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.cachedCandidatesAtLocked(groupID, candidates, at)
}

func (e *Engine) cachedCandidatesAtLocked(groupID int64, candidates []Candidate, at time.Time) Snapshot {
	s := e.ResolvedSettings(groupID)
	g := e.cachedMetadataAtLocked(groupID, s, at)
	g.Accounts = make([]AccountSnapshot, 0, len(candidates))
	for _, candidate := range candidates {
		id := accountIdentity(candidate.AccountID, candidate.UpstreamKeyID)
		a, exists := e.accounts[id]
		if !exists {
			a.AccountState = AccountState{AccountID: candidate.AccountID, UpstreamKeyID: candidate.UpstreamKeyID, Phase: Healthy}
		}
		a = e.accountSnapshotAtLocked(id, a, at)
		a = projectAccountSnapshot(a, s)
		if a.Available {
			g.Accounts = append(g.Accounts, a)
		}
	}
	return g
}
