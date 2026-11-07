package upstreamcacheguard

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/upstreamcacheusage"
)

func affinityFixture(t *testing.T) (*Engine, *memoryStore, Request, Admission) {
	t.Helper()
	e, store := testEngine(t)
	r := request("affinity-old", "idle-affinity", testNow.Add(-2*time.Hour))
	a := admit(t, e, r)
	complete(t, e, r, a, 95, true, upstreamcacheusage.Known)
	if err := e.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	return e, store, r, a
}

func TestSessionAffinityIdleBoundaryAndProtocolScope(t *testing.T) {
	e, _ := testEngine(t)
	for _, tc := range []struct {
		name, protocol string
		ttl, offset    time.Duration
		want           bool
	}{
		{"before", "openai", 0, time.Hour - time.Nanosecond, false},
		{"exact", "openai", 0, time.Hour, true},
		{"after", "openai", 0, time.Hour + time.Nanosecond, true},
		{"websocket", "openai_ws", 0, time.Hour, true},
		{"other_protocol", "anthropic", 0, 2 * time.Hour, false},
		{"custom", "openai", 10 * time.Minute, 10 * time.Minute, true},
		{"custom_before", "openai", 10 * time.Minute, 10*time.Minute - time.Nanosecond, false},
		{"nonpositive_defaults", "openai", -time.Minute, 30 * time.Minute, false},
		{"clock_rollback", "openai", 0, -time.Second, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e.SetSessionAffinityTTL(tc.ttl)
			s := SessionState{LastAccountID: 2, LastStartedAt: testNow.Add(-time.Minute), LastCompletedAt: testNow}
			if got := e.sessionAffinityExpired(s, tc.protocol, testNow.Add(tc.offset)); got != tc.want {
				t.Fatalf("expired=%v, want %v", got, tc.want)
			}
		})
	}
	// A still-recent real admission keeps an existing route active even when its
	// latest successful completion is older than the idle lifetime.
	s := SessionState{LastAccountID: 2, LastStartedAt: testNow.Add(-time.Minute), LastCompletedAt: testNow.Add(-2 * time.Hour)}
	if e.sessionAffinityExpired(s, "openai", testNow) {
		t.Fatal("an in-flight recent admission was treated as idle")
	}
	legacy := SessionState{LastAccountID: 2, LastRouteKey: "legacy", Routes: map[string]SessionRoute{
		"legacy": {LastSuccessfulAt: testNow.Add(-time.Minute)},
	}}
	if e.sessionAffinityExpired(legacy, "openai", testNow) {
		t.Fatal("legacy route's real completion was ignored")
	}
}

func TestSessionAffinityReadAndPlanDoNotRenewOrResetIdentity(t *testing.T) {
	e, store, old, _ := affinityFixture(t)
	before := captureSession(t, store, old)
	r := request("after-idle-read", "idle-affinity", testNow)
	read := e.CachedSessionRead(r)
	if !read.Known || !read.AffinityExpired || read.PreferredAccountID != 0 || read.Protected || read.Trial || read.PendingLow {
		t.Fatalf("expired route projection: %+v", read)
	}
	for range 3 {
		if _, err := e.PlanTrial(context.Background(), r); err != nil {
			t.Fatal(err)
		}
		if _, err := e.SessionRead(context.Background(), r); err != nil {
			t.Fatal(err)
		}
	}
	if after := captureSession(t, store, old); !reflect.DeepEqual(before, after) {
		t.Fatalf("reads/planning mutated durable session: before=%+v after=%+v", before, after)
	}
}

func TestSessionAffinityFailureDoesNotReviveOldRoute(t *testing.T) {
	e, store, old, oldAdmission := affinityFixture(t)
	before := captureSession(t, store, old)
	r := request("after-idle-failure", "idle-affinity", testNow)
	r.AccountID, r.UpstreamKeyID = 4, 5
	a := admit(t, e, r)
	if a.AffinityGeneration != 1 || a.NaturalNew {
		t.Fatalf("idle continuation admission: %+v", a)
	}
	complete(t, e, r, a, 0, false, upstreamcacheusage.Absent)
	after := captureSession(t, store, r)
	if after.LastAccountID != 0 || after.LastRouteKey != "" || after.AffinityGeneration != 1 {
		t.Fatalf("failed continuation revived affinity: %+v", after)
	}
	if after.FirstSeenAt != before.FirstSeenAt || after.NaturalRequestID != before.NaturalRequestID || after.NaturalTrialOrdinal != before.NaturalTrialOrdinal || after.NaturalAccountID != before.NaturalAccountID {
		t.Fatal("idle reset changed natural session identity")
	}
	// Replaying an old admission is immutable and cannot advance/reset the fence.
	oldAdmission.Completed = true
	if replay := admit(t, e, old); !reflect.DeepEqual(replay, oldAdmission) {
		t.Fatalf("old admission changed on replay: %+v", replay)
	}
	if got := captureSession(t, store, r); got.AffinityGeneration != 1 {
		t.Fatalf("replay advanced generation: %+v", got)
	}
	if err := e.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	next := request("after-failed-continuation", "idle-affinity", testNow.Add(time.Second))
	read := e.CachedSessionReadAt(next, testNow)
	if !read.Known || !read.AffinityExpired || read.PreferredAccountID != 0 {
		t.Fatalf("failed new request renewed old affinity: %+v", read)
	}
	// A real successful continuation can establish the newly selected route.
	next.AccountID, next.UpstreamKeyID = 6, 7
	na := admit(t, e, next)
	if na.AffinityGeneration != 1 || na.NaturalNew {
		t.Fatalf("continuation reallocated a natural slot: %+v", na)
	}
	complete(t, e, next, na, 95, true, upstreamcacheusage.Known)
	if got := captureSession(t, store, next); got.LastAccountID != 6 || got.AffinityGeneration != 1 {
		t.Fatalf("new route not established: %+v", got)
	}
}

