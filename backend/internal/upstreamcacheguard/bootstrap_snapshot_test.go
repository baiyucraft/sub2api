package upstreamcacheguard

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/upstreamcacheusage"
)

type bootstrapDirectoryStore struct {
	*incrementalMemoryStore
	failDirectory bool
}

func (s *bootstrapDirectoryStore) IdentityVersions(ctx context.Context) ([]IdentityVersion, error) {
	if s.failDirectory {
		return nil, errors.New("directory unavailable")
	}
	return s.incrementalMemoryStore.IdentityVersions(ctx)
}

func bootstrapSnapshotFixture(t *testing.T) (*Engine, *bootstrapDirectoryStore, Request) {
	t.Helper()
	e, memory := testEngine(t)
	store := &bootstrapDirectoryStore{incrementalMemoryStore: &incrementalMemoryStore{memoryStore: memory}}
	e.store = store
	global := e.Settings()
	global.GroupIDs = []int64{5, 7, 8, 21}
	if err := e.ApplySettings(global); err != nil {
		t.Fatal(err)
	}
	independent := global
	independent.GroupIDs = []int64{}
	independent.ThresholdPercent, independent.RecoveryThresholdPercent = 90, 93
	if err := e.ApplyPolicySettings(map[int64]Settings{22: independent}); err != nil {
		t.Fatal(err)
	}
	r := request("first-bootstrap", "bootstrap-session", testNow)
	r.GroupID, r.AccountID, r.UpstreamKeyID = 22, 13963, 425
	return e, store, r
}

func TestNewAccountSnapshotAllowsFirstAdmissionAfterIncrementalRefresh(t *testing.T) {
	e, store, r := bootstrapSnapshotFixture(t)
	for tick := 0; tick < 3; tick++ {
		now := testNow.Add(time.Duration(tick) * time.Second)
		e.now = func() time.Time { return now }
		if err := e.Refresh(context.Background()); err != nil {
			t.Fatal(err)
		}
		a, available := e.CachedAccountForGroupAt(r.GroupID, r.AccountID, r.UpstreamKeyID, now)
		if !available || !a.Fresh || a.Phase != Healthy || a.ValidRequests != 0 {
			t.Fatalf("tick=%d new account cannot reach first admission: available=%v snapshot=%+v", tick, available, a)
		}
		if versions, err := store.IdentityVersions(context.Background()); err != nil || len(versions) != 0 {
			t.Fatalf("management snapshot created durable state: versions=%+v err=%v", versions, err)
		}
	}
	r.StartedAt = testNow.Add(2 * time.Second)
	first := admit(t, e, r)
	if !first.Sample || first.WarmAtStart {
		t.Fatalf("first request must be sampled as cold: %+v", first)
	}
	e.now = func() time.Time { return r.StartedAt.Add(time.Second) }
	complete(t, e, r, first, 90, true, upstreamcacheusage.Known)
	nextAt := r.StartedAt.Add(2 * time.Second)
	e.now = func() time.Time { return nextAt }
	if err := e.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	next := r
	next.ID, next.StartedAt = "warm-bootstrap", nextAt
	second := admit(t, e, next)
	if !second.Sample || !second.WarmAtStart {
		t.Fatalf("continuation after successful cold request must be warm: %+v", second)
	}
	e.now = func() time.Time { return nextAt.Add(time.Second) }
	complete(t, e, next, second, 90, true, upstreamcacheusage.Known)
	if err := e.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	a, available := e.CachedAccountForGroupAt(r.GroupID, r.AccountID, r.UpstreamKeyID, e.now())
	if !available || !a.Fresh || a.ValidRequests != 1 || a.CachePercent != 90 {
		t.Fatalf("cold and warm evidence classification changed: %+v available=%v", a, available)
	}
}

func TestNewAccountSnapshotRequiresSuccessfulRefreshAndCurrentPolicy(t *testing.T) {
	e, store, r := bootstrapSnapshotFixture(t)
	assertAvailability := func(label string, available, fresh bool) {
		t.Helper()
		a, usable := e.CachedAccountForGroupAt(r.GroupID, r.AccountID, r.UpstreamKeyID, e.now())
		if usable != available || a.Fresh != fresh {
			t.Fatalf("%s: available=%v fresh=%v, want %v/%v", label, usable, a.Fresh, available, fresh)
		}
	}
	assertAvailability("never refreshed", false, false)
	store.failDirectory = true
	if err := e.Refresh(context.Background()); err == nil {
		t.Fatal("expected failed initial directory read")
	}
	assertAvailability("failed initial refresh", false, false)
	store.failDirectory = false
	if err := e.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertAvailability("successful refresh", true, true)
	policy := e.ResolvedSettings(r.GroupID)
	policy.ThresholdPercent = 89
	if err := e.ApplyPolicySettings(map[int64]Settings{r.GroupID: policy}); err != nil {
		t.Fatal(err)
	}
	assertAvailability("changed policy awaits directory verification", true, false)
	store.failDirectory = true
	if err := e.Refresh(context.Background()); err == nil {
		t.Fatal("expected failed policy verification")
	}
	assertAvailability("failed verification cannot bless new policy", true, false)
	store.failDirectory = false
	if err := e.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertAvailability("new policy verified", true, true)
}

