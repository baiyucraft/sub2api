package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type gatewayCapacityFailoverSettingRepo struct {
	value  string
	getErr error
	setErr error
}

func (r *gatewayCapacityFailoverSettingRepo) Get(context.Context, string) (*Setting, error) {
	if r.getErr != nil {
		return nil, r.getErr
	}
	if r.value == "" {
		return nil, ErrSettingNotFound
	}
	return &Setting{Key: SettingKeyGatewayCapacityFailoverSettings, Value: r.value}, nil
}

func (r *gatewayCapacityFailoverSettingRepo) GetValue(context.Context, string) (string, error) {
	if r.getErr != nil {
		return "", r.getErr
	}
	if r.value == "" {
		return "", ErrSettingNotFound
	}
	return r.value, nil
}

func (r *gatewayCapacityFailoverSettingRepo) Set(_ context.Context, _ string, value string) error {
	if r.setErr != nil {
		return r.setErr
	}
	r.value = value
	return nil
}

func (r *gatewayCapacityFailoverSettingRepo) GetMultiple(context.Context, []string) (map[string]string, error) {
	return map[string]string{}, nil
}
func (r *gatewayCapacityFailoverSettingRepo) SetMultiple(context.Context, map[string]string) error {
	return nil
}
func (r *gatewayCapacityFailoverSettingRepo) GetAll(context.Context) (map[string]string, error) {
	return map[string]string{}, nil
}
func (r *gatewayCapacityFailoverSettingRepo) Delete(context.Context, string) error {
	r.value = ""
	return nil
}

func gatewayCapacityFallbackConfig() *config.Config {
	return &config.Config{Gateway: config.GatewayConfig{Scheduling: config.GatewaySchedulingConfig{
		CapacityFailoverEnabled:             true,
		CapacityFailoverMaxSwitches:         7,
		CapacityFailoverExhaustedStatusCode: 429,
	}}}
}

func TestGatewayCapacityFailoverSettingsFallbackAndOverride(t *testing.T) {
	repo := &gatewayCapacityFailoverSettingRepo{}
	svc := NewSettingService(repo, gatewayCapacityFallbackConfig())

	fallback, err := svc.GetGatewayCapacityFailoverSettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, GatewayCapacityFailoverSettings{Enabled: true, MaxSwitches: 7, ExhaustedStatusCode: 429}, *fallback)

	repo.value = `{"enabled":false,"max_switches":0,"exhausted_status_code":599}`
	override, err := svc.GetGatewayCapacityFailoverSettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, GatewayCapacityFailoverSettings{Enabled: false, MaxSwitches: 0, ExhaustedStatusCode: 599}, *override)
	require.Equal(t, *override, svc.WarmGatewayCapacityFailoverSettings(context.Background()))
}

func TestGatewayCapacityFailoverSettingsDecodeMissingSwitchesUsesDefault(t *testing.T) {
	repo := &gatewayCapacityFailoverSettingRepo{
		value: `{"enabled":true,"exhausted_status_code":429}`,
	}
	svc := NewSettingService(repo, gatewayCapacityFallbackConfig())

	settings, err := svc.GetGatewayCapacityFailoverSettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, GatewayCapacityFailoverSettings{
		Enabled: true, MaxSwitches: 10, ExhaustedStatusCode: 429,
	}, *settings)
}

func TestGatewayCapacityFailoverSettingsDecodeExplicitZeroKeepsUnlimited(t *testing.T) {
	repo := &gatewayCapacityFailoverSettingRepo{
		value: `{"enabled":true,"max_switches":0,"exhausted_status_code":503}`,
	}
	svc := NewSettingService(repo, gatewayCapacityFallbackConfig())

	settings, err := svc.GetGatewayCapacityFailoverSettings(context.Background())
	require.NoError(t, err)
	require.Zero(t, settings.MaxSwitches)
}

