//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type gpt61LongContextRepoStub struct {
	values    map[string]string
	getErr    error
	setValues map[string]string
}

func (r *gpt61LongContextRepoStub) Get(context.Context, string) (*Setting, error) {
	return nil, ErrSettingNotFound
}

func (r *gpt61LongContextRepoStub) GetValue(_ context.Context, key string) (string, error) {
	if r.getErr != nil {
		return "", r.getErr
	}
	value, ok := r.values[key]
	if !ok {
		return "", ErrSettingNotFound
	}
	return value, nil
}

func (r *gpt61LongContextRepoStub) Set(context.Context, string, string) error { return nil }

func (r *gpt61LongContextRepoStub) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	if r.getErr != nil {
		return nil, r.getErr
	}
	result := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, ok := r.values[key]; ok {
			result[key] = value
		}
	}
	return result, nil
}

func (r *gpt61LongContextRepoStub) SetMultiple(_ context.Context, values map[string]string) error {
	r.setValues = make(map[string]string, len(values))
	if r.values == nil {
		r.values = make(map[string]string)
	}
	for key, value := range values {
		r.setValues[key] = value
		r.values[key] = value
	}
	return nil
}

func (r *gpt61LongContextRepoStub) GetAll(context.Context) (map[string]string, error) {
	result := make(map[string]string, len(r.values))
	for key, value := range r.values {
		result[key] = value
	}
	return result, nil
}

func (r *gpt61LongContextRepoStub) Delete(context.Context, string) error { return nil }

func resetGPT61LongContextSettingsCacheForTest(t *testing.T) {
	t.Helper()
	previous := gatewayForwardingCache.Load()
	gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{})
	gatewayForwardingSF.Forget("gateway_forwarding")
	t.Cleanup(func() {
		gatewayForwardingSF.Forget("gateway_forwarding")
		if previous != nil {
			gatewayForwardingCache.Store(previous)
		} else {
			gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{})
		}
	})
}

func TestParseSettingsGPT61SolLongContextSwitchDefaultsOffAndRejectsInvalid(t *testing.T) {
	svc := &SettingService{cfg: &config.Config{}}
	require.False(t, svc.parseSettings(map[string]string{}).OpenAIGPT61SolNewLongContextEnabled)
	require.True(t, svc.parseSettings(map[string]string{
		SettingKeyOpenAIGPT61SolNewLongContextEnabled: "true",
	}).OpenAIGPT61SolNewLongContextEnabled)
	for _, raw := range []string{"false", "TRUE", "1", "yes", ""} {
		require.False(t, svc.parseSettings(map[string]string{
			SettingKeyOpenAIGPT61SolNewLongContextEnabled: raw,
		}).OpenAIGPT61SolNewLongContextEnabled, raw)
	}
}

func TestUpdateSettingsGPT61SolLongContextPersistsAndRefreshesCache(t *testing.T) {
	resetGPT61LongContextSettingsCacheForTest(t)
	repo := &gpt61LongContextRepoStub{values: map[string]string{}}
	svc := &SettingService{settingRepo: repo, cfg: &config.Config{}}
	ctx := context.Background()

	require.NoError(t, svc.UpdateSettings(ctx, &SystemSettings{
		OpenAIGPT61SolNewLongContextEnabled: true,
	}))
	require.Equal(t, "true", repo.setValues[SettingKeyOpenAIGPT61SolNewLongContextEnabled])
	require.True(t, svc.IsOpenAIGPT61SolNewLongContextEnabled(ctx))
	stored, err := svc.GetAllSettings(ctx)
	require.NoError(t, err)
	require.True(t, stored.OpenAIGPT61SolNewLongContextEnabled)

	// A subsequent update with the switch disabled must publish the new value
	// immediately, without waiting for the 60-second cache TTL.
	require.NoError(t, svc.UpdateSettings(ctx, &SystemSettings{}))
	require.Equal(t, "false", repo.setValues[SettingKeyOpenAIGPT61SolNewLongContextEnabled])
	require.False(t, svc.IsOpenAIGPT61SolNewLongContextEnabled(ctx))
	stored, err = svc.GetAllSettings(ctx)
	require.NoError(t, err)
	require.False(t, stored.OpenAIGPT61SolNewLongContextEnabled)
}

func TestGetGPT61SolLongContextSwitchFailedReadAndNilServiceDefaultOff(t *testing.T) {
	resetGPT61LongContextSettingsCacheForTest(t)
	ctx := context.Background()
	require.False(t, (*SettingService)(nil).IsOpenAIGPT61SolNewLongContextEnabled(ctx))

	previous := &cachedGatewayForwardingSettings{
		openAIGPT61SolNewLongContextEnabled: true,
		expiresAt:                           time.Now().Add(time.Minute).UnixNano(),
	}
	gatewayForwardingCache.Store(previous)
	require.False(t, (&SettingService{}).IsOpenAIGPT61SolNewLongContextEnabled(ctx))

	// Expire the cached value before a failing first read. A storage error must
	// fail closed instead of reusing a stale switch value.
	gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{expiresAt: 0})
	svc := &SettingService{settingRepo: &gpt61LongContextRepoStub{getErr: errors.New("db unavailable")}}
	require.False(t, svc.IsOpenAIGPT61SolNewLongContextEnabled(ctx))
}

func TestGetGPT61SolLongContextSwitchReadsStrictValueAndExpires(t *testing.T) {
	for _, raw := range []string{"", "false", "TRUE", "yes", "true"} {
		t.Run(raw, func(t *testing.T) {
			resetGPT61LongContextSettingsCacheForTest(t)
			repo := &gpt61LongContextRepoStub{values: map[string]string{
				SettingKeyOpenAIGPT61SolNewLongContextEnabled: raw,
			}}
			svc := &SettingService{settingRepo: repo}
			ctx := context.Background()
			require.Equal(t, raw == "true", svc.IsOpenAIGPT61SolNewLongContextEnabled(ctx))
			cached := gatewayForwardingCache.Load().(*cachedGatewayForwardingSettings)
			require.InDelta(t, 60, time.Until(time.Unix(0, cached.expiresAt)).Seconds(), 1)
			repo.values[SettingKeyOpenAIGPT61SolNewLongContextEnabled] = "false"
			require.Equal(t, raw == "true", svc.IsOpenAIGPT61SolNewLongContextEnabled(ctx), "another instance remains cached until TTL")
			expired := *cached
			expired.expiresAt = 0
			gatewayForwardingCache.Store(&expired)
			require.False(t, svc.IsOpenAIGPT61SolNewLongContextEnabled(ctx))
		})
	}
}

func TestInitializeDefaultSettingsGPT61SolLongContextDisabled(t *testing.T) {
	repo := &gpt61LongContextRepoStub{}
	svc := NewSettingService(repo, &config.Config{})
	require.NoError(t, svc.InitializeDefaultSettings(context.Background()))
	require.Equal(t, "false", repo.values[SettingKeyOpenAIGPT61SolNewLongContextEnabled])
}
