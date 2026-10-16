package upstreamcacheguard

// Reuse only a projection verified against the exact durable state and facts.
// Unknown revisions, clock rollback, sampling gaps and time boundaries replay.
func (e *Engine) reuseAccountProjection(tx Tx, r Request, version int64, a *AccountState) (bool, error) {
	revisionTx, ok := tx.(FactVersionTx)
	if !ok {
		return false, nil
	}
	revision, err := revisionTx.FactVersion(r.AccountID, r.UpstreamKeyID)
	if err != nil {
		return false, err
	}
	id := accountIdentity(r.AccountID, r.UpstreamKeyID)
	e.mu.RLock()
	defer e.mu.RUnlock()
	prior, exists := e.accounts[id]
	v := e.refreshVersions[id]
	boundary := e.nextBoundaries[id]
	policyEpoch := e.refreshPolicyEpoch
	if e.accountPolicyEpoch != nil {
		policyEpoch = e.accountPolicyEpoch[id]
	}
	if !exists || version == 0 || prior.Version != version || v.Version != version || v.FactVersion != revision || policyEpoch != e.policyEpoch.Load() || e.accountGaps[id] || len(e.samplingFailures[id]) > 0 || r.StartedAt.Before(prior.LoadedAt) || (!boundary.IsZero() && !r.StartedAt.Before(boundary)) {
		return false, nil
	}
	*a = cloneAccountSnapshot(prior).AccountState
	return true, nil
}
