package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	defaultOpenAITTFTGuardDegradationSeconds = 20
	defaultOpenAITTFTGuardSettingsMinSamples = 5
	minOpenAITTFTGuardDegradationSeconds     = 5
	maxOpenAITTFTGuardDegradationSeconds     = 300
	minOpenAITTFTGuardSamples                = 2
	maxOpenAITTFTGuardSamples                = 20

	openAITTFTGuardConfigCacheTTL   = 60 * time.Second
	openAITTFTGuardConfigErrorTTL   = 5 * time.Second
	openAITTFTGuardConfigDBTimeout  = 5 * time.Second
	openAITTFTGuardConfigRefreshKey = "openai_ttft_guard_settings"
)

// OpenAITTFTGuardSettings is the persisted/admin-facing representation.
// Runtime consumers receive a duration-based OpenAITTFTGuardConfigSnapshot.
type OpenAITTFTGuardSettings struct {
	Enabled                bool `json:"enabled"`
	DegradationTTFTSeconds int  `json:"degradation_ttft_seconds"`
	MinSamples             int  `json:"min_samples"`
}

type cachedOpenAITTFTGuardConfig struct {
	snapshot  OpenAITTFTGuardConfigSnapshot
	expiresAt int64
}

func DefaultOpenAITTFTGuardSettings() *OpenAITTFTGuardSettings {
	return &OpenAITTFTGuardSettings{
		Enabled:                false,
		DegradationTTFTSeconds: defaultOpenAITTFTGuardDegradationSeconds,
		MinSamples:             defaultOpenAITTFTGuardSettingsMinSamples,
	}
}

func validateOpenAITTFTGuardSettings(settings *OpenAITTFTGuardSettings) error {
	if settings == nil {
		return infraerrors.BadRequest("INVALID_OPENAI_TTFT_GUARD_SETTINGS", "OpenAI TTFT guard settings are required")
	}
	if settings.DegradationTTFTSeconds < minOpenAITTFTGuardDegradationSeconds || settings.DegradationTTFTSeconds > maxOpenAITTFTGuardDegradationSeconds {
		return infraerrors.BadRequest(
			"INVALID_OPENAI_TTFT_GUARD_THRESHOLD",
			fmt.Sprintf("degradation_ttft_seconds must be between %d and %d", minOpenAITTFTGuardDegradationSeconds, maxOpenAITTFTGuardDegradationSeconds),
		)
	}
	if settings.MinSamples < minOpenAITTFTGuardSamples || settings.MinSamples > maxOpenAITTFTGuardSamples {
		return infraerrors.BadRequest(
			"INVALID_OPENAI_TTFT_GUARD_MIN_SAMPLES",
			fmt.Sprintf("min_samples must be between %d and %d", minOpenAITTFTGuardSamples, maxOpenAITTFTGuardSamples),
		)
	}
	return nil
}

func openAITTFTGuardSnapshot(settings *OpenAITTFTGuardSettings) OpenAITTFTGuardConfigSnapshot {
	if settings == nil {
		settings = DefaultOpenAITTFTGuardSettings()
	}
	return OpenAITTFTGuardConfigSnapshot{
		Enabled:    settings.Enabled,
		Threshold:  time.Duration(settings.DegradationTTFTSeconds) * time.Second,
		MinSamples: settings.MinSamples,
	}
}

func parseOpenAITTFTGuardSettings(raw string) (*OpenAITTFTGuardSettings, error) {
	if raw == "" {
		return DefaultOpenAITTFTGuardSettings(), nil
	}
	var settings OpenAITTFTGuardSettings
	if err := json.Unmarshal([]byte(raw), &settings); err != nil {
		return nil, fmt.Errorf("unmarshal OpenAI TTFT guard settings: %w", err)
	}
	if err := validateOpenAITTFTGuardSettings(&settings); err != nil {
		return nil, err
	}
	return &settings, nil
}

// GetOpenAITTFTGuardSettings reads the persisted admin settings. Missing or
// malformed data fails closed to the disabled defaults.
func (s *SettingService) GetOpenAITTFTGuardSettings(ctx context.Context) (*OpenAITTFTGuardSettings, error) {
	if s == nil || s.settingRepo == nil {
		return DefaultOpenAITTFTGuardSettings(), nil
	}
	revision := s.openAITTFTGuardRevision.Load()
	raw, err := s.settingRepo.GetValue(ctx, SettingKeyOpenAITTFTGuardSettings)
	if err != nil {
		if errors.Is(err, ErrSettingNotFound) {
			return DefaultOpenAITTFTGuardSettings(), nil
		}
		return nil, fmt.Errorf("get OpenAI TTFT guard settings: %w", err)
	}
	settings, parseErr := parseOpenAITTFTGuardSettings(raw)
	if parseErr != nil {
		slog.Warn("invalid OpenAI TTFT guard settings, using disabled defaults", "error", parseErr)
		return DefaultOpenAITTFTGuardSettings(), nil
	}
	if s.openAITTFTGuardRevision.Load() == revision {
		s.storeOpenAITTFTGuardSnapshot(openAITTFTGuardSnapshot(settings), openAITTFTGuardConfigCacheTTL)
	}
	return settings, nil
}