func TestSetGatewayCapacityFailoverSettingsPublishesImmediately(t *testing.T) {
	repo := &gatewayCapacityFailoverSettingRepo{}
	svc := NewSettingService(repo, gatewayCapacityFallbackConfig())
	want := &GatewayCapacityFailoverSettings{Enabled: true, MaxSwitches: 2, ExhaustedStatusCode: 503}

	require.NoError(t, svc.SetGatewayCapacityFailoverSettings(context.Background(), want))
	require.JSONEq(t, `{"enabled":true,"max_switches":2,"exhausted_status_code":503}`, repo.value)
	require.Equal(t, *want, svc.GatewayCapacityFailoverSettingsSnapshot(context.Background()))
}

func TestGatewayCapacityFailoverSettingsValidation(t *testing.T) {
	svc := NewSettingService(&gatewayCapacityFailoverSettingRepo{}, nil)
	for _, settings := range []*GatewayCapacityFailoverSettings{
		{Enabled: true, MaxSwitches: -1, ExhaustedStatusCode: 503},
		{Enabled: true, MaxSwitches: 1001, ExhaustedStatusCode: 503},
		{Enabled: true, MaxSwitches: 3, ExhaustedStatusCode: 399},
		{Enabled: true, MaxSwitches: 3, ExhaustedStatusCode: 600},
	} {
		require.Error(t, svc.SetGatewayCapacityFailoverSettings(context.Background(), settings))
	}
	for _, status := range []int{400, 429, 503, 599} {
		require.NoError(t, svc.SetGatewayCapacityFailoverSettings(context.Background(), &GatewayCapacityFailoverSettings{
			Enabled: true, MaxSwitches: 1000, ExhaustedStatusCode: status,
		}))
	}
	for _, maxSwitches := range []int{0, 1000} {
		require.NoError(t, svc.SetGatewayCapacityFailoverSettings(context.Background(), &GatewayCapacityFailoverSettings{
			Enabled: true, MaxSwitches: maxSwitches, ExhaustedStatusCode: 503,
		}))
	}
}

func TestGatewayCapacityFailoverRefreshKeepsLastSnapshotOnRepositoryError(t *testing.T) {
	repo := &gatewayCapacityFailoverSettingRepo{}
	svc := NewSettingService(repo, gatewayCapacityFallbackConfig())
	last := GatewayCapacityFailoverSettings{Enabled: false, MaxSwitches: 1, ExhaustedStatusCode: 503}
	svc.storeGatewayCapacityFailoverSettings(last, -time.Second)
	repo.getErr = errors.New("database unavailable")

	svc.refreshGatewayCapacityFailoverSettings(context.Background())
	require.Equal(t, last, svc.GatewayCapacityFailoverSettingsSnapshot(context.Background()))
}

func TestGatewayCapacityFailoverRefreshKeepsLastSnapshotOnInvalidJSON(t *testing.T) {
	repo := &gatewayCapacityFailoverSettingRepo{value: `{invalid`}
	svc := NewSettingService(repo, gatewayCapacityFallbackConfig())
	last := GatewayCapacityFailoverSettings{Enabled: false, MaxSwitches: 1, ExhaustedStatusCode: 503}
	svc.storeGatewayCapacityFailoverSettings(last, -time.Second)

	svc.refreshGatewayCapacityFailoverSettings(context.Background())
	require.Equal(t, last, svc.GatewayCapacityFailoverSettingsSnapshot(context.Background()))
}

func TestGatewayCapacityFailoverWriteFailureDoesNotPublish(t *testing.T) {
	repo := &gatewayCapacityFailoverSettingRepo{setErr: errors.New("write failed")}
	svc := NewSettingService(repo, gatewayCapacityFallbackConfig())
	previous := svc.WarmGatewayCapacityFailoverSettings(context.Background())

	err := svc.SetGatewayCapacityFailoverSettings(context.Background(), &GatewayCapacityFailoverSettings{
		Enabled: false, MaxSwitches: 1, ExhaustedStatusCode: 503,
	})
	require.Error(t, err)
	require.Equal(t, previous, svc.GatewayCapacityFailoverSettingsSnapshot(context.Background()))
}
