package service

import (
	"context"
	"time"
)

const (
	sessionSwitchSettingsCacheTTL   = 60 * time.Second
	sessionSwitchSettingsErrorTTL   = 5 * time.Second
	sessionSwitchSettingsDBTimeout  = 5 * time.Second
	sessionSwitchSettingsRefreshKey = "session_switch_settings"
)

type cachedSessionSwitchSettings struct {
	settings  SessionSwitchSettings
	available bool
	expiresAt int64
}

// GetSessionSwitchSettingsSnapshot is the gateway hot-path reader. It never
// waits for the settings repository: stale snapshots trigger one background
// refresh, and an unavailable snapshot fails open to cache-first behavior.
func (s *SettingService) GetSessionSwitchSettingsSnapshot(ctx context.Context) (SessionSwitchSettings, bool) {
	if s == nil {
		return SessionSwitchSettings{}, false
	}
	cached, _ := s.sessionSwitchSettingsCache.Load().(*cachedSessionSwitchSettings)
	now := time.Now().UnixNano()
	if cached != nil && now < cached.expiresAt {
		return cloneSessionSwitchSettings(cached.settings), cached.available
	}
	s.sessionSwitchSettingsSF.DoChan(sessionSwitchSettingsRefreshKey, func() (any, error) {
		s.refreshSessionSwitchSettings(context.Background())
		return nil, nil
	})
	if cached == nil {
		return SessionSwitchSettings{}, false
	}
	return cloneSessionSwitchSettings(cached.settings), cached.available
}

// WarmSessionSwitchSettings performs the bounded startup/test load. Errors are
// represented as an unavailable snapshot so runtime routing remains fail-open.
func (s *SettingService) WarmSessionSwitchSettings(ctx context.Context) (SessionSwitchSettings, bool) {
	if s == nil {
		return SessionSwitchSettings{}, false
	}
	s.refreshSessionSwitchSettings(ctx)
	cached, _ := s.sessionSwitchSettingsCache.Load().(*cachedSessionSwitchSettings)
	if cached == nil {
		return SessionSwitchSettings{}, false
	}
	return cloneSessionSwitchSettings(cached.settings), cached.available
}

func (s *SettingService) refreshSessionSwitchSettings(ctx context.Context) {
	if s == nil {
		return
	}
	generation := s.sessionSwitchSettingsGen.Load()
	if s.settingRepo == nil {
		s.storeSessionSwitchRefreshSnapshot(SessionSwitchSettings{}, false, sessionSwitchSettingsErrorTTL, generation)
		return
	}
	dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sessionSwitchSettingsDBTimeout)
	defer cancel()
	settings, err := s.GetSessionSwitchSettings(dbCtx)
	if err != nil {
		s.storeSessionSwitchRefreshSnapshot(SessionSwitchSettings{}, false, sessionSwitchSettingsErrorTTL, generation)
		return
	}
	s.storeSessionSwitchRefreshSnapshot(settings, true, sessionSwitchSettingsCacheTTL, generation)
}

func (s *SettingService) publishSessionSwitchSettingsSnapshot(settings SessionSwitchSettings, available bool, ttl time.Duration) {
	if s == nil {
		return
	}
	s.sessionSwitchSettingsMu.Lock()
	defer s.sessionSwitchSettingsMu.Unlock()
	s.sessionSwitchSettingsGen.Add(1)
	s.storeSessionSwitchSettingsSnapshot(settings, available, ttl)
}

func (s *SettingService) storeSessionSwitchRefreshSnapshot(settings SessionSwitchSettings, available bool, ttl time.Duration, generation uint64) bool {
	if s == nil {
		return false
	}
	s.sessionSwitchSettingsMu.Lock()
	defer s.sessionSwitchSettingsMu.Unlock()
	if s.sessionSwitchSettingsGen.Load() != generation {
		return false
	}
	s.storeSessionSwitchSettingsSnapshot(settings, available, ttl)
	return true
}

func (s *SettingService) storeSessionSwitchSettingsSnapshot(settings SessionSwitchSettings, available bool, ttl time.Duration) {
	s.sessionSwitchSettingsCache.Store(&cachedSessionSwitchSettings{
		settings:  cloneSessionSwitchSettings(settings),
		available: available,
		expiresAt: time.Now().Add(ttl).UnixNano(),
	})
}

func cloneSessionSwitchSettings(settings SessionSwitchSettings) SessionSwitchSettings {
	settings.StatusCodes = append([]int(nil), settings.StatusCodes...)
	return settings
}
