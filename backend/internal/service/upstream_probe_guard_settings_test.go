package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestUpstreamProbeGuardSettingsDefaultsAndNormalization(t *testing.T) {
	settings, err := NormalizeUpstreamProbeGuardSettings(UpstreamProbeGuardSettings{Enabled: true, CustomErrorCodes: []int{404, 429, 404}})
	require.NoError(t, err)
	require.Equal(t, 3, settings.SuspendAfterFailures)
	require.Equal(t, 3, settings.RecoverySuccesses)
	require.Equal(t, []int{404, 429}, settings.CustomErrorCodes)

	_, err = NormalizeUpstreamProbeGuardSettings(UpstreamProbeGuardSettings{SuspendAfterFailures: 21, RecoverySuccesses: 3})
	require.Error(t, err)
}

func TestProbeGuardDefault404DegradesWithoutSuspending(t *testing.T) {
	registry := &UpstreamHealthRegistry{items: make(map[int64]UpstreamHealthSnapshot)}
	settings := DefaultUpstreamProbeGuardSettings()
	for i := 0; i < 5; i++ {
		transition := registry.RecordProbeFailureWithGuardTransition(1, "404", "upstream_http_error", nil, time.Now(), settings)
		require.Equal(t, UpstreamHealthDegraded, transition.Current.Status)
		require.Zero(t, transition.Current.ConsecutiveFails)
	}
}

func TestProbeGuardCustom404UsesFailureThreshold(t *testing.T) {
	registry := &UpstreamHealthRegistry{items: make(map[int64]UpstreamHealthSnapshot)}
	settings := DefaultUpstreamProbeGuardSettings()
	settings.CustomErrorCodesEnabled = true
	settings.CustomErrorCodes = []int{404}
	for i := 1; i <= 2; i++ {
		transition := registry.RecordProbeFailureWithGuardTransition(1, "404", "upstream_http_error", nil, time.Now(), settings)
		require.Equal(t, UpstreamHealthDegraded, transition.Current.Status)
		require.Equal(t, i, transition.Current.ConsecutiveFails)
	}
	transition := registry.RecordProbeFailureWithGuardTransition(1, "404", "upstream_http_error", nil, time.Now(), settings)
	require.Equal(t, UpstreamHealthSuspended, transition.Current.Status)
	require.Equal(t, "probe", transition.Current.SuspensionSource)
}

func TestProbeGuardCapacityCodesAreOnlyThresholdedWhenCustom(t *testing.T) {
	registry := &UpstreamHealthRegistry{items: make(map[int64]UpstreamHealthSnapshot)}
	defaultSettings := DefaultUpstreamProbeGuardSettings()
	for _, code := range []string{"429", "529"} {
		transition := registry.RecordProbeFailureWithGuardTransition(1, code, "capacity_limited", nil, time.Now(), defaultSettings)
		require.Equal(t, UpstreamHealthDegraded, transition.Current.Status)
		require.Zero(t, transition.Current.ConsecutiveFails)
	}

	custom := defaultSettings
	custom.CustomErrorCodesEnabled = true
	custom.CustomErrorCodes = []int{429, 529}
	for i := 1; i <= 3; i++ {
		transition := registry.RecordProbeFailureWithGuardTransition(2, "429", "capacity_limited", nil, time.Now(), custom)
		if i < 3 {
			require.Equal(t, UpstreamHealthDegraded, transition.Current.Status)
		} else {
			require.Equal(t, UpstreamHealthSuspended, transition.Current.Status)
		}
	}
}

func TestProbeGuardAuthenticationAndGateway403Classification(t *testing.T) {
	registry := &UpstreamHealthRegistry{items: make(map[int64]UpstreamHealthSnapshot)}
	settings := DefaultUpstreamProbeGuardSettings()
	auth := registry.RecordProbeFailureWithGuardTransition(1, "403", "authentication_failed", nil, time.Now(), settings)
	require.Equal(t, UpstreamHealthSuspended, auth.Current.Status)
	require.Equal(t, 1, auth.Current.ConsecutiveFails)

	gateway := registry.RecordProbeFailureWithGuardTransition(2, "403", "gateway_intercepted", nil, time.Now(), settings)
	require.Equal(t, UpstreamHealthDegraded, gateway.Current.Status)
	require.Zero(t, gateway.Current.ConsecutiveFails)
}

