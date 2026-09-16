package service

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type sessionSwitchSettingsCacheRepoStub struct {
	SettingRepository
	values   map[string]string
	getCalls atomic.Int64
	wait     <-chan struct{}
	err      error
}

func (r *sessionSwitchSettingsCacheRepoStub) GetMultiple(ctx context.Context, keys []string) (map[string]string, error) {
	r.getCalls.Add(1)
	if r.wait != nil {
		select {
		case <-r.wait:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if r.err != nil {
		return nil, r.err
	}
	values := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, ok := r.values[key]; ok {
			values[key] = value
		}
	}
	return values, nil
}

func TestSessionSwitchSettingsSnapshotUsesWarmCacheWithoutDBRead(t *testing.T) {
	repo := &sessionSwitchSettingsCacheRepoStub{values: map[string]string{
		SettingKeySessionSwitchWindowSeconds:    "120",
		SettingKeySessionSwitchFailureThreshold: "4",
		SettingKeySessionSwitchCooldownSeconds:  "600",
		SettingKeySessionSwitchStatusCodes:      `[502,503]`,
	}}
	svc := NewSettingService(repo, nil)

	warmed, available := svc.WarmSessionSwitchSettings(context.Background())
	require.True(t, available)
	require.Equal(t, 120, warmed.WindowSeconds)
	require.Equal(t, int64(1), repo.getCalls.Load())

	for range 5 {
		cached, ok := svc.GetSessionSwitchSettingsSnapshot(context.Background())
		require.True(t, ok)
		require.Equal(t, warmed, cached)
	}
	require.Equal(t, int64(1), repo.getCalls.Load(), "fresh hot-path reads must not query the settings repository")
}

func TestSessionSwitchSettingsSnapshotColdCacheIsNonBlocking(t *testing.T) {
	release := make(chan struct{})
	repo := &sessionSwitchSettingsCacheRepoStub{wait: release, values: map[string]string{}}
	svc := NewSettingService(repo, nil)
	t.Cleanup(func() { close(release) })

	start := time.Now()
	settings, available := svc.GetSessionSwitchSettingsSnapshot(context.Background())
	require.Less(t, time.Since(start), 50*time.Millisecond)
	require.False(t, available)
	require.Equal(t, SessionSwitchSettings{}, settings)
}

func TestSessionSwitchSettingsSnapshotFailureDisablesOptionalPolicy(t *testing.T) {
	repo := &sessionSwitchSettingsCacheRepoStub{err: errors.New("settings unavailable")}
	svc := NewSettingService(repo, nil)

	settings, available := svc.WarmSessionSwitchSettings(context.Background())
	require.False(t, available)
	require.Equal(t, SessionSwitchSettings{}, settings)

	cached, ok := svc.GetSessionSwitchSettingsSnapshot(context.Background())
	require.False(t, ok)
	require.Equal(t, SessionSwitchSettings{}, cached)
}

func TestSessionSwitchSettingsRefreshCannotOverwriteNewerPublishedSnapshot(t *testing.T) {
	svc := NewSettingService(&sessionSwitchSettingsCacheRepoStub{}, nil)
	oldSettings := DefaultSessionSwitchSettings()
	newSettings := oldSettings
	newSettings.FailureThreshold = 7
	generation := svc.sessionSwitchSettingsGen.Load()

	svc.publishSessionSwitchSettingsSnapshot(newSettings, true, sessionSwitchSettingsCacheTTL)
	require.False(t, svc.storeSessionSwitchRefreshSnapshot(oldSettings, true, sessionSwitchSettingsCacheTTL, generation))

	got, available := svc.GetSessionSwitchSettingsSnapshot(context.Background())
	require.True(t, available)
	require.Equal(t, 7, got.FailureThreshold)
}
