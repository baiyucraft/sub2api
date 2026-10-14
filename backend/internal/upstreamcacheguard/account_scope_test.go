package upstreamcacheguard

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/upstreamcacheusage"
)

func (s *memoryStore) TransactAccount(ctx context.Context, group, account, keyID int64, fn func(Tx) error) error {
	return s.Transact(ctx, group, fn)
}
func (t *memoryTx) ReadAccount(id, keyID int64, out any) (int64, error) {
	r, ok := t.s.accounts[accountIdentity(id, keyID)]
	if !ok {
		return 0, nil
	}
	return r.Version, json.Unmarshal(r.Payload, out)
}
func (t *memoryTx) WriteAccount(id, keyID int64, value any) (int64, error) {
	r := t.s.accounts[accountIdentity(id, keyID)]
	r.AccountID, r.UpstreamKeyID = id, keyID
	r.Version++
	var err error
	r.Payload, err = json.Marshal(value)
	if err != nil {
		return 0, err
	}
	t.s.accounts[accountIdentity(id, keyID)] = r
	return r.Version, nil
}
func (t *memoryTx) AccountFacts(id, keyID int64, since time.Time) ([]Evidence, error) {
	var facts []Evidence
	for key, fact := range t.s.facts {
		if t.s.processed[key] && fact.Request.AccountID == id && fact.Request.UpstreamKeyID == keyID && !fact.CompletedAt.Before(since) {
			facts = append(facts, fact)
		}
	}
	sort.Slice(facts, func(i, j int) bool {
		if facts[i].CompletedAt.Equal(facts[j].CompletedAt) {
			return facts[i].ID < facts[j].ID
		}
		return facts[i].CompletedAt.Before(facts[j].CompletedAt)
	})
	return facts, nil
}
func (t *memoryTx) LegacyAccounts(id, keyID int64) ([]LegacyAccountState, error) {
	var out []LegacyAccountState
	for _, r := range t.s.rows {
		if r.Kind != AccountKind {
			continue
		}
		var a AccountState
		if err := json.Unmarshal(r.Payload, &a); err != nil {
			return nil, err
		}
		if a.AccountID == id && a.UpstreamKeyID == keyID {
			out = append(out, LegacyAccountState{GroupID: r.GroupID, AccountState: a})
		}
	}
	return out, nil
}
func (s *memoryStore) AccountRows(ctx context.Context) ([]AccountRuntimeRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	values := map[string]AccountRuntimeRow{}
	for k, r := range s.accounts {
		values[k] = r
	}
	for _, r := range s.rows {
		if r.Kind != AccountKind {
			continue
		}
		var a AccountState
		if err := json.Unmarshal(r.Payload, &a); err != nil {
			return nil, err
		}
		id := accountIdentity(a.AccountID, a.UpstreamKeyID)
		if _, ok := values[id]; !ok && a.UpstreamKeyID > 0 {
			values[id] = AccountRuntimeRow{AccountID: a.AccountID, UpstreamKeyID: a.UpstreamKeyID}
		}
	}
	var out []AccountRuntimeRow
	for _, r := range values {
		out = append(out, r)
	}
	return out, ctx.Err()
}

func (s *memoryStore) AccountWindows(ctx context.Context, since, until time.Time) (map[string]AccountWindow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]AccountWindow{}
	for id, row := range s.accounts {
		var a AccountState
		if err := json.Unmarshal(row.Payload, &a); err != nil {
			return nil, err
		}
		w := AccountWindow{AccountID: a.AccountID, UpstreamKeyID: a.UpstreamKeyID, Version: row.Version, State: a}
		seen := map[string]bool{}
		for key, f := range s.facts {
			if !s.processed[key] || f.Request.AccountID != a.AccountID || f.Request.UpstreamKeyID != a.UpstreamKeyID || !f.ValidSample() || f.CompletedAt.Before(since) || f.CompletedAt.After(until) {
				continue
			}
			if !a.QualitySince.IsZero() && !f.CompletedAt.After(a.QualitySince) {
				continue
			}
			if reset := a.LegacyQualitySince[f.Request.GroupID]; !reset.IsZero() && !f.CompletedAt.After(reset) {
				continue
			}
			w.ValidRequests++
			w.TotalInputTokens += f.RawUsage.TotalInputTokens
			w.CacheReadTokens += f.RawUsage.CacheReadTokens
			w.GroupIDs = appendGroup(w.GroupIDs, f.Request.GroupID)
			if f.CompletedAt.After(w.LastEvidenceAt) {
				w.LastEvidenceAt = f.CompletedAt
			}
			if a.CohortID != "" && f.Admission.Trial && f.Admission.CohortID == a.CohortID {
				w.TrialValidRequests++
				w.TrialInputTokens += f.RawUsage.TotalInputTokens
				w.TrialCacheReadTokens += f.RawUsage.CacheReadTokens
				seen[sessionCacheKey(f.Request.GroupID, f.Admission.SessionKey)] = true
				w.TrialGroupIDs = appendGroup(w.TrialGroupIDs, f.Request.GroupID)
			}
		}
		w.TrialSessions = len(seen)
		out[id] = w
	}
	return out, ctx.Err()
}

