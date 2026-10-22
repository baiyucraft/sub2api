package upstreamcacheguard

import (
	"context"
	"time"
)

// AdmissionReadView fixes one evaluation time and policy generation for the
// metadata, account and session read at a physical account's send boundary.
type AdmissionReadView struct {
	Metadata      Snapshot
	Account       AccountSnapshot
	Session       SessionRead
	EvaluatedAt   time.Time
	PolicyVersion uint64
	Consistent    bool
	SessionCached bool
	Settings      Settings
	Diagnostics   AccountReadDiagnostics
}

type AccountReadDiagnostics struct {
	PolicyVersion       uint64    `json:"policy_version"`
	AccountPolicyEpoch  uint64    `json:"account_policy_epoch"`
	PolicyRegistered    bool      `json:"policy_registered"`
	PolicyEpochMismatch bool      `json:"policy_epoch_mismatch"`
	AccountPublished    bool      `json:"account_published"`
	AccountLoadedAt     time.Time `json:"account_loaded_at"`
	MetadataLoadedAt    time.Time `json:"metadata_loaded_at"`
	Fresh               bool      `json:"fresh"`
	Available           bool      `json:"available"`
	HasPendingGap       bool      `json:"has_account_gap"`
	HasSamplingFailure  bool      `json:"has_sampling_failure"`
	UnavailableReason   string    `json:"unavailable_reason,omitempty"`
}

type CandidatesReadView struct {
	Snapshot      Snapshot
	Session       SessionRead
	EvaluatedAt   time.Time
	PolicyVersion uint64
	Consistent    bool
	SessionCached bool
	Settings      Settings
}

func (e *Engine) cachedMetadataAtLocked(groupID int64, s Settings, at time.Time) Snapshot {
	g := e.groups[groupID]
	g.GroupID, g.Mode, g.LoadedAt = groupID, s.Mode, e.loadedAt
	g.Available = e.AnyPolicyActive() && !e.loadedAt.IsZero() && !at.Before(e.loadedAt) && at.Sub(e.loadedAt) <= UsableSnapshotLifetime
	g.Fresh = g.Available && at.Sub(e.loadedAt) <= RefreshLifetime
	g.Accounts = nil
	return g
}

func (e *Engine) cachedAccountForGroupAtLocked(r Request, s Settings, at time.Time) AccountSnapshot {
	id := accountIdentity(r.AccountID, r.UpstreamKeyID)
	a, exists := e.accounts[id]
	if !exists {
		a.AccountState = AccountState{AccountID: r.AccountID, UpstreamKeyID: r.UpstreamKeyID, Phase: Healthy}
	}
	a = e.accountSnapshotAtLocked(id, a, at)
	a.Available = a.Available && e.AnyPolicyActive()
	a.Fresh = a.Fresh && e.AnyPolicyActive()
	return projectAccountSnapshot(a, s)
}

// The clock is read after acquiring the publication lock. A refresh completed
// while this reader was waiting is therefore not mistaken for a clock rollback.
func (e *Engine) CachedAdmissionReadView(r Request) AdmissionReadView {
	return e.cachedAdmissionReadView(r, time.Time{}, false)
}

// An explicit evaluation time remains strict, including genuine clock rollback.
func (e *Engine) CachedAdmissionReadViewAt(r Request, at time.Time) AdmissionReadView {
	return e.cachedAdmissionReadView(r, at, true)
}

func (e *Engine) cachedAdmissionReadView(r Request, at time.Time, fixed bool) AdmissionReadView {
	for attempt := 0; attempt < 2; attempt++ {
		e.mu.RLock()
		version := e.PolicyVersion()
		s := e.ResolvedSettings(r.GroupID)
		if !fixed {
			at = e.now().UTC()
		}
		view := AdmissionReadView{EvaluatedAt: at, PolicyVersion: version, Settings: s}
		view.Metadata = e.cachedMetadataAtLocked(r.GroupID, s, at)
		view.Account = e.cachedAccountForGroupAtLocked(r, s, at)
		view.Diagnostics = e.accountReadDiagnosticsLocked(r, view.Account, view.Metadata, at, version)
		_, view.SessionCached = e.sessions[sessionCacheKey(r.GroupID, SessionKey(r))]
		view.Session = e.cachedSessionReadAtLocked(r, s, at, view.Metadata)
		e.mu.RUnlock()
		if version == e.PolicyVersion() {
			view.Consistent = true
			return view
		}
	}
	return AdmissionReadView{}
}

