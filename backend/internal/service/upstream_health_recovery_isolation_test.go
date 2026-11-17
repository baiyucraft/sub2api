package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestUpstreamHealthFailurePersistsRecoveryResetWithoutReleasingKey(t *testing.T) {
	const keyID int64 = 92117
	registry := GlobalUpstreamHealthRegistry()
	defer registry.Forget(keyID)
	registry.Hydrate(UpstreamHealthSnapshot{
		KeyID: keyID, Status: UpstreamHealthRecovering, ObservationEnabled: true,
		SuspensionSource: "probe", RecoverySamples: 1,
	})
	repo := &healthEventCaptureRepo{}
	svc := &UpstreamConfigService{repo: repo}
	account := &Account{ID: 7, UpstreamKeyID: int64Ptr(keyID)}

	svc.RecordUpstreamTrafficFailure(context.Background(), account, 429)

	require.Contains(t, registry.ExcludedKeyIDs([]int64{keyID}), keyID)
	require.Len(t, repo.patches, 1)
	require.Equal(t, string(UpstreamHealthSuspended), repo.patches[0]["status"])
	require.Equal(t, "probe", repo.patches[0]["suspension_source"])
	require.Equal(t, 0, repo.patches[0]["recovery_samples"])
	require.Len(t, repo.events, 1)
	require.Equal(t, "recovering", repo.events[0].Payload["previous_status"])
	require.Equal(t, "suspended", repo.events[0].Payload["current_status"])
	require.Len(t, repo.histories[keyID], 1)
	require.Equal(t, "429", repo.histories[keyID][0].Result)
	require.Equal(t, UpstreamHealthSuspended, repo.histories[keyID][0].State)

	// A subsequent independent authentication failure owns the quarantine even
	// if the original probe guard is explicitly disabled afterwards.
	svc.RecordUpstreamTrafficFailure(context.Background(), account, 401)
	settings := DefaultUpstreamProbeGuardSettings()
	settings.Enabled = false
	registry.ReevaluateProbeGuard(settings, time.Now())
	require.Equal(t, "traffic", registry.Snapshot(keyID).SuspensionSource)
	require.Contains(t, registry.ExcludedKeyIDs([]int64{keyID}), keyID)
}

func TestUpstreamHealthFailureRetainsExistingIsolation(t *testing.T) {
	now := time.Date(2026, 11, 17, 4, 0, 0, 0, time.UTC)
	for _, source := range []string{"probe", "traffic"} {
		for _, state := range []UpstreamHealthStatus{UpstreamHealthSuspended, UpstreamHealthRecovering} {
			for _, failure := range []struct {
				status string
				reason string
			}{
				{"429", "capacity_limited"},
				{"529", "capacity_limited"},
				{"500", "upstream_server_error"},
				{"timeout", "probe_timeout"},
				{"401", "authentication_failed"},
				{"unsupported_model", "probe_model_unsupported"},
			} {
				for _, probe := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/%s/%s/probe=%t", source, state, failure.status, probe), func(t *testing.T) {
						registry := &UpstreamHealthRegistry{items: make(map[int64]UpstreamHealthSnapshot)}
						registry.Hydrate(UpstreamHealthSnapshot{
							KeyID: 1, Status: state, ObservationEnabled: true,
							SuspensionSource: source, RecoverySamples: 1,
						})
						var transition UpstreamHealthTransition
						if probe {
							transition = registry.RecordProbeFailureWithGuardTransition(1, failure.status, failure.reason, nil, now, DefaultUpstreamProbeGuardSettings())
						} else {
							transition = registry.RecordTrafficFailureTransition(1, failure.status, failure.reason, now)
						}
						require.Equal(t, UpstreamHealthSuspended, transition.Current.Status)
						expectedSource := source
						if !probe && failure.status == "401" {
							expectedSource = "traffic"
						}
						require.Equal(t, expectedSource, transition.Current.SuspensionSource)
						require.Zero(t, transition.Current.RecoverySamples)
						require.Equal(t, failure.reason, transition.Current.Reason)
						require.Contains(t, registry.ExcludedKeyIDs([]int64{1}), int64(1))
						require.True(t, registry.HasTemporaryExclusions())
					})
				}
			}
		}
	}
}