func TestAccountWindowCombinesAllGroupsWithTokenWeighting(t *testing.T) {
	e, _ := testEngine(t)
	s := e.Settings()
	s.GroupIDs = []int64{1}
	s.MinValidRequests = 3
	if err := e.ApplySettings(s); err != nil {
		t.Fatal(err)
	}
	for _, g := range []int64{1, 2} {
		for round := 0; round < 3; round++ {
			r := request(fmt.Sprintf("g%d-%d", g, round), "session", testNow.Add(time.Duration(round-4)*time.Minute))
			r.GroupID = g
			if g == 2 {
				r.Model = "other-model"
				r.Protocol = "anthropic"
			}
			a := admit(t, e, r)
			if g == 2 && (a.Trial || a.ProtectedAtStart) {
				t.Fatal("unselected group executed protection")
			}
			v := Evidence{ID: r.ID, Request: r, Admission: a, Success: true, CompletedAt: r.StartedAt.Add(time.Second), RawUsage: RawUsage{Status: upstreamcacheusage.Known, TotalInputTokens: 100, CacheReadTokens: 0}}
			if g == 2 {
				v.RawUsage.TotalInputTokens = 900
				v.RawUsage.CacheReadTokens = 900
			}
			if err := e.Complete(context.Background(), v); err != nil {
				t.Fatal(err)
			}
		}
	}
	a := account(t, e)
	if a.ValidRequests != 4 || a.CachePercent != 90 || a.Phase != Healthy {
		t.Fatalf("weighted account window=%+v", a)
	}
	if view := e.CachedSnapshot(2); len(view.Accounts) != 1 || view.Accounts[0].CachePercent != 90 {
		t.Fatalf("unselected group sampling missing=%+v", view)
	}
	if e.CachedSessionRead(request("unselected", "session", testNow)).Trial {
		t.Fatal("ordinary samples became trial")
	}
}

