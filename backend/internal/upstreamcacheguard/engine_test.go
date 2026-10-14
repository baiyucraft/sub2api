package upstreamcacheguard

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/upstreamcacheusage"
)

// The fake commits a complete copy, so a callback failure has real rollback.
type memoryStore struct {
	mu        sync.Mutex
	rows      map[string]RuntimeRow
	facts     map[string]Evidence
	processed map[string]bool
	accounts  map[string]AccountRuntimeRow
}

func newMemoryStore() *memoryStore {
	return &memoryStore{rows: map[string]RuntimeRow{}, facts: map[string]Evidence{}, processed: map[string]bool{}, accounts: map[string]AccountRuntimeRow{}}
}

type memoryTx struct {
	s     *memoryStore
	group int64
}

func key(g int64, k, v string) string { return fmt.Sprintf("%d:%s:%s", g, k, v) }
func (s *memoryStore) Transact(ctx context.Context, g int64, fn func(Tx) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	copy := newMemoryStore()
	for k, v := range s.rows {
		copy.rows[k] = v
	}
	for k, v := range s.accounts {
		copy.accounts[k] = v
	}
	for k, v := range s.facts {
		copy.facts[k] = v
	}
	for k, v := range s.processed {
		copy.processed[k] = v
	}
	if err := fn(&memoryTx{copy, g}); err != nil {
		return err
	}
	s.rows = copy.rows
	s.facts = copy.facts
	s.processed = copy.processed
	s.accounts = copy.accounts
	return nil
}
func (t *memoryTx) Read(kind, k string, out any) (int64, error) {
	v, ok := t.s.rows[key(t.group, kind, k)]
	if !ok {
		return 0, nil
	}
	return v.Version, json.Unmarshal(v.Payload, out)
}
func (t *memoryTx) Write(kind, k string, v any) (int64, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return 0, err
	}
	old := t.s.rows[key(t.group, kind, k)]
	old.GroupID = t.group
	old.Kind = kind
	old.Key = k
	old.Version++
	old.Payload = raw
	t.s.rows[key(t.group, kind, k)] = old
	return old.Version, nil
}
func (t *memoryTx) InsertFact(e Evidence) (bool, error) {
	k := key(t.group, "fact", e.ID)
	if t.s.processed[k] {
		return false, nil
	}
	t.s.facts[k] = e
	t.s.processed[k] = true
	return true, nil
}
func (t *memoryTx) Facts(account int64, since time.Time) ([]Evidence, error) {
	var out []Evidence
	for k, e := range t.s.facts {
		if t.s.processed[k] && e.Request.GroupID == t.group && e.Request.AccountID == account && !e.CompletedAt.Before(since) {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CompletedAt.Before(out[j].CompletedAt) })
	return out, nil
}
func (s *memoryStore) Rows(_ context.Context, ids []int64) ([]RuntimeRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []RuntimeRow
	for _, v := range s.rows {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool {
		return key(out[i].GroupID, out[i].Kind, out[i].Key) < key(out[j].GroupID, out[j].Kind, out[j].Key)
	})
	return out, nil
}
func (s *memoryStore) Enqueue(_ context.Context, e Evidence) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := key(e.Request.GroupID, "fact", e.ID)
	if _, ok := s.facts[k]; !ok {
		s.facts[k] = e
	}
	return nil
}
func (s *memoryStore) ClaimPending(_ context.Context, _ string, _ int, _ time.Duration) ([]Evidence, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Evidence
	for k, e := range s.facts {
		if !s.processed[k] {
			out = append(out, e)
		}
	}
	return out, nil
}
func (s *memoryStore) Cleanup(_ context.Context, before time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, e := range s.facts {
		if s.processed[k] && e.CompletedAt.Before(before) {
			delete(s.facts, k)
			delete(s.processed, k)
		}
	}
	return nil
}
func (s *memoryStore) HasPending(context.Context) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k := range s.facts {
		if !s.processed[k] {
			return true, nil
		}
	}
	return false, nil
}