func TestNewAccountSnapshotKeepsFreshAndUsableTimeBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name      string
		age       time.Duration
		available bool
		fresh     bool
	}{
		{"clock rollback", -time.Nanosecond, false, false},
		{"exact fresh boundary", 5 * time.Second, true, true},
		{"after fresh boundary", 5*time.Second + time.Nanosecond, true, false},
		{"exact usable boundary", 30 * time.Second, true, false},
		{"after usable boundary", 30*time.Second + time.Nanosecond, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, store, r := bootstrapSnapshotFixture(t)
			if err := e.Refresh(context.Background()); err != nil {
				t.Fatal(err)
			}
			now := testNow.Add(tc.age)
			e.now = func() time.Time { return now }
			store.failDirectory = true
			if err := e.Refresh(context.Background()); err == nil {
				t.Fatal("expected failed refresh")
			}
			a, available := e.CachedAccountForGroupAt(r.GroupID, r.AccountID, r.UpstreamKeyID, now)
			if available != tc.available || a.Fresh != tc.fresh || !a.LoadedAt.Equal(testNow) {
				t.Fatalf("failed refresh changed snapshot lifetime: available=%v snapshot=%+v", available, a)
			}
		})
	}
}

func TestNewAccountSnapshotDoesNotBorrowFreshnessForUnverifiedIdentity(t *testing.T) {
	for _, scenario := range []string{"cached state missing epoch", "cached state old epoch", "fact version without state", "previous epoch without state", "account gap", "sampling failure"} {
		t.Run(scenario, func(t *testing.T) {
			e, _, r := bootstrapSnapshotFixture(t)
			if err := e.Refresh(context.Background()); err != nil {
				t.Fatal(err)
			}
			id := accountIdentity(r.AccountID, r.UpstreamKeyID)
			switch scenario {
			case "cached state missing epoch", "cached state old epoch":
				e.accounts[id] = AccountSnapshot{AccountState: AccountState{AccountID: r.AccountID, UpstreamKeyID: r.UpstreamKeyID, Phase: Cooling}, LoadedAt: testNow}
				if scenario == "cached state old epoch" {
					e.accountPolicyEpoch[id] = e.PolicyVersion() - 1
				}
			case "fact version without state":
				e.refreshVersions[id] = IdentityVersion{AccountID: r.AccountID, UpstreamKeyID: r.UpstreamKeyID, FactVersion: 1}
			case "previous epoch without state":
				e.accountPolicyEpoch[id] = e.PolicyVersion() - 1
			case "account gap":
				e.accountGaps[id] = true
			case "sampling failure":
				e.samplingFailures[id] = map[string]struct{}{"undelivered-admission": {}}
			}
			a, _ := e.CachedAccountForGroupAt(r.GroupID, r.AccountID, r.UpstreamKeyID, testNow)
			if a.Fresh {
				t.Fatalf("%s was incorrectly treated as a new verified identity: %+v", scenario, a)
			}
		})
	}
}

func TestNewAccountSnapshotKeyRotationStartsColdWithoutBorrowingOldState(t *testing.T) {
	e, _, old := bootstrapSnapshotFixture(t)
	if err := e.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := admit(t, e, old)
	e.now = func() time.Time { return testNow.Add(time.Second) }
	complete(t, e, old, first, 90, true, upstreamcacheusage.Known)
	e.now = func() time.Time { return testNow.Add(2 * time.Second) }
	if err := e.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	rotated := old
	rotated.ID, rotated.UpstreamKeyID, rotated.StartedAt = "rotated-bootstrap", old.UpstreamKeyID+1, e.now()
	a, available := e.CachedAccountForGroupAt(rotated.GroupID, rotated.AccountID, rotated.UpstreamKeyID, rotated.StartedAt)
	if !available || !a.Fresh || a.Phase != Healthy || a.ValidRequests != 0 || a.TotalInputTokens != 0 || a.CacheReadTokens != 0 {
		t.Fatalf("rotated Key inherited old state or cannot bootstrap: %+v available=%v", a, available)
	}
	admission := admit(t, e, rotated)
	if !admission.Sample || admission.WarmAtStart || admission.UpstreamKeyID != rotated.UpstreamKeyID || admission.RouteKey == first.RouteKey {
		t.Fatalf("rotated Key reused the old warm route: old=%+v new=%+v", first, admission)
	}
}
