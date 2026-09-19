package service

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/forkscheduling"
)

var _ forkscheduling.TTFTRuntime = legacyTTFTRuntime{}
var _ forkscheduling.HealthRuntime = legacyHealthRuntime{}
var _ forkscheduling.RetryPolicyReader = legacyRetryPolicy{}
var _ forkscheduling.CapacityRuntime = legacyCapacityRuntime{}

func TestLegacyTTFTRuntimeSharesStateAcrossObserverReaderAndExcluder(t *testing.T) {
	now := time.Date(2026, 9, 13, 1, 2, 3, 0, time.UTC)
	guard := newOpenAITTFTGuardWithOptions(16, time.Hour, func() time.Time { return now })
	runtime := legacyTTFTRuntime{guard: guard}
	cfg := forkscheduling.TTFTConfig{Enabled: true, Threshold: 10 * time.Second, MinSamples: 2}
	firstTokenMs := 30000
	runtime.Report(forkscheduling.TTFTSample{GroupID: 100, AccountID: 7, Model: "gpt-5", Success: false, FirstTokenMs: &firstTokenMs}, cfg)

	degradations := runtime.Degradations([]int64{7})
	if len(degradations[7]) != 1 || degradations[7][0].Reason != "critical_sample" {
		t.Fatalf("degradations = %#v", degradations)
	}
	excluded := runtime.Exclusions([]forkscheduling.CandidateView{{GroupID: 100, ID: 7, Model: "gpt-5"}}, nil, cfg)
	if _, ok := excluded[7]; !ok {
		t.Fatalf("excluded accounts = %#v, want account 7", excluded)
	}
}

func TestLegacyHealthRuntimeDelegatesToOneRegistry(t *testing.T) {
	registry := &UpstreamHealthRegistry{items: make(map[int64]UpstreamHealthSnapshot)}
	runtime := legacyHealthRuntime{registry: registry, owner: testLegacyHealthRuntimeOwner{registry: registry}}
	now := time.Date(2026, 9, 13, 2, 0, 0, 0, time.UTC)
	transition, err := runtime.RecordTrafficFailure(9, "401", "authentication_failed", now)
	if err != nil {
		t.Fatalf("RecordTrafficFailure() error = %v", err)
	}
	if transition.Current.Status != forkscheduling.HealthStatus(UpstreamHealthSuspended) {
		t.Fatalf("transition = %#v", transition)
	}
	if got := runtime.Snapshot(9); got.Status != forkscheduling.HealthStatus(UpstreamHealthSuspended) {
		t.Fatalf("snapshot status = %q", got.Status)
	}
	if _, ok := runtime.ExcludedKeyIDs([]int64{9})[9]; !ok {
		t.Fatal("registry adapter did not expose excluded key")
	}
}

func TestLegacyHealthRuntimeDoesNotWriteRegistryWithoutOwner(t *testing.T) {
	registry := &UpstreamHealthRegistry{items: make(map[int64]UpstreamHealthSnapshot)}
	registry.Hydrate(defaultUpstreamHealthSnapshot(10))
	runtime := legacyHealthRuntime{registry: registry}
	now := time.Date(2026, 9, 13, 2, 0, 0, 0, time.UTC)
	transition, err := runtime.RecordTrafficFailure(10, "401", "authentication_failed", now)
	if err != nil {
		t.Fatalf("RecordTrafficFailure() error = %v", err)
	}
	if transition.Current.Status != transition.Previous.Status {
		t.Fatalf("unowned runtime changed health: %#v", transition)
	}
	if got := registry.Snapshot(10); got.Status != UpstreamHealthObserving {
		t.Fatalf("registry status = %q, want %q", got.Status, UpstreamHealthObserving)
	}
}

