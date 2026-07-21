package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type openAITTFTGuardSettingsRepoStub struct {
	mu      sync.Mutex
	values  map[string]string
	getErr  error
	setErr  error
	getGate <-chan struct{}
}

func (r *openAITTFTGuardSettingsRepoStub) Get(context.Context, string) (*Setting, error) {
	return nil, ErrSettingNotFound
}

func (r *openAITTFTGuardSettingsRepoStub) GetValue(ctx context.Context, key string) (string, error) {
	if r.getGate != nil {
		select {
		case <-r.getGate:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.getErr != nil {
		return "", r.getErr
	}
	value, ok := r.values[key]
	if !ok {
		return "", ErrSettingNotFound
	}
	return value, nil
}

func (r *openAITTFTGuardSettingsRepoStub) Set(_ context.Context, key, value string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.setErr != nil {
		return r.setErr
	}
	if r.values == nil {
		r.values = map[string]string{}
	}
	r.values[key] = value
	return nil
}

func (r *openAITTFTGuardSettingsRepoStub) GetMultiple(context.Context, []string) (map[string]string, error) {
	return nil, errors.New("unexpected GetMultiple call")
}

func (r *openAITTFTGuardSettingsRepoStub) SetMultiple(context.Context, map[string]string) error {
	return errors.New("unexpected SetMultiple call")
}

func (r *openAITTFTGuardSettingsRepoStub) GetAll(context.Context) (map[string]string, error) {
	return nil, errors.New("unexpected GetAll call")
}

func (r *openAITTFTGuardSettingsRepoStub) Delete(context.Context, string) error {
	return errors.New("unexpected Delete call")
}

func TestOpenAITTFTGuardSettingsDefaults(t *testing.T) {
	repo := &openAITTFTGuardSettingsRepoStub{values: map[string]string{}}
	svc := NewSettingService(repo, nil)

	settings, err := svc.GetOpenAITTFTGuardSettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, &OpenAITTFTGuardSettings{
		Enabled:                false,
		DegradationTTFTSeconds: 20,
		MinSamples:             5,
	}, settings)

	snapshot := svc.WarmOpenAITTFTGuardConfig(context.Background())
	require.False(t, snapshot.Enabled)
	require.Equal(t, 20*time.Second, snapshot.Threshold)
	require.Equal(t, 5, snapshot.MinSamples)
}

func TestSetOpenAITTFTGuardSettingsValidatesBounds(t *testing.T) {
	svc := NewSettingService(&openAITTFTGuardSettingsRepoStub{values: map[string]string{}}, nil)

	tests := []OpenAITTFTGuardSettings{
		{Enabled: true, DegradationTTFTSeconds: 4, MinSamples: 5},
		{Enabled: true, DegradationTTFTSeconds: 301, MinSamples: 5},
		{Enabled: true, DegradationTTFTSeconds: 20, MinSamples: 1},
		{Enabled: true, DegradationTTFTSeconds: 20, MinSamples: 21},
	}
	for _, settings := range tests {
		require.Error(t, svc.SetOpenAITTFTGuardSettings(context.Background(), &settings))
	}
}

func TestSetOpenAITTFTGuardSettingsPersistsAndPublishesImmediately(t *testing.T) {
	repo := &openAITTFTGuardSettingsRepoStub{values: map[string]string{}}
	svc := NewSettingService(repo, nil)
	want := &OpenAITTFTGuardSettings{Enabled: true, DegradationTTFTSeconds: 45, MinSamples: 8}

	require.NoError(t, svc.SetOpenAITTFTGuardSettings(context.Background(), want))
	repo.mu.Lock()
	raw := repo.values[SettingKeyOpenAITTFTGuardSettings]
	repo.mu.Unlock()
	require.JSONEq(t, `{"enabled":true,"degradation_ttft_seconds":45,"min_samples":8}`, raw)

	snapshot := svc.OpenAITTFTGuardConfigSnapshot()
	require.True(t, snapshot.Enabled)
	require.Equal(t, 45*time.Second, snapshot.Threshold)
	require.Equal(t, 8, snapshot.MinSamples)
}

func TestOpenAITTFTGuardRuntimeRefreshFailureDisablesGuard(t *testing.T) {
	repo := &openAITTFTGuardSettingsRepoStub{values: map[string]string{
		SettingKeyOpenAITTFTGuardSettings: `{"enabled":true,"degradation_ttft_seconds":30,"min_samples":6}`,
	}}
	svc := NewSettingService(repo, nil)
	require.True(t, svc.WarmOpenAITTFTGuardConfig(context.Background()).Enabled)

	repo.mu.Lock()
	repo.values[SettingKeyOpenAITTFTGuardSettings] = `{broken`
	repo.mu.Unlock()
	svc.refreshOpenAITTFTGuardConfig(context.Background())

	snapshot := svc.OpenAITTFTGuardConfigSnapshot()
	require.False(t, snapshot.Enabled)
	require.Equal(t, 20*time.Second, snapshot.Threshold)
	require.Equal(t, 5, snapshot.MinSamples)

	repo.mu.Lock()
	repo.values[SettingKeyOpenAITTFTGuardSettings] = `{"enabled":true,"degradation_ttft_seconds":30,"min_samples":6}`
	repo.getErr = errors.New("read failed")
	repo.mu.Unlock()
	svc.refreshOpenAITTFTGuardConfig(context.Background())
	require.False(t, svc.OpenAITTFTGuardConfigSnapshot().Enabled)
}

func TestOpenAITTFTGuardRuntimeColdReadDoesNotBlockOnDB(t *testing.T) {
	gate := make(chan struct{})
	repo := &openAITTFTGuardSettingsRepoStub{
		values: map[string]string{
			SettingKeyOpenAITTFTGuardSettings: `{"enabled":true,"degradation_ttft_seconds":25,"min_samples":4}`,
		},
		getGate: gate,
	}
	svc := NewSettingService(repo, nil)

	started := time.Now()
	snapshot := svc.OpenAITTFTGuardConfigSnapshot()
	require.Less(t, time.Since(started), 100*time.Millisecond)
	require.False(t, snapshot.Enabled)
	require.Equal(t, 20*time.Second, snapshot.Threshold)
	close(gate)

	require.Eventually(t, func() bool {
		return svc.OpenAITTFTGuardConfigSnapshot().Enabled
	}, time.Second, 10*time.Millisecond)
}

func TestSetOpenAITTFTGuardSettingsDoesNotPublishFailedWrite(t *testing.T) {
	repo := &openAITTFTGuardSettingsRepoStub{
		values: map[string]string{
			SettingKeyOpenAITTFTGuardSettings: `{"enabled":false,"degradation_ttft_seconds":20,"min_samples":5}`,
		},
		setErr: errors.New("write failed"),
	}
	svc := NewSettingService(repo, nil)
	require.False(t, svc.WarmOpenAITTFTGuardConfig(context.Background()).Enabled)

	err := svc.SetOpenAITTFTGuardSettings(context.Background(), &OpenAITTFTGuardSettings{
		Enabled: true, DegradationTTFTSeconds: 40, MinSamples: 7,
	})
	require.Error(t, err)
	require.False(t, svc.OpenAITTFTGuardConfigSnapshot().Enabled)
}
