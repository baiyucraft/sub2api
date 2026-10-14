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
		if old.Phase != Healthy && old.Phase != "" {
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
	if err = RecomputeAccount(&a, facts, s, now, true); err != nil {
		return a, 0, err
	}
	version, err = t.WriteAccount(r.AccountID, r.UpstreamKeyID, &a)
	return a, version, err
}

func (e *Engine) refreshAccounts(ctx context.Context) error {
	store, ok := e.store.(AccountStore)
	if !ok {
		return ErrUnavailable
	}
	s := e.Settings()
	rows, err := e.store.Rows(ctx, nil)
	if err != nil {
		return err
	}
	accountRows, err := store.AccountRows(ctx)
	if err != nil {
		return err
	}
	if len(rows)+len(accountRows) > MaxRuntimeRows {
		return errors.New("cache guard runtime bound exceeded")
	}
	now := e.now().UTC()
	groups := map[int64]Snapshot{}
	sessions := map[string]SessionState{}
	gaps := map[string]bool{}
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
			sessions[sessionCacheKey(row.GroupID, row.Key)] = session
			for _, a := range session.Admissions {
				if a.Completed || (!a.StartedAt.Before(e.bornAt) && now.Sub(a.StartedAt) <= 5*time.Minute) {
					continue
				}
				keyID := a.UpstreamKeyID
				if keyID == 0 {
					keyID = session.Routes[a.RouteKey].UpstreamKeyID
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
	// Bootstrap legacy protection before the single-statement account/window read.
	for _, row := range accountRows {
		if row.Version != 0 {
			continue
		}
		r := Request{AccountID: row.AccountID, UpstreamKeyID: row.UpstreamKeyID}
		if err := e.transactAccount(ctx, r, func(tx Tx) error {
			_, _, err := e.readAccount(tx, r, s, now)
			return err
		}); err != nil {
			return err
		}
	}
	windows, err := store.AccountWindows(ctx, now.Add(-time.Duration(s.WindowMinutes)*time.Minute), now)
	if err != nil {
		return err
	}
	if len(windows) > MaxRuntimeRows {
		return errors.New("cache guard account window bound exceeded")
	}
	e.mu.RLock()
	previous := e.accounts
	e.mu.RUnlock()
	accounts := map[string]AccountSnapshot{}
	for id, window := range windows {
		a := window.State
		if a.AccountID != window.AccountID || a.UpstreamKeyID != window.UpstreamKeyID || id != accountIdentity(a.AccountID, a.UpstreamKeyID) {
			return errors.New("cache guard account identity mismatch")
		}
		// Reuse the last projection of an unchanged durable version so window
		// expiry changes its display timestamp once, not on every refresh.
		if prior, ok := previous[id]; ok && prior.Version == window.Version {
			a = cloneAccountSnapshot(prior).AccountState
		}
		window.Apply(&a, now)
		accounts[id] = AccountSnapshot{AccountState: a, Version: window.Version}
	}
	e.mu.Lock()
	e.groups, e.sessions, e.accounts, e.accountGaps, e.loadedAt = groups, sessions, accounts, gaps, now
	e.mu.Unlock()
	return nil
}

func (e *Engine) freshLocked() bool {
	now := e.now().UTC()
	return !e.loadedAt.IsZero() && !now.Before(e.loadedAt) && now.Sub(e.loadedAt) <= RefreshLifetime
}

func cloneAccountSnapshot(a AccountSnapshot) AccountSnapshot {
	a.GroupIDs = append([]int64(nil), a.GroupIDs...)
	a.TrialGroupIDs = append([]int64(nil), a.TrialGroupIDs...)
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
func (e *Engine) CachedAccount(accountID, keyID int64) (AccountSnapshot, bool) {
	s := e.Settings()
	e.mu.RLock()
	defer e.mu.RUnlock()
	id := accountIdentity(accountID, keyID)
	a, exists := e.accounts[id]
	if !exists {
		a.AccountState = AccountState{AccountID: accountID, UpstreamKeyID: keyID, Phase: Healthy}
	}
	return cloneAccountSnapshot(a), s.Mode != Disabled && e.freshLocked() && !e.accountGaps[id]
}

func (e *Engine) CachedAccountGroups(accountID, keyID int64) []int64 {
	a, _ := e.CachedAccount(accountID, keyID)
	return a.GroupIDs
}
