package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestUpstreamHealthRegistryObservationAndFailureState(t *testing.T) {
	registry := &UpstreamHealthRegistry{items: make(map[int64]UpstreamHealthSnapshot)}
	now := time.Date(2026, 8, 10, 1, 2, 3, 0, time.UTC)

	initial := registry.Snapshot(7)
	require.Equal(t, UpstreamHealthObserving, initial.Status)
	require.True(t, initial.ObservationEnabled)

	disabled := registry.SetObservation(7, false, now)
	require.Equal(t, UpstreamHealthDisabled, disabled.Status)
	require.False(t, disabled.ObservationEnabled)

	registry.SetObservation(7, true, now.Add(time.Second))
	for i := 0; i < 3; i++ {
		registry.RecordFailure(7, "timeout", "probe_timeout", now.Add(time.Duration(i+2)*time.Second))
	}
	suspended := registry.Snapshot(7)
	require.Equal(t, UpstreamHealthSuspended, suspended.Status)
	require.Equal(t, 3, suspended.ConsecutiveFails)

	recovering := registry.RecordProbe(7, "200", "probe_succeeded", now.Add(10*time.Second))
	require.Equal(t, UpstreamHealthRecovering, recovering.Status)
	require.Zero(t, recovering.ConsecutiveFails)
}

func TestUpstreamHealthRegistryAuthFailureSuspendsImmediately(t *testing.T) {
	registry := &UpstreamHealthRegistry{items: make(map[int64]UpstreamHealthSnapshot)}
	item := registry.RecordFailure(9, "401", "authentication_failed", time.Now())
	require.Equal(t, UpstreamHealthSuspended, item.Status)
	require.Contains(t, registry.ExcludedKeyIDs([]int64{8, 9}), int64(9))
}

func TestUpstreamHealthRegistryTransitionCapturesAtomicBeforeAfter(t *testing.T) {
	registry := &UpstreamHealthRegistry{items: make(map[int64]UpstreamHealthSnapshot)}
	now := time.Date(2026, 8, 10, 8, 0, 0, 0, time.UTC)

	transition := registry.RecordProbeFailureTransition(11, "500", "upstream_server_error", now)
	require.Equal(t, UpstreamHealthObserving, transition.Previous.Status)
	require.Equal(t, UpstreamHealthDegraded, transition.Current.Status)
	require.True(t, transition.StateChanged())
	require.Equal(t, transition.Current, registry.Snapshot(11))
}

func TestUpstreamHealthRegistryCapacityDoesNotAccumulateSuspension(t *testing.T) {
	registry := &UpstreamHealthRegistry{items: make(map[int64]UpstreamHealthSnapshot)}
	now := time.Date(2026, 8, 10, 8, 0, 0, 0, time.UTC)

	for i := 0; i < 10; i++ {
		registry.RecordProbeFailure(12, "429", "capacity_limited", now.Add(time.Duration(i)*time.Second))
	}
	item := registry.Snapshot(12)
	require.Equal(t, UpstreamHealthDegraded, item.Status)
	require.Zero(t, item.ConsecutiveFails)
}

func TestUpstreamHealthRegistryDisabledIgnoresEvidenceAndRecoveryNeedsThreeSamples(t *testing.T) {
	registry := &UpstreamHealthRegistry{items: make(map[int64]UpstreamHealthSnapshot)}
	now := time.Date(2026, 8, 10, 8, 0, 0, 0, time.UTC)

	registry.SetObservation(13, false, now)
	registry.RecordProbe(13, "success", "probe_succeeded", now.Add(time.Second))
	registry.RecordTrafficFailure(13, "500", "upstream_server_error", now.Add(2*time.Second))
	require.Equal(t, UpstreamHealthDisabled, registry.Snapshot(13).Status)

	registry.SetObservation(14, true, now)
	registry.RecordProbeFailure(14, "401", "authentication_failed", now.Add(time.Second))
	for i := 1; i <= 2; i++ {
		item := registry.RecordProbe(14, "success", "probe_succeeded", now.Add(time.Duration(i+1)*time.Second))
		require.Equal(t, UpstreamHealthRecovering, item.Status)
		require.Equal(t, i, item.RecoverySamples)
	}
	recovered := registry.RecordProbe(14, "success", "probe_succeeded", now.Add(4*time.Second))
	require.Equal(t, UpstreamHealthHealthy, recovered.Status)
	require.Zero(t, recovered.RecoverySamples)
}

func TestUpstreamHealthRegistryHasTemporaryExclusions(t *testing.T) {
	registry := &UpstreamHealthRegistry{items: make(map[int64]UpstreamHealthSnapshot)}
	require.False(t, registry.HasTemporaryExclusions())

	registry.Hydrate(UpstreamHealthSnapshot{KeyID: 15, Status: UpstreamHealthHealthy, ObservationEnabled: true})
	require.False(t, registry.HasTemporaryExclusions())

	registry.Hydrate(UpstreamHealthSnapshot{KeyID: 16, Status: UpstreamHealthSuspended, ObservationEnabled: true})
	require.True(t, registry.HasTemporaryExclusions())
}

func TestUpstreamHealthHistoryKeepsBoundedChronologicalObservations(t *testing.T) {
	extra := map[string]any{}
	base := time.Date(2026, 8, 10, 8, 0, 0, 0, time.UTC)
	for i := 0; i < UpstreamHealthHistoryLimit+4; i++ {
		AppendUpstreamHealthObservation(extra, UpstreamHealthObservation{
			ObservedAt: base.Add(time.Duration(i) * time.Minute), State: UpstreamHealthHealthy,
			Source: "probe", Result: "success", Reason: "probe_succeeded",
		})
	}
	history := UpstreamHealthHistoryFromExtra(extra, UpstreamHealthHistoryLimit)
	require.Len(t, history, UpstreamHealthHistoryLimit)
	require.Equal(t, base.Add(4*time.Minute), history[0].ObservedAt)
	require.Equal(t, base.Add((UpstreamHealthHistoryLimit+3)*time.Minute), history[len(history)-1].ObservedAt)
}

func TestUpstreamHealthHistoryThrottlesTrafficSuccessesButKeepsFailures(t *testing.T) {
	extra := map[string]any{}
	base := time.Date(2026, 8, 10, 8, 0, 0, 0, time.UTC)
	for _, item := range []UpstreamHealthObservation{
		{ObservedAt: base, State: UpstreamHealthHealthy, Source: "traffic", Result: "200", Reason: "traffic_succeeded"},
		{ObservedAt: base.Add(time.Minute), State: UpstreamHealthHealthy, Source: "traffic", Result: "200", Reason: "traffic_succeeded"},
		{ObservedAt: base.Add(2 * time.Minute), State: UpstreamHealthDegraded, Source: "traffic", Result: "500", Reason: "upstream_server_error"},
		{ObservedAt: base.Add(6 * time.Minute), State: UpstreamHealthHealthy, Source: "traffic", Result: "200", Reason: "traffic_succeeded"},
	} {
		AppendUpstreamHealthObservation(extra, item)
	}
	history := UpstreamHealthHistoryFromExtra(extra, 0)
	require.Len(t, history, 3)
	require.Equal(t, "500", history[1].Result)
	require.Equal(t, base.Add(6*time.Minute), history[2].ObservedAt)
}