func (s *memoryStore) HasPendingBefore(ctx context.Context, cutoff time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	for k, e := range s.facts {
		if !s.processed[k] && e.CompletedAt.Before(cutoff) {
			return true, nil
		}
	}
	return false, nil
}

var testNow = time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)

func testEngine(t *testing.T) (*Engine, *memoryStore) {
	t.Helper()
	s := newMemoryStore()
	e := NewEngine(s)
	e.now = func() time.Time { return testNow }
	e.bornAt = testNow.Add(-time.Hour)
	settings := DefaultSettings()
	settings.Mode = Protect
	if err := e.ApplySettings(settings); err != nil {
		t.Fatal(err)
	}
	return e, s
}
func request(id, session string, at time.Time) Request {
	return Request{ID: id, LogicalSessionDigest: digest(session), APIKeyID: 1, GroupID: 1, AccountID: 2, UpstreamKeyID: 3, Model: "gpt-test", Protocol: "openai", StartedAt: at}
}
func complete(t *testing.T, e *Engine, r Request, a Admission, read int64, success bool, status upstreamcacheusage.Status) Evidence {
	t.Helper()
	v := Evidence{ID: fmt.Sprintf("%s:%d:%d", r.ID, r.AccountID, r.StartedAt.UnixNano()), Request: r, Admission: a, RawUsage: RawUsage{Status: status, TotalInputTokens: 100, CacheReadTokens: read}, Success: success, CompletedAt: r.StartedAt.Add(time.Second)}
	if err := e.Complete(context.Background(), v); err != nil {
		t.Fatal(err)
	}
	return v
}
func admit(t *testing.T, e *Engine, r Request) Admission {
	t.Helper()
	a, err := e.Admit(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	return a
}
func account(t *testing.T, e *Engine) AccountState {
	t.Helper()
	if err := e.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	a, _ := e.CachedAccount(2, 3)
	return a.AccountState
}

func TestAccountProtectionIsSharedAcrossGroupsAndIsolatedAcrossKeyChanges(t *testing.T) {
	e, _ := testEngine(t)
	for _, group := range []int64{1, 2} {
		for round := 0; round < 22; round++ {
			r := request(fmt.Sprintf("group-%d-round-%d", group, round), "same-session", testNow.Add(time.Duration(round-23)*time.Minute))
			r.GroupID = group
			a := admit(t, e, r)
			read := int64(10)
			if group == 2 {
				read = 90
			}
			complete(t, e, r, a, read, true, upstreamcacheusage.Known)
		}
	}
	if err := e.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	low, high := e.CachedSnapshot(1), e.CachedSnapshot(2)
	if len(low.Accounts) != 1 || len(high.Accounts) != 1 || low.Accounts[0].Phase != Cooling || high.Accounts[0].Phase != Cooling || low.Accounts[0].ValidRequests != high.Accounts[0].ValidRequests || low.Accounts[0].CachePercent != high.Accounts[0].CachePercent {
		t.Fatalf("same account must share a global state: low=%+v high=%+v", low, high)
	}
	// Two subsequent lows under protection make this concrete warm route pending.
	for round := 0; round < 2; round++ {
		r := request(fmt.Sprintf("protected-%d", round), "same-session", testNow.Add(time.Duration(round*2-4)*time.Second))
		complete(t, e, r, admit(t, e, r), 10, true, upstreamcacheusage.Known)
	}
	if err := e.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	next := request("next", "same-session", testNow.Add(2*time.Second))
	if !e.CachedSessionRead(next).PendingLow {
		t.Fatal("the original protected warm route should be pending migration")
	}
	next.UpstreamKeyID++
	if read := e.CachedSessionRead(next); read.Known || read.PreferredAccountID != 0 || read.PendingLow || read.WarmAtStart || read.Protected {
		t.Fatalf("replacing the upstream key must not reuse the old route's low streak: %+v", read)
	}
}

func TestConcurrentColdAdmissionsFrozenAndIdempotent(t *testing.T) {
	e, _ := testEngine(t)
	var wg sync.WaitGroup
	admissions := make([]Admission, 10)
	reqs := make([]Request, 10)
	for i := range admissions {
		reqs[i] = request(fmt.Sprint(i), "same", testNow.Add(-time.Minute))
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			a, err := e.Admit(context.Background(), reqs[i])
			if err != nil {
				t.Error(err)
			}
			admissions[i] = a
		}(i)
	}
	wg.Wait()
	for i, a := range admissions {
		if a.WarmAtStart {
			t.Fatal("concurrent cold became warm")
		}
		v := complete(t, e, reqs[i], a, 90, true, upstreamcacheusage.Known)
		if err := e.Complete(context.Background(), v); err != nil {
			t.Fatal(err)
		}
	}
	if a := account(t, e); a.ValidRequests != 0 {
		t.Fatalf("cold counted=%d", a.ValidRequests)
	}
}

