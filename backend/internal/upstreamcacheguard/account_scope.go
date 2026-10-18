package upstreamcacheguard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"
)

func accountIdentity(accountID, keyID int64) string {
	return fmt.Sprintf("%d:%d", accountID, keyID)
}

func appendGroup(ids []int64, groupID int64) []int64 {
	for _, id := range ids {
		if id == groupID {
			return ids
		}
	}
	ids = append(ids, groupID)
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func (e *Engine) transactAccount(ctx context.Context, r Request, fn func(Tx) error) error {
	store, ok := e.store.(AccountStore)
	if !ok {
		return ErrUnavailable
	}
	return store.TransactAccount(ctx, r.GroupID, r.AccountID, r.UpstreamKeyID, func(tx Tx) error {
		if _, ok := tx.(AccountTx); !ok {
			return ErrUnavailable
		}
		return fn(tx)
	})
}

// Bootstrap never borrows a legacy trial cohort or interprets a missing new
// row as recovery. Old reset boundaries remain attached to their source facts.
func (e *Engine) readAccount(tx Tx, r Request, s Settings, now time.Time) (AccountState, int64, error) {
	t, ok := tx.(AccountTx)
	if !ok {
		return AccountState{}, 0, ErrUnavailable
	}
	var a AccountState
	version, err := t.ReadAccount(r.AccountID, r.UpstreamKeyID, &a)
	if err != nil || version > 0 {
		return a, version, err
	}
	a = AccountState{AccountID: r.AccountID, UpstreamKeyID: r.UpstreamKeyID, Phase: Healthy, LegacyQualitySince: map[int64]time.Time{}}
	legacy, err := t.LegacyAccounts(r.AccountID, r.UpstreamKeyID)
	if err != nil {
		return a, 0, err
	}
	for _, old := range legacy {
		if old.AccountID != r.AccountID || old.UpstreamKeyID != r.UpstreamKeyID {
			continue
		}
		a.GroupIDs = appendGroup(a.GroupIDs, old.GroupID)
		if old.QualitySince.After(a.LegacyQualitySince[old.GroupID]) {
			a.LegacyQualitySince[old.GroupID] = old.QualitySince
		}
		if old.LastEvidenceAt.After(a.LastEvidenceAt) {
			a.LastEvidenceAt = old.LastEvidenceAt
		}
		if old.Phase == Degraded || old.Phase == Cooling || old.Phase == Trial {
			a.Phase = Cooling
			if old.CooldownUntil.After(a.CooldownUntil) {
				a.CooldownUntil = old.CooldownUntil
			}
			if a.DegradedAt.IsZero() || (!old.DegradedAt.IsZero() && old.DegradedAt.Before(a.DegradedAt)) {
				a.DegradedAt = old.DegradedAt
			}
		}
	}
	facts, err := t.AccountFacts(r.AccountID, r.UpstreamKeyID, now.Add(-time.Duration(s.WindowMinutes)*time.Minute))
	if err != nil {
		return a, 0, err
	}
	for _, f := range facts {
		a.GroupIDs = appendGroup(a.GroupIDs, f.Request.GroupID)
	}
	// A new unified identity must judge the combined existing window at once.
	// Legacy protection is Cooling, never Trial, so replay cannot recover it.
	if err = RecomputeAccount(&a, facts, e.Settings(), now, true); err != nil {
		return a, 0, err
	}
	version, err = t.WriteAccount(r.AccountID, r.UpstreamKeyID, &a)
	return a, version, err
}

func (e *Engine) refreshAccounts(ctx context.Context) error {
	// Refresh may be triggered by both the runner and an explicit rebuild. A
	// slower earlier read must never publish over a later successful refresh.
	e.refreshMu.Lock()
	defer e.refreshMu.Unlock()
	store, ok := e.store.(AccountStore)
	if !ok {
		return ErrUnavailable
	}
	s := e.Settings()
	now := e.now().UTC()
	epoch := e.policyEpoch.Load()
	activePolicies := e.activePolicies()
	incremental, optimized := e.store.(IncrementalStore)
	reconcileDue, pageEnd := false, 0
	var versions []IdentityVersion
	var ids []int64
	unchanged := map[string]AccountSnapshot{}
	var allowed map[string]bool
	var rows []RuntimeRow
	var err error
	deferred := map[string]bool{}
	processed := map[string]bool{}
	nextCursor := e.refreshCursor
	if optimized {
		versions, err = incremental.IdentityVersions(ctx)
		if err != nil {
			return err
		}
		reconcileDue = e.lastReconciled.IsZero() || now.Before(e.lastReconciled) || now.Sub(e.lastReconciled) >= 5*time.Minute
		if now.Before(e.lastReconciled) || e.reconcileCursor >= len(versions) {
			e.reconcileCursor = 0
		}
		pageEnd = e.reconcileCursor
		if reconcileDue {
			pageEnd = min(len(versions), e.reconcileCursor+500)
		}
		_, bounded := ctx.Deadline()
		var pending []int
		e.mu.Lock()
		active := make([]SessionIdentity, 0, len(e.sessions))
		for key, session := range e.sessions {
			last := e.sessionAccess[key]
			if last.IsZero() {
				last = session.LastStartedAt
				if session.LastCompletedAt.After(last) {
					last = session.LastCompletedAt
				}
			}
			if !last.IsZero() && !now.Before(last) && now.Sub(last) > 30*time.Minute {
				delete(e.sessions, key)
				delete(e.sessionAccess, key)
				delete(e.sessionVersions, key)
				continue
			}
			var group int64
			fmt.Sscanf(key, "%d:", &group)
			active = append(active, SessionIdentity{GroupID: group, Key: session.Key, Version: e.sessionVersions[key]})
		}
		for index, version := range versions {
			policyEpoch := e.accountPolicyEpoch[idForVersion(version)]
			if e.accountPolicyEpoch == nil {
				policyEpoch = e.refreshPolicyEpoch
			}
			id := accountIdentity(version.AccountID, version.UpstreamKeyID)
			prior, exists := e.accounts[id]
			policyChanged := epoch != policyEpoch && !accountPoliciesCompatible(prior.AccountState, activePolicies)
			reconcile := reconcileDue && index >= e.reconcileCursor && index < pageEnd
			boundary := e.nextBoundaries[id]
			reusable := !policyChanged && exists && !e.accountGaps[id] && len(e.samplingFailures[id]) == 0 && version == e.refreshVersions[id] && !now.Before(prior.LoadedAt) && (boundary.IsZero() || now.Before(boundary))
			if reusable {
				unchanged[id] = prior
			}
			if !reusable || reconcile {
				pending = append(pending, index)
			}
		}
		if bounded && len(pending) > 0 {
			start := sort.SearchInts(pending, e.refreshCursor)
			if start == len(pending) {
				start = 0
			}
			chosen := map[int]bool{}
			for _, index := range pending[start:min(start+256, len(pending))] {
				chosen[index] = true
			}
			for _, index := range pending {
				v := versions[index]
				id := idForVersion(v)
				if chosen[index] {
					ids = append(ids, v.AccountID)
				} else if _, reusable := unchanged[id]; !reusable {
					// A consistency check waiting for its bounded turn is not a
					// failed data refresh. Both directory reads still verify the
					// unchanged durable state before its timestamp can advance.
					deferred[id] = true
				}
			}
		} else {
			for _, index := range pending {
				ids = append(ids, versions[index].AccountID)
			}
		}
		e.mu.Unlock()
		rows, err = incremental.ActiveRuntimeRows(ctx, active, now.Add(-30*time.Minute))
	} else {
		rows, err = e.store.Rows(ctx, nil)
	}
	if err != nil {
		return err
	}
	var accountRows []AccountRuntimeRow
	if optimized {
		accountRows, err = incremental.AccountRowsFor(ctx, ids)
	} else {
		accountRows, err = store.AccountRows(ctx)
	}
	if err != nil {
		return err
	}
	sort.Slice(accountRows, func(i, j int) bool { return accountRows[i].AccountID < accountRows[j].AccountID })
	if len(rows)+len(accountRows) > MaxRuntimeRows {
		return errors.New("cache guard runtime bound exceeded")
	}
	groups := map[int64]Snapshot{}
	sessions := map[string]SessionState{}
	if optimized {
		e.mu.RLock()
		for key, session := range e.sessions {
			sessions[key] = session
		}
		e.mu.RUnlock()
	}
	sessionVersions := map[string]int64{}
	gaps := map[string]bool{}
	acknowledged := map[string][]string{}
	for _, row := range rows {
		g := groups[row.GroupID]
		g.GroupID, g.Mode, g.Available, g.LoadedAt = row.GroupID, s.Mode, true, now
		switch row.Kind {
		case GroupKind:
			var quota GroupState
			if err := json.Unmarshal(row.Payload, &quota); err != nil {
				return err
			}
			g.NaturalNewCount, g.TrialCount = quota.NaturalNewCount, quota.TrialCount
		case AccountKind:
			// Legacy rows are consumed only by readAccount under the account lock.
		case SessionKind:
			var session SessionState
			if err := json.Unmarshal(row.Payload, &session); err != nil {
				return err
			}
			cacheKey := sessionCacheKey(row.GroupID, row.Key)
			e.mu.RLock()
			currentVersion := e.sessionVersions[cacheKey]
			current := e.sessions[cacheKey]
			e.mu.RUnlock()
			if optimized && currentVersion > row.Version {
				session = current
			} else {
				sessionVersions[cacheKey] = row.Version
			}
			sessions[cacheKey] = session
			for key, a := range session.Admissions {
				keyID := a.UpstreamKeyID
				if keyID == 0 {
					keyID = session.Routes[a.RouteKey].UpstreamKeyID
				}
				if a.Completed {
					id := accountIdentity(a.AccountID, keyID)
					acknowledged[id] = append(acknowledged[id], key)
					continue
				}
				if !a.StartedAt.Before(e.bornAt) && now.Sub(a.StartedAt) <= 5*time.Minute {
					continue
				}
				if keyID > 0 {
					gaps[accountIdentity(a.AccountID, keyID)] = true
				}
			}
		default:
			return errors.New("unknown cache guard typed state")
		}
		groups[row.GroupID] = g
	}
	// Materialize new policy projections under the same account lock as facts.
	if optimized {
		for _, session := range sessions {
			for key, a := range session.Admissions {
				keyID := a.UpstreamKeyID
				if keyID == 0 {
					keyID = session.Routes[a.RouteKey].UpstreamKeyID
				}
				id := accountIdentity(a.AccountID, keyID)
				if a.Completed {
					acknowledged[id] = append(acknowledged[id], key)
					continue
				}
				if keyID > 0 && (a.StartedAt.Before(e.bornAt) || now.Sub(a.StartedAt) > 5*time.Minute) {
					gaps[id] = true
					delete(unchanged, id)
				}
			}
		}
	}
	var accountErrors []error
	materializeCtx := ctx
	materializeCancel := func() {}
	if deadline, ok := ctx.Deadline(); ok {
		materializeCtx, materializeCancel = context.WithDeadline(ctx, time.Now().Add(time.Until(deadline)/2))
	}
	defer materializeCancel()
	eligibleIDs := make([]int64, 0, len(ids))
	returned := map[string]bool{}
	for _, row := range accountRows {
		id := accountIdentity(row.AccountID, row.UpstreamKeyID)
		returned[id] = true
		if materializeCtx.Err() == nil {
			nextCursor = sort.Search(len(versions), func(i int) bool { return versions[i].AccountID > row.AccountID })
		}
		var existing AccountState
		if len(row.Payload) > 0 {
			if err := json.Unmarshal(row.Payload, &existing); err != nil {
				gaps[id] = true
				accountErrors = append(accountErrors, err)
				continue
			}
		}
		missing := existing.Policies == nil
		active := e.activePolicies()
		for sig, settings := range active {
			projection, exists := existing.Policies[sig]
			missing = missing || !exists || !projectionSettingsCompatible(projection.Settings, settings)
		}
		if row.Version != 0 && !missing && len(active) == len(existing.Policies) {
			eligibleIDs = append(eligibleIDs, row.AccountID)
			processed[id] = true
			continue
		}
		if materializeCtx.Err() != nil {
			if _, reusable := unchanged[id]; !reusable {
				deferred[id] = true
			}
			continue
		}
		r := Request{AccountID: row.AccountID, UpstreamKeyID: row.UpstreamKeyID}
		if err := e.transactAccount(materializeCtx, r, func(tx Tx) error {
			a, _, err := e.readAccount(tx, r, s, now)
			if err != nil {
				return err
			}
			e.preparePolicies(&a)
			facts, err := tx.(AccountTx).AccountFacts(r.AccountID, r.UpstreamKeyID, now.Add(-time.Duration(s.WindowMinutes)*time.Minute))
			if err != nil {
				return err
			}
			if err = e.recomputePolicies(&a, facts, now, false); err != nil {
				return err
			}
			_, err = tx.(AccountTx).WriteAccount(r.AccountID, r.UpstreamKeyID, &a)
			return err
		}); err != nil {
			gaps[id] = true
			accountErrors = append(accountErrors, err)
			deferred[id] = true
		} else {
			eligibleIDs = append(eligibleIDs, row.AccountID)
			processed[id] = true
			nextCursor = sort.Search(len(versions), func(i int) bool { return versions[i].AccountID > row.AccountID })
		}
	}
	if optimized {
		// The directory can expose a fact-only identity before its typed account
		// row is materialized. Do not let the shared LoadedAt make that identity
		// look healthy; it remains unknown until a row can be confirmed.
		for _, version := range versions {
			id := idForVersion(version)
			selected := false
			for _, accountID := range ids {
				if accountID == version.AccountID {
					selected = true
					break
				}
			}
			if selected && !returned[id] {
				gaps[id] = true
				deferred[id] = true
			}
		}
		ids = eligibleIDs
		for id := range deferred {
			gaps[id] = true
		}
	}
	var windows map[string]AccountWindow
	if optimized {
		windows, err = incremental.AccountWindowsFor(ctx, ids, now.Add(-time.Duration(s.WindowMinutes)*time.Minute), now)
	} else {
		windows, err = store.AccountWindows(ctx, now.Add(-time.Duration(s.WindowMinutes)*time.Minute), now)
	}
	if err != nil {
		return err
	}
	if optimized {
		confirmed, checkErr := incremental.IdentityVersions(ctx)
		if checkErr != nil {
			return checkErr
		}
		before := map[string]IdentityVersion{}
		for _, version := range versions {
			before[accountIdentity(version.AccountID, version.UpstreamKeyID)] = version
		}
		versions = confirmed
		allowed = map[string]bool{}
		for _, v := range confirmed {
			allowed[accountIdentity(v.AccountID, v.UpstreamKeyID)] = true
		}
		for id := range windows {
			if !allowed[id] {
				delete(windows, id)
			}
		}
		for id := range unchanged {
			if !allowed[id] {
				delete(unchanged, id)
			}
		}
		for id := range gaps {
			if !allowed[id] {
				delete(gaps, id)
			}
		}
		for _, version := range confirmed {
			id := accountIdentity(version.AccountID, version.UpstreamKeyID)
			// Materialization can advance state.version; facts must remain fixed.
			prior, exists := before[id]
			window, loaded := windows[id]
			if !exists || prior.FactVersion != version.FactVersion || (loaded && window.Version != version.Version) || (!loaded && prior.Version != version.Version) {
				gaps[id] = true
				delete(unchanged, id)
			}
		}
		if epoch != e.policyEpoch.Load() {
			return ErrUnavailable
		}
	}
	if len(windows) > MaxRuntimeRows {
		return errors.New("cache guard account window bound exceeded")
	}
	e.mu.Lock()
	// A different worker may have completed a retried delivery. Its durable
	// admission acknowledgment also releases this instance's transient marker.
	for id, keys := range acknowledged {
		for _, key := range keys {
			delete(e.samplingFailures[id], key)
		}
		if len(e.samplingFailures[id]) == 0 {
			delete(e.samplingFailures, id)
		}
	}
	previous := e.accounts
	for id := range e.samplingFailures {
		if optimized && !allowed[id] {
			continue
		}
		gaps[id] = true
	}
	e.mu.Unlock()
	accounts := map[string]AccountSnapshot{}
	for id, prior := range unchanged {
		prior.LoadedAt = now
		accounts[id] = prior
	}
	for id, window := range windows {
		if gaps[id] {
			continue
		}
		if window.Err != nil {
			gaps[id] = true
			accountErrors = append(accountErrors, window.Err)
			continue
		}
		a := window.State
		if a.AccountID != window.AccountID || a.UpstreamKeyID != window.UpstreamKeyID || id != accountIdentity(a.AccountID, a.UpstreamKeyID) {
			gaps[id] = true
			accountErrors = append(accountErrors, errors.New("cache guard account identity mismatch"))
			continue
		}
		if prior, ok := previous[id]; ok && prior.Version > window.Version {
			gaps[id] = true
			accountErrors = append(accountErrors, errors.New("cache guard account version regressed"))
			continue
		}
		// Reuse the last projection of an unchanged durable version so window
		// expiry changes its display timestamp once, not on every refresh.
		if prior, ok := previous[id]; ok && prior.Version == window.Version {
			a = cloneAccountSnapshot(prior).AccountState
		}
		window.Apply(&a, now)
		accounts[id] = AccountSnapshot{AccountState: a, Version: window.Version, LoadedAt: now}
	}
	// An account-local failure retains that identity's last successful value and
	// timestamp. Healthy accounts can advance without extending this one's age.
	for id := range gaps {
		if prior, ok := previous[id]; ok {
			accounts[id] = prior
		}
	}
	e.mu.Lock()
	// A completion can fail while a refresh reads the database. Do not publish
	// a new success timestamp over a failure observed during that read.
	for id := range e.samplingFailures {
		if optimized && !allowed[id] {
			continue
		}
		gaps[id] = true
		if prior, ok := e.accounts[id]; ok {
			accounts[id] = prior
		} else {
			delete(accounts, id)
		}
	}
	if optimized {
		for key, session := range e.sessions {
			if e.sessionVersions[key] > sessionVersions[key] {
				sessions[key] = session
			}
		}
	}
	e.groups, e.sessions, e.accounts, e.accountGaps, e.loadedAt = groups, sessions, accounts, gaps, now
	if optimized {
		if reconcileDue {
			// A bounded round may process only part of this consistency page.
			// Never count deferred or failed identities as precisely reconciled.
			cursor := min(e.reconcileCursor, len(versions))
			for cursor < min(pageEnd, len(versions)) {
				id := idForVersion(versions[cursor])
				if gaps[id] || !processed[id] {
					break
				}
				cursor++
			}
			e.reconcileCursor = cursor
			if cursor >= len(versions) {
				e.reconcileCursor = 0
				e.lastReconciled = now
			}
		}
		for key, session := range e.sessions {
			if e.sessionVersions[key] > sessionVersions[key] {
				sessions[key] = session
			}
		}
		for key, version := range sessionVersions {
			if version > e.sessionVersions[key] {
				e.sessionVersions[key] = version
			}
		}
		for len(e.sessions) > MaxActiveSessions {
			for key := range e.sessions {
				delete(e.sessions, key)
				delete(e.sessionAccess, key)
				delete(e.sessionVersions, key)
				break
			}
		}
		for id := range e.nextBoundaries {
			if !allowed[id] {
				delete(e.nextBoundaries, id)
			}
		}
		if e.accountPolicyEpoch == nil {
			e.accountPolicyEpoch = map[string]uint64{}
		}
		for _, version := range versions {
			id := idForVersion(version)
			if !gaps[id] && (processed[id] || unchanged[id].AccountID != 0) {
				e.refreshVersions[id] = version
				e.accountPolicyEpoch[id] = epoch
			}
		}
		for id := range e.refreshVersions {
			if !allowed[id] {
				delete(e.refreshVersions, id)
				delete(e.accountPolicyEpoch, id)
			}
		}
		for id, window := range windows {
			if !gaps[id] {
				e.nextBoundaries[id] = window.NextBoundary
			}
		}
		e.refreshPolicyEpoch = epoch
		e.refreshCursor = nextCursor
	}
	e.mu.Unlock()
	return errors.Join(accountErrors...)
}

func idForVersion(v IdentityVersion) string { return accountIdentity(v.AccountID, v.UpstreamKeyID) }

func (e *Engine) freshLocked() bool {
	now := e.now().UTC()
	return !e.loadedAt.IsZero() && !now.Before(e.loadedAt) && now.Sub(e.loadedAt) <= RefreshLifetime
}

func (e *Engine) usableLocked() bool {
	now := e.now().UTC()
	return !e.loadedAt.IsZero() && !now.Before(e.loadedAt) && now.Sub(e.loadedAt) <= UsableSnapshotLifetime
}

// Missing evidence after a successful refresh is a known empty window. A gap
// without any successful account snapshot is unknown, never a zero-rate value.
func (e *Engine) accountSnapshotLocked(id string, a AccountSnapshot) AccountSnapshot {
	return e.accountSnapshotAtLocked(id, a, e.now().UTC())
}
func (e *Engine) accountSnapshotAtLocked(id string, a AccountSnapshot, now time.Time) AccountSnapshot {
	// Expiry is derived at read time as well as during durable recomputation so
	// an old protected phase cannot remain schedulable between refresh ticks.
	// Published projections are immutable. Most reads need no copy at all;
	// copy only the map shell when an expired projection must be replaced.
	var expired map[string]PolicyProjection
	for sig, projection := range a.Policies {
		if expireProtectionState(&projection.State, projection.Settings, now) {
			if expired == nil {
				expired = make(map[string]PolicyProjection, len(a.Policies))
				for key, value := range a.Policies {
					expired[key] = value
				}
			}
			expired[sig] = projection
		}
	}
	if expired != nil {
		a.Policies = expired
	}
	if len(a.Policies) == 0 {
		expireProtectionState(&a.AccountState, e.Settings(), now)
	}
	gap := e.accountGaps[id] || len(e.samplingFailures[id]) > 0
	loadedAt := a.LoadedAt
	if loadedAt.IsZero() && !gap {
		loadedAt = e.loadedAt
	}
	ageValid := !loadedAt.IsZero() && !now.Before(loadedAt)
	a.LoadedAt = loadedAt
	a.Available = ageValid && now.Sub(loadedAt) <= UsableSnapshotLifetime
	a.Fresh = a.Available && !gap && now.Sub(loadedAt) <= RefreshLifetime
	if e.accountPolicyEpoch != nil && a.AccountID != 0 && e.accountPolicyEpoch[id] != e.policyEpoch.Load() {
		a.Fresh = false
	}
	return a
}

func cloneAccountSnapshot(a AccountSnapshot) AccountSnapshot {
	if a.LastDegradedEvidenceAt != nil {
		anchor := *a.LastDegradedEvidenceAt
		a.LastDegradedEvidenceAt = &anchor
	}
	a.GroupIDs = append([]int64(nil), a.GroupIDs...)
	a.TrialGroupIDs = append([]int64(nil), a.TrialGroupIDs...)
	if a.Policies != nil {
		policies := make(map[string]PolicyProjection, len(a.Policies))
		for sig, p := range a.Policies {
			p.State = cloneAccountSnapshot(AccountSnapshot{AccountState: p.State}).AccountState
			p.Settings.GroupIDs = append([]int64(nil), p.Settings.GroupIDs...)
			policies[sig] = p
		}
		a.Policies = policies
	}
	if a.LegacyQualitySince != nil {
		values := make(map[int64]time.Time, len(a.LegacyQualitySince))
		for id, at := range a.LegacyQualitySince {
			values[id] = at
		}
		a.LegacyQualitySince = values
	}
	return a
}

// Missing account evidence is a fresh empty window, not a zero-percent sample.
func (e *Engine) CachedSharedAccount(accountID, keyID int64) (AccountSnapshot, bool) {
	active := e.AnyPolicyActive()
	e.mu.RLock()
	defer e.mu.RUnlock()
	id := accountIdentity(accountID, keyID)
	a, exists := e.accounts[id]
	if !exists {
		a.AccountState = AccountState{AccountID: accountID, UpstreamKeyID: keyID, Phase: Healthy}
	}
	a = e.accountSnapshotLocked(id, a)
	a.Available = active && a.Available
	a.Fresh = active && a.Fresh
	return cloneAccountSnapshot(a), a.Available
}

// CachedAccount preserves the global-policy reader contract for old callers.
func (e *Engine) CachedAccount(accountID, keyID int64) (AccountSnapshot, bool) {
	a, fresh := e.CachedSharedAccount(accountID, keyID)
	return projectAccountSnapshot(a, e.Settings()), fresh
}

func (e *Engine) CachedAccountGroups(accountID, keyID int64) []int64 {
	a, _ := e.CachedSharedAccount(accountID, keyID)
	return a.GroupIDs
}