func TestUpstreamHealthFailedRecoveryRequiresFreshSuccessSequence(t *testing.T) {
	for _, probe := range []bool{false, true} {
		t.Run(fmt.Sprintf("probe=%t", probe), func(t *testing.T) {
			registry := &UpstreamHealthRegistry{items: make(map[int64]UpstreamHealthSnapshot)}
			settings := DefaultUpstreamProbeGuardSettings()
			now := time.Date(2026, 11, 17, 4, 0, 0, 0, time.UTC)
			fail := func(status, reason string) UpstreamHealthSnapshot {
				now = now.Add(time.Second)
				if probe {
					return registry.RecordProbeFailureWithGuardTransition(1, status, reason, nil, now, settings).Current
				}
				return registry.RecordTrafficFailureTransition(1, status, reason, now).Current
			}
			succeed := func() UpstreamHealthSnapshot {
				now = now.Add(time.Second)
				if probe {
					return registry.RecordProbeWithGuardSuccessTransition(1, "success", "probe_succeeded", nil, now, settings).Current
				}
				return registry.RecordTrafficSuccessTransition(1, "200", "traffic_succeeded", now).Current
			}
			require.Equal(t, UpstreamHealthSuspended, fail("401", "authentication_failed").Status)
			require.Equal(t, 1, succeed().RecoverySamples)
			require.Equal(t, UpstreamHealthSuspended, fail("500", "upstream_server_error").Status)
			require.Equal(t, 1, succeed().RecoverySamples)
			require.Equal(t, UpstreamHealthSuspended, fail("429", "capacity_limited").Status)
			for i := 1; i < settings.RecoverySuccesses; i++ {
				item := succeed()
				require.Equal(t, UpstreamHealthRecovering, item.Status)
				require.Equal(t, i, item.RecoverySamples)
				require.Contains(t, registry.ExcludedKeyIDs([]int64{1}), int64(1))
			}
			item := succeed()
			require.Equal(t, UpstreamHealthHealthy, item.Status)
			require.Empty(t, item.SuspensionSource)
			require.NotContains(t, registry.ExcludedKeyIDs([]int64{1}), int64(1))
		})
	}
}

func TestUpstreamHealthFailureTrafficThresholdTakesOverProbeIsolation(t *testing.T) {
	for _, state := range []UpstreamHealthStatus{UpstreamHealthSuspended, UpstreamHealthRecovering} {
		t.Run(string(state), func(t *testing.T) {
			registry := &UpstreamHealthRegistry{items: make(map[int64]UpstreamHealthSnapshot)}
			registry.Hydrate(UpstreamHealthSnapshot{
				KeyID: 1, Status: state, ObservationEnabled: true,
				SuspensionSource: "probe", RecoverySamples: 1,
			})
			now := time.Date(2026, 11, 17, 4, 0, 0, 0, time.UTC)
			for i := 1; i <= 3; i++ {
				now = now.Add(time.Second)
				item := registry.RecordTrafficFailureTransition(1, "500", "upstream_server_error", now).Current
				require.Equal(t, UpstreamHealthSuspended, item.Status)
				require.Zero(t, item.RecoverySamples)
				require.Equal(t, i, item.ConsecutiveFails)
				if i < 3 {
					require.Equal(t, "probe", item.SuspensionSource)
				} else {
					require.Equal(t, "traffic", item.SuspensionSource)
				}
				require.Contains(t, registry.ExcludedKeyIDs([]int64{1}), int64(1))
			}
			settings := DefaultUpstreamProbeGuardSettings()
			settings.Enabled = false
			registry.ReevaluateProbeGuard(settings, now.Add(time.Second))
			require.Equal(t, UpstreamHealthSuspended, registry.Snapshot(1).Status)
			require.Equal(t, "traffic", registry.Snapshot(1).SuspensionSource)
			require.Contains(t, registry.ExcludedKeyIDs([]int64{1}), int64(1))
		})
	}
}

func TestProbeGuardExplicitReleasePreservesTrafficIsolation(t *testing.T) {
	now := time.Date(2026, 11, 17, 4, 0, 0, 0, time.UTC)
	for _, source := range []string{"probe", "traffic"} {
		for _, release := range []string{"disabled_failure", "reconfigure", "manual"} {
			t.Run(source+"/"+release, func(t *testing.T) {
				registry := &UpstreamHealthRegistry{items: make(map[int64]UpstreamHealthSnapshot)}
				registry.Hydrate(UpstreamHealthSnapshot{
					KeyID: 1, Status: UpstreamHealthSuspended, ObservationEnabled: true,
					SuspensionSource: source, LastProbeStatus: "401", Reason: "authentication_failed",
				})
				settings := DefaultUpstreamProbeGuardSettings()
				settings.Enabled = false
				switch release {
				case "disabled_failure":
					registry.RecordProbeFailureWithGuardTransition(1, "500", "upstream_server_error", nil, now, settings)
				case "reconfigure":
					registry.ReevaluateProbeGuard(settings, now)
				case "manual":
					_, reset := registry.ResetProbeSuspension(1, now)
					require.True(t, reset)
				}
				item := registry.Snapshot(1)
				if source == "probe" || release == "manual" {
					require.Equal(t, UpstreamHealthDegraded, item.Status)
					require.Empty(t, item.SuspensionSource)
				} else {
					require.Equal(t, UpstreamHealthSuspended, item.Status)
					require.Equal(t, "traffic", item.SuspensionSource)
				}
			})
		}
	}
}
