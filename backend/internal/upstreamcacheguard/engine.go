package upstreamcacheguard

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

const WarmLifetime = 15 * time.Minute
const ResidenceLifetime = 5 * time.Minute
const RefreshLifetime = 5 * time.Second
const FactLifetime = 48 * time.Hour
const MinRecoverySessions = 3
const RecoveryLifetime = 10 * time.Minute

type Engine struct {
	store    Store
	settings atomic.Pointer[Settings]
	mu       sync.RWMutex
	groups   map[int64]Snapshot
	sessions map[string]SessionState
	loadedAt time.Time
	now      func() time.Time
	bornAt   time.Time
}

func NewEngine(store Store) *Engine {
	e := &Engine{store: store, groups: map[int64]Snapshot{}, sessions: map[string]SessionState{}, now: time.Now, bornAt: time.Now().UTC()}
	s := DefaultSettings()
	e.settings.Store(&s)
	return e
}
func (e *Engine) ApplySettings(s Settings) error {
	if err := s.Validate(); err != nil {
		return err
	}
	s.GroupIDs = append([]int64(nil), s.GroupIDs...)
	sort.Slice(s.GroupIDs, func(i, j int) bool { return s.GroupIDs[i] < s.GroupIDs[j] })
	ids := s.GroupIDs[:0]
	for _, id := range s.GroupIDs {
		if len(ids) == 0 || ids[len(ids)-1] != id {
			ids = append(ids, id)
		}
	}
	s.GroupIDs = ids
	e.settings.Store(&s)
	return nil
}
func (e *Engine) Settings() Settings {
	s := *e.settings.Load()
	s.GroupIDs = append([]int64{}, s.GroupIDs...)
	return s
}
func (e *Engine) Close()     {}
func digest(v string) string { h := sha256.Sum256([]byte(v)); return hex.EncodeToString(h[:]) }
func SessionKey(r Request) string {
	return digest(fmt.Sprintf("%d:%d:%s", r.GroupID, r.APIKeyID, r.LogicalSessionDigest))
}

// Quality and recovery are shared by every model/protocol in one group/account.
func AccountKey(r Request) string { return fmt.Sprintf("%d", r.AccountID) }

// Warmness, unlike account quality, belongs to a concrete cache context.
func WarmRouteKey(r Request) string {
	return digest(fmt.Sprintf("%d:%d:%s:%s", r.AccountID, r.UpstreamKeyID, r.Model, r.Protocol))
}
func sessionCacheKey(group int64, key string) string { return fmt.Sprintf("%d:%s", group, key) }
func admissionKey(r Request) string {
	return digest(fmt.Sprintf("%s:%s:%d", r.ID, AccountKey(r), r.StartedAt.UnixNano()))
}
func routeWarm(route SessionRoute, at time.Time) bool {
	return !route.LastSuccessfulAt.IsZero() && !at.Before(route.LastSuccessfulAt) && at.Sub(route.LastSuccessfulAt) <= WarmLifetime
}

func planInTx(tx Tx, r Request, session *SessionState, s Settings) (TrialPlan, error) {
	p := TrialPlan{RequestID: r.ID, SessionKey: SessionKey(r), GroupID: r.GroupID}
	if !session.FirstSeenAt.IsZero() {
		p.NaturalNew = session.NaturalRequestID == r.ID
		p.TrialSlot = p.NaturalNew && session.NaturalTrialSlot
		p.TrialOrdinal = session.NaturalTrialOrdinal
		return p, nil
	}
	var group GroupState
	if _, err := tx.Read(GroupKind, "quota", &group); err != nil {
		return p, err
	}
	old := group.NaturalNewCount
	group.NaturalNewCount++
	p.NaturalNew = true
	p.TrialSlot = group.NaturalNewCount*int64(s.TrialPercent)/100 > old*int64(s.TrialPercent)/100
	if p.TrialSlot {
		p.TrialOrdinal = group.NaturalNewCount * int64(s.TrialPercent) / 100
	}
	session.Key = p.SessionKey
	session.FirstSeenAt = r.StartedAt.UTC()
	session.NaturalRequestID = r.ID
	session.NaturalTrialSlot = p.TrialSlot
	session.NaturalTrialOrdinal = p.TrialOrdinal
	if _, err := tx.Write(GroupKind, "quota", &group); err != nil {
		return p, err
	}
	return p, nil
}

