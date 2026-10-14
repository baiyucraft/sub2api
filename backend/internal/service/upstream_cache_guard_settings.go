package service

import (
	"context"
	"encoding/json"
	"errors"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/upstreamcacheguard"
)

const SettingKeyUpstreamCacheGuard = "upstream_cache_guard_settings"

type UpstreamCacheGuardSettings = upstreamcacheguard.Settings

func DefaultUpstreamCacheGuardSettings() UpstreamCacheGuardSettings {
	return upstreamcacheguard.DefaultSettings()
}

func (s *SettingService) GetUpstreamCacheGuardSettings(ctx context.Context) (UpstreamCacheGuardSettings, error) {
	defaults := DefaultUpstreamCacheGuardSettings()
	if s == nil || s.settingRepo == nil {
		return defaults, nil
	}
	raw, err := s.settingRepo.GetValue(ctx, SettingKeyUpstreamCacheGuard)
	if errors.Is(err, ErrSettingNotFound) {
		return defaults, nil
	}
	if err != nil {
		return defaults, err
	}
	var settings UpstreamCacheGuardSettings
	if json.Unmarshal([]byte(raw), &settings) != nil {
		return defaults, nil
	}
	if settings.Priority == "" {
		settings.Priority = "cache_first"
	}
	if settings.Validate() != nil {
		return defaults, nil
	}
	return settings, nil
}

func validateUpstreamCacheGuardSettings(settings *UpstreamCacheGuardSettings) error {
	if settings == nil {
		return nil
	}
	if err := settings.Validate(); err != nil {
		return infraerrors.BadRequest("INVALID_UPSTREAM_CACHE_GUARD_SETTINGS", err.Error())
	}
	return nil
}

func (s *SettingService) SetUpstreamCacheGuardSettings(ctx context.Context, settings UpstreamCacheGuardSettings) error {
	if err := validateUpstreamCacheGuardSettings(&settings); err != nil {
		return err
	}
	if s == nil || s.settingRepo == nil {
		return infraerrors.ServiceUnavailable("UPSTREAM_CACHE_GUARD_UNAVAILABLE", "upstream cache guard settings are unavailable")
	}
	encoded, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	if err := s.settingRepo.Set(ctx, SettingKeyUpstreamCacheGuard, string(encoded)); err != nil {
		return err
	}
	if runner := currentUpstreamCacheGuardRunner(); runner != nil {
		runner.applySettings(settings)
	}
	_ = s.notifyOpenAITTFTGuardGlobalUpdate(ctx)
	return nil
}