func TestForkSchedulingRuntimeResolvesServiceHealthOwnerAtCallTime(t *testing.T) {
	const keyID = 92005
	GlobalUpstreamHealthRegistry().Hydrate(defaultUpstreamHealthSnapshot(keyID))
	SetGlobalUpstreamHealthEvidenceRecorder(nil)
	t.Cleanup(func() {
		GlobalUpstreamHealthRegistry().Forget(keyID)
		SetGlobalUpstreamHealthEvidenceRecorder(nil)
	})
	runtime := (&OpenAIGatewayService{}).ForkSchedulingRuntime()
	repo := &healthEventCaptureRepo{}
	owner := &UpstreamConfigService{repo: repo}
	SetGlobalUpstreamHealthEvidenceRecorder(owner)

	transition, err := runtime.HealthEvidence.RecordTrafficFailure(keyID, "401", "authentication_failed", time.Date(2026, 9, 13, 3, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("RecordTrafficFailure() error = %v", err)
	}
	if transition.Current.Status != forkscheduling.HealthStatus(UpstreamHealthSuspended) {
		t.Fatalf("transition = %#v", transition)
	}
	if len(repo.events) != 1 || len(repo.patches) != 1 {
		t.Fatalf("health owner was not used: events=%d patches=%d", len(repo.events), len(repo.patches))
	}
}

type testLegacyHealthRuntimeOwner struct {
	registry     *UpstreamHealthRegistry
	afterTraffic func()
	err          error
}

func (o testLegacyHealthRuntimeOwner) recordUpstreamTrafficEvidenceAt(_ context.Context, keyID int64, success bool, status, reason string, now time.Time) (UpstreamHealthTransition, error) {
	var transition UpstreamHealthTransition
	if success {
		transition = o.registry.RecordTrafficSuccessTransition(keyID, status, reason, now)
	} else {
		transition = o.registry.RecordTrafficFailureTransition(keyID, status, reason, now)
	}
	if o.afterTraffic != nil {
		o.afterTraffic()
	}
	return transition, o.err
}

func (o testLegacyHealthRuntimeOwner) setKeyObservationAt(_ context.Context, keyID int64, enabled bool, now time.Time) (UpstreamHealthTransition, error) {
	return o.registry.SetObservationTransition(keyID, enabled, now), nil
}

func (o testLegacyHealthRuntimeOwner) clearProbeSuspensionAt(_ context.Context, keyID int64, now time.Time) (UpstreamHealthTransition, bool, error) {
	transition, changed := o.registry.ResetProbeSuspension(keyID, now)
	return transition, changed, nil
}

func TestForkSchedulingRuntimeUsesExistingLegacyStateWithoutCreatingNewStores(t *testing.T) {
	svc := &OpenAIGatewayService{}
	runtime := svc.ForkSchedulingRuntime()
	if runtime.TTFT != svc.ForkSchedulingRuntime().TTFT || runtime.HealthReader != svc.ForkSchedulingRuntime().HealthReader {
		t.Fatal("ForkSchedulingRuntime should return stable views for one service instance")
	}
	if runtime.TTFT == nil || runtime.HealthReader == nil || runtime.HealthEvidence == nil || runtime.HealthLifecycle == nil {
		t.Fatalf("runtime = %#v, want all legacy views", runtime)
	}
	if runtime.RPMReader != nil || runtime.RPM != nil {
		t.Fatalf("runtime RPM = %#v, want nil for an unconfigured service", runtime)
	}
}

func TestLegacyHealthRuntimeUsesOwnerTransitionWithoutRacyResnapshot(t *testing.T) {
	const keyID = 92006
	registry := &UpstreamHealthRegistry{items: make(map[int64]UpstreamHealthSnapshot)}
	registry.Hydrate(defaultUpstreamHealthSnapshot(keyID))
	now := time.Date(2026, 9, 13, 4, 0, 0, 0, time.UTC)
	owner := testLegacyHealthRuntimeOwner{
		registry: registry,
		afterTraffic: func() {
			registry.SetObservationTransition(keyID, false, now.Add(time.Second))
		},
	}
	runtime := legacyHealthRuntime{registry: registry, owner: owner}

	transition, err := runtime.RecordTrafficFailure(keyID, "401", "authentication_failed", now)
	if err != nil {
		t.Fatalf("RecordTrafficFailure() error = %v", err)
	}
	if transition.Current.Status != forkscheduling.HealthStatus(UpstreamHealthSuspended) {
		t.Fatalf("transition current = %#v, want the owner's atomic transition", transition.Current)
	}
	if got := runtime.Snapshot(keyID); got.Status != forkscheduling.HealthStatus(UpstreamHealthDisabled) {
		t.Fatalf("registry snapshot = %#v, want later independent mutation", got)
	}
}

func TestLegacyHealthRuntimePropagatesPersistenceFailureAndRollback(t *testing.T) {
	const keyID = 92007
	previous := defaultUpstreamHealthSnapshot(keyID)
	GlobalUpstreamHealthRegistry().Hydrate(previous)
	t.Cleanup(func() { GlobalUpstreamHealthRegistry().Forget(keyID) })
	owner := &UpstreamConfigService{repo: &healthEventCaptureRepo{err: errors.New("write failed")}}
	runtime := legacyHealthRuntime{registry: GlobalUpstreamHealthRegistry(), owner: owner}

	transition, err := runtime.RecordTrafficFailure(keyID, "401", "authentication_failed", time.Date(2026, 9, 13, 5, 0, 0, 0, time.UTC))
	if err == nil {
		t.Fatal("RecordTrafficFailure() error = nil, want persistence failure")
	}
	if !reflect.DeepEqual(transition.Current, transition.Previous) {
		t.Fatalf("transition = %#v, want rolled-back current snapshot", transition)
	}
	if got := GlobalUpstreamHealthRegistry().Snapshot(keyID); !reflect.DeepEqual(got, previous) {
		t.Fatalf("registry snapshot = %#v, want %#v", got, previous)
	}
}

func TestForkSchedulingRuntimeReloadsRPMDependencyAfterSetter(t *testing.T) {
	svc := &OpenAIGatewayService{}
	if runtime := svc.ForkSchedulingRuntime(); runtime.RPMReader != nil || runtime.RPM != nil {
		t.Fatalf("initial runtime RPM = %#v, want nil", runtime)
	}
	cache := &accountRPMGateTestCache{counts: map[int64]int{}}
	svc.SetRPMCache(cache)

	runtime := svc.ForkSchedulingRuntime()
	if runtime.RPMReader != cache || runtime.RPM != cache {
		t.Fatalf("runtime RPM dependencies were not refreshed: %#v", runtime)
	}
}

func TestLegacyRetryPolicyPreservesDefaultForUnconfiguredAccount(t *testing.T) {
	ResetGlobalPoolModeRetryStatusCodesForTest()
	t.Cleanup(ResetGlobalPoolModeRetryStatusCodesForTest)
	policy := retryPolicyForAccount(&Account{})
	if !policy.IsRetryableStatus(401) || !policy.IsRetryableStatus(403) || !policy.IsRetryableStatus(429) || policy.IsRetryableStatus(500) {
		t.Fatalf("default retry policy codes = %v", policy.StatusCodes())
	}
}