// PlanTrial atomically reserves a group-shared natural-new slot before routing.
// The request must already have its stable gateway ID and HMAC logical identity.
func (e *Engine) PlanTrial(ctx context.Context, r Request) (TrialPlan, error) {
	s := e.Settings()
	p := TrialPlan{RequestID: r.ID, GroupID: r.GroupID}
	if s.Mode == Disabled || !s.ContainsGroup(r.GroupID) {
		return p, nil
	}
	v := r
	if v.AccountID == 0 {
		v.AccountID = 1
	}
	if err := validateRequest(v); err != nil {
		return p, err
	}
	if e.store == nil {
		return p, ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	err := e.store.Transact(ctx, r.GroupID, func(tx Tx) error {
		var session SessionState
		if _, err := tx.Read(SessionKind, SessionKey(r), &session); err != nil {
			return err
		}
		var err error
		p, err = planInTx(tx, r, &session, s)
		if err != nil {
			return err
		}
		_, err = tx.Write(SessionKind, SessionKey(r), &session)
		return err
	})
	return p, err
}

func (e *Engine) Admit(ctx context.Context, r Request) (Admission, error) {
	s := e.Settings()
	a := Admission{RequestID: r.ID, GroupID: r.GroupID, AccountID: r.AccountID, StartedAt: r.StartedAt.UTC(), Mode: s.Mode}
	if s.Mode == Disabled || !s.ContainsGroup(r.GroupID) {
		return a, nil
	}
	if err := validateRequest(r); err != nil {
		return a, err
	}
	if e.store == nil {
		return a, ErrUnavailable
	}
	a.SessionKey = SessionKey(r)
	var saved SessionState
	linkedProtection := snapshotProtectsAccount(e.CachedSnapshot(r.GroupID), r.AccountID, r.UpstreamKeyID)
	// The caller may already have a deadline; this never extends it.
	ctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	err := e.store.Transact(ctx, r.GroupID, func(tx Tx) error {
		var session SessionState
		if _, err := tx.Read(SessionKind, a.SessionKey, &session); err != nil {
			return err
		}
		if old, ok := session.Admissions[admissionKey(r)]; ok {
			a = old
			return nil
		}
		plan, err := planInTx(tx, r, &session, s)
		if err != nil {
			return err
		}
		var account AccountState
		if _, err := tx.Read(AccountKind, AccountKey(r), &account); err != nil {
			return err
		}
		a.NaturalNew = plan.NaturalNew
		a.Sample = true
		a.RouteKey = WarmRouteKey(r)
		if session.Routes == nil {
			session.Routes = map[string]SessionRoute{}
		}
		if session.Admissions == nil {
			session.Admissions = map[string]Admission{}
		}
		a.WarmAtStart = routeWarm(session.Routes[WarmRouteKey(r)], r.StartedAt)
		a.ProtectedAtStart = (account.Phase != "" && account.Phase != Healthy) || linkedProtection
		if plan.NaturalNew && session.NaturalAccountID == 0 {
			session.NaturalAccountID = r.AccountID
			if plan.TrialSlot && account.Phase != Healthy && account.Phase != "" && !r.StartedAt.Before(account.CooldownUntil) {
				if account.CohortID == "" {
					account.CohortID = digest(fmt.Sprintf("%s:%d:%s", AccountKey(r), r.StartedAt.UnixNano(), session.Key))
					account.FirstTrialAt = r.StartedAt.UTC()
					account.TrialValidRequests = 0
					account.TrialSessions = 0
					account.TrialCachePercent = 0
				}
				account.Phase = Trial
				a.Trial = true
				a.CohortID = account.CohortID
				var group GroupState
				if _, err := tx.Read(GroupKind, "quota", &group); err != nil {
					return err
				}
				group.TrialCount++
				session.TrialAccountID = r.AccountID
				session.TrialModel = r.Model
				session.TrialProtocol = r.Protocol
				session.CohortID = a.CohortID
				if _, err := tx.Write(AccountKind, AccountKey(r), &account); err != nil {
					return err
				}
				if _, err := tx.Write(GroupKind, "quota", &group); err != nil {
					return err
				}
			}
		} else if session.TrialAccountID == r.AccountID && session.CohortID != "" && session.CohortID == account.CohortID {
			a.Trial = true
			a.CohortID = session.CohortID
		}
		if r.StartedAt.After(session.LastStartedAt) {
			session.LastStartedAt = r.StartedAt.UTC()
		}
		for k, old := range session.Admissions {
			if old.Completed && r.StartedAt.Sub(old.StartedAt) > FactLifetime {
				delete(session.Admissions, k)
			}
		}
		// In-flight admissions cannot evict the frozen cold classification of an
		// earlier request. Large pathological sessions fail open instead.
		if len(session.Admissions) >= 100000 {
			return errors.New("cache guard session admission bound exceeded")
		}
		session.Admissions[admissionKey(r)] = a
		saved = session
		_, err = tx.Write(SessionKind, a.SessionKey, &session)
		return err
	})
	if err != nil {
		return Admission{RequestID: r.ID, GroupID: r.GroupID, AccountID: r.AccountID, StartedAt: r.StartedAt.UTC(), Mode: s.Mode}, err
	}
	if saved.Key != "" {
		e.mu.Lock()
		e.sessions[sessionCacheKey(r.GroupID, saved.Key)] = saved
		e.mu.Unlock()
	}
	return a, nil
}

func (e *Engine) Complete(ctx context.Context, v Evidence) error {
	s := e.Settings()
	if e.store == nil {
		return ErrUnavailable
	}
	if err := validateRequest(v.Request); err != nil {
		return err
	}
	if v.ID == "" || len(v.ID) > 200 || v.CompletedAt.IsZero() || v.CompletedAt.Before(v.Request.StartedAt) || v.Admission.SessionKey != SessionKey(v.Request) || v.Admission.RequestID != v.Request.ID || v.Admission.AccountID != v.Request.AccountID || v.Admission.GroupID != v.Request.GroupID {
		return errors.New("cache guard evidence/admission mismatch")
	}
	now := e.now().UTC()
	if v.CompletedAt.After(now.Add(time.Minute)) {
		return errors.New("cache guard completion in future")
	}
	// Very late delivery is outside the deduplication/recomputation horizon.
	applyPolicy := v.Admission.Mode != Disabled && v.Admission.Sample && s.ContainsGroup(v.Request.GroupID) && s.Mode != Disabled && !v.CompletedAt.Before(now.Add(-FactLifetime))
	v.CompletedAt = v.CompletedAt.UTC()
	v.RawUsage.CompletedAt = v.CompletedAt
	v.RawUsage.Success = v.Success
	return e.store.Transact(ctx, v.Request.GroupID, func(tx Tx) error {
		inserted, err := tx.InsertFact(v)
		if err != nil || !inserted {
			return err
		}
		var session SessionState
		if _, err = tx.Read(SessionKind, v.Admission.SessionKey, &session); err != nil {
			return err
		}
		// Rebuild from the immutable admission when recovering a missing typed row.
		if session.Routes == nil {
			session.Routes = map[string]SessionRoute{}
		}
		if session.FirstSeenAt.IsZero() {
			session.Key = v.Admission.SessionKey
			session.FirstSeenAt = v.Request.StartedAt
			session.Admissions = map[string]Admission{admissionKey(v.Request): v.Admission}
		}
		if session.Admissions == nil {
			session.Admissions = map[string]Admission{}
		}
		completed := v.Admission
		completed.Completed = true
		session.Admissions[admissionKey(v.Request)] = completed
		if !applyPolicy {
			_, err = tx.Write(SessionKind, v.Admission.SessionKey, &session)
			return err
		}
		key := AccountKey(v.Request)
		routeKey := WarmRouteKey(v.Request)
		route := session.Routes[routeKey]
		if v.Success && v.RawUsage.Known() && v.CompletedAt.After(route.LastSuccessfulAt) {
			if session.LastAccountID > 0 && session.LastAccountID != v.Request.AccountID && !v.CompletedAt.Before(session.LastCompletedAt) {
				session.ResidenceUntil = v.CompletedAt.Add(ResidenceLifetime)
			}
			rate, _ := v.RawUsage.Rate()
			route.AccountID = v.Request.AccountID
			route.UpstreamKeyID = v.Request.UpstreamKeyID
			route.LastSuccessfulAt = v.CompletedAt
			route.LastCachePercent = rate
			if rate >= s.ThresholdPercent {
				route.HighCacheSeen = true
			}
			if !v.CompletedAt.Before(session.LastCompletedAt) {
				session.LastAccountID = v.Request.AccountID
				session.LastModel = v.Request.Model
				session.LastProtocol = v.Request.Protocol
				session.LastRouteKey = routeKey
				session.LastCompletedAt = v.CompletedAt
			}
		}
		var account AccountState
		if _, err = tx.Read(AccountKind, key, &account); err != nil {
			return err
		}
		if account.AccountID == 0 {
			account = AccountState{AccountID: v.Request.AccountID, UpstreamKeyID: v.Request.UpstreamKeyID, Phase: Healthy}
		}
		reference := now
		if v.CompletedAt.After(reference) {
			reference = v.CompletedAt
		}
		historyWindow := time.Duration(s.WindowMinutes) * time.Minute
		if historyWindow < WarmLifetime {
			historyWindow = WarmLifetime
		}
		facts, err := tx.Facts(v.Request.AccountID, reference.Add(-historyWindow))
		if err != nil {
			return err
		}
		if len(facts) > MaxFacts {
			return errors.New("cache guard bounded recomputation exceeded")
		}
		fresh := v.ValidSample() && !v.CompletedAt.Before(reference.Add(-time.Duration(s.WindowMinutes)*time.Minute)) &&
			(account.QualitySince.IsZero() || v.CompletedAt.After(account.QualitySince))
		if err = RecomputeAccount(&account, facts, s, reference, fresh); err != nil {
			return err
		}
		// Recompute the last two valid warm outcomes, including out-of-order facts.
		var own []Evidence
		for _, f := range facts {
			if f.Admission.SessionKey == v.Admission.SessionKey && WarmRouteKey(f.Request) == routeKey && !f.CompletedAt.Before(now.Add(-historyWindow)) {
				own = append(own, f)
				if f.ValidSample() {
					if rate, ok := f.RawUsage.Rate(); ok && rate >= s.ThresholdPercent {
						route.HighCacheSeen = true
					}
				}
			}
		}
		// An unfinished intervening attempt is also a barrier. Otherwise two
		// quickly completed lows could be joined across a slower in-flight call.
		for _, pending := range session.Admissions {
			if !pending.Completed && pending.AccountID == v.Request.AccountID && (pending.RouteKey == routeKey || pending.RouteKey == "") {
				own = append(own, Evidence{ID: pending.RequestID, Request: Request{StartedAt: pending.StartedAt}})
			}
		}
		sort.Slice(own, func(i, j int) bool {
			if own[i].Request.StartedAt.Equal(own[j].Request.StartedAt) {
				return own[i].ID < own[j].ID
			}
			return own[i].Request.StartedAt.Before(own[j].Request.StartedAt)
		})
		route.LowStreak = 0
		for i := len(own) - 1; i >= 0 && route.LowStreak < 2; i-- {
			if !own[i].ValidSample() || !own[i].Admission.ProtectedAtStart {
				break
			}
			rate, _ := own[i].RawUsage.Rate()
			if rate >= s.ThresholdPercent {
				break
			}
			route.LowStreak++
		}
		route.PendingLow = route.LowStreak >= 2
		session.Routes[routeKey] = route
		if _, err = tx.Write(AccountKind, key, &account); err != nil {
			return err
		}
		_, err = tx.Write(SessionKind, v.Admission.SessionKey, &session)
		return err
	})
}

// RecomputeAccount is deliberately a bounded replay of immutable raw facts;
// retries and late arrival cannot double count or reverse the evidence clock.
func RecomputeAccount(a *AccountState, facts []Evidence, s Settings, now time.Time, fresh bool) error {
	var total, read, trialTotal, trialRead int64
	valid, trialValid := 0, 0
	trialSessions := map[string]struct{}{}
	cutoff := now.Add(-time.Duration(s.WindowMinutes) * time.Minute)
	var newest time.Time
	for _, f := range facts {
		if !f.ValidSample() || f.CompletedAt.Before(cutoff) || f.CompletedAt.After(now) || (!a.QualitySince.IsZero() && !f.CompletedAt.After(a.QualitySince)) {
			continue
		}
		if f.CompletedAt.After(newest) {
			newest = f.CompletedAt
		}
		if math.MaxInt64-total < f.RawUsage.TotalInputTokens || math.MaxInt64-read < f.RawUsage.CacheReadTokens {
			return errors.New("cache guard token sum overflow")
		}
		total += f.RawUsage.TotalInputTokens
		read += f.RawUsage.CacheReadTokens
		valid++
		if a.CohortID != "" && f.Admission.Trial && f.Admission.CohortID == a.CohortID {
			trialTotal += f.RawUsage.TotalInputTokens
			trialRead += f.RawUsage.CacheReadTokens
			trialValid++
			trialSessions[f.Admission.SessionKey] = struct{}{}
		}
	}
	a.ValidRequests = valid
	a.TotalInputTokens = total
	a.CacheReadTokens = read
	a.CachePercent = 0
	if total > 0 {
		a.CachePercent = float64(read) / float64(total) * 100
	}
	a.TrialValidRequests = trialValid
	a.TrialSessions = len(trialSessions)
	a.TrialCachePercent = 0
	if trialTotal > 0 {
		a.TrialCachePercent = float64(trialRead) / float64(trialTotal) * 100
	}
	if now.After(a.UpdatedAt) {
		a.UpdatedAt = now
	}
	if a.Phase == "" {
		a.Phase = Healthy
	}
	// New evidence is a transaction fact, not a net count or completion-clock
	// change. Expiry can offset a late insertion without making it any less new.
	if newest.After(a.LastEvidenceAt) {
		a.LastEvidenceAt = newest
	}
	if !fresh {
		return nil
	}
	if a.Phase == Healthy && valid > s.MinValidRequests && a.CachePercent < s.ThresholdPercent {
		a.Phase = Cooling
		a.DegradedAt = now
		a.CooldownUntil = now.Add(time.Duration(s.CooldownMinutes) * time.Minute)
		a.CohortID = ""
		a.FirstTrialAt = time.Time{}
		return nil
	}
	// No-data and wall-clock passage alone never restore an account.
	if a.Phase != Trial || a.FirstTrialAt.IsZero() || now.Before(a.FirstTrialAt.Add(RecoveryLifetime)) || trialValid <= s.MinValidRequests || len(trialSessions) < MinRecoverySessions {
		return nil
	}
	if a.TrialCachePercent >= s.RecoveryThresholdPercent {
		a.Phase = Healthy
		a.CohortID = ""
		a.FirstTrialAt = time.Time{}
		a.CooldownUntil = time.Time{}
		a.QualitySince = now
		a.ValidRequests = 0
		a.TotalInputTokens = 0
		a.CacheReadTokens = 0
		a.CachePercent = 0
		return nil
	}
	if a.TrialCachePercent < s.ThresholdPercent {
		a.Phase = Cooling
		a.CooldownUntil = now.Add(time.Duration(s.CooldownMinutes) * time.Minute)
		a.CohortID = ""
		a.FirstTrialAt = time.Time{}
	}
	return nil
}

func (e *Engine) Refresh(ctx context.Context) error {
	if e.store == nil {
		return ErrUnavailable
	}
	s := e.Settings()
	rows, err := e.store.Rows(ctx, s.GroupIDs)
	if err != nil {
		return err
	}
	if len(rows) > MaxRuntimeRows {
		return errors.New("cache guard runtime bound exceeded")
	}
	now := e.now().UTC()
	groups := map[int64]Snapshot{}
	sessions := map[string]SessionState{}
	for _, row := range rows {
		if !s.ContainsGroup(row.GroupID) {
			continue
		}
		snapshot := groups[row.GroupID]
		snapshot.GroupID = row.GroupID
		snapshot.Mode = s.Mode
		snapshot.Available = true
		snapshot.LoadedAt = now
		switch row.Kind {
		case GroupKind:
			var g GroupState
			if err = json.Unmarshal(row.Payload, &g); err != nil {
				return err
			}
			snapshot.NaturalNewCount = g.NaturalNewCount
			snapshot.TrialCount = g.TrialCount
		case AccountKind:
			var a AccountState
			if err = json.Unmarshal(row.Payload, &a); err != nil {
				return err
			}
			// Refresh expired counts even without new completions. State transitions
			// still require new valid evidence, so time alone never recovers quality.
			version := row.Version
			err = e.store.Transact(ctx, row.GroupID, func(tx Tx) error {
				if _, err := tx.Read(AccountKind, row.Key, &a); err != nil {
					return err
				}
				facts, err := tx.Facts(a.AccountID, now.Add(-time.Duration(s.WindowMinutes)*time.Minute))
				if err != nil {
					return err
				}
				if err = RecomputeAccount(&a, facts, s, now, false); err != nil {
					return err
				}
				version, err = tx.Write(AccountKind, row.Key, &a)
				return err
			})
			if err != nil {
				return err
			}
			snapshot.Accounts = append(snapshot.Accounts, AccountSnapshot{AccountState: a, Version: version})
		case SessionKind:
			var x SessionState
			if err = json.Unmarshal(row.Payload, &x); err != nil {
				return err
			}
			sessions[sessionCacheKey(row.GroupID, row.Key)] = x
			for _, admission := range x.Admissions {
				// A prior-process in-flight admission or a five-minute overdue
				// outcome is durable evidence of an incomplete sampling window.
				if !admission.Completed && (admission.StartedAt.Before(e.bornAt) || now.Sub(admission.StartedAt) > 5*time.Minute) {
					snapshot.SamplingGap = true
				}
			}
		default:
			return errors.New("unknown cache guard typed state")
		}
		groups[row.GroupID] = snapshot
	}
	for id, g := range groups {
		g.Available = !g.SamplingGap
		sort.Slice(g.Accounts, func(i, j int) bool { return g.Accounts[i].AccountID < g.Accounts[j].AccountID })
		groups[id] = g
	}
	e.mu.Lock()
	e.groups = groups
	e.sessions = sessions
	e.loadedAt = now
	e.mu.Unlock()
	return nil
}

// Rebuild rehydrates typed durable runtime rows after process restart. Every
// completed fact and its versioned state were committed in the same transaction.
func (e *Engine) Rebuild(ctx context.Context) error { return e.Refresh(ctx) }
func (e *Engine) Cleanup(ctx context.Context) error {
	if e.store == nil {
		return ErrUnavailable
	}
	return e.store.Cleanup(ctx, e.now().UTC().Add(-FactLifetime))
}
func (e *Engine) CachedSnapshot(groupID int64) Snapshot {
	s := e.Settings()
	e.mu.RLock()
	defer e.mu.RUnlock()
	g := e.groups[groupID]
	g.GroupID = groupID
	g.Mode = s.Mode
	g.LoadedAt = e.loadedAt
	now := e.now().UTC()
	g.Available = !g.SamplingGap && !e.loadedAt.IsZero() && !now.Before(e.loadedAt) && now.Sub(e.loadedAt) <= RefreshLifetime && s.ContainsGroup(groupID)
	g.Accounts = append([]AccountSnapshot(nil), g.Accounts...)
	return e.cachedCascade(g, s, now)
}
func (e *Engine) Snapshot(_ context.Context, groupID int64) (Snapshot, error) {
	s := e.CachedSnapshot(groupID)
	if !s.Available {
		return s, ErrUnavailable
	}
	return s, nil
}
func (e *Engine) CachedSessionRead(r Request) SessionRead {
	if !e.CachedSnapshot(r.GroupID).Available {
		return SessionRead{}
	}
	s := e.Settings()
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.loadedAt.IsZero() || e.now().Sub(e.loadedAt) > RefreshLifetime || !s.ContainsGroup(r.GroupID) {
		return SessionRead{}
	}
	x, ok := e.sessions[sessionCacheKey(r.GroupID, SessionKey(r))]
	if !ok {
		return SessionRead{}
	}
	read := SessionRead{Known: true, PreferredAccountID: x.LastAccountID, ResidenceUntil: x.ResidenceUntil}
	if x.LastModel != r.Model || x.LastProtocol != r.Protocol ||
		(r.AccountID > 0 && r.UpstreamKeyID > 0 && x.LastRouteKey != WarmRouteKey(r)) {
		return read
	}
	if route, ok := x.Routes[x.LastRouteKey]; ok {
		read.LastSuccessfulAt = route.LastSuccessfulAt
		read.WarmAtStart = routeWarm(route, r.StartedAt)
		read.PendingLow = route.PendingLow
		read.Protected = read.WarmAtStart && ((route.HighCacheSeen && !route.PendingLow) || r.StartedAt.Before(x.ResidenceUntil))
	}
	read.Trial = x.CohortID != "" && x.TrialAccountID == x.LastAccountID && x.TrialModel == r.Model && x.TrialProtocol == r.Protocol
	read.CohortID = x.CohortID
	if s.Mode != Protect {
		read.Protected = false
	}
	return read
}
func (e *Engine) SessionRead(_ context.Context, r Request) (SessionRead, error) {
	if !e.CachedSnapshot(r.GroupID).Available {
		return SessionRead{}, ErrUnavailable
	}
	return e.CachedSessionRead(r), nil
}
func CandidatePreference(snapshot Snapshot, session SessionRead, c Candidate) int {
	if !snapshot.Available || snapshot.Mode != Protect {
		return 0
	}
	if session.Protected && session.PreferredAccountID == c.AccountID {
		return 2
	}
	for _, a := range snapshot.Accounts {
		if a.AccountID == c.AccountID && a.Phase != Healthy {
			return -1
		}
	}
	return 0
}
func PreferCandidates(snapshot Snapshot, session SessionRead, candidates []Candidate) []Candidate {
	out := append([]Candidate(nil), candidates...)
	for i := range out {
		out[i].OriginalIndex = i
		out[i].Preference = CandidatePreference(snapshot, session, out[i])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Preference > out[j].Preference })
	return out
}