func TestSessionAffinityLateCompletionCannotOverwriteNewRoute(t *testing.T) {
	e, store, _, _ := affinityFixture(t)
	late := request("old-in-flight", "idle-affinity", testNow.Add(-90*time.Minute))
	la := admit(t, e, late)
	next := request("new-route", "idle-affinity", testNow)
	next.AccountID, next.UpstreamKeyID = 4, 5
	na := admit(t, e, next)
	complete(t, e, next, na, 95, true, upstreamcacheusage.Known)
	before := captureSession(t, store, next)
	// The old request actually completes later, so CompletedAt ordering alone
	// would switch the session back to its discarded physical account.
	v := Evidence{ID: "late-affinity-completion", Request: late, Admission: la, Success: true,
		RawUsage: RawUsage{Status: upstreamcacheusage.Known, TotalInputTokens: 100, CacheReadTokens: 95}, CompletedAt: testNow.Add(20 * time.Second)}
	if err := e.Complete(context.Background(), v); err != nil {
		t.Fatal(err)
	}
	after := captureSession(t, store, next)
	if after.LastAccountID != 4 || after.LastCompletedAt != before.LastCompletedAt || after.LastRouteKey != before.LastRouteKey || after.AffinityGeneration != 1 {
		t.Fatalf("late old completion changed new route: before=%+v after=%+v", before, after)
	}
	if _, exists := store.facts[key(late.GroupID, "fact", v.ID)]; !exists {
		t.Fatal("old completion's account fact was discarded")
	}
	if !after.Admissions[admissionKey(late)].Completed {
		t.Fatal("old in-flight admission was not completed")
	}
}

func TestSessionAffinityConcurrentSameTimestampClaimsShareFence(t *testing.T) {
	e, store, _, _ := affinityFixture(t)
	var wg sync.WaitGroup
	admissions := make([]Admission, 2)
	errs := make([]error, 2)
	for i := range admissions {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := request(fmt.Sprintf("simultaneous-%d", i), "idle-affinity", testNow)
			admissions[i], errs[i] = e.Admit(context.Background(), r)
		}(i)
	}
	wg.Wait()
	for i, a := range admissions {
		if errs[i] != nil || a.AffinityGeneration != 1 || a.NaturalNew {
			t.Fatalf("concurrent admission %d: %+v, %v", i, a, errs[i])
		}
	}
	r := request("read-concurrent", "idle-affinity", testNow)
	if got := captureSession(t, store, r); got.AffinityGeneration != 1 || got.LastAccountID != 0 {
		t.Fatalf("concurrent claims reset more than once: %+v", got)
	}
}

func TestSessionAffinityCaptureOnlyResetAndRestart(t *testing.T) {
	e, store, old, _ := affinityFixture(t)
	r := request("capture-after-idle", "idle-affinity", testNow)
	r.AccountID, r.UpstreamKeyID = 4, 5
	a := captureAdmission(t, e, r)
	if a.AffinityGeneration != 1 || a.NaturalNew {
		t.Fatalf("capture reset: %+v", a)
	}
	complete(t, e, r, a, 95, true, upstreamcacheusage.Known)
	restarted := NewEngine(store)
	restarted.now = func() time.Time { return testNow }
	if err := restarted.ApplySettings(e.Settings()); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	read, err := restarted.SessionRead(context.Background(), r)
	if err != nil || !read.Known || !read.AffinityExpired || read.PreferredAccountID != 0 {
		t.Fatalf("restart resurrected expired affinity: %+v, %v", read, err)
	}
	if got := captureSession(t, store, old); got.LastAccountID != 0 || got.AffinityGeneration != 1 {
		t.Fatalf("capture created affinity: %+v", got)
	}
}

type affinityRollbackStore struct{ *memoryStore }

func (s affinityRollbackStore) TransactAccount(ctx context.Context, group, account, keyID int64, fn func(Tx) error) error {
	return s.memoryStore.TransactAccount(ctx, group, account, keyID, func(tx Tx) error {
		if err := fn(tx); err != nil {
			return err
		}
		return errors.New("affinity transaction rollback")
	})
}

func TestSessionAffinityResetRollsBackWithAdmission(t *testing.T) {
	e, store, old, _ := affinityFixture(t)
	before := captureSession(t, store, old)
	e.store = affinityRollbackStore{store}
	r := request("rollback-expiry", "idle-affinity", testNow)
	if _, err := e.Admit(context.Background(), r); err == nil {
		t.Fatal("expected admission rollback")
	}
	if after := captureSession(t, store, old); !reflect.DeepEqual(before, after) {
		t.Fatal("rolled-back admission changed affinity or identity")
	}
}