func TestAccountLegacyBootstrapPreservesProtectionAndResetBoundaries(t *testing.T) {
	e, store := testEngine(t)
	for _, g := range []int64{1, 2} {
		if err := store.Transact(context.Background(), g, func(tx Tx) error {
			_, err := tx.Write(AccountKind, "2", AccountState{AccountID: 2, UpstreamKeyID: 3, Phase: Trial, CohortID: "old", CooldownUntil: testNow.Add(time.Duration(g) * time.Hour), QualitySince: testNow.Add(-time.Minute)})
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	a, ok := e.CachedAccount(2, 3)
	if !ok || a.Phase != Cooling || a.CohortID != "" || !a.CooldownUntil.Equal(testNow.Add(2*time.Hour)) || len(a.LegacyQualitySince) != 2 {
		t.Fatalf("unsafe bootstrap=%+v", a)
	}
	newKey, ok := e.CachedAccount(2, 4)
	if !ok || newKey.Phase != Healthy || newKey.ValidRequests != 0 {
		t.Fatalf("key inherited old state=%+v", newKey)
	}
}

func TestAccountLegacyBootstrapImmediatelyJudgesCombinedWindow(t *testing.T) {
	e, store := testEngine(t)
	s := e.Settings()
	s.MinValidRequests = 3
	if err := e.ApplySettings(s); err != nil {
		t.Fatal(err)
	}
	for _, groupID := range []int64{1, 2} {
		if err := store.Transact(context.Background(), groupID, func(tx Tx) error {
			if _, err := tx.Write(AccountKind, "2", AccountState{AccountID: 2, UpstreamKeyID: 3, Phase: Healthy}); err != nil {
				return err
			}
			for i := 0; i < 2; i++ {
				r := request(fmt.Sprintf("legacy-%d-%d", groupID, i), "warm", testNow.Add(-time.Minute))
				r.GroupID = groupID
				_, err := tx.InsertFact(Evidence{ID: r.ID, Request: r, Admission: Admission{Sample: true, WarmAtStart: true},
					Success: true, CompletedAt: r.StartedAt.Add(time.Second), RawUsage: RawUsage{Status: upstreamcacheusage.Known, TotalInputTokens: 100}})
				if err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if got := account(t, e); got.ValidRequests != 4 || got.Phase != Cooling || got.CooldownUntil.IsZero() {
		t.Fatalf("combined legacy window not judged immediately: %+v", got)
	}
}

func TestAccountCompletionAcrossGroupsIsIdempotentAndConcurrent(t *testing.T) {
	e, _ := testEngine(t)
	var facts []Evidence
	for i := 0; i < 24; i++ {
		r := request(fmt.Sprint(i), fmt.Sprint(i), testNow.Add(-time.Minute))
		r.GroupID = int64(i%2 + 1)
		a := admit(t, e, r)
		a.WarmAtStart = true
		facts = append(facts, Evidence{ID: r.ID, Request: r, Admission: a, Success: true, CompletedAt: r.StartedAt.Add(time.Second), RawUsage: RawUsage{Status: upstreamcacheusage.Known, TotalInputTokens: 100, CacheReadTokens: 90}})
	}
	var wg sync.WaitGroup
	for _, v := range facts {
		wg.Add(1)
		go func(v Evidence) {
			defer wg.Done()
			for n := 0; n < 2; n++ {
				if err := e.Complete(context.Background(), v); err != nil {
					t.Errorf("complete: %v", err)
				}
			}
		}(v)
	}
	wg.Wait()
	a := account(t, e)
	if a.ValidRequests != 24 || a.TotalInputTokens != 2400 || a.CachePercent != 90 {
		t.Fatalf("duplicate or lost completion=%+v", a)
	}
}

func TestAccountRecoveryCombinesOnlyCurrentEnabledTrialCohort(t *testing.T) {
	s := DefaultSettings()
	s.Mode = Protect
	s.MinValidRequests = 2
	s.GroupIDs = []int64{1, 2}
	a := AccountState{AccountID: 2, UpstreamKeyID: 3, Phase: Trial, CohortID: "current", FirstTrialAt: testNow.Add(-11 * time.Minute)}
	var facts []Evidence
	for i := 0; i < 3; i++ {
		g := int64(i%2 + 1)
		facts = append(facts, Evidence{Request: Request{AccountID: 2, UpstreamKeyID: 3, GroupID: g}, Admission: Admission{Sample: true, WarmAtStart: true, Trial: true, CohortID: "current", SessionKey: fmt.Sprint(i)}, Success: true, CompletedAt: testNow.Add(-time.Second), RawUsage: RawUsage{Status: upstreamcacheusage.Known, TotalInputTokens: 100, CacheReadTokens: 90}})
	}
	ordinary := facts[0]
	ordinary.Request.GroupID = 3
	ordinary.Admission.SessionKey = "foreign"
	ordinary.Admission.Trial = false
	ordinary.RawUsage.CacheReadTokens = 0
	facts = append(facts, ordinary)
	if err := RecomputeAccount(&a, facts, s, testNow, true); err != nil {
		t.Fatal(err)
	}
	if a.Phase != Healthy {
		t.Fatalf("cross-group recovery failed=%+v", a)
	}
	a = AccountState{AccountID: 2, UpstreamKeyID: 3, Phase: Trial, CohortID: "current", FirstTrialAt: testNow.Add(-11 * time.Minute)}
	for i := range facts {
		facts[i].Admission.CohortID = "old"
	}
	if err := RecomputeAccount(&a, facts, s, testNow, true); err != nil {
		t.Fatal(err)
	}
	if a.Phase != Trial || a.TrialValidRequests != 0 {
		t.Fatal("old cohort restored account")
	}
}