func TestStrictCountCrossModelAndExpiredWindow(t *testing.T) {
	e, _ := testEngine(t)
	var facts []Evidence
	for i := 0; i < 21; i++ {
		r := request(fmt.Sprint(i), "s", testNow.Add(-time.Duration(30-i)*time.Second))
		if i%2 == 0 {
			r.Model = "other"
		}
		facts = append(facts, Evidence{Request: r, Admission: Admission{Sample: true, WarmAtStart: true}, RawUsage: RawUsage{Status: upstreamcacheusage.Known, TotalInputTokens: 100, CacheReadTokens: 50}, Success: true, CompletedAt: r.StartedAt})
	}
	a := AccountState{AccountID: 2, Phase: Healthy}
	s := e.Settings()
	if err := RecomputeAccount(&a, facts[:20], s, testNow, true); err != nil {
		t.Fatal(err)
	}
	if a.Phase != Healthy {
		t.Fatal("20 must not degrade")
	}
	if err := RecomputeAccount(&a, facts, s, testNow, true); err != nil {
		t.Fatal(err)
	}
	if a.Phase != Cooling || a.ValidRequests != 21 {
		t.Fatalf("cross model aggregation=%+v", a)
	}
	if err := RecomputeAccount(&a, nil, s, testNow.Add(2*time.Hour), false); err != nil {
		t.Fatal(err)
	}
	if a.ValidRequests != 0 || a.Phase != Cooling {
		t.Fatalf("expired stats auto recovered=%+v", a)
	}
}

func TestSequentialLowFailureBarrierOutOfOrder(t *testing.T) {
	e, _ := testEngine(t)
	seedCooling(t, e)
	base := testNow.Add(-10 * time.Minute)
	r := request("seed", "s", base)
	complete(t, e, r, admit(t, e, r), 90, true, upstreamcacheusage.Known)
	for i, step := range []struct {
		read    int64
		success bool
		status  upstreamcacheusage.Status
	}{{20, true, upstreamcacheusage.Known}, {0, false, upstreamcacheusage.Absent}, {20, true, upstreamcacheusage.Known}} {
		r = request(fmt.Sprint(i), "s", base.Add(time.Duration(i+1)*time.Minute))
		complete(t, e, r, admit(t, e, r), step.read, step.success, step.status)
	}
	account(t, e)
	read := e.CachedSessionRead(request("next", "s", base.Add(4*time.Minute)))
	if read.PendingLow || !read.Protected {
		t.Fatalf("cross-failure low streak=%+v", read)
	}
	r = request("last", "s", base.Add(4*time.Minute))
	complete(t, e, r, admit(t, e, r), 20, true, upstreamcacheusage.Known)
	account(t, e)
	read = e.CachedSessionRead(request("next2", "s", base.Add(6*time.Minute)))
	if !read.PendingLow || read.Protected {
		t.Fatalf("two adjacent low ignored=%+v", read)
	}
	old := request("late-old", "s", base.Add(500*time.Millisecond))
	complete(t, e, old, Admission{RequestID: old.ID, SessionKey: SessionKey(old), GroupID: 1, AccountID: 2, RouteKey: WarmRouteKey(old), StartedAt: old.StartedAt, Mode: Protect, Sample: true, WarmAtStart: true}, 95, true, upstreamcacheusage.Known)
	account(t, e)
	read = e.CachedSessionRead(request("next3", "s", base.Add(6*time.Minute)))
	if !read.PendingLow || !read.LastSuccessfulAt.Equal(r.StartedAt.Add(time.Second)) {
		t.Fatalf("late result reversed latest=%+v", read)
	}
}