// SetOpenAITTFTGuardSettings persists a validated config and publishes it to
// the runtime snapshot immediately after the database write succeeds.
func (s *SettingService) SetOpenAITTFTGuardSettings(ctx context.Context, settings *OpenAITTFTGuardSettings) error {
	if err := validateOpenAITTFTGuardSettings(settings); err != nil {
		return err
	}
	if s == nil || s.settingRepo == nil {
		return fmt.Errorf("setting repository is unavailable")
	}
	s.openAITTFTGuardUpdateMu.Lock()
	defer s.openAITTFTGuardUpdateMu.Unlock()
	raw, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("marshal OpenAI TTFT guard settings: %w", err)
	}
	if err := s.settingRepo.Set(ctx, SettingKeyOpenAITTFTGuardSettings, string(raw)); err != nil {
		return fmt.Errorf("set OpenAI TTFT guard settings: %w", err)
	}
	s.openAITTFTGuardRevision.Add(1)
	s.storeOpenAITTFTGuardSnapshot(openAITTFTGuardSnapshot(settings), openAITTFTGuardConfigCacheTTL)
	return nil
}

// OpenAITTFTGuardConfigSnapshot implements OpenAITTFTGuardConfigProvider. It
// only performs an atomic read on the hot path and refreshes stale data in the
// background.
func (s *SettingService) OpenAITTFTGuardConfigSnapshot() OpenAITTFTGuardConfigSnapshot {
	defaults := openAITTFTGuardSnapshot(DefaultOpenAITTFTGuardSettings())
	if s == nil {
		return defaults
	}
	cached, _ := s.openAITTFTGuardConfigCache.Load().(*cachedOpenAITTFTGuardConfig)
	if cached != nil && time.Now().UnixNano() < cached.expiresAt {
		return cached.snapshot
	}
	s.openAITTFTGuardConfigSF.DoChan(openAITTFTGuardConfigRefreshKey, func() (any, error) {
		s.refreshOpenAITTFTGuardConfig(context.Background())
		return nil, nil
	})
	if cached != nil {
		return cached.snapshot
	}
	return defaults
}

// WarmOpenAITTFTGuardConfig synchronously loads the DB-backed config. It is a
// cold-path helper for deterministic startup/tests; scheduler reads do not use it.
func (s *SettingService) WarmOpenAITTFTGuardConfig(ctx context.Context) OpenAITTFTGuardConfigSnapshot {
	if s == nil {
		return openAITTFTGuardSnapshot(DefaultOpenAITTFTGuardSettings())
	}
	s.refreshOpenAITTFTGuardConfig(ctx)
	cached, _ := s.openAITTFTGuardConfigCache.Load().(*cachedOpenAITTFTGuardConfig)
	if cached == nil {
		return openAITTFTGuardSnapshot(DefaultOpenAITTFTGuardSettings())
	}
	return cached.snapshot
}

func (s *SettingService) refreshOpenAITTFTGuardConfig(ctx context.Context) {
	if s == nil || s.settingRepo == nil {
		return
	}
	revision := s.openAITTFTGuardRevision.Load()
	dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), openAITTFTGuardConfigDBTimeout)
	defer cancel()

	snapshot := openAITTFTGuardSnapshot(DefaultOpenAITTFTGuardSettings())
	ttl := openAITTFTGuardConfigCacheTTL
	raw, err := s.settingRepo.GetValue(dbCtx, SettingKeyOpenAITTFTGuardSettings)
	if err == nil {
		settings, parseErr := parseOpenAITTFTGuardSettings(raw)
		if parseErr == nil {
			snapshot = openAITTFTGuardSnapshot(settings)
		} else {
			ttl = openAITTFTGuardConfigErrorTTL
			slog.Warn("failed to refresh OpenAI TTFT guard settings, disabling guard", "error", parseErr)
		}
	} else if !errors.Is(err, ErrSettingNotFound) {
		ttl = openAITTFTGuardConfigErrorTTL
		slog.Warn("failed to refresh OpenAI TTFT guard settings, disabling guard", "error", err)
	}

	// A PUT may have committed while this refresh was reading. Never overwrite
	// the just-published snapshot with an older database observation.
	if s.openAITTFTGuardRevision.Load() != revision {
		return
	}
	s.storeOpenAITTFTGuardSnapshot(snapshot, ttl)
}

func (s *SettingService) storeOpenAITTFTGuardSnapshot(snapshot OpenAITTFTGuardConfigSnapshot, ttl time.Duration) {
	s.openAITTFTGuardConfigCache.Store(&cachedOpenAITTFTGuardConfig{
		snapshot:  snapshot,
		expiresAt: time.Now().Add(ttl).UnixNano(),
	})
}