func (e *Engine) accountReadDiagnosticsLocked(r Request, a AccountSnapshot, metadata Snapshot, at time.Time, version uint64) AccountReadDiagnostics {
	id := accountIdentity(r.AccountID, r.UpstreamKeyID)
	epoch, registered := e.accountPolicyEpoch[id]
	_, published := e.accounts[id]
	d := AccountReadDiagnostics{PolicyVersion: version, AccountPolicyEpoch: epoch, PolicyRegistered: registered,
		PolicyEpochMismatch: registered && epoch != version, AccountPublished: published, AccountLoadedAt: a.LoadedAt,
		MetadataLoadedAt: metadata.LoadedAt, Fresh: a.Fresh, Available: a.Available,
		HasPendingGap: e.accountGaps[id], HasSamplingFailure: len(e.samplingFailures[id]) > 0}
	switch {
	case !e.AnyPolicyActive():
		d.UnavailableReason = "disabled"
	case d.HasSamplingFailure:
		d.UnavailableReason = "sampling_failure"
	case d.HasPendingGap:
		d.UnavailableReason = "account_gap"
	case a.LoadedAt.IsZero():
		d.UnavailableReason = "never_verified"
	case at.Before(a.LoadedAt):
		d.UnavailableReason = "clock_rollback"
	case at.Sub(a.LoadedAt) > UsableSnapshotLifetime:
		d.UnavailableReason = "verification_expired"
	case !a.Available:
		d.UnavailableReason = "policy_projection_unavailable"
	case d.PolicyEpochMismatch:
		d.UnavailableReason = "policy_epoch_mismatch"
	case !a.Fresh:
		d.UnavailableReason = "stale"
	}
	return d
}

// A cold load may overlap snapshot publication. Reevaluate all fields together
// after it completes rather than combining the new session with an old time.
func (e *Engine) SessionReadViewWithDatabaseContext(ctx context.Context, r Request, databaseContext func(context.Context) (context.Context, context.CancelFunc)) (AdmissionReadView, error) {
	view := e.CachedAdmissionReadView(r)
	if !view.Consistent || !view.Metadata.Available {
		return view, ErrUnavailable
	}
	key := sessionCacheKey(r.GroupID, SessionKey(r))
	if !view.SessionCached {
		if err := e.loadSessionForReadView(ctx, r, databaseContext); err != nil {
			return AdmissionReadView{}, err
		}
	}
	view = e.CachedAdmissionReadView(r)
	if !view.Consistent || !view.Metadata.Available {
		return view, ErrUnavailable
	}
	e.mu.Lock()
	if _, exists := e.sessions[key]; exists && view.EvaluatedAt.After(e.sessionAccess[key]) {
		e.sessionAccess[key] = view.EvaluatedAt
	}
	e.mu.Unlock()
	return view, nil
}

func (e *Engine) CachedCandidatesReadView(r Request, candidates []Candidate) CandidatesReadView {
	for attempt := 0; attempt < 2; attempt++ {
		e.mu.RLock()
		version := e.PolicyVersion()
		at := e.now().UTC()
		s := e.ResolvedSettings(r.GroupID)
		view := CandidatesReadView{EvaluatedAt: at, PolicyVersion: version, Settings: s}
		view.Snapshot = e.cachedCandidatesAtLocked(r.GroupID, candidates, at)
		_, view.SessionCached = e.sessions[sessionCacheKey(r.GroupID, SessionKey(r))]
		view.Session = e.cachedSessionReadAtLocked(r, s, at, view.Snapshot)
		e.mu.RUnlock()
		if version == e.PolicyVersion() {
			view.Consistent = true
			return view
		}
	}
	return CandidatesReadView{}
}

func (e *Engine) CandidatesReadViewWithDatabaseContext(ctx context.Context, r Request, candidates []Candidate, databaseContext func(context.Context) (context.Context, context.CancelFunc)) (CandidatesReadView, error) {
	view := e.CachedCandidatesReadView(r, candidates)
	if !view.Consistent || !view.Snapshot.Available {
		return view, ErrUnavailable
	}
	if !view.SessionCached {
		if err := e.loadSessionForReadView(ctx, r, databaseContext); err != nil {
			return CandidatesReadView{}, err
		}
		view = e.CachedCandidatesReadView(r, candidates)
		if !view.Consistent || !view.Snapshot.Available {
			return view, ErrUnavailable
		}
	}
	e.mu.Lock()
	key := sessionCacheKey(r.GroupID, SessionKey(r))
	if _, exists := e.sessions[key]; exists && view.EvaluatedAt.After(e.sessionAccess[key]) {
		e.sessionAccess[key] = view.EvaluatedAt
	}
	e.mu.Unlock()
	return view, nil
}

func (e *Engine) loadSessionForReadView(ctx context.Context, r Request, databaseContext func(context.Context) (context.Context, context.CancelFunc)) error {
	loader, ok := e.store.(SessionLoader)
	if !ok {
		return nil
	}
	loadCtx, cancel := ctx, func() {}
	if databaseContext != nil {
		loadCtx, cancel = databaseContext(ctx)
	}
	x, version, err := loader.LoadSession(loadCtx, r.GroupID, SessionKey(r))
	cancel()
	if err != nil {
		return err
	}
	if x != nil {
		key := sessionCacheKey(r.GroupID, SessionKey(r))
		e.mu.Lock()
		if _, present := e.sessions[key]; !present {
			e.cacheSessionLocked(key, *x, version, e.now().UTC())
		}
		e.mu.Unlock()
	}
	return nil
}
