package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type probeTempUnschedAccountRepo struct {
	AccountRepository
	accounts []Account
}

func (r *probeTempUnschedAccountRepo) ListByUpstreamKeyID(context.Context, int64) ([]Account, error) {
	return append([]Account(nil), r.accounts...), nil
}

func (r *probeTempUnschedAccountRepo) SetTempUnschedulable(_ context.Context, id int64, until time.Time, reason string) error {
	for i := range r.accounts {
		if r.accounts[i].ID == id {
			r.accounts[i].TempUnschedulableUntil = &until
			r.accounts[i].TempUnschedulableReason = reason
		}
	}
	return nil
}

func (r *probeTempUnschedAccountRepo) ClearTempUnschedulable(_ context.Context, id int64) error {
	for i := range r.accounts {
		if r.accounts[i].ID == id {
			r.accounts[i].TempUnschedulableUntil = nil
			r.accounts[i].TempUnschedulableReason = ""
		}
	}
	return nil
}

func TestUpstreamProbeSchedulingStateLinksAndClearsProbeBlock(t *testing.T) {
	now := time.Now().UTC()
	repo := &probeTempUnschedAccountRepo{accounts: []Account{{ID: 7}}}
	svc := &UpstreamConfigService{accountRepo: repo}

	suspended := UpstreamHealthSnapshot{
		KeyID:              9,
		Status:             UpstreamHealthSuspended,
		ObservationEnabled: true,
		SuspensionSource:   "probe",
		LastFailureClass:   "server",
	}
	require.NoError(t, svc.syncProbeSchedulingState(context.Background(), 9, suspended))
	require.NotNil(t, repo.accounts[0].TempUnschedulableUntil)
	require.True(t, repo.accounts[0].TempUnschedulableUntil.After(now))
	require.Equal(t, "upstream_probe:server", repo.accounts[0].TempUnschedulableReason)

	require.NoError(t, svc.syncProbeSchedulingState(context.Background(), 9, UpstreamHealthSnapshot{
		KeyID: 9, Status: UpstreamHealthHealthy, ObservationEnabled: true,
	}))
	require.Nil(t, repo.accounts[0].TempUnschedulableUntil)
	require.Empty(t, repo.accounts[0].TempUnschedulableReason)
}

func TestUpstreamProbeSchedulingStateDoesNotOverwriteOtherBlock(t *testing.T) {
	repo := &probeTempUnschedAccountRepo{accounts: []Account{{
		ID: 7, TempUnschedulableUntil: func() *time.Time { v := time.Now().Add(time.Hour); return &v }(),
		TempUnschedulableReason: "manual_maintenance",
	}}}
	svc := &UpstreamConfigService{accountRepo: repo}

	require.NoError(t, svc.syncProbeSchedulingState(context.Background(), 9, UpstreamHealthSnapshot{
		KeyID: 9, Status: UpstreamHealthSuspended, ObservationEnabled: true, SuspensionSource: "probe", LastFailureClass: "server",
	}))
	require.Equal(t, "manual_maintenance", repo.accounts[0].TempUnschedulableReason)

	require.NoError(t, svc.syncProbeSchedulingState(context.Background(), 9, UpstreamHealthSnapshot{
		KeyID: 9, Status: UpstreamHealthHealthy, ObservationEnabled: true,
	}))
	require.Equal(t, "manual_maintenance", repo.accounts[0].TempUnschedulableReason)
}

func TestUpstreamHealthRegistryResetProbeSuspension(t *testing.T) {
	registry := &UpstreamHealthRegistry{items: make(map[int64]UpstreamHealthSnapshot)}
	settings := DefaultUpstreamProbeGuardSettings()
	settings.SuspendAfterFailures = 1
	now := time.Now().UTC()
	transition := registry.RecordProbeFailureWithGuardTransition(9, "503", "upstream_server_error", nil, now, settings)
	require.Equal(t, UpstreamHealthSuspended, transition.Current.Status)

	reset, changed := registry.ResetProbeSuspension(9, now.Add(time.Minute))
	require.True(t, changed)
	require.Equal(t, UpstreamHealthDegraded, reset.Current.Status)
	require.Empty(t, reset.Current.SuspensionSource)
	require.Equal(t, "manual_recovery", reset.Current.Reason)
	require.Zero(t, reset.Current.ConsecutiveFails)
}