func TestSharedNaturalQuotaAndNoNewOnModelIdleMigration(t *testing.T) {
	e, s := testEngine(t)
	e2 := NewEngine(s)
	e2.now = e.now
	if err := e2.ApplySettings(e.Settings()); err != nil {
		t.Fatal(err)
	}
	slots := 0
	for i := 1; i <= 100; i++ {
		r := request(fmt.Sprint(i), fmt.Sprint(i), testNow.Add(-time.Minute))
		r.AccountID = 0
		engine := e
		if i%2 == 0 {
			engine = e2
		}
		p, err := engine.PlanTrial(context.Background(), r)
		if err != nil {
			t.Fatal(err)
		}
		if p.TrialSlot {
			slots++
		}
		again, err := e.PlanTrial(context.Background(), r)
		if err != nil || again != p {
			t.Fatalf("plan not idempotent %v", err)
		}
		r.ID += "next"
		r.Model = "different"
		r.StartedAt = testNow.Add(time.Hour)
		p, err = e2.PlanTrial(context.Background(), r)
		if err != nil || p.NaturalNew || p.TrialSlot {
			t.Fatalf("idle/model acquired quota %+v %v", p, err)
		}
	}
	if slots != 10 {
		t.Fatalf("shared slots=%d", slots)
	}
}

func TestRecoveryCohortRequiresSessionsCountTimeAndFreshEvidence(t *testing.T) {
	e, _ := testEngine(t)
	s := e.Settings()
	var facts []Evidence
	for i := 0; i < 21; i++ {
		facts = append(facts, Evidence{Request: Request{GroupID: 1}, Admission: Admission{Sample: true, WarmAtStart: true, Trial: true, CohortID: "c", SessionKey: fmt.Sprint(i % 3)}, Success: true, RawUsage: RawUsage{Status: upstreamcacheusage.Known, TotalInputTokens: 100, CacheReadTokens: 85}, CompletedAt: testNow.Add(-time.Duration(21-i) * time.Second)})
	}
	a := AccountState{Phase: Trial, CohortID: "c", FirstTrialAt: testNow.Add(-9 * time.Minute)}
	if err := RecomputeAccount(&a, facts, s, testNow, true); err != nil {
		t.Fatal(err)
	}
	if a.Phase != Trial {
		t.Fatal("early recovery")
	}
	if err := RecomputeAccount(&a, facts, s, testNow.Add(2*time.Minute), false); err != nil {
		t.Fatal(err)
	}
	if a.Phase != Trial {
		t.Fatal("timer alone recovered")
	}
	facts = append(facts, Evidence{Request: facts[0].Request, Admission: facts[0].Admission, Success: true, RawUsage: facts[0].RawUsage, CompletedAt: testNow.Add(2 * time.Minute)})
	if err := RecomputeAccount(&a, facts, s, testNow.Add(2*time.Minute), true); err != nil {
		t.Fatal(err)
	}
	if a.Phase != Healthy || a.ValidRequests != 0 || a.QualitySince.IsZero() {
		t.Fatalf("recovery=%+v", a)
	}
	if err := RecomputeAccount(&a, facts, s, testNow.Add(3*time.Minute), false); err != nil {
		t.Fatal(err)
	}
	if a.Phase != Healthy || a.ValidRequests != 0 {
		t.Fatalf("legacy cohort triggered quality=%+v", a)
	}
}