func TestProbeGuardDoesNotCountProtocolOrCapabilityFailures(t *testing.T) {
	registry := &UpstreamHealthRegistry{items: make(map[int64]UpstreamHealthSnapshot)}
	settings := DefaultUpstreamProbeGuardSettings()
	settings.SuspendAfterFailures = 1
	for _, tc := range []struct {
		status string
		reason string
	}{
		{status: "invalid_response", reason: "probe_response_mismatch"},
		{status: "invalid_response", reason: "probe_protocol_mismatch"},
		{status: "incomplete", reason: "probe_incomplete_stream"},
		{status: "incomplete", reason: "probe_response_incomplete"},
		{status: "unsupported_model", reason: "probe_model_unsupported"},
		{status: "configuration_error", reason: "probe_base_url_invalid"},
		{status: "unavailable", reason: "probe_transport_unavailable"},
		{status: "challenge_error", reason: "probe_challenge_error"},
		{status: "unsupported_account", reason: "probe_account_unsupported"},
		{status: "cancelled", reason: "probe_cancelled"},
		{status: "invalid_response", reason: "probe_response_empty"},
		{status: "quality_degraded", reason: "probe_quality_degraded"},
	} {
		transition := registry.RecordProbeFailureWithGuardTransition(1, tc.status, tc.reason, nil, time.Now(), settings)
		require.Equal(t, UpstreamHealthDegraded, transition.Current.Status, tc.reason)
		require.Zero(t, transition.Current.ConsecutiveFails, tc.reason)
	}
}

func TestProbeGuardCountsExplicitUpstreamResponseErrors(t *testing.T) {
	registry := &UpstreamHealthRegistry{items: make(map[int64]UpstreamHealthSnapshot)}
	settings := DefaultUpstreamProbeGuardSettings()
	settings.SuspendAfterFailures = 1

	transition := registry.RecordProbeFailureWithGuardTransition(1, "failed", "probe_response_failed", nil, time.Now(), settings)

	require.Equal(t, UpstreamHealthSuspended, transition.Current.Status)
	require.Equal(t, 1, transition.Current.ConsecutiveFails)
}

func TestProbeGuardDisabledKeepsEvidenceWithoutSchedulingSuspension(t *testing.T) {
	registry := &UpstreamHealthRegistry{items: make(map[int64]UpstreamHealthSnapshot)}
	settings := DefaultUpstreamProbeGuardSettings()
	settings.Enabled = false
	for i := 0; i < 5; i++ {
		transition := registry.RecordProbeFailureWithGuardTransition(1, "503", "upstream_server_error", nil, time.Now(), settings)
		require.NotEqual(t, UpstreamHealthSuspended, transition.Current.Status)
		require.Zero(t, transition.Current.ConsecutiveFails)
		require.Equal(t, "503", transition.Current.LastProbeStatus)
	}
}

func TestProbeGuardRecoveryUsesConfiguredSuccessCountAndReevaluation(t *testing.T) {
	registry := &UpstreamHealthRegistry{items: make(map[int64]UpstreamHealthSnapshot)}
	settings := DefaultUpstreamProbeGuardSettings()
	settings.CustomErrorCodesEnabled = true
	settings.CustomErrorCodes = []int{404}
	for i := 0; i < 3; i++ {
		registry.RecordProbeFailureWithGuardTransition(1, "404", "upstream_http_error", nil, time.Now(), settings)
	}
	require.Equal(t, UpstreamHealthSuspended, registry.Snapshot(1).Status)

	settings.CustomErrorCodes = nil
	registry.ReevaluateProbeGuard(settings, time.Now())
	require.Equal(t, UpstreamHealthDegraded, registry.Snapshot(1).Status)

	settings.CustomErrorCodesEnabled = true
	settings.CustomErrorCodes = []int{404}
	settings.RecoverySuccesses = 2
	for i := 0; i < settings.SuspendAfterFailures; i++ {
		registry.RecordProbeFailureWithGuardTransition(1, "404", "upstream_http_error", nil, time.Now(), settings)
	}
	for i := 1; i <= 2; i++ {
		transition := registry.RecordProbeWithGuardSuccessTransition(1, "success", "probe_succeeded", nil, time.Now(), settings)
		if i == 1 {
			require.Equal(t, UpstreamHealthRecovering, transition.Current.Status)
		} else {
			require.Equal(t, UpstreamHealthHealthy, transition.Current.Status)
		}
	}
}

func TestUpstreamProbeGuardSettingsPersistence(t *testing.T) {
	repo := &upstreamManagementSettingRepoStub{values: map[string]string{}}
	service := NewSettingService(repo, nil)
	settings := DefaultUpstreamProbeGuardSettings()
	settings.CustomErrorCodesEnabled = true
	settings.CustomErrorCodes = []int{404, 429}
	require.NoError(t, service.SetUpstreamProbeGuardSettings(context.Background(), settings))
	got, err := service.GetUpstreamProbeGuardSettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, settings, got)
}

func TestLooksLikeGatewayIntercepted(t *testing.T) {
	require.True(t, looksLikeGatewayIntercepted([]byte("<html><title>Access denied</title></html>"), "text/html"))
	require.False(t, looksLikeGatewayIntercepted([]byte(`{"error":"forbidden"}`), "application/json"))
}