func TestUnknownInvalidFailedAndKnownZero(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  upstreamcacheusage.Status
		read    int64
		success bool
		valid   bool
	}{{"zero", upstreamcacheusage.Known, 0, true, true}, {"missing", upstreamcacheusage.Absent, 0, true, false}, {"invalid", upstreamcacheusage.Invalid, 0, true, false}, {"failed", upstreamcacheusage.Known, 90, false, false}, {"too-large", upstreamcacheusage.Known, 101, true, false}} {
		t.Run(tc.name, func(t *testing.T) {
			v := Evidence{Success: tc.success, Admission: Admission{Sample: true, WarmAtStart: true}, RawUsage: RawUsage{Status: tc.status, TotalInputTokens: 100, CacheReadTokens: tc.read}}
			if v.ValidSample() != tc.valid {
				t.Fatalf("valid=%v", v.ValidSample())
			}
		})
	}
}

func TestDurableOutboxRestartGapAndOldDisabledAck(t *testing.T) {
	e, s := testEngine(t)
	r := request("x", "s", testNow.Add(-time.Minute))
	a := admit(t, e, r)
	v := Evidence{ID: "x", Request: r, Admission: a, Success: true, CompletedAt: r.StartedAt.Add(time.Second), RawUsage: RawUsage{Status: upstreamcacheusage.Known, TotalInputTokens: 100, CacheReadTokens: 90}}
	if err := s.Enqueue(context.Background(), v); err != nil {
		t.Fatal(err)
	}
	restarted := NewEngine(s)
	restarted.now = e.now
	restarted.bornAt = testNow
	if err := restarted.ApplySettings(e.Settings()); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Rebuild(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := restarted.CachedAccount(2, 3); ok {
		t.Fatal("missing outcome became available on restart")
	}
	if err := restarted.Complete(context.Background(), v); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := restarted.CachedAccount(2, 3); !ok {
		t.Fatal("completed durable outcome did not restore")
	}
	if pending, _ := s.ClaimPending(context.Background(), "test", 10, time.Second); len(pending) != 0 {
		t.Fatal("ack was not atomic")
	}
	old := v
	old.ID = "older"
	old.CompletedAt = testNow.Add(-72 * time.Hour)
	old.Request.StartedAt = old.CompletedAt.Add(-time.Second)
	old.Admission.RequestID = old.Request.ID
	old.Admission.StartedAt = old.Request.StartedAt
	if err := s.Enqueue(context.Background(), old); err != nil {
		t.Fatal(err)
	}
	if err := s.Cleanup(context.Background(), testNow); err != nil {
		t.Fatal(err)
	}
	if pending, _ := s.ClaimPending(context.Background(), "test", 10, time.Second); len(pending) != 1 {
		t.Fatal("unprocessed old fact deleted")
	}
	settings := e.Settings()
	settings.Mode = Disabled
	_ = restarted.ApplySettings(settings)
	if err := restarted.Complete(context.Background(), old); err != nil {
		t.Fatal(err)
	}
	if pending, _ := s.ClaimPending(context.Background(), "test", 10, time.Second); len(pending) != 0 {
		t.Fatal("disabled old pending not acknowledged")
	}
}

func TestSnapshotExpiryObserveNeutralAndSettingsValidation(t *testing.T) {
	e, _ := testEngine(t)
	if err := e.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !e.CachedSnapshot(1).Available {
		t.Fatal("fresh unavailable")
	}
	e.now = func() time.Time { return testNow.Add(6 * time.Second) }
	if e.CachedSnapshot(1).Available {
		t.Fatal("expired snapshot available")
	}
	s := e.Settings()
	s.Mode = Observe
	_ = e.ApplySettings(s)
	got := PreferCandidates(Snapshot{Available: true, Mode: Observe, Accounts: []AccountSnapshot{{AccountState: AccountState{AccountID: 1, Phase: Cooling}}}}, SessionRead{}, []Candidate{{AccountID: 1}, {AccountID: 2}})
	if got[0].AccountID != 1 {
		t.Fatal("observe reordered")
	}
	s = DefaultSettings()
	s.GroupIDs = []int64{2, 1, 2}
	if err := e.ApplySettings(s); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(e.Settings().GroupIDs) != "[1 2]" {
		t.Fatal("groups not normalized")
	}
	s.RecoveryThresholdPercent = 75
	if s.Validate() == nil {
		t.Fatal("invalid thresholds accepted")
	}
	r := request("x", "s", testNow)
	r.LogicalSessionDigest = strings.Repeat("z", 64)
	if validateRequest(r) == nil {
		t.Fatal("raw session accepted")
	}
}

func TestInFlightBarrierAndLateLowWarmFact(t *testing.T) {
	e, _ := testEngine(t)
	seedCooling(t, e)
	base := testNow.Add(-10 * time.Minute)
	r := request("seed", "s", base)
	complete(t, e, r, admit(t, e, r), 90, true, upstreamcacheusage.Known)
	r = request("low1", "s", base.Add(time.Minute))
	complete(t, e, r, admit(t, e, r), 20, true, upstreamcacheusage.Known)
	pending := request("pending", "s", base.Add(2*time.Minute))
	pendingAdmission := admit(t, e, pending)
	r = request("low2", "s", base.Add(3*time.Minute))
	complete(t, e, r, admit(t, e, r), 20, true, upstreamcacheusage.Known)
	account(t, e)
	if read := e.CachedSessionRead(request("next", "s", base.Add(4*time.Minute))); read.PendingLow {
		t.Fatal("in-flight call did not interrupt low sequence")
	}
	complete(t, e, pending, pendingAdmission, 0, false, upstreamcacheusage.Absent)
	account(t, e)
	if read := e.CachedSessionRead(request("next2", "s", base.Add(4*time.Minute))); read.PendingLow {
		t.Fatal("late failure removed barrier")
	}
	s := e.Settings()
	a := AccountState{Phase: Healthy, ValidRequests: 20, LastEvidenceAt: testNow.Add(-time.Second)}
	facts := make([]Evidence, 21)
	for i := range facts {
		facts[i] = Evidence{Success: true, Admission: Admission{Sample: true, WarmAtStart: true}, RawUsage: RawUsage{Status: upstreamcacheusage.Known, TotalInputTokens: 100, CacheReadTokens: 50}, CompletedAt: testNow.Add(-time.Duration(i+1) * time.Second)}
	}
	if err := RecomputeAccount(&a, facts, s, testNow, true); err != nil {
		t.Fatal(err)
	}
	if a.Phase != Cooling {
		t.Fatal("late valid evidence crossing minimum count was ignored")
	}
}

func TestCompleteLateLargeFactStillEvaluatesWhenWindowExpiryOffsetsCount(t *testing.T) {
	for _, expired := range []int{1, 2} {
		t.Run(fmt.Sprintf("expired_%d", expired), func(t *testing.T) {
			e, _ := testEngine(t)
			seedCount := 20 + expired
			makeEvidence := func(id string, completed time.Time, input, read int64) Evidence {
				r := request(id, id, completed.Add(-time.Second))
				return Evidence{ID: id, Request: r, Admission: Admission{
					RequestID: r.ID, SessionKey: SessionKey(r), GroupID: r.GroupID, AccountID: r.AccountID, RouteKey: WarmRouteKey(r),
					StartedAt: r.StartedAt, Mode: Protect, Sample: true, WarmAtStart: true,
				}, Success: true, CompletedAt: completed, RawUsage: RawUsage{
					Status: upstreamcacheusage.Known, TotalInputTokens: input, CacheReadTokens: read,
				}}
			}
			for i := 0; i < seedCount; i++ {
				at := testNow.Add(-time.Minute - time.Duration(i)*time.Second)
				if i < expired {
					at = testNow.Add(-time.Hour + time.Second)
				}
				if err := e.Complete(context.Background(), makeEvidence(fmt.Sprint(i), at, 100, 80)); err != nil {
					t.Fatal(err)
				}
			}
			before := account(t, e)
			if before.Phase != Healthy || before.ValidRequests != seedCount || before.CachePercent != 80 {
				t.Fatalf("seed=%+v", before)
			}
			e.now = func() time.Time { return testNow.Add(2 * time.Second) }
			late := makeEvidence("late-large", testNow.Add(-30*time.Minute), 100000, 0)
			if err := e.Complete(context.Background(), late); err != nil {
				t.Fatal(err)
			}
			after := account(t, e)
			if after.Phase != Cooling || after.ValidRequests != 21 || after.TotalInputTokens != 102000 || after.CacheReadTokens != 1600 {
				t.Fatalf("late evidence was hidden by net window count: %+v", after)
			}
			if !after.LastEvidenceAt.Equal(before.LastEvidenceAt) {
				t.Fatal("late completion reversed the evidence clock")
			}
			if err := e.Complete(context.Background(), late); err != nil {
				t.Fatal(err)
			}
			if replay := account(t, e); replay.ValidRequests != 21 || replay.TotalInputTokens != after.TotalInputTokens {
				t.Fatalf("late replay double counted: %+v", replay)
			}
		})
	}
}

func seedCooling(t *testing.T, e *Engine) {
	t.Helper()
	if err := e.store.Transact(context.Background(), 1, func(tx Tx) error {
		_, err := tx.Write(AccountKind, "2", &AccountState{AccountID: 2, UpstreamKeyID: 3, Phase: Cooling, CooldownUntil: testNow.Add(time.Hour)})
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPreEntryLowsExcludedAndResidenceOnlyAfterMigration(t *testing.T) {
	e, _ := testEngine(t)
	s := e.Settings()
	s.MinValidRequests = 1
	if err := e.ApplySettings(s); err != nil {
		t.Fatal(err)
	}
	base := testNow.Add(-10 * time.Minute)
	r := request("seed", "s", base)
	complete(t, e, r, admit(t, e, r), 90, true, upstreamcacheusage.Known)
	account(t, e)
	if read := e.CachedSessionRead(request("next", "s", base.Add(30*time.Second))); !read.ResidenceUntil.IsZero() {
		t.Fatal("initial prewarm acquired migration residence")
	}
	for i := 1; i <= 4; i++ {
		r = request(fmt.Sprint(i), "s", base.Add(time.Duration(i)*time.Minute))
		a := admit(t, e, r)
		if a.ProtectedAtStart != (i >= 3) {
			t.Fatalf("entry freeze step=%d protected=%v", i, a.ProtectedAtStart)
		}
		complete(t, e, r, a, 20, true, upstreamcacheusage.Known)
		account(t, e)
		read := e.CachedSessionRead(request("next", "s", base.Add(time.Duration(i)*time.Minute+30*time.Second)))
		if read.PendingLow != (i == 4) {
			t.Fatalf("pre-entry low counted step=%d pending=%v", i, read.PendingLow)
		}
	}
	r = request("migrate", "s", base.Add(5*time.Minute))
	r.AccountID = 9
	r.UpstreamKeyID = 10
	complete(t, e, r, admit(t, e, r), 0, true, upstreamcacheusage.Known)
	if err := e.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	next := request("next", "s", base.Add(6*time.Minute))
	next.AccountID, next.UpstreamKeyID = 9, 10
	read := e.CachedSessionRead(next)
	if read.PreferredAccountID != 9 || !read.ResidenceUntil.Equal(r.StartedAt.Add(time.Second+ResidenceLifetime)) || !read.Protected {
		t.Fatalf("successful migration residence=%+v", read)
	}
}
